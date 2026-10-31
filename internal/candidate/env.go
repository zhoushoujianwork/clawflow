package candidate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

// inheritedEnv is the complete list of variables copied from the caller.
// Everything else (tokens, DSNs, provider keys, agent sockets, the stable
// HOME and state directory) is dropped.
var inheritedEnv = []string{"PATH", "LANG", "LC_ALL", "TZ", "USER", "LOGNAME"}

// Env builds the environment of a candidate process. dsn is the
// schema-pinned database DSN; it travels only through this environment and is
// never written to the manifest, configuration or evidence.
func (p Plan) Env(dsn string) []string {
	env := []string{}
	for _, name := range inheritedEnv {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	env = append(env,
		"HOME="+p.Home,
		"TMPDIR="+p.Tmp,
		"CLAWFLOW_STATE_DIR="+p.State,
		"GOCACHE="+filepath.Join(p.Cache, "go-build"),
		"XDG_CACHE_HOME="+p.Cache,
		devmode.EnvID+"="+p.ID,
		devmode.EnvRoot+"="+p.Root,
		devmode.EnvSchema+"="+p.Schema,
		devmode.EnvPort+"="+strconv.Itoa(p.Port),
	)
	if dsn != "" {
		env = append(env, "CLAWFLOW_DATABASE_DSN="+dsn)
	}
	return env
}

// ConfigOptions controls the generated candidate configuration.
type ConfigOptions struct {
	// Fixtures maps repository names to loopback base URLs of fake forges.
	Fixtures map[string]string
	// Dispatch enables the fixture repositories. It is false by default so a
	// fresh candidate cannot start any work.
	Dispatch bool
}

// BuildConfig returns the candidate's configuration. It contains no
// credentials, no real repositories, no auto-merge and no scheduler interval.
func (p Plan) BuildConfig(opts ConfigOptions) (*config.Config, error) {
	cfg := &config.Config{Repos: map[string]config.Repo{}}
	cfg.Settings.Coordinator = config.CoordinatorSettings{Enabled: true, Listen: fmt.Sprintf("127.0.0.1:%d", p.Port)}
	cfg.Settings.MaxConcurrentAgents = 2
	cfg.Settings.RunIntervalMinutes = 0
	cfg.Settings.Housekeeping.Disabled = true
	names := make([]string, 0, len(opts.Fixtures))
	for name := range opts.Fixtures {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		base := opts.Fixtures[name]
		if err := devmode.CheckBaseURL(base); err != nil {
			return nil, fmt.Errorf("fixture %s: %w", name, err)
		}
		cfg.Repos[name] = config.Repo{
			Enabled: opts.Dispatch, Platform: "gitlab", BaseURL: base, BaseBranch: "main",
			MaxInFlight: 1, AutoMerge: false, AcceptanceRequired: true, AcceptanceWorker: "different",
			WorkerTags: []string{p.Lane("dev")}, WorkerTagsByKind: map[string][]string{"acceptance": {p.Lane("review")}},
			TestingSOPRequired: p.Pin.SOPRequired, TestingSOPDigest: p.Pin.SOPDigest, RequiredCapabilities: p.Pin.Capabilities,
		}
	}
	return cfg, nil
}

// Lane returns the routing tag of a worker lane, unique to this candidate.
func (p Plan) Lane(kind string) string { return "candidate-" + p.ID + "-" + kind }

// WriteConfig writes config.yaml below the candidate home.
func (p Plan) WriteConfig(opts ConfigOptions) error {
	cfg, err := p.BuildConfig(opts)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := devmode.CheckWritePath(filepath.Join(p.Config, "config.yaml")); err != nil && devmode.Active() {
		return err
	}
	return os.WriteFile(filepath.Join(p.Config, "config.yaml"), data, 0o600)
}

// Worker is one lane process of the candidate.
type Worker struct {
	Name     string
	Role     string // operator or acceptance
	Lane     string
	StateDir string
}

// Lanes returns the bounded development and independent review workers. Each
// has its own identity (token), session state directory and lane tag, and a
// concurrency of one. Both receive the same pinned capabilities.
func (p Plan) Lanes() (dev, review Worker) {
	dev = Worker{Name: "dev-" + p.ID, Role: "operator", Lane: p.Lane("dev"), StateDir: filepath.Join(p.State, "worker-dev")}
	review = Worker{Name: "review-" + p.ID, Role: "acceptance", Lane: p.Lane("review"), StateDir: filepath.Join(p.ReviewDir, "worker-review")}
	return dev, review
}

// WorkerArgs returns the `clawflow worker` arguments for a lane.
func (p Plan) WorkerArgs(w Worker, tokenFile, fingerprint string) []string {
	args := []string{"worker",
		"--coordinator", fmt.Sprintf("https://127.0.0.1:%d", p.Port),
		"--token-file", tokenFile, "--fingerprint", fingerprint,
		"--state-dir", w.StateDir, "--roles", w.Role,
		"--tags", w.Lane, "--run-untagged=false", "--max-parallel", "1",
	}
	if len(p.Pin.Capabilities) > 0 {
		args = append(args, "--capabilities", strings.Join(p.Pin.Capabilities, ","))
	}
	return args
}

// CheckIndependence refuses a review lane that shares identity, session state
// or lane with the development lane.
func CheckIndependence(dev, review Worker) error {
	switch {
	case dev.Name == "" || review.Name == "":
		return fmt.Errorf("worker identities must be named")
	case dev.Name == review.Name:
		return fmt.Errorf("reviewer reuses the development worker identity %q", dev.Name)
	case dev.StateDir == review.StateDir || devmodeWithin(dev.StateDir, review.StateDir) || devmodeWithin(review.StateDir, dev.StateDir):
		return fmt.Errorf("reviewer shares session state with the development worker")
	case dev.Lane == review.Lane:
		return fmt.Errorf("reviewer shares the development lane tag")
	case review.Role != "acceptance" || dev.Role == "acceptance":
		return fmt.Errorf("lane roles must be operator for development and acceptance for review")
	}
	return nil
}

func devmodeWithin(dir, path string) bool { return devmode.Within(dir, path) }
