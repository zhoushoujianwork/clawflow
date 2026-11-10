package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	var keepSchema, taskFixture, requireTask bool
	var taskTimeout time.Duration
	var buildCache, modCache string

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
		p, err := candidate.NewPlan(commit, base, port, candidate.Pin{SOPRequired: sopRequired, SOPDigest: sopDigest, Capabilities: capabilities})
		if err != nil {
			return p, err
		}
		if taskFixture {
			p = p.WithForge(0)
		}
		return p, nil
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
		opts := candidate.ConfigOptions{Fixtures: fx, ExecutionEngine: candidateExecutionEngine()}
		if taskFixture {
			opts = p.TaskConfigOptions()
		}
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
		if taskFixture {
			// The forge port is part of the plan; keep it with the manifest so
			// up, exercise and down act on the same fixture.
			m.Plan = p
			if err := m.Save(); err != nil {
				return err
			}
			if err := candidate.SetupTaskFixture(p); err != nil {
				return err
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "prepared candidate %s\nroot=%s\nport=%d\nschema=%s\nbundle=%s\n", m.Plan.ID, m.Plan.Root, m.Plan.Port, m.Plan.Schema, m.Plan.BundleID)
		return nil
	}}
	flags(prepare)
	pinFlags(prepare)
	prepare.Flags().StringVar(&repoName, "repo", "", "repository whose primary-project testing SOP is pinned")
	prepare.Flags().StringSliceVar(&machineCaps, "machine-capability", nil, "capabilities this machine declares")
	prepare.Flags().StringSliceVar(&fixtures, "fixture", nil, "loopback fixture repo as owner/repo=URL")
	prepare.Flags().BoolVar(&taskFixture, "task-fixture", false, "also create the loopback forge, git clone, fixture agent and profile that `candidate exercise` needs")

	up := &cobra.Command{Use: "up", Short: "Build the pinned commit and start the dev controller and worker pair", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
		p, err := load()
		if err != nil {
			return err
		}
		m, err := candidate.ReadManifest(p.Root)
		if err != nil {
			return fmt.Errorf("candidate %s is not prepared: run `clawflow candidate prepare` first", p.ID)
		}
		if m.Running() {
			return fmt.Errorf("candidate %s is already running; stop its owned processes before another up", p.ID)
		}
		ev, err := beginCandidateAttempt(p)
		if err != nil {
			return err
		}
		defer func() {
			if runErr == nil {
				return
			}
			if ev.Layers["environment-ready"] != candidate.Passed {
				ev.Layers["environment-ready"] = candidate.Blocked
				ev.Readiness = candidate.Readiness{Status: candidate.Blocked, NextOwner: "environment-maintainer", Action: "resolve the startup failure and start a new candidate attempt"}
			} else if ev.Layers["candidate-loaded"] != candidate.Passed {
				ev.Layers["candidate-loaded"] = candidate.Failed
			} else {
				ev.Layers["lifecycle"] = candidate.Failed
			}
			ev.Command("up", []string{"candidate", "up"}, 1, runErr.Error())
			ev.Finish()
			for _, name := range []string{"evidence-partial.json", "evidence.json"} {
				if err := ev.Write(filepath.Join(p.Root, name)); err != nil {
					runErr = fmt.Errorf("%w; cannot persist failed attempt: %v", runErr, err)
				}
			}
		}()
		ev.Layers["environment-ready"] = candidate.Passed
		stableBefore, err := candidate.TakeStable(stableHost(), candidate.DefaultStableProbe(config.RootDir(), p.Base))
		if err != nil {
			return err
		}
		if err := writeJSON0600(filepath.Join(p.Root, "stable-before.json"), stableBefore); err != nil {
			return err
		}
		p = m.Plan
		binary, sha, err := candidate.BuildBinaryWith(cmd.Context(), p, candidate.BuildOptions{Checkout: checkout, GoCache: buildCache, ModCache: modCache})
		if err != nil {
			return err
		}
		ev.Environment["binary_sha256"] = sha
		ev.Command("build", []string{"go", "build", "./cmd/clawflow"}, 0, "ok")
		ev.Layers["candidate-loaded"] = candidate.Passed
		res, err := m.Up(cmd.Context(), binary, "")
		if err != nil {
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
	up.Flags().StringVar(&buildCache, "go-cache", "", "shared Go build cache directory (never the stable host's)")
	up.Flags().StringVar(&modCache, "mod-cache", "", "existing Go module cache, reused with GOPROXY=off (nothing is downloaded into it)")

	exercise := &cobra.Command{Use: "exercise", Short: "Run one real task through the running candidate and record the outcome", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, err := load()
		if err != nil {
			return err
		}
		m, err := candidate.ReadManifest(p.Root)
		if err != nil {
			return fmt.Errorf("candidate %s is not prepared: %w", p.ID, err)
		}
		if m.Plan.ForgePort == 0 {
			return fmt.Errorf("candidate %s was prepared without --task-fixture", p.ID)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), taskTimeout+time.Minute)
		defer cancel()
		run, err := candidate.Exercise(ctx, m, candidate.TaskOptions{Timeout: taskTimeout})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(m.Plan.Root, "task-run.json"), run.Marshal(), 0o600); err != nil {
			return err
		}
		for _, st := range run.Steps {
			fmt.Fprintf(cmd.OutOrStdout(), "%-13s passed=%v  %s\n", st.Name, st.Passed, st.Evidence)
		}
		if run.Failure != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "task run failed: %s\n", run.Failure)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "task-run: %s (processes and locks are checked by `candidate down`)\n", filepath.Join(m.Plan.Root, "task-run.json"))
		return nil
	}}
	flags(exercise)
	exercise.Flags().DurationVar(&taskTimeout, "task-timeout", 3*time.Minute, "how long to wait for the task to finish")

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
		taskRun, taskErr := readTaskRun(p.Root)
		if taskErr != nil {
			return taskErr
		}
		if taskRun != nil {
			// Processes are checked after the services stopped: whatever is still
			// alive was left behind by the run and fails the layer.
			candidate.FinishTask(m, taskRun, 5*time.Second)
		}
		var before candidate.StableSnapshot
		data, err := os.ReadFile(filepath.Join(p.Root, "stable-before.json"))
		if err == nil {
			err = json.Unmarshal(data, &before)
		}
		ev := candidate.NewEvidence(p, "")
		if raw, rerr := os.ReadFile(filepath.Join(p.Root, "evidence-partial.json")); rerr == nil {
			if err := json.Unmarshal(raw, ev); err != nil {
				return fmt.Errorf("candidate partial evidence is unreadable: %w", err)
			}
			if ev.Commit != p.Commit || ev.ID != p.ID {
				return fmt.Errorf("candidate partial evidence belongs to another runtime")
			}
		} else if !os.IsNotExist(rerr) {
			return rerr
		}
		ev.Layers["offline"] = offline
		if taskRun != nil {
			ev.TaskRequired, ev.Task = true, taskRun
			ev.Command("exercise", []string{"candidate", "exercise"}, 0, "task "+taskRun.TaskID+" state "+taskRun.State)
		}
		if err == nil {
			after, terr := candidate.TakeStable(stableHost(), candidate.DefaultStableProbe(config.RootDir(), p.Base))
			if terr != nil {
				return terr
			}
			ev.StableDiff = before.Diff(after)
			ev.StableIgnored = before.Ignored(after)
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
		if schemaRecorded(p) {
			return fmt.Errorf("legacy PostgreSQL candidate schema is recorded; preserve this runtime and retire its schema separately")
		}
		return m.Remove()
	}}
	flags(clean)
	clean.Flags().BoolVar(&keepSchema, "keep-schema", false, "leave the database schema in place")

	verify := &cobra.Command{Use: "verify EVIDENCE", Short: "Check that evidence matches the candidate commit under review", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		e, err := candidate.VerifyWith(args[0], commit, requireTask)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "evidence ok: commit=%s runtime=%s status=%s task-lifecycle=%s native-visual=%s\n", e.Commit, e.ID, e.Status, e.Layers[candidate.TaskLayer], e.Layers["native-visual"])
		return nil
	}}
	verify.Flags().StringVar(&commit, "commit", "", "commit under review (required)")
	_ = verify.MarkFlagRequired("commit")
	verify.Flags().BoolVar(&requireTask, "require-task", false, "also require a passed real-task run in the evidence")

	root.AddCommand(prepare, up, exercise, down, clean, verify)
	return root
}

func schemaRecorded(p candidate.Plan) bool {
	_, err := os.Stat(filepath.Join(p.Root, ".schema-created"))
	return err == nil
}

func candidateExecutionEngine() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	return cfg.Settings.Execution.Engine
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

// Every up attempt gets fresh evidence before setup can fail. Prior records
// remain available for audit and can never supply this attempt's verdict.
func beginCandidateAttempt(p candidate.Plan) (*candidate.Evidence, error) {
	names := []string{"evidence.json", "evidence-partial.json", "stable-before.json", "task-run.json"}
	var existing []string
	for _, name := range names {
		_, err := os.Lstat(filepath.Join(p.Root, name))
		if err == nil {
			existing = append(existing, name)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if len(existing) > 0 {
		archive, err := os.MkdirTemp(p.Root, "attempt-")
		if err != nil {
			return nil, err
		}
		for _, name := range existing {
			if err := os.Rename(filepath.Join(p.Root, name), filepath.Join(archive, name)); err != nil {
				return nil, err
			}
		}
	}
	ev := candidate.NewEvidence(p, "")
	if err := ev.Write(filepath.Join(p.Root, "evidence-partial.json")); err != nil {
		return nil, err
	}
	return ev, nil
}

// readTaskRun loads the run recorded by `candidate exercise`, or nil when the
// candidate never exercised a task. An unreadable record is an error, not nil.
func readTaskRun(root string) (*candidate.TaskRun, error) {
	raw, err := os.ReadFile(filepath.Join(root, "task-run.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var run candidate.TaskRun
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("task run record is unreadable: %w", err)
	}
	return &run, nil
}

// goEnv reads one Go environment value.
func goEnv(name string) (string, error) {
	out, err := exec.Command("go", "env", name).Output()
	return strings.TrimSpace(string(out)), err
}
