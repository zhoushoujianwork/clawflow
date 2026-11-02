package upgrade

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
)

// Options tune an engine. Zero values select safe defaults.
type Options struct {
	SettleTimeout time.Duration // drain of embedded work and unsettled results
	VerifyTimeout time.Duration // health, reconnect and continuity checks
	WorkerTimeout time.Duration // per-worker drain and readiness window
	Poll          time.Duration // sampling interval and measurement resolution
	// MinCapacity is the parallel capacity that must stay available while a
	// worker is replaced. Zero permits draining the last worker, explicitly.
	MinCapacity int
	// Hook runs at every stage boundary ("begin" and "end"). Returning
	// ErrInterrupted stops the engine like a crash would.
	Hook func(stage, phase string) error
	Now  func() time.Time
}

func (o Options) withDefaults() Options {
	if o.SettleTimeout <= 0 {
		o.SettleTimeout = 15 * time.Minute
	}
	if o.VerifyTimeout <= 0 {
		o.VerifyTimeout = 2 * time.Minute
	}
	if o.WorkerTimeout <= 0 {
		o.WorkerTimeout = 30 * time.Minute
	}
	if o.Poll <= 0 {
		o.Poll = time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Engine drives upgrades. It holds no state beyond Records; everything needed
// to resume after a crash is in the persisted record and the observed host.
type Engine struct {
	Host    Host
	Records Store
	Opt     Options
}

// NewEngine applies option defaults.
func NewEngine(h Host, records Store, opt Options) *Engine {
	return &Engine{Host: h, Records: records, Opt: opt.withDefaults()}
}

// Begin runs read-only preflight and, when it passes, creates the record.
// Nothing about the host changes.
func (e *Engine) Begin(ctx context.Context, kind string, target Artifact, gates Gates, workers []WorkerStep) (*Record, error) {
	if err := e.checkGates(target, gates); err != nil {
		return nil, err
	}
	if kind != KindController && kind != KindWorkers {
		return nil, fmt.Errorf("unknown upgrade kind %q", kind)
	}
	now := e.Opt.Now().UTC()
	rec := &Record{ID: NewID(now), Kind: kind, Direction: "forward", Stage: StagePreflight, StartedAt: now, UpdatedAt: now,
		Target: target, Gates: gates, SettleTimeoutSec: int64(e.Opt.SettleTimeout / time.Second), VerifyTimeoutSec: int64(e.Opt.VerifyTimeout / time.Second),
		Workers: workers}
	if kind == KindWorkers && len(workers) == 0 {
		return nil, fmt.Errorf("worker promotion needs at least one worker")
	}
	if err := e.Records.Save(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func (e *Engine) checkGates(target Artifact, g Gates) error {
	switch {
	case !ValidCommit(target.Manifest.Commit) || target.Commit != target.Manifest.Commit:
		return fmt.Errorf("target needs one full commit SHA, got %q / %q", target.Commit, target.Manifest.Commit)
	case g.ApprovedSHA != target.Commit:
		return fmt.Errorf("approval names %q, not the target %s", g.ApprovedSHA, target.Commit)
	case strings.TrimSpace(g.EvidenceDigest) == "":
		return fmt.Errorf("isolated candidate evidence is required before promotion")
	case strings.TrimSpace(g.CIRef) == "" || strings.TrimSpace(g.AcceptanceRef) == "":
		return fmt.Errorf("required CI and independent acceptance references are required")
	case target.SHA256 == "" || target.Path == "":
		return fmt.Errorf("target artifact is not identified")
	}
	return nil
}

// Run executes or continues a record. It is idempotent: finished stages are
// skipped and unfinished ones are repeated against the observed host.
func (e *Engine) Run(ctx context.Context, rec *Record) error {
	if rec.Terminal() {
		return nil
	}
	var err error
	switch {
	case rec.Kind == KindWorkers:
		err = e.runWorkers(ctx, rec)
	case rec.Direction == "rollback":
		err = e.rollback(ctx, rec, rec.ResultNote)
	default:
		err = e.runController(ctx, rec)
	}
	return err
}

// Resume reloads a record and continues it.
func (e *Engine) Resume(ctx context.Context, id string) (*Record, error) {
	rec, err := e.Records.Load(id)
	if err != nil {
		return nil, err
	}
	if rec.Terminal() {
		return rec, nil
	}
	if rec.Kind == KindController {
		if err := e.checkOwner(ctx, rec); err != nil {
			return rec, err
		}
	}
	return rec, e.Run(ctx, rec)
}

// checkOwner refuses to continue while the running controller is neither the
// prior nor the target binary, or when its identity cannot be established.
func (e *Engine) checkOwner(ctx context.Context, rec *Record) error {
	obs, err := e.Host.Observe(ctx)
	if err != nil {
		return fmt.Errorf("cannot observe controller ownership: %w", err)
	}
	if !obs.Running {
		return nil
	}
	if obs.RunningSHA == "" || (obs.RunningSHA != rec.Prior.SHA256 && obs.RunningSHA != rec.Target.SHA256) {
		return fmt.Errorf("ownership is ambiguous: the running controller is neither the recorded prior nor target binary")
	}
	return nil
}

// Plan runs the read-only checks of a forward upgrade and reports the first
// reason it could not proceed. It writes no record and changes nothing on the
// host.
func (e *Engine) Plan(ctx context.Context, kind string, target, prior Artifact, gates Gates, workers []WorkerStep) (string, error) {
	if err := e.checkGates(target, gates); err != nil {
		return "", err
	}
	rec := &Record{Kind: kind, Target: target, Prior: prior, Gates: gates, Workers: workers}
	if kind == KindWorkers {
		return e.workerPreflight(ctx, rec)
	}
	detail, err := e.preflight(ctx, rec, nil)
	if err != nil {
		return "", err
	}
	if rec.Prior.Manifest.Inferred {
		detail += " (prior schema inferred from the database)"
	}
	return detail, nil
}

// OwnerView states who owns scheduling right now, from the host and the
// record rather than from the record alone, so status stays truthful after a
// crash. Values: none, prior, target, ambiguous.
func (e *Engine) OwnerView(ctx context.Context, rec *Record) (string, error) {
	obs, err := e.Host.Observe(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case !obs.Running && !obs.LockHeld:
		return "none", nil
	case obs.Running && obs.RunningSHA != "" && obs.RunningSHA == rec.Prior.SHA256:
		return "prior", nil
	case obs.Running && obs.RunningSHA != "" && obs.RunningSHA == rec.Target.SHA256:
		return "target", nil
	}
	return "ambiguous", nil
}

// ---- stage plumbing ------------------------------------------------------

func (e *Engine) save(rec *Record) error {
	rec.UpdatedAt = e.Opt.Now().UTC()
	return e.Records.Save(rec)
}

func (e *Engine) hook(stage, phase string) error {
	if e.Opt.Hook == nil {
		return nil
	}
	return e.Opt.Hook(stage, phase)
}

// step runs fn as a recorded, resumable stage. A finished stage is skipped.
func (e *Engine) step(rec *Record, name string, deadline time.Duration, fn func(*StageRecord) (string, error)) error {
	if rec.stageDone(name) {
		return nil
	}
	rec.Stage = name
	st := rec.stage(name)
	if st == nil || st.Status != "running" {
		now := e.Opt.Now().UTC()
		rec.Stages = append(rec.Stages, StageRecord{Name: name, Status: "running", StartedAt: now})
		st = &rec.Stages[len(rec.Stages)-1]
		if deadline > 0 {
			d := now.Add(deadline)
			st.Deadline = &d
		}
	}
	if err := e.save(rec); err != nil {
		return err
	}
	if err := e.hook(name, "begin"); err != nil {
		return err
	}
	detail, err := fn(rec.stage(name))
	st = rec.stage(name)
	if errors.Is(err, ErrInterrupted) {
		return err
	}
	end := e.Opt.Now().UTC()
	st.EndedAt, st.Detail = &end, detail
	var pp *postponeError
	switch {
	case err == nil:
		st.Status = "done"
	case errors.As(err, &pp):
		st.Status, st.Detail = "postponed", pp.reason
	default:
		st.Status, st.Detail = "failed", err.Error()
	}
	if serr := e.save(rec); serr != nil {
		return serr
	}
	if err != nil {
		return err
	}
	return e.hook(name, "end")
}

func (e *Engine) finish(rec *Record, result, note, recovery string) error {
	rec.Result, rec.ResultNote, rec.Recovery = result, note, recovery
	return e.save(rec)
}

// waitUntil polls cond until it holds or the absolute deadline passes. The
// condition is evaluated once more at the deadline, so a record resumed after
// its deadline still gets one honest check.
func (e *Engine) waitUntil(ctx context.Context, deadline time.Time, cond func() (bool, string)) (bool, string, error) {
	for {
		ok, detail := cond()
		if ok {
			return true, detail, nil
		}
		if !e.Opt.Now().Before(deadline) {
			return false, detail, nil
		}
		if err := sleep(ctx, e.Opt.Poll); err != nil {
			return false, detail, err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---- controller upgrade --------------------------------------------------

func (e *Engine) runController(ctx context.Context, rec *Record) error {
	phase1 := []struct {
		name string
		fn   func(context.Context, *Record, *StageRecord) (string, error)
		wait time.Duration
	}{
		{StagePreflight, e.preflight, 0},
		{StageArtifacts, e.artifacts, 0},
		{StageMigrate, e.migrate, 0},
		{StageQuiesce, e.quiesce, 0},
		{StageSettle, e.settle, e.Opt.SettleTimeout},
	}
	for _, s := range phase1 {
		s := s
		err := e.step(rec, s.name, s.wait, func(st *StageRecord) (string, error) { return s.fn(ctx, rec, st) })
		if err != nil {
			if errors.Is(err, ErrInterrupted) {
				return err
			}
			return e.abort(ctx, rec, err)
		}
	}
	cutover := []struct {
		name string
		fn   func(context.Context, *Record, *StageRecord) (string, error)
		wait time.Duration
	}{
		{StageStop, e.stopOld, 0},
		{StageFence, e.fence, 0},
		{StageInstall, e.install, 0},
		{StageStart, e.startNew, 0},
		{StageVerify, e.verify, e.Opt.VerifyTimeout},
	}
	for _, s := range cutover {
		s := s
		err := e.step(rec, s.name, s.wait, func(st *StageRecord) (string, error) { return s.fn(ctx, rec, st) })
		if err != nil {
			if errors.Is(err, ErrInterrupted) {
				return err
			}
			var un *unchangedError
			if errors.As(err, &un) {
				return e.abort(ctx, rec, err)
			}
			return e.rollback(ctx, rec, err.Error())
		}
	}
	if err := e.step(rec, StageResume, 0, func(*StageRecord) (string, error) { return e.resumeDispatch(ctx, rec) }); err != nil {
		if errors.Is(err, ErrInterrupted) {
			return err
		}
		return e.finish(rec, ResultFailed, "promoted and healthy, but dispatch could not be fully restored: "+err.Error(),
			"The target controller is serving. Resume paused workers with `clawflow dashboard resume NAME` after checking them.")
	}
	return e.finish(rec, ResultSucceeded, "controller promoted and verified", "")
}

// abort ends a run that never changed the serving controller. Dispatch paused
// by this upgrade is restored; nothing is cancelled or replayed.
func (e *Engine) abort(ctx context.Context, rec *Record, cause error) error {
	// Compensation must run even when the caller's context is already done.
	cctx := context.WithoutCancel(ctx)
	restoreErr := e.restore(cctx, rec)
	result := ResultFailed
	var pp *postponeError
	if errors.As(cause, &pp) {
		result = ResultPostponed
	}
	note := "the current controller was left running unchanged: " + cause.Error()
	recovery := "No action needed for the service. Fix the cause and run the upgrade again."
	if restoreErr != nil {
		note += "; dispatch restore incomplete: " + restoreErr.Error()
		recovery = "Workers listed in quiesced_by_upgrade may still be paused: resume them with `clawflow dashboard resume NAME`."
	}
	if result == ResultPostponed {
		recovery = "Work was neither cancelled nor replayed. Run the upgrade again when active work has settled."
	}
	return e.finish(rec, result, note, recovery)
}

// restore resumes exactly the workers this upgrade paused.
func (e *Engine) restore(ctx context.Context, rec *Record) error {
	var failed []string
	for _, name := range rec.Quiesced {
		if err := e.Host.SetPaused(ctx, name, false); err != nil {
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not resume %s", strings.Join(failed, ", "))
	}
	rec.Quiesced = nil
	return nil
}

func (e *Engine) preflight(ctx context.Context, rec *Record, _ *StageRecord) (string, error) {
	obs, err := e.Host.Observe(ctx)
	if err != nil {
		return "", unchanged("observe controller: %w", err)
	}
	if !obs.Running || !obs.LockHeld {
		return "", unchanged("no running controller owns scheduling; this is not a rolling upgrade")
	}
	if rec.Prior.SHA256 == "" {
		return "", unchanged("prior binary is not identified")
	}
	prior, err := e.Host.Preserve(ctx, rec.Prior)
	if err != nil {
		return "", unchanged("preserve prior binary: %w", err)
	}
	if obs.RunningSHA == "" || obs.RunningSHA != prior.SHA256 {
		return "", unchanged("the running controller is not the recorded prior binary; ownership is ambiguous")
	}
	if err := e.Host.Probe(ctx); err != nil {
		return "", unchanged("API is not healthy before the upgrade: %w", err)
	}
	if err := e.Host.ViewerProbe(ctx); err != nil {
		return "", unchanged("viewer endpoint is not healthy before the upgrade: %w", err)
	}
	applied, err := e.Host.Applied(ctx)
	if err != nil {
		return "", unchanged("read schema state: %w", err)
	}
	rec.SchemaBefore = maxInt(applied)
	if rec.Prior.Manifest.Inferred {
		// A binary without a manifest is trusted only up to the schema level
		// it was demonstrably running against.
		rec.Prior.Manifest.SchemaLatest = rec.SchemaBefore
	}
	if err := CheckTransition(rec.Prior.Manifest, rec.Target.Manifest, applied); err != nil {
		return "", unchanged("incompatible transition: %w", err)
	}
	snap, err := e.Host.Snapshot(ctx, nil)
	if err != nil {
		return "", unchanged("read scheduling state: %w", err)
	}
	rec.LiveBefore = rec.LiveBefore[:0]
	for _, w := range snap.Workers {
		if w.Live && !w.Revoked {
			rec.LiveBefore = append(rec.LiveBefore, w.Name)
		}
	}
	sort.Strings(rec.LiveBefore)
	return fmt.Sprintf("prior %s, schema %d, %d live worker(s)", short(prior.SHA256), rec.SchemaBefore, len(rec.LiveBefore)), nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func (e *Engine) artifacts(ctx context.Context, rec *Record, _ *StageRecord) (string, error) {
	prior, err := e.Host.Preserve(ctx, rec.Prior)
	if err != nil {
		return "", unchanged("preserve prior binary: %w", err)
	}
	target, err := e.Host.Preserve(ctx, rec.Target)
	if err != nil {
		return "", unchanged("preserve target binary: %w", err)
	}
	m, err := e.Host.Describe(ctx, target)
	if err != nil {
		return "", unchanged("target manifest: %w", err)
	}
	if m.Commit != rec.Target.Commit {
		return "", unchanged("target binary reports commit %s, not the approved %s", m.Commit, rec.Target.Commit)
	}
	if m.SchemaLatest != rec.Target.Manifest.SchemaLatest || m.WorkerProtocol != rec.Target.Manifest.WorkerProtocol {
		return "", unchanged("target binary reports schema %d / protocol %d, differing from the evaluated manifest", m.SchemaLatest, m.WorkerProtocol)
	}
	rec.Prior.Path, rec.Target.Path = prior.Path, target.Path
	return "prior and target preserved as immutable artifacts", nil
}

func (e *Engine) migrate(ctx context.Context, rec *Record, _ *StageRecord) (string, error) {
	// Re-check immediately before changing the database: it may have moved.
	applied, err := e.Host.Applied(ctx)
	if err != nil {
		return "", unchanged("read schema state: %w", err)
	}
	if err := CheckTransition(rec.Prior.Manifest, rec.Target.Manifest, applied); err != nil {
		return "", unchanged("incompatible transition: %w", err)
	}
	done, err := e.Host.MigrateExpand(ctx, rec.Target)
	after, aerr := e.Host.Applied(ctx)
	if aerr == nil {
		rec.SchemaAfter = maxInt(after)
	}
	if err != nil {
		return "", unchanged("expand migration failed; applied versions %v remain additive and compatible with the running controller: %v", done, err)
	}
	return fmt.Sprintf("expand migrations applied: %v", done), nil
}

func (e *Engine) quiesce(ctx context.Context, rec *Record, _ *StageRecord) (string, error) {
	snap, err := e.Host.Snapshot(ctx, nil)
	if err != nil {
		return "", unchanged("read workers: %w", err)
	}
	var todo []string
	for _, w := range snap.Workers {
		if !w.Paused && !w.Revoked {
			todo = append(todo, w.Name)
		}
	}
	sort.Strings(todo)
	// Write ahead: a crash after pausing must still know what to resume.
	rec.Quiesced = union(rec.Quiesced, todo)
	if err := e.save(rec); err != nil {
		return "", err
	}
	for _, name := range todo {
		if err := e.Host.SetPaused(ctx, name, true); err != nil {
			return "", unchanged("pause %s: %w", name, err)
		}
	}
	return fmt.Sprintf("new assignments stopped for %d worker(s); already-paused workers untouched", len(todo)), nil
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// settle waits until nothing depends on the controller process staying up:
// no lease belongs to a worker tied to the controller, and no accepted result
// still awaits effect application. Durable executors keep running.
func (e *Engine) settle(ctx context.Context, rec *Record, st *StageRecord) (string, error) {
	deadline := e.Opt.Now().Add(e.Opt.SettleTimeout)
	if st.Deadline != nil {
		deadline = *st.Deadline
	}
	var last coordinator.UpgradeSnapshot
	ok, detail, err := e.waitUntil(ctx, deadline, func() (bool, string) {
		snap, err := e.Host.Snapshot(ctx, nil)
		if err != nil {
			return false, "cannot read leases: " + err.Error()
		}
		last = snap
		var embedded, unsettled int
		for _, l := range snap.Leases {
			if !l.Durable {
				embedded++
			} else if l.ResultAccepted || l.UnsettledEffects > 0 {
				unsettled++
			}
		}
		if embedded == 0 && unsettled == 0 {
			return true, ""
		}
		return false, fmt.Sprintf("%d controller-tied lease(s) and %d unsettled result(s) remain", embedded, unsettled)
	})
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &postponeError{reason: "settle deadline expired: " + detail + "; no work was cancelled"}
	}
	rec.LeasesBefore = rec.LeasesBefore[:0]
	var watch []string
	for _, l := range last.Leases {
		watch = append(watch, l.TaskID)
	}
	watched, err := e.Host.Snapshot(ctx, watch)
	if err != nil {
		return "", unchanged("read task state: %w", err)
	}
	byID := map[string]coordinator.UpgradeTask{}
	for _, t := range watched.Watched {
		byID[t.ID] = t
	}
	for _, l := range watched.Leases {
		t := byID[l.TaskID]
		rec.LeasesBefore = append(rec.LeasesBefore, LeaseRef{TaskID: l.TaskID, Owner: l.Owner, Fence: l.Fence, Attempt: t.Attempt, ApplicationAttempts: t.ApplicationAttempts})
	}
	return fmt.Sprintf("handoff proven: %d durable lease(s) continue in independent executors", len(rec.LeasesBefore)), nil
}

func (e *Engine) stopOld(ctx context.Context, rec *Record, _ *StageRecord) (string, error) {
	if err := e.checkOwner(ctx, rec); err != nil {
		return "", unchanged("%w", err)
	}
	// Last healthy sample: interruption is measured from here.
	// Keep the first sample: a resumed stage must not shorten the measurement.
	if rec.Measured.LastHealthyBefore == nil {
		if err := e.Host.Probe(ctx); err == nil {
			now := e.Opt.Now().UTC()
			rec.Measured.LastHealthyBefore = &now
			rec.Measured.ResolutionMS = e.Opt.Poll.Milliseconds()
			if err := e.save(rec); err != nil {
				return "", err
			}
		}
	}
	if err := e.Host.StopController(ctx); err != nil {
		obs, oerr := e.Host.Observe(ctx)
		if oerr == nil && obs.Running && obs.RunningSHA == rec.Prior.SHA256 {
			return "", unchanged("prior controller did not stop and is still serving: %w", err)
		}
		return "", fmt.Errorf("stop prior controller: %w", err)
	}
	return "prior controller stopped; durable executors keep running", nil
}

func (e *Engine) fence(ctx context.Context, rec *Record, _ *StageRecord) (string, error) {
	release, err := e.Host.Fence(ctx)
	if err != nil {
		return "", fmt.Errorf("fence ownership: %w", err)
	}
	release()
	return "ownership lock acquired while no controller served", nil
}

func (e *Engine) install(ctx context.Context, rec *Record, _ *StageRecord) (string, error) {
	// The fence is held across the service definition change so nothing else
	// can take scheduling authority in between.
	release, err := e.Host.Fence(ctx)
	if err != nil {
		return "", fmt.Errorf("fence ownership: %w", err)
	}
	defer release()
	if err := e.Host.Install(ctx, rec.Target); err != nil {
		return "", fmt.Errorf("install target: %w", err)
	}
	return "service definition points at the immutable target artifact", nil
}

func (e *Engine) startNew(ctx context.Context, rec *Record, _ *StageRecord) (string, error) {
	obs, err := e.Host.Observe(ctx)
	if err != nil {
		return "", err
	}
	if obs.Running {
		if obs.RunningSHA == rec.Target.SHA256 {
			return "target already serving", nil
		}
		return "", fmt.Errorf("a different controller is already serving; refusing to start a second")
	}
	rec.ServingSince = e.Opt.Now().UTC()
	if err := e.save(rec); err != nil {
		return "", err
	}
	if err := e.Host.StartController(ctx); err != nil {
		return "", fmt.Errorf("start target: %w", err)
	}
	return "target controller started", nil
}

func (e *Engine) verify(ctx context.Context, rec *Record, st *StageRecord) (string, error) {
	return e.verifyServing(ctx, rec, st, rec.Target.SHA256, false)
}

// verifyServing waits for the expected binary to own scheduling and checks
// health, viewer endpoint, worker reconnection and lease continuity.
func (e *Engine) verifyServing(ctx context.Context, rec *Record, st *StageRecord, wantSHA string, rolledBack bool) (string, error) {
	deadline := e.Opt.Now().Add(e.Opt.VerifyTimeout)
	if st != nil && st.Deadline != nil {
		deadline = *st.Deadline
	}
	var apiAt, viewerAt *time.Time
	ok, detail, err := e.waitUntil(ctx, deadline, func() (bool, string) {
		obs, err := e.Host.Observe(ctx)
		if err != nil {
			return false, "observe: " + err.Error()
		}
		if !obs.Running || !obs.LockHeld {
			return false, "controller is not serving"
		}
		if obs.RunningSHA != wantSHA {
			return false, "serving binary is not the expected one"
		}
		if apiAt == nil {
			if err := e.Host.Probe(ctx); err != nil {
				return false, "API probe: " + err.Error()
			}
			now := e.Opt.Now().UTC()
			apiAt = &now
		}
		if viewerAt == nil {
			if err := e.Host.ViewerProbe(ctx); err != nil {
				return false, "viewer endpoint: " + err.Error()
			}
			now := e.Opt.Now().UTC()
			viewerAt = &now
		}
		return true, ""
	})
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("health check failed: %s", detail)
	}
	if lb := rec.Measured.LastHealthyBefore; lb != nil && apiAt != nil {
		ms := apiAt.Sub(*lb).Milliseconds()
		if rolledBack {
			rec.Measured.RollbackInterruptionMS = ms
		} else {
			rec.Measured.APIInterruptionMS, rec.Measured.APIRecovered = ms, true
		}
		if viewerAt != nil && !rolledBack {
			rec.Measured.ViewerEndpointMS, rec.Measured.ViewerEndpointRecovered = viewerAt.Sub(*lb).Milliseconds(), true
		}
		rec.Measured.Note = "measured from the last healthy probe before stop to the first healthy probe after start; resolution is the poll interval. The native app is a separate client: its reconnect is not observed by this CLI and the app was not restarted."
	}
	// Workers reconnect on their own; require every worker that was live.
	var missing []string
	okW, detailW, err := e.waitUntil(ctx, deadline, func() (bool, string) {
		snap, err := e.Host.Snapshot(ctx, nil)
		if err != nil {
			return false, err.Error()
		}
		live := map[string]bool{}
		seen := map[string]*time.Time{}
		for _, w := range snap.Workers {
			live[w.Name] = w.Live && !w.Revoked
			seen[w.Name] = w.LastSeen
		}
		missing = missing[:0]
		for _, name := range rec.LiveBefore {
			// Liveness lasts two minutes, so an outage shorter than that does
			// not prove reconnection. Require contact after the new start.
			if !live[name] || seen[name] == nil || !seen[name].After(rec.ServingSince) {
				missing = append(missing, name)
			}
		}
		return len(missing) == 0, "workers not reconnected: " + strings.Join(missing, ", ")
	})
	if err != nil {
		return "", err
	}
	if !okW {
		return "", fmt.Errorf("%s", detailW)
	}
	if err := e.checkContinuity(ctx, rec); err != nil {
		return "", err
	}
	return "serving binary verified; API and viewer endpoint healthy; workers reconnected; leases intact", nil
}

// checkContinuity proves no lease was lost or replayed across the cutover.
func (e *Engine) checkContinuity(ctx context.Context, rec *Record) error {
	if len(rec.LeasesBefore) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rec.LeasesBefore))
	for _, l := range rec.LeasesBefore {
		ids = append(ids, l.TaskID)
	}
	snap, err := e.Host.Snapshot(ctx, ids)
	if err != nil {
		return fmt.Errorf("continuity check: %w", err)
	}
	now := map[string]coordinator.UpgradeTask{}
	for _, t := range snap.Watched {
		now[t.ID] = t
	}
	for _, before := range rec.LeasesBefore {
		t, ok := now[before.TaskID]
		switch {
		case !ok:
			return fmt.Errorf("task %s disappeared", before.TaskID)
		case t.Fence != before.Fence:
			return fmt.Errorf("task %s lease epoch changed from %d to %d", before.TaskID, before.Fence, t.Fence)
		case t.State == "pending":
			return fmt.Errorf("task %s returned to the queue and could be replayed", before.TaskID)
		case t.State == "leased" && t.Owner != before.Owner:
			return fmt.Errorf("task %s changed owner", before.TaskID)
		case t.State == "leased" && (t.Attempt != before.Attempt || t.ApplicationAttempts != before.ApplicationAttempts):
			return fmt.Errorf("task %s retry counters changed during cutover", before.TaskID)
		}
	}
	return nil
}

func (e *Engine) resumeDispatch(ctx context.Context, rec *Record) (string, error) {
	if err := e.restore(ctx, rec); err != nil {
		return "", err
	}
	return "dispatch restored for workers paused by this upgrade", nil
}

// ---- rollback ------------------------------------------------------------

// rollback returns the serving controller to the prior binary when promotion
// failed after the serving controller was stopped. A binary rollback leaves
// the database schema where it is, so it runs only when that is safe.
func (e *Engine) rollback(ctx context.Context, rec *Record, cause string) error {
	cctx := context.WithoutCancel(ctx)
	if rec.Direction != "rollback" {
		rec.Direction = "rollback"
		applied, err := e.Host.Applied(cctx)
		durable := true
		if snap, serr := e.Host.Snapshot(cctx, nil); serr == nil {
			durable = len(snap.Leases) > 0
		}
		var elig RollbackEligibility
		if err != nil {
			elig = RollbackEligibility{Reason: "schema state unreadable: " + err.Error()}
		} else {
			elig = CheckRollback(rec.Prior.Manifest, rec.Target.Manifest, applied, durable)
		}
		rec.Rollback = &elig
		rec.ResultNote = cause
		if err := e.save(rec); err != nil {
			return err
		}
	}
	if !rec.Rollback.Eligible {
		return e.finish(rec, ResultRollbackBlocked, "promotion failed ("+cause+") and rollback is not eligible: "+rec.Rollback.Reason,
			"Dispatch stays paused for the workers in quiesced_by_upgrade. Do not start the prior binary against this database. Repair forward or restore the database from a verified backup, then resume workers.")
	}
	steps := []struct {
		name string
		fn   func() (string, error)
		wait time.Duration
	}{
		{StageRbStop, func() (string, error) { return e.rollbackStop(cctx, rec) }, 0},
		{StageRbFence, func() (string, error) {
			release, err := e.Host.Fence(cctx)
			if err != nil {
				return "", err
			}
			release()
			return "ownership lock acquired", nil
		}, 0},
		{StageRbInstall, func() (string, error) {
			release, err := e.Host.Fence(cctx)
			if err != nil {
				return "", err
			}
			defer release()
			if err := e.Host.Install(cctx, rec.Prior); err != nil {
				return "", err
			}
			return "service definition points at the preserved prior artifact", nil
		}, 0},
		{StageRbStart, func() (string, error) {
			obs, err := e.Host.Observe(cctx)
			if err != nil {
				return "", err
			}
			if obs.Running {
				if obs.RunningSHA == rec.Prior.SHA256 {
					return "prior already serving", nil
				}
				return "", fmt.Errorf("another controller is serving")
			}
			rec.ServingSince = e.Opt.Now().UTC()
			if err := e.save(rec); err != nil {
				return "", err
			}
			return "prior controller started", e.Host.StartController(cctx)
		}, 0},
		{StageRbVerify, func() (string, error) {
			return e.verifyServing(cctx, rec, rec.stage(StageRbVerify), rec.Prior.SHA256, true)
		}, e.Opt.VerifyTimeout},
		{StageRbResume, func() (string, error) { return e.resumeDispatch(cctx, rec) }, 0},
	}
	for _, s := range steps {
		s := s
		err := e.step(rec, s.name, s.wait, func(*StageRecord) (string, error) { return s.fn() })
		if err != nil {
			if errors.Is(err, ErrInterrupted) {
				return err
			}
			return e.finish(rec, ResultFailed, "promotion failed ("+cause+") and the rollback also failed at "+s.name+": "+err.Error(),
				"Neither version is verified healthy. Dispatch stays paused. Inspect with `clawflow upgrade status`, then start the prior artifact recorded in prior.path manually under the controller lock.")
		}
	}
	return e.finish(rec, ResultRolledBack, "promotion failed ("+cause+"); the prior controller was restored and verified", "The database keeps its expanded schema; the prior binary tolerates it. Fix the target and retry.")
}

func (e *Engine) rollbackStop(ctx context.Context, rec *Record) (string, error) {
	obs, err := e.Host.Observe(ctx)
	if err != nil {
		return "", err
	}
	if !obs.Running && !obs.LockHeld {
		return "no controller running", nil
	}
	if obs.Running && obs.RunningSHA == rec.Prior.SHA256 {
		return "prior controller is already serving", nil
	}
	if err := e.Host.StopController(ctx); err != nil {
		return "", err
	}
	return "unhealthy target stopped", nil
}
