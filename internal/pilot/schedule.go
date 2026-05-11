// Package pilot wakes per-project "pilot" agents at the
// end of each `clawflow run` pass. Each enabled project is fanned out
// to a non-interactive `claude -p` invocation that triages the
// backlog at the EDGES of the operator pipeline:
//
//   - upstream: files new issues, fixes missing trigger labels
//     (e.g. user opened a clear bug without the `bug` label)
//   - downstream: closes stale, duplicate, or already-fixed issues
//
// Pilot stays out of the middle (evaluate → ready-for-agent → implement
// → merge), which is fully owned by operators + repo-level
// `auto_approve` (auto-adds ready-for-agent after evaluate) +
// `auto_merge` (auto-merges agent-implemented PRs after CI). Pilot does
// NOT add ready-for-agent — that's auto_approve's domain. Trying to
// have Pilot apply judgment there would just race auto_approve.
//
// Closed loop:
//
//	clawflow run → operators process labeled issues →
//	  pilot.Schedule wakes the Pilot →
//	  Pilot triages backlog (file/label/close/comment) →
//	  next `clawflow run` pass executes the changes
//
// Pilots are the "what should we be working on, and is the backlog
// coherent?" layer; operators are the "execute the work that's
// already on the board" layer.
//
// Two safety rails are enforced via prompt (not code), since the Pilot
// invokes the same `clawflow` CLI users do and the CLI doesn't know
// whether its caller is a Pilot or a human:
//
//   - Pilot must skip issues carrying agent-running (operator mid-flight).
//   - Pilot must not duplicate its own prior actions; cooldown gives this
//     room to mean something. If runaway Pilot noise is observed in the
//     wild, the next iteration will add a pilot-touched marker label
//     so the runner can enforce mechanically rather than by prompt.
package pilot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/pilot/budget"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

// Schedule fans out to every project with Automation.Enabled and
// past its cooldown. Sequential by design: Pilots are heavy claude
// invocations, parallelism here would mostly buy rate-limit pain.
//
// Errors per-project are logged and swallowed — one stuck project
// must never block the rest. Returns the count of Pilots actually
// woken, for the caller's run summary.
func Schedule(ctx context.Context, perWakeTimeout time.Duration) (int, error) {
	projects, err := project.ListAutomationEnabled()
	if err != nil {
		return 0, fmt.Errorf("list automation-enabled projects: %w", err)
	}
	if len(projects) == 0 {
		return 0, nil
	}

	// Resolve the current machine's hostname once. Used to skip projects
	// bound to a different machine. Non-fatal: an empty hostname means no
	// projects will be skipped by the binding check (conservative / safe).
	hostname, _ := os.Hostname()

	cfg, err := config.Load()
	if err != nil {
		return 0, fmt.Errorf("load config: %w", err)
	}
	creds, _ := config.LoadCredentials()

	// Open the pilot log here (in addition to inside wake) so binding /
	// require_binding skips — which never reach wake — are still visible
	// in pilot.log. Without this, a project that is silently skipped on
	// every pass leaves no trace anywhere except stderr, which is where
	// the original "Pilot on but two days no activity" bug came from.
	skipLog, _ := clog.Open("pilot")
	defer skipLog.Close()

	// Refresh projects.json on exit so the dashboard picks up any
	// MarkSkipped / MarkWoken changes from this pass. Best-effort: the
	// snapshot is a convenience for the UI, never a source of truth, so
	// an error here doesn't fail the Schedule.
	defer func() {
		if err := snapshot.WriteProjects(); err != nil {
			fmt.Fprintf(os.Stderr, "[pilot] snapshot WriteProjects: %v\n", err)
		}
	}()

	now := time.Now()
	var ready []*project.Project
	for _, p := range projects {
		// Skip projects bound to a different machine. A project with no
		// BoundMachine (empty string) is processed by every machine —
		// the common case. When hostname resolution failed (empty string),
		// we conservatively process all projects so a misconfigured machine
		// doesn't silently drop work.
		if p.Automation.BoundMachine != "" && hostname != "" && p.Automation.BoundMachine != hostname {
			reason := fmt.Sprintf("bound to %s (current machine: %s)", p.Automation.BoundMachine, hostname)
			fmt.Fprintf(os.Stderr, "[pilot] %s: %s — skip\n", p.Name, reason)
			skipLog.Info("pilot/skip", "project", p.Name, "reason", reason)
			if err := project.MarkSkipped(p.Name, reason); err != nil {
				fmt.Fprintf(os.Stderr, "[pilot] %s: mark-skipped failed: %v\n", p.Name, err)
			}
			continue
		}
		// When require_binding is set globally, skip projects that have no
		// BoundMachine configured. Mirrors the repo-level RequireBinding
		// behaviour in run.go.
		if cfg.Settings.RequireBinding && p.Automation.BoundMachine == "" && hostname != "" {
			reason := "no bound_machine and require_binding=true"
			fmt.Fprintf(os.Stderr, "[pilot] %s: %s — skip\n", p.Name, reason)
			skipLog.Info("pilot/skip", "project", p.Name, "reason", reason)
			if err := project.MarkSkipped(p.Name, reason); err != nil {
				fmt.Fprintf(os.Stderr, "[pilot] %s: mark-skipped failed: %v\n", p.Name, err)
			}
			continue
		}
		// Cooldown is intentionally NOT persisted via MarkSkipped: it is
		// expected, transient, and changes every pass. Logging to pilot.log
		// is enough for post-mortem; persisting it would just churn yaml
		// and bury the *real* skip reasons (bound_machine / require_binding).
		if rem := p.CooldownRemaining(now); rem > 0 {
			fmt.Fprintf(os.Stderr, "[pilot] %s: cooldown %s remaining — skip\n", p.Name, rem.Round(time.Second))
			skipLog.Info("pilot/skip", "project", p.Name, "reason", "cooldown", "remaining", rem.Round(time.Second).String())
			continue
		}
		ready = append(ready, p)
	}
	if len(ready) == 0 {
		return 0, nil
	}

	woken := 0
	for _, p := range ready {
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "[pilot] context canceled — stopping after %d wake(s)\n", woken)
			return woken, ctx.Err()
		default:
		}

		fmt.Fprintf(os.Stderr, "[pilot] waking project %q (cooldown=%dmin)\n", p.Name, p.Automation.CooldownMinutes)
		// Stamp BEFORE the wake so a slow Pilot doesn't get re-fired
		// the instant it returns. If the wake fails we still want
		// the cooldown to apply — repeated failures shouldn't busy-
		// loop claude.
		if err := project.MarkWoken(p.Name); err != nil {
			fmt.Fprintf(os.Stderr, "[pilot] %s: mark-woken failed: %v — continuing anyway\n", p.Name, err)
		}
		if err := wake(ctx, p, cfg, creds, perWakeTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "[pilot] %s: wake failed: %v\n", p.Name, err)
			continue
		}
		woken++
	}
	return woken, nil
}

// WakeOne triggers a Pilot wake for a single project on demand. Used by
// the dashboard's "Wake now" button (and the `clawflow pilot wake` CLI
// subcommand it spawns).
//
// Differences from Schedule:
//
//   - Cooldown is intentionally ignored — the click IS the override. The
//     wake updates LastWokenAt via MarkWoken, so the cooldown clock
//     restarts from this wake's start time, exactly like a scheduled
//     wake. The user's mental model: "wake now, then leave it alone for
//     <cooldown> minutes".
//   - bound_machine + require_binding are still enforced. A click from
//     a non-Pilot machine should fail clearly rather than run claude
//     with the wrong credentials / clones. If the user wants to wake on
//     a different machine, they should change the binding first.
//   - Automation.Enabled is still checked. A disabled project should
//     not be wake-able via the dashboard either — the toggle is the
//     single source of truth.
//
// Returns nil on a successful wake, error on any pre-flight failure or
// on wake() itself failing.
func WakeOne(ctx context.Context, projectName string, timeout time.Duration) error {
	p, err := project.Get(projectName)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	if !p.Automation.Enabled {
		return fmt.Errorf("automation disabled for project %q — enable it first", projectName)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	creds, _ := config.LoadCredentials()

	hostname, _ := os.Hostname()
	if p.Automation.BoundMachine != "" && hostname != "" && p.Automation.BoundMachine != hostname {
		return fmt.Errorf("project %q is bound to %s, current machine is %s — wake from there or rebind",
			projectName, p.Automation.BoundMachine, hostname)
	}
	if cfg.Settings.RequireBinding && p.Automation.BoundMachine == "" && hostname != "" {
		return fmt.Errorf("project %q has no bound_machine and require_binding=true — bind it first", projectName)
	}

	// Refresh projects.json on exit so the dashboard reflects the new
	// LastWokenAt / cleared skip reason without waiting for the next
	// `clawflow run` pass.
	defer func() {
		if werr := snapshot.WriteProjects(); werr != nil {
			fmt.Fprintf(os.Stderr, "[pilot] snapshot WriteProjects: %v\n", werr)
		}
	}()

	// Stamp before the wake, same as Schedule, so a slow wake doesn't
	// get re-fired by an impatient click. MarkWoken also clears
	// LastSkipReason — a manual wake is unambiguous proof the prior
	// skip no longer applies.
	if err := project.MarkWoken(p.Name); err != nil {
		fmt.Fprintf(os.Stderr, "[pilot] %s: mark-woken failed: %v — continuing anyway\n", p.Name, err)
	}

	fmt.Fprintf(os.Stderr, "[pilot] manual wake project=%q\n", p.Name)
	return wake(ctx, p, cfg, creds, timeout)
}

// wake builds the digest for one project, constructs the Pilot prompt,
// and invokes claude -p. The Pilot's stdout is streamed to the user's
// stderr by operator.RunClaude; we additionally echo the parsed
// PILOT-RESULT line so the run summary reads cleanly.
//
// Each wake is persisted to ~/.clawflow/data/pilot-runs/<project>/<ts>/
// with meta.json + events.jsonl so the dashboard can show Pilot activity.
func wake(ctx context.Context, p *project.Project, cfg *config.Config, creds *config.Credentials, timeout time.Duration) error {
	// Bound the ENTIRE wake — including pre-claude digest fetches and
	// post-claude finalization — to the configured timeout. Without this,
	// the deadline only covers the `claude -p` subprocess; any blocking
	// downstream call (context.md write, snapshot index, VCS digest fetch)
	// can hang indefinitely after claude exits. This is the same class of
	// bug fixed for the operator runner in #117.
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Pilot wakes happen inside `clawflow run` (process owns the run.log
	// handle) but emitting them on a separate "pilot" log keeps the run
	// tail readable. Open per-wake — wakes are tens of seconds apart and
	// the file is opened with O_APPEND so it's safe.
	lg, _ := clog.Open("pilot")
	defer lg.Close()

	startedAt := time.Now()
	runDir := snapshot.PilotRunDir(p.Name, startedAt)
	_ = os.MkdirAll(runDir, 0o755)

	meta := snapshot.PilotRunMeta{
		Project:   p.Name,
		StartedAt: startedAt.UTC(),
		Status:    "running",
	}
	_ = snapshot.WritePilotRunMeta(runDir, meta)
	lg.Info("pilot/start", "project", p.Name, "model", creds.EffectiveOperatorModel(), "timeout", timeout)

	// Refresh CLAUDE.md from current project.yaml + cfg.Repos before
	// every wake. `claude -p` will auto-load it from the workdir, giving
	// the Pilot a stable identity (which repos belong, where they live)
	// without us having to inline it into every per-wake prompt.
	if err := project.RefreshClaudeMD(p.Name); err != nil {
		fmt.Fprintf(os.Stderr, "[pilot] %s: CLAUDE.md refresh failed: %v — continuing\n", p.Name, err)
	}

	digests := buildDigests(p, cfg, creds)
	contextMD, _ := project.ReadContext(p.Name)
	deploymentMD, _ := project.ReadDeployment(p.Name)
	recent := pilotRecentSummaries(p.Name, 3)

	prompt := chat.BuildPilotContext(p.Name, contextMD, deploymentMD, recent, digests)
	model := creds.EffectiveOperatorModel()
	workdir := project.ProjectDir(p.Name)

	eventsFile, _ := os.Create(filepath.Join(runDir, "events.jsonl"))

	// Per-wake VCS write budget: code-layer hard cap on the number of
	// VCS mutations Pilot can make in a single wake. The budget file
	// lives next to meta.json/events.jsonl so post-mortem inspection
	// has all wake artifacts in one place. The env var is the activation
	// signal for the wrapped vcs.Client decorator in
	// cmd/clawflow/commands/vcs_client.go — any `clawflow` subcommand
	// the Pilot shells out to will see it via os.Environ inheritance.
	budgetPath := filepath.Join(runDir, "budget.json")
	if err := budget.Init(budgetPath, budget.DefaultMax); err != nil {
		fmt.Fprintf(os.Stderr, "[pilot] %s: budget init failed: %v — continuing without budget enforcement\n", p.Name, err)
		budgetPath = ""
	}
	if budgetPath != "" {
		_ = os.Setenv(budget.EnvPath, budgetPath)
		defer os.Unsetenv(budget.EnvPath)
	}

	output, err := operator.RunClaude(ctx, prompt, workdir, timeout, eventsFile, model)
	if eventsFile != nil {
		_ = eventsFile.Close()
	}

	if budgetPath != "" {
		if s, rerr := budget.Read(budgetPath); rerr == nil {
			fmt.Fprintf(os.Stderr, "[pilot] %s: budget %d/%d ops used\n", p.Name, s.Used, s.Max)
			lg.Info("pilot/budget", "project", p.Name, "used", s.Used, "max", s.Max)
		}
	}

	endedAt := time.Now().UTC()
	meta.EndedAt = &endedAt

	resultLine := extractResult(output)
	meta.Result = resultLine
	// Parse the duties YAML block first, then strip it from Summary so
	// the dashboard's free-form Detail view doesn't show raw fenced
	// YAML below the structured cards. Legacy / failed-to-parse runs
	// leave meta.Duties nil; UI falls back to the full output.
	if duties := ExtractDuties(output); duties != nil {
		// Runner is authoritative for the digest counts — Pilot only
		// writes the prose summary. This keeps the YAML short and the
		// numbers reliable (Pilot can't drift, miscount, or hallucinate).
		FillIssueDigestCounts(duties, p.Repos)
		meta.Duties = duties
		meta.Verdict = resultLine
		meta.Summary = strings.TrimSpace(StripDutiesBlock(output))
	} else {
		meta.Summary = output
	}

	if err != nil {
		meta.Status = "failed"
		meta.Error = err.Error()
		// When the top-level deadline fires, resultLine is empty because
		// claude never emitted a PILOT-RESULT. Fill it with a diagnostic
		// message so pilot/end is never logged with result="" on timeout.
		if resultLine == "" && ctx.Err() != nil {
			elapsed := endedAt.Sub(startedAt).Round(time.Second)
			resultLine = fmt.Sprintf("PILOT-RESULT: pilot deadline exceeded after %s (configured timeout=%s)", elapsed, timeout)
			meta.Result = resultLine
		}
	} else {
		meta.Status = "success"
		// On success, look for an updated context.md in the output and
		// persist it. The Pilot is the sole writer of context.md; we
		// only honour writes when the wake didn't error out (a failed
		// wake's view of the project may be inconsistent).
		if updated := chat.ExtractLastContextMD(output); updated != "" && strings.TrimSpace(updated) != strings.TrimSpace(contextMD) {
			if werr := project.WriteContext(p.Name, updated); werr != nil {
				fmt.Fprintf(os.Stderr, "[pilot] %s: write context.md: %v\n", p.Name, werr)
			} else {
				fmt.Fprintf(os.Stderr, "[pilot] %s: context.md updated by Pilot\n", p.Name)
			}
		}
		// On success, look for an updated deployment.md in the output and
		// persist it. Symmetric to context.md: Pilot may rewrite the runtime
		// SOP when Play 3 detects that the existing log commands don't match
		// the project's actual log layout (SOP drift). Only honour writes on
		// a successful wake — a failed wake's view of the environment may be
		// inconsistent.
		if updated := chat.ExtractFencedBlock(output, "deployment.md"); updated != "" && strings.TrimSpace(updated) != strings.TrimSpace(deploymentMD) {
			if werr := project.WriteDeployment(p.Name, updated); werr != nil {
				fmt.Fprintf(os.Stderr, "[pilot] %s: write deployment.md: %v\n", p.Name, werr)
			} else {
				fmt.Fprintf(os.Stderr, "[pilot] %s: deployment.md updated by Pilot\n", p.Name)
			}
		}
	}

	if u, uerr := snapshot.ExtractUsage(filepath.Join(runDir, "events.jsonl")); uerr == nil {
		meta.Usage = u
	}
	_ = snapshot.WritePilotRunMeta(runDir, meta)
	lg.Info("pilot/end",
		"project", p.Name,
		"status", meta.Status,
		"duration", endedAt.Sub(startedAt).Round(time.Second),
		"result", resultLine,
	)

	if _, ierr := snapshot.WritePilotRunsIndex(20); ierr != nil {
		fmt.Fprintf(os.Stderr, "[pilot] %s: snapshot pilot-runs index: %v\n", p.Name, ierr)
	}

	if resultLine != "" {
		fmt.Fprintf(os.Stderr, "[pilot] %s: %s\n", p.Name, resultLine)
	} else if err == nil {
		fmt.Fprintf(os.Stderr, "[pilot] %s: completed (no PILOT-RESULT line found)\n", p.Name)
	}
	return err
}

// buildDigests collects per-repo open-issue and open-PR snapshots.
// One repo's failure does not abort the others — the failed repo
// gets a row with FetchError populated so the Pilot can still see it.
func buildDigests(p *project.Project, cfg *config.Config, creds *config.Credentials) []chat.PilotRepoDigest {
	out := make([]chat.PilotRepoDigest, 0, len(p.Repos))
	for _, name := range p.Repos {
		d := chat.PilotRepoDigest{Name: name}
		client, err := clientFor(name, cfg, creds)
		if err != nil {
			d.FetchError = err.Error()
			out = append(out, d)
			continue
		}
		issues, err := client.ListOpenIssues(name)
		if err != nil {
			d.FetchError = "list issues: " + err.Error()
			out = append(out, d)
			continue
		}
		for _, iss := range issues {
			d.OpenIssues = append(d.OpenIssues, chat.PilotIssueRow{
				Number:    iss.Number,
				Title:     iss.Title,
				Labels:    iss.Labels,
				UpdatedAt: iss.UpdatedAt,
			})
		}
		prs, err := client.ListOpenPRs(name)
		if err != nil {
			// Issues fetched OK; just note the PR failure inline
			// rather than discarding the issue data.
			d.FetchError = "list PRs: " + err.Error()
		} else {
			for _, pr := range prs {
				d.OpenPRs = append(d.OpenPRs, chat.PilotPRRow{
					Number:    pr.Number,
					Title:     pr.Title,
					UpdatedAt: pr.UpdatedAt,
				})
			}
		}
		out = append(out, d)
	}
	return out
}

func clientFor(repo string, cfg *config.Config, creds *config.Credentials) (vcs.Client, error) {
	rc, ok := cfg.Repos[repo]
	if !ok {
		return nil, fmt.Errorf("repo %q not in config", repo)
	}
	platform := rc.Platform
	if platform == "" {
		platform = "github"
	}
	switch platform {
	case "github":
		return github.New(creds.GHToken, rc.BaseURL), nil
	case "gitlab":
		return gitlab.New(creds.GitLabToken, rc.BaseURL), nil
	default:
		return nil, fmt.Errorf("unsupported platform %q for repo %q", platform, repo)
	}
}

// pilotRecentSummaries adapts the most recent N PilotRunMeta entries
// for the project into the chat package's compact PilotWakeSummary
// rows. Keeps `chat` free of `snapshot` imports.
func pilotRecentSummaries(projectName string, n int) []chat.PilotWakeSummary {
	metas := snapshot.LastPilotRunSummaries(projectName, n)
	out := make([]chat.PilotWakeSummary, 0, len(metas))
	for _, m := range metas {
		out = append(out, chat.PilotWakeSummary{
			StartedAt: m.StartedAt.UTC().Format(time.RFC3339),
			Status:    m.Status,
			Result:    m.Result,
		})
	}
	return out
}

// extractResult pulls the last `PILOT-RESULT: ...` line out of the Pilot's
// stdout. Used purely for logging — the meaningful side effect (any
// new issues filed) already happened via the Pilot's Bash calls.
func extractResult(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "PILOT-RESULT:") {
			return s
		}
	}
	return ""
}
