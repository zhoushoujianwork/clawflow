package commands

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

const (
	launchAgentPrefix = "com.zhoushoujianwork.clawflow."
	// launchd supplies this PATH to jobs that do not set one.
	launchdDefaultPath = "/usr/bin:/bin:/usr/sbin:/sbin"
)

// workerName matches the native app's "Worker · name" title.
func workerName(label string) string { return strings.TrimPrefix(label, launchAgentPrefix+"worker-") }

func osLookup(key string) (string, bool) { return os.LookupEnv(key) }

// loadExecutionSettings reads only the canonical config, read-only, and applies
// the overrides visible in the given environment.
func loadExecutionSettings(get func(string) (string, bool)) (execution.Config, error) {
	var c execution.Config
	path := config.ConfigPath()
	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		var cfg config.Config
		if err := readCheckYAML(path, &cfg); err != nil {
			return c, fmt.Errorf("Offload settings unreadable: fix or recreate ~/.clawflow/config/config.yaml")
		}
		c = cfg.Settings.Execution
	case os.IsNotExist(statErr):
		if v, ok := get("CLAWFLOW_OFFLOAD_PROFILE"); !ok || v == "" {
			return c, fmt.Errorf("Offload settings unavailable: configure settings.execution.profile or CLAWFLOW_OFFLOAD_PROFILE")
		}
	default:
		return c, fmt.Errorf("Offload settings unreadable: ~/.clawflow/config/config.yaml is not accessible")
	}
	return execution.WithEnvironmentFrom(c, get), nil
}

// service is one installed local execution worker as launchd would start it.
type service struct {
	Label string
	Roles []string
	Env   map[string]string
	Args  []string
}

func (s service) lookup(key string) (string, bool) {
	v, ok := s.Env[key]
	if !ok && key == "PATH" {
		return launchdDefaultPath, true
	}
	return v, ok
}

// readServices lists installed worker LaunchAgents. A plist it cannot parse is
// returned with err set so the caller reports it as unreported, not ready.
func readServices(dir string) (services []service, unreadable []string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, launchAgentPrefix+"worker-") || !strings.HasSuffix(name, ".plist") {
			continue
		}
		label := strings.TrimSuffix(name, ".plist")
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(data) > 1<<20 {
			unreadable = append(unreadable, label)
			continue
		}
		dict, err := parsePlistDict(data)
		if err != nil {
			unreadable = append(unreadable, label)
			continue
		}
		svc := service{Label: label, Roles: []string{"operator"}, Env: map[string]string{}}
		if m, ok := dict["EnvironmentVariables"].(map[string]any); ok {
			for k, v := range m {
				if s, ok := v.(string); ok {
					svc.Env[k] = s
				}
			}
		}
		if a, ok := dict["ProgramArguments"].([]any); ok {
			for _, v := range a {
				if s, ok := v.(string); ok {
					svc.Args = append(svc.Args, s)
				}
			}
		}
		for i, arg := range svc.Args {
			if arg == "--roles" && i+1 < len(svc.Args) {
				svc.Roles = strings.Split(svc.Args[i+1], ",")
			} else if v, ok := strings.CutPrefix(arg, "--roles="); ok {
				svc.Roles = strings.Split(v, ",")
			}
		}
		services = append(services, svc)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Label < services[j].Label })
	return services, unreadable
}

// parsePlistDict decodes the XML property list subset launchd agents use.
func parsePlistDict(data []byte) (map[string]any, error) {
	d := xml.NewDecoder(strings.NewReader(string(data)))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("not an XML property list")
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			v, err := parsePlistValue(d, se)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			if m == nil {
				return nil, fmt.Errorf("plist root is not a dictionary")
			}
			return m, nil
		}
	}
}

func parsePlistValue(d *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		m := map[string]any{}
		key := ""
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					var k string
					if err := d.DecodeElement(&k, &t); err != nil {
						return nil, err
					}
					key = k
					continue
				}
				v, err := parsePlistValue(d, t)
				if err != nil {
					return nil, err
				}
				m[key] = v
			case xml.EndElement:
				return m, nil
			}
		}
	case "array":
		out := []any{}
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := parsePlistValue(d, t)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			case xml.EndElement:
				return out, nil
			}
		}
	default:
		var s string
		if err := d.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		if start.Name.Local == "string" {
			return s, nil
		}
		return nil, nil
	}
}

// statusInputs are the seams tests replace.
type statusInputs struct {
	AgentsDir    string
	Settings     func(get func(string) (string, bool)) (execution.Config, error)
	Cache        string
	Now          func() time.Time
	Shell        bool
	Role         string
	Recheck      bool
	LocalWorker  bool // controller embeds a worker (settings.local_worker.enabled)
	ControllerEn map[string]string
}

// collectStatus builds the shared status document. It inspects each installed
// worker service with that service's own PATH and overrides. Machines with no
// execution worker report not_required.
func collectStatus(ctx context.Context, in statusInputs) execution.StatusDoc {
	now := time.Now
	if in.Now != nil {
		now = in.Now
	}
	if !in.Recheck {
		if doc, ok := execution.ReadStatusCache(in.Cache, now()); ok && !in.Shell && in.Role == "" {
			return doc
		}
	}
	var workers []execution.Readiness
	services, unreadable := readServices(in.AgentsDir)
	for _, label := range unreadable {
		workers = append(workers, execution.Readiness{SchemaVersion: execution.ReadinessSchemaVersion, State: execution.StateUnreported,
			Worker: workerName(label), Environment: "service", CheckedAt: now().UTC(),
			Summary:  "Service definition could not be read; its Offload environment is unknown.",
			Guidance: &execution.Guidance{Steps: []string{"Reinstall the worker LaunchAgent so its environment can be inspected."}, Recheck: execution.RecheckCommand}})
	}
	for _, svc := range services {
		roles := svc.Roles
		if in.Role != "" {
			roles = []string{in.Role}
		}
		opts := execution.ReadinessOptions{Env: svc.lookup, Worker: workerName(svc.Label), Environment: "service", Now: now}
		c, err := in.Settings(svc.lookup)
		if err != nil {
			workers = append(workers, execution.ConfigBlockedReadiness(err.Error(), opts))
			continue
		}
		workers = append(workers, execution.CheckReadiness(ctx, c, roles, opts))
	}
	if in.LocalWorker {
		get := func(k string) (string, bool) { v, ok := in.ControllerEn[k]; return v, ok }
		opts := execution.ReadinessOptions{Env: get, Worker: "controller (embedded local worker)", Environment: "service", Now: now}
		if c, err := in.Settings(get); err != nil {
			workers = append(workers, execution.ConfigBlockedReadiness(err.Error(), opts))
		} else {
			role := []string{"operator", "acceptance"}
			workers = append(workers, execution.CheckReadiness(ctx, c, role, opts))
		}
	}
	if in.Shell {
		roles := []string{"operator"}
		if in.Role != "" {
			roles = []string{in.Role}
		}
		opts := execution.ReadinessOptions{Worker: "this shell", Environment: "interactive", Now: now}
		if c, err := in.Settings(osLookup); err != nil {
			workers = append(workers, execution.ConfigBlockedReadiness(err.Error(), opts))
		} else {
			workers = append(workers, execution.CheckReadiness(ctx, c, roles, opts))
		}
	}
	if len(workers) == 0 {
		workers = append(workers, execution.Readiness{SchemaVersion: execution.ReadinessSchemaVersion, State: execution.StateNotRequired,
			Worker: "this machine", Summary: "No execution worker is installed here (controller-only or viewer-only); a local Offload is not required.", CheckedAt: now().UTC()})
	}
	doc := execution.Summarize(now(), workers)
	if !in.Shell && in.Role == "" {
		_ = execution.WriteStatusCache(in.Cache, doc)
	}
	return doc
}

func printStatus(out io.Writer, doc execution.StatusDoc) {
	fmt.Fprintf(out, "Offload execution: %s\n%s\n", doc.State, doc.Summary)
	for _, w := range doc.Workers {
		fmt.Fprintf(out, "\n[%s] %s", w.Worker, w.State)
		if w.Reason != "" {
			fmt.Fprintf(out, " (%s)", w.Reason)
		}
		fmt.Fprintf(out, "\n  %s\n", w.Summary)
		if w.ConfiguredCommand != "" && w.State != execution.StateNotRequired {
			fmt.Fprintf(out, "  Configured command: %s\n", w.ConfiguredCommand)
		}
		if w.Command != "" {
			fmt.Fprintf(out, "  Resolved: %s\n", w.Command)
		}
		if len(w.Profiles) > 0 {
			fmt.Fprintf(out, "  Profile: %s\n", strings.Join(w.Profiles, ", "))
		}
		if w.Guidance != nil {
			for _, step := range w.Guidance.Steps {
				fmt.Fprintf(out, "  - %s\n", step)
			}
			if w.Guidance.Link != "" {
				fmt.Fprintf(out, "  Docs: %s\n", w.Guidance.Link)
			}
			fmt.Fprintf(out, "  Recheck: %s\n", w.Guidance.Recheck)
		}
		if len(w.Unverified) > 0 {
			fmt.Fprintf(out, "  Unverified: %s\n", strings.Join(w.Unverified, ", "))
		}
	}
}

func statusCachePath() string { return filepath.Join(config.RootDir(), "run") }

func newExecutionStatusCmd() *cobra.Command {
	var jsonOutput, recheck, shell, noFail bool
	var role string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show Offload readiness for every local execution worker",
		Long: `Report whether the Offload dependency is ready for each installed local worker.

Each worker LaunchAgent is inspected with its own PATH and CLAWFLOW_OFFLOAD_*
overrides, not this shell's. Machines without an execution worker (controller-only
or viewer-only) need no local Offload. The check is read-only and bounded: it
runs 'offload -h' and 'offload profiles', never starts a task, installs or
upgrades anything, or contacts a provider. Results are cached for 60 seconds
(15 seconds while blocked); --recheck forces a fresh probe. Live authentication
is never verified.`,
		Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			in := statusInputs{AgentsDir: filepath.Join(home, "Library", "LaunchAgents"), Settings: loadExecutionSettings, Cache: statusCachePath(),
				Shell: shell, Role: role, Recheck: recheck}
			if cfg, err := config.Load(); err == nil && cfg.Settings.Coordinator.LocalWorker.Enabled {
				in.LocalWorker = true
				in.ControllerEn = controllerEnvironment(in.AgentsDir)
			}
			doc := collectStatus(cmd.Context(), in)
			if jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(doc); err != nil {
					return err
				}
			} else {
				printStatus(cmd.OutOrStdout(), doc)
			}
			if doc.State == execution.StateBlocked && !noFail {
				return errExecutionBlocked
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noFail, "no-fail", false, "Exit 0 even when blocked (for presenters that read the JSON)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the status document as JSON")
	cmd.Flags().BoolVar(&recheck, "recheck", false, "Ignore the cached result and probe again")
	cmd.Flags().BoolVar(&shell, "shell", false, "Also check this interactive shell's environment (for setup scripts)")
	cmd.Flags().StringVar(&role, "role", "", "Check this role's profile instead of each worker's roles")
	return cmd
}

// controllerEnvironment returns the controller LaunchAgent's environment, which
// the embedded local worker inherits; the current process is used otherwise.
func controllerEnvironment(dir string) map[string]string {
	env := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, launchAgentPrefix+"controller.plist"))
	if err == nil {
		if dict, err := parsePlistDict(data); err == nil {
			if m, ok := dict["EnvironmentVariables"].(map[string]any); ok {
				for k, v := range m {
					if s, ok := v.(string); ok {
						env[k] = s
					}
				}
			}
		}
	}
	if _, ok := env["PATH"]; !ok {
		env["PATH"] = launchdDefaultPath
	}
	return env
}

// warnOffloadForService reports, without blocking installation, whether the
// PATH a LaunchAgent is about to be given can resolve the configured Offload.
func warnOffloadForService(out io.Writer, servicePATH string) {
	get := func(k string) (string, bool) {
		if k == "PATH" {
			return servicePATH, true
		}
		return os.LookupEnv(k)
	}
	opts := execution.ReadinessOptions{Worker: "service", Environment: "service", Env: get}
	var r execution.Readiness
	if c, err := loadExecutionSettings(get); err != nil {
		r = execution.ConfigBlockedReadiness(err.Error(), opts)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), gateBudget)
		defer cancel()
		r = execution.CheckReadiness(ctx, c, []string{config.RoleOperator}, opts)
	}
	if !r.Blocked() {
		return
	}
	fmt.Fprintf(out, "Warning: Offload is not ready for this service: %s\n", r.Summary)
	if r.Guidance != nil {
		for _, step := range r.Guidance.Steps {
			fmt.Fprintf(out, "  - %s\n", step)
		}
		fmt.Fprintf(out, "  Recheck: %s\n", r.Guidance.Recheck)
	}
}
