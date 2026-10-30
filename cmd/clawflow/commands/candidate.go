package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/candidate"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

const adminDSNEnv = "CLAWFLOW_CANDIDATE_ADMIN_DSN"

func candidateBase() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "ClawFlow", "candidates"), nil
}

// stableHost describes the installed host. Its ports come from the stable
// configuration plus the documented dashboard and database defaults.
func stableHost() candidate.Stable {
	s := candidate.Stable{Root: config.RootDir(), Ports: []int{8090, 8093, 5432}, LabelPrefix: "com.zhoushoujianwork.clawflow."}
	if cfg, err := config.Load(); err == nil {
		if _, port, ok := strings.Cut(cfg.Settings.Coordinator.ListenAddress(), ":"); ok {
			var n int
			if _, err := fmt.Sscanf(port, "%d", &n); err == nil && n > 0 {
				s.Ports = append(s.Ports, n)
			}
		}
	}
	return s
}

// NewCandidateCmd manages disposable per-commit development candidates. It is
// unavailable inside a candidate (see devmode.HostMutatingCommands).
func NewCandidateCmd() *cobra.Command {
	var commit, base, checkout, offline, repoName string
	var port int
	var sopRequired bool
	var sopDigest string
	var capabilities, machineCaps, fixtures []string
	var keepSchema bool

	root := &cobra.Command{Use: "candidate", Short: "Run an isolated development candidate beside the stable host", SilenceUsage: true,
		Long: `Creates a per-commit runtime with its own HOME, state, config, locks, port,
database schema, launchd label namespace and app bundle id, then starts a
dev controller plus a development worker and an independent review worker.
Nothing of the installed host is replaced, restarted or read for credentials.
Fixture repositories must use loopback URLs; external dispatch, auto-merge and
write-back are disabled by default.`}
	flags := func(c *cobra.Command) {
		c.Flags().StringVar(&commit, "commit", "", "full 40-character candidate commit (required)")
		c.Flags().StringVar(&base, "base", "", "directory for candidate runtimes (default: user cache)")
		_ = c.MarkFlagRequired("commit")
	}
	load := func() (candidate.Plan, error) {
		if devmode.Active() {
			return candidate.Plan{}, fmt.Errorf("%w: candidates cannot manage candidates", devmode.ErrBlocked)
		}
		if base == "" {
			var err error
			if base, err = candidateBase(); err != nil {
				return candidate.Plan{}, err
			}
		}
		return candidate.NewPlan(commit, base, port, candidate.Pin{SOPRequired: sopRequired, SOPDigest: sopDigest, Capabilities: capabilities})
	}
	pinFlags := func(c *cobra.Command) {
		c.Flags().IntVar(&port, "port", 0, "candidate loopback port (default: derived from the commit)")
		c.Flags().BoolVar(&sopRequired, "sop-required", false, "require the pinned testing SOP")
		c.Flags().StringVar(&sopDigest, "sop-digest", "", "approved SHA-256 of testing.md")
		c.Flags().StringSliceVar(&capabilities, "capability", nil, "required logical capability (applied to both lanes)")
	}

	prepare := &cobra.Command{Use: "prepare", Short: "Verify readiness, refuse collisions, create the candidate runtime", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, err := load()
		if err != nil {
			return err
		}
		ready := candidate.CheckReadiness(p.Pin, candidateSOP(repoName), machineCaps)
		if err := ready.Err(); err != nil {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(ready)
			return err
		}
		// Validate every input before the first directory is created.
		fx := map[string]string{}
		for _, f := range fixtures {
			name, url, ok := strings.Cut(f, "=")
			if !ok {
				return fmt.Errorf("--fixture must be owner/repo=http://127.0.0.1:PORT")
			}
			fx[name] = url
		}
		opts := candidate.ConfigOptions{Fixtures: fx}
		if _, err := p.BuildConfig(opts); err != nil {
			return err
		}
		m, err := candidate.Prepare(p, stableHost(), candidate.FreePort)
		if err != nil {
			return err
		}
		if err := p.WriteConfig(opts); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "prepared candidate %s\nroot=%s\nport=%d\nschema=%s\nbundle=%s\n", m.Plan.ID, m.Plan.Root, m.Plan.Port, m.Plan.Schema, m.Plan.BundleID)
		return nil
	}}
	flags(prepare)
	pinFlags(prepare)
	prepare.Flags().StringVar(&repoName, "repo", "", "repository whose primary-project testing SOP is pinned")
	prepare.Flags().StringSliceVar(&machineCaps, "machine-capability", nil, "capabilities this machine declares")
	prepare.Flags().StringSliceVar(&fixtures, "fixture", nil, "loopback fixture repo as owner/repo=URL")

	up := &cobra.Command{Use: "up", Short: "Build the pinned commit and start the dev controller and worker pair", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, err := load()
		if err != nil {
			return err
		}
		m, err := candidate.ReadManifest(p.Root)
		if err != nil {
			return fmt.Errorf("candidate %s is not prepared: run `clawflow candidate prepare` first", p.ID)
		}
		admin := strings.TrimSpace(os.Getenv(adminDSNEnv))
		if admin == "" {
			return fmt.Errorf("blocked (environment-maintainer): %s is not set; a PostgreSQL server is required to own the candidate schema", adminDSNEnv)
		}
		pinned, err := candidate.PinDSN(admin, p.Schema)
		if err != nil {
			return err
		}
		ev := candidate.NewEvidence(p, "")
		ev.Redact(admin, pinned)
		ev.Layers["environment-ready"] = candidate.Passed
		stableBefore, err := candidate.TakeStable(stableHost(), candidate.DefaultStableProbe(config.RootDir(), p.Base))
		if err != nil {
			return err
		}
		if err := writeJSON0600(filepath.Join(p.Root, "stable-before.json"), stableBefore); err != nil {
			return err
		}
		if err := (candidate.PostgresSchemas{DSN: admin}).Ensure(cmd.Context(), p.Schema, schemaRecorded(p)); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(p.Root, ".schema-created"), []byte(p.Schema+"\n"), 0o600); err != nil {
			return err
		}
		binary, sha, err := candidate.BuildBinary(cmd.Context(), p, checkout)
		if err != nil {
			return err
		}
		ev.Environment["binary_sha256"] = sha
		ev.Command("build", []string{"go", "build", "./cmd/clawflow"}, 0, "ok")
		ev.Layers["candidate-loaded"] = candidate.Passed
		res, err := m.Up(cmd.Context(), binary, pinned)
		if err != nil {
			ev.Layers["lifecycle"] = candidate.Failed
			ev.Command("up", []string{"candidate", "up"}, 1, err.Error())
			ev.Finish()
			_ = ev.Write(filepath.Join(p.Root, "evidence.json"))
			return err
		}
		ev.Layers["lifecycle"] = candidate.Passed
		ev.Workers = res.Identities
		ev.Command("up", []string{"candidate", "up"}, 0, "controller and worker lanes running")
		if err := writeJSON0600(filepath.Join(p.Root, "evidence-partial.json"), ev); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "candidate %s running: controller pid %d, dev lane %s, review lane %s\n", p.ID, res.Controller, res.Identities["operator"], res.Identities["acceptance"])
		return nil
	}}
	flags(up)
	pinFlags(up)
	up.Flags().StringVar(&checkout, "checkout", "", "disposable git checkout of the candidate commit (required)")
	_ = up.MarkFlagRequired("checkout")

	down := &cobra.Command{Use: "down", Short: "Stop only owned candidate processes and write evidence", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, err := load()
		if err != nil {
			return err
		}
		m, err := candidate.ReadManifest(p.Root)
		if err != nil {
			return err
		}
		switch offline {
		case candidate.Passed, candidate.Failed, candidate.NotRun:
		default:
			return fmt.Errorf("--offline must be passed, failed or not-run")
		}
		stopped, stopErr := m.Down()
		fmt.Fprintf(cmd.OutOrStdout(), "stopped: %s\n", strings.Join(stopped, ", "))
		var before candidate.StableSnapshot
		data, err := os.ReadFile(filepath.Join(p.Root, "stable-before.json"))
		if err == nil {
			err = json.Unmarshal(data, &before)
		}
		ev := candidate.NewEvidence(p, "")
		if raw, rerr := os.ReadFile(filepath.Join(p.Root, "evidence-partial.json")); rerr == nil {
			_ = json.Unmarshal(raw, ev)
		}
		ev.Layers["offline"] = offline
		if err == nil {
			after, terr := candidate.TakeStable(stableHost(), candidate.DefaultStableProbe(config.RootDir(), p.Base))
			if terr != nil {
				return terr
			}
			ev.StableDiff = before.Diff(after)
		} else {
			ev.StableDiff = []string{"no stable snapshot was recorded before the run"}
		}
		ev.Finish()
		if werr := ev.Write(filepath.Join(p.Root, "evidence.json")); werr != nil {
			return werr
		}
		fmt.Fprintf(cmd.OutOrStdout(), "evidence: %s status=%s\n", filepath.Join(p.Root, "evidence.json"), ev.Status)
		return stopErr
	}}
	flags(down)
	pinFlags(down)
	down.Flags().StringVar(&offline, "offline", candidate.NotRun, "offline test result observed for this commit: passed|failed|not-run")

	clean := &cobra.Command{Use: "clean", Short: "Delete an owned, stopped candidate and its schema", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, err := load()
		if err != nil {
			return err
		}
		m, err := candidate.ReadManifest(p.Root)
		if err != nil {
			return fmt.Errorf("refusing to clean %s: %w", p.Root, err)
		}
		if _, err := m.Down(); err != nil {
			return err
		}
		if admin := strings.TrimSpace(os.Getenv(adminDSNEnv)); admin != "" && !keepSchema && schemaRecorded(p) {
			if err := (candidate.PostgresSchemas{DSN: admin}).Drop(cmd.Context(), p.Schema); err != nil {
				return err
			}
		}
		return m.Remove()
	}}
	flags(clean)
	clean.Flags().BoolVar(&keepSchema, "keep-schema", false, "leave the database schema in place")

	verify := &cobra.Command{Use: "verify EVIDENCE", Short: "Check that evidence matches the candidate commit under review", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		e, err := candidate.Verify(args[0], commit)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "evidence ok: commit=%s runtime=%s status=%s native-visual=%s\n", e.Commit, e.ID, e.Status, e.Layers["native-visual"])
		return nil
	}}
	verify.Flags().StringVar(&commit, "commit", "", "commit under review (required)")
	_ = verify.MarkFlagRequired("commit")

	root.AddCommand(prepare, up, down, clean, verify)
	return root
}

func schemaRecorded(p candidate.Plan) bool {
	_, err := os.Stat(filepath.Join(p.Root, ".schema-created"))
	return err == nil
}

// candidateSOP reads the same machine-local testing.md that development and
// acceptance use for repo; it returns "" when none is mapped.
func candidateSOP(repo string) string {
	if repo == "" {
		return ""
	}
	return localTesting(repo)
}

func writeJSON0600(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
