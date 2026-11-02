package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhoushoujianwork/clawflow/internal/candidate"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/upgrade"
)

// upgradeInputs are the promotion gates and the target, shared by plan and run.
type upgradeInputs struct {
	target, commit, evidence, ciRef, acceptanceRef, approved string
	settle, verify, worker, poll                             time.Duration
	minCapacity                                              int
}

func (in *upgradeInputs) bind(c *cobra.Command) {
	f := c.Flags()
	f.StringVar(&in.target, "binary", "", "path of the built target clawflow binary (required)")
	f.StringVar(&in.commit, "commit", "", "full 40-character commit the binary was built from (required)")
	f.StringVar(&in.evidence, "evidence", "", "passing isolated candidate evidence file for this commit (required)")
	f.StringVar(&in.ciRef, "ci-ref", "", "reference to the required exact-head CI run (required)")
	f.StringVar(&in.acceptanceRef, "acceptance-ref", "", "reference to the independent acceptance verdict (required)")
	f.StringVar(&in.approved, "approve", "", "the commit you approve for promotion; must equal --commit (required)")
	f.DurationVar(&in.settle, "settle-timeout", 15*time.Minute, "how long to wait for controller-tied work to settle before postponing")
	f.DurationVar(&in.verify, "verify-timeout", 2*time.Minute, "how long to wait for health and worker reconnection before rolling back")
	f.DurationVar(&in.worker, "worker-timeout", 30*time.Minute, "per-worker drain window before that worker is postponed")
	f.DurationVar(&in.poll, "poll", time.Second, "sampling interval; also the resolution of the interruption measurement")
	f.IntVar(&in.minCapacity, "min-capacity", 1, "parallel slots other workers must keep available while one is replaced")
	for _, name := range []string{"binary", "commit", "evidence", "ci-ref", "acceptance-ref", "approve"} {
		_ = c.MarkFlagRequired(name)
	}
}

// artifact builds the target from the flags and checks the candidate evidence
// is for this exact commit and this exact binary.
func (in *upgradeInputs) artifact(h upgrade.Host) (upgrade.Artifact, upgrade.Gates, error) {
	var zero upgrade.Artifact
	if !upgrade.ValidCommit(in.commit) {
		return zero, upgrade.Gates{}, fmt.Errorf("--commit must be a full 40-character lowercase SHA")
	}
	path, err := filepath.Abs(in.target)
	if err != nil {
		return zero, upgrade.Gates{}, err
	}
	sum, err := upgrade.FileSHA(path)
	if err != nil {
		return zero, upgrade.Gates{}, fmt.Errorf("target binary: %w", err)
	}
	ev, err := candidate.Verify(in.evidence, in.commit)
	if err != nil {
		return zero, upgrade.Gates{}, fmt.Errorf("candidate evidence: %w", err)
	}
	if ev.Environment["binary_sha256"] != sum {
		return zero, upgrade.Gates{}, fmt.Errorf("candidate evidence was recorded for a different binary than %s", filepath.Base(path))
	}
	raw, err := os.ReadFile(in.evidence)
	if err != nil {
		return zero, upgrade.Gates{}, err
	}
	digest := sha256.Sum256(raw)
	a := upgrade.Artifact{Commit: in.commit, SHA256: sum, Path: path}
	if h != nil {
		m, err := h.Describe(context.Background(), a)
		if err != nil {
			return zero, upgrade.Gates{}, err
		}
		if m.Inferred {
			return zero, upgrade.Gates{}, fmt.Errorf("the target binary has no `upgrade manifest` command; it cannot be promoted")
		}
		a.Manifest = m
	}
	return a, upgrade.Gates{EvidenceDigest: hex.EncodeToString(digest[:]), CIRef: in.ciRef, AcceptanceRef: in.acceptanceRef, ApprovedSHA: in.approved}, nil
}

func (in *upgradeInputs) engine(h upgrade.Host) *upgrade.Engine {
	return upgrade.NewEngine(h, upgrade.Store{Dir: filepath.Join(upgradeDir(), "records")}, upgrade.Options{
		SettleTimeout: in.settle, VerifyTimeout: in.verify, WorkerTimeout: in.worker, Poll: in.poll, MinCapacity: in.minCapacity})
}

func refuseInCandidate() error {
	if devmode.Active() {
		return fmt.Errorf("%w: promotion changes the installed host and is unavailable to a candidate", devmode.ErrBlocked)
	}
	return nil
}

// priorArtifact identifies the binary that currently owns scheduling.
func priorArtifact(ctx context.Context, h *upgradeHost) (upgrade.Artifact, error) {
	obs, err := h.Observe(ctx)
	if err != nil {
		return upgrade.Artifact{}, err
	}
	if !obs.Running || obs.RunningPath == "" || obs.RunningSHA == "" {
		return upgrade.Artifact{}, fmt.Errorf("cannot identify the running controller binary; refusing to start an upgrade")
	}
	a := upgrade.Artifact{SHA256: obs.RunningSHA, Path: obs.RunningPath}
	m, err := h.Describe(ctx, a)
	if err != nil {
		return a, err
	}
	a.Manifest = m
	a.Commit = m.Commit
	return a, nil
}

func printRecord(cmd *cobra.Command, rec *upgrade.Record, owner string) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "upgrade %s  kind=%s  stage=%s  result=%s\n", rec.ID, rec.Kind, rec.Stage, orDash(rec.Result))
	fmt.Fprintf(out, "target %s  sha256 %s\n", rec.Target.Commit, rec.Target.SHA256)
	if owner != "" {
		fmt.Fprintf(out, "scheduling owner now: %s\n", owner)
	}
	for _, s := range rec.Stages {
		dl := ""
		if s.Deadline != nil {
			dl = "  deadline " + s.Deadline.Format(time.RFC3339)
		}
		fmt.Fprintf(out, "  %-24s %-9s%s  %s\n", s.Name, s.Status, dl, s.Detail)
	}
	m := rec.Measured
	if m.APIRecovered {
		fmt.Fprintf(out, "API interruption: %d ms (resolution %d ms); viewer endpoint: %d ms\n", m.APIInterruptionMS, m.ResolutionMS, m.ViewerEndpointMS)
	}
	if rec.ResultNote != "" {
		fmt.Fprintf(out, "result: %s\n", rec.ResultNote)
	}
	if rec.Recovery != "" {
		fmt.Fprintf(out, "recovery: %s\n", rec.Recovery)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// NewUpgradeCmd drives the rolling upgrade of the installed controller and
// workers. See docs/controller-upgrade.md.
func NewUpgradeCmd() *cobra.Command {
	root := &cobra.Command{Use: "upgrade", Short: "Rolling, observable upgrade of the installed controller and workers", SilenceUsage: true,
		Long: `Promotes a verified binary to the installed controller or to workers while
tasks keep running. It never starts a second controller, never cancels or
replays a task, and records every stage and deadline without secrets.

A single-host controller cutover includes a short API interruption. It is
measured and reported; zero downtime is not claimed. See docs/controller-upgrade.md.`}

	root.AddCommand(&cobra.Command{Use: "manifest", Short: "Print this binary's schema and protocol compatibility as JSON", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		m := upgrade.LocalManifest(Version, installedCommit(), store.Migrations)
		return json.NewEncoder(cmd.OutOrStdout()).Encode(m)
	}})

	load := func() (*config.Config, *upgradeHost, error) {
		if err := refuseInCandidate(); err != nil {
			return nil, nil, err
		}
		cfg, err := config.Load()
		if err != nil {
			return nil, nil, err
		}
		h, err := newUpgradeHost(cfg)
		return cfg, h, err
	}

	var in upgradeInputs
	plan := &cobra.Command{Use: "plan", Short: "Run the read-only preflight; change nothing", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, h, err := load()
		if err != nil {
			return err
		}
		defer h.close()
		target, gates, err := in.artifact(h)
		if err != nil {
			return err
		}
		prior, err := priorArtifact(cmd.Context(), h)
		if err != nil {
			return err
		}
		detail, err := in.engine(h).Plan(cmd.Context(), upgrade.KindController, target, prior, gates, nil)
		if err != nil {
			return fmt.Errorf("not promotable: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "promotable: %s\nNothing was changed.\n", detail)
		return nil
	}}
	in.bind(plan)

	var inC upgradeInputs
	var execute bool
	controller := &cobra.Command{Use: "controller", Short: "Promote the controller: quiesce, settle, fenced cutover, verify, roll back on failure", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !execute {
			return fmt.Errorf("this stops the controller briefly; add --execute after `clawflow upgrade plan` succeeds")
		}
		_, h, err := load()
		if err != nil {
			return err
		}
		defer h.close()
		target, gates, err := inC.artifact(h)
		if err != nil {
			return err
		}
		prior, err := priorArtifact(cmd.Context(), h)
		if err != nil {
			return err
		}
		e := inC.engine(h)
		rec, err := e.Begin(cmd.Context(), upgrade.KindController, target, gates, nil)
		if err != nil {
			return err
		}
		rec.Prior = prior
		if err := e.Records.Save(rec); err != nil {
			return err
		}
		return runRecord(cmd, e, h, rec)
	}}
	inC.bind(controller)
	controller.Flags().BoolVar(&execute, "execute", false, "perform the promotion (without it, nothing happens)")

	var inW upgradeInputs
	var workers []string
	var executeW bool
	workerCmd := &cobra.Command{Use: "workers", Short: "Replace workers one at a time after draining their leases", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !executeW {
			return fmt.Errorf("add --execute to promote workers")
		}
		var steps []upgrade.WorkerStep
		for _, w := range workers {
			name, desc, ok := strings.Cut(w, "=")
			if !ok || name == "" || desc == "" {
				return fmt.Errorf("--worker must be NAME=PATH_TO_generation.json, got %q", w)
			}
			steps = append(steps, upgrade.WorkerStep{Name: name, Descriptor: desc, Status: upgrade.WorkerPending})
		}
		_, h, err := load()
		if err != nil {
			return err
		}
		defer h.close()
		target, gates, err := inW.artifact(h)
		if err != nil {
			return err
		}
		e := inW.engine(h)
		rec, err := e.Begin(cmd.Context(), upgrade.KindWorkers, target, gates, steps)
		if err != nil {
			return err
		}
		return runRecord(cmd, e, h, rec)
	}}
	inW.bind(workerCmd)
	workerCmd.Flags().StringArrayVar(&workers, "worker", nil, "NAME=PATH_TO_generation.json, in promotion order (repeatable)")
	workerCmd.Flags().BoolVar(&executeW, "execute", false, "perform the promotion")
	_ = workerCmd.MarkFlagRequired("worker")

	var poll, settle, verify, workerT time.Duration
	resume := &cobra.Command{Use: "resume ID", Short: "Continue an interrupted upgrade from its record", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, h, err := load()
		if err != nil {
			return err
		}
		defer h.close()
		e := upgrade.NewEngine(h, upgrade.Store{Dir: filepath.Join(upgradeDir(), "records")}, upgrade.Options{Poll: poll, SettleTimeout: settle, VerifyTimeout: verify, WorkerTimeout: workerT, MinCapacity: 1})
		rec, err := e.Resume(cmd.Context(), args[0])
		if rec != nil {
			owner, _ := e.OwnerView(cmd.Context(), rec)
			printRecord(cmd, rec, owner)
		}
		return err
	}}
	resume.Flags().DurationVar(&poll, "poll", time.Second, "sampling interval")
	resume.Flags().DurationVar(&settle, "settle-timeout", 15*time.Minute, "settle window")
	resume.Flags().DurationVar(&verify, "verify-timeout", 2*time.Minute, "verification window")
	resume.Flags().DurationVar(&workerT, "worker-timeout", 30*time.Minute, "per-worker drain window")

	status := &cobra.Command{Use: "status [ID]", Short: "Show an upgrade record and who owns scheduling right now", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		records := upgrade.Store{Dir: filepath.Join(upgradeDir(), "records")}
		id := ""
		if len(args) == 1 {
			id = args[0]
		} else {
			ids, err := records.List()
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no upgrades recorded")
				return nil
			}
			id = ids[len(ids)-1]
		}
		rec, err := records.Load(id)
		if err != nil {
			return err
		}
		owner := ""
		if !devmode.Active() {
			if cfg, err := config.Load(); err == nil {
				if h, err := newUpgradeHost(cfg); err == nil {
					if o, err := upgrade.NewEngine(h, records, upgrade.Options{}).OwnerView(cmd.Context(), rec); err == nil {
						owner = o
					}
					h.close()
				}
			}
		}
		printRecord(cmd, rec, owner)
		return nil
	}}

	root.AddCommand(plan, controller, workerCmd, resume, status)
	return root
}

func runRecord(cmd *cobra.Command, e *upgrade.Engine, h *upgradeHost, rec *upgrade.Record) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "upgrade %s started; resume with `clawflow upgrade resume %s` if this process is interrupted\n", rec.ID, rec.ID)
	err := e.Run(cmd.Context(), rec)
	owner, _ := e.OwnerView(context.WithoutCancel(cmd.Context()), rec)
	printRecord(cmd, rec, owner)
	if err != nil {
		return err
	}
	if rec.Result != upgrade.ResultSucceeded {
		return fmt.Errorf("upgrade %s", rec.Result)
	}
	return nil
}
