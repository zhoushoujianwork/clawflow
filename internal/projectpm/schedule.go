// Package projectpm wakes per-project "project manager" agents at the
// end of each `clawflow run` pass. Each enabled project is fanned out
// to a non-interactive `claude -p` invocation that triages the
// backlog at the EDGES of the operator pipeline:
//
//   - upstream: files new issues, fixes missing trigger labels
//     (e.g. user opened a clear bug without the `bug` label)
//   - downstream: closes stale, duplicate, or already-fixed issues
//
// PM stays out of the middle (evaluate → ready-for-agent → implement
// → merge), which is fully owned by operators + repo-level
// `auto_approve` (auto-adds ready-for-agent after evaluate) +
// `auto_merge` (auto-merges agent-implemented PRs after CI). PM does
// NOT add ready-for-agent — that's auto_approve's domain. Trying to
// have PM apply judgment there would just race auto_approve.
//
// Closed loop:
//
//	clawflow run → operators process labeled issues →
//	  projectpm.Schedule wakes the PM →
//	  PM triages backlog (file/label/close/comment) →
//	  next `clawflow run` pass executes the changes
//
// PMs are the "what should we be working on, and is the backlog
// coherent?" layer; operators are the "execute the work that's
// already on the board" layer.
//
// Two safety rails are enforced via prompt (not code), since the PM
// invokes the same `clawflow` CLI users do and the CLI doesn't know
// whether its caller is a PM or a human:
//
//   - PM must skip issues carrying agent-running (operator mid-flight).
//   - PM must not duplicate its own prior actions; cooldown gives this
//     room to mean something. If runaway PM noise is observed in the
//     wild, the next iteration will add a pm-touched marker label
//     so the runner can enforce mechanically rather than by prompt.
package projectpm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

// Schedule fans out to every project with Automation.Enabled and
// past its cooldown. Sequential by design: PMs are heavy claude
// invocations, parallelism here would mostly buy rate-limit pain.
//
// Errors per-project are logged and swallowed — one stuck project
// must never block the rest. Returns the count of PMs actually
// woken, for the caller's run summary.
func Schedule(ctx context.Context, perWakeTimeout time.Duration) (int, error) {
	projects, err := project.ListAutomationEnabled()
	if err != nil {
		return 0, fmt.Errorf("list automation-enabled projects: %w", err)
	}
	if len(projects) == 0 {
		return 0, nil
	}

	now := time.Now()
	var ready []*project.Project
	for _, p := range projects {
		if rem := p.CooldownRemaining(now); rem > 0 {
			fmt.Fprintf(os.Stderr, "[pm] %s: cooldown %s remaining — skip\n", p.Name, rem.Round(time.Second))
			continue
		}
		ready = append(ready, p)
	}
	if len(ready) == 0 {
		return 0, nil
	}

	cfg, err := config.Load()
	if err != nil {
		return 0, fmt.Errorf("load config: %w", err)
	}
	creds, _ := config.LoadCredentials()

	woken := 0
	for _, p := range ready {
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "[pm] context canceled — stopping after %d wake(s)\n", woken)
			return woken, ctx.Err()
		default:
		}

		fmt.Fprintf(os.Stderr, "[pm] waking project %q (cooldown=%dmin)\n", p.Name, p.Automation.CooldownMinutes)
		// Stamp BEFORE the wake so a slow PM doesn't get re-fired
		// the instant it returns. If the wake fails we still want
		// the cooldown to apply — repeated failures shouldn't busy-
		// loop claude.
		if err := project.MarkWoken(p.Name); err != nil {
			fmt.Fprintf(os.Stderr, "[pm] %s: mark-woken failed: %v — continuing anyway\n", p.Name, err)
		}
		if err := wake(ctx, p, cfg, creds, perWakeTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "[pm] %s: wake failed: %v\n", p.Name, err)
			continue
		}
		woken++
	}
	return woken, nil
}

// wake builds the digest for one project, constructs the PM prompt,
// and invokes claude -p. The PM's stdout is streamed to the user's
// stderr by operator.RunClaude; we additionally echo the parsed
// PM-RESULT line so the run summary reads cleanly.
//
// Each wake is persisted to ~/.clawflow/dashboard/data/pm-runs/<project>/<ts>/
// with meta.json + events.jsonl so the dashboard can show PM activity.
func wake(ctx context.Context, p *project.Project, cfg *config.Config, creds *config.Credentials, timeout time.Duration) error {
	startedAt := time.Now()
	runDir := snapshot.PMRunDir(p.Name, startedAt)
	_ = os.MkdirAll(runDir, 0o755)

	meta := snapshot.PMRunMeta{
		Project:   p.Name,
		StartedAt: startedAt.UTC(),
		Status:    "running",
	}
	_ = snapshot.WritePMRunMeta(runDir, meta)

	digests := buildDigests(p, cfg, creds)
	contextMD, _ := project.ReadContext(p.Name)

	prompt := chat.BuildProjectPMContext(p.Name, contextMD, digests)
	model := creds.EffectiveOperatorModel()
	workdir := project.ProjectDir(p.Name)

	eventsFile, _ := os.Create(filepath.Join(runDir, "events.jsonl"))

	output, err := operator.RunClaude(ctx, prompt, workdir, timeout, eventsFile, model)
	if eventsFile != nil {
		_ = eventsFile.Close()
	}

	endedAt := time.Now().UTC()
	meta.EndedAt = &endedAt

	resultLine := extractResult(output)
	meta.Result = resultLine
	meta.Summary = output

	if err != nil {
		meta.Status = "failed"
		meta.Error = err.Error()
	} else {
		meta.Status = "success"
	}

	if u, uerr := snapshot.ExtractUsage(filepath.Join(runDir, "events.jsonl")); uerr == nil {
		meta.Usage = u
	}
	_ = snapshot.WritePMRunMeta(runDir, meta)

	if _, ierr := snapshot.WritePMRunsIndex(20); ierr != nil {
		fmt.Fprintf(os.Stderr, "[pm] %s: snapshot pm-runs index: %v\n", p.Name, ierr)
	}

	if resultLine != "" {
		fmt.Fprintf(os.Stderr, "[pm] %s: %s\n", p.Name, resultLine)
	} else if err == nil {
		fmt.Fprintf(os.Stderr, "[pm] %s: completed (no PM-RESULT line found)\n", p.Name)
	}
	return err
}

// buildDigests collects per-repo open-issue and open-PR snapshots.
// One repo's failure does not abort the others — the failed repo
// gets a row with FetchError populated so the PM can still see it.
func buildDigests(p *project.Project, cfg *config.Config, creds *config.Credentials) []chat.PMRepoDigest {
	out := make([]chat.PMRepoDigest, 0, len(p.Repos))
	for _, name := range p.Repos {
		d := chat.PMRepoDigest{Name: name}
		if rc, ok := cfg.Repos[name]; ok {
			d.LocalPath = rc.LocalPath
		}
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
			d.OpenIssues = append(d.OpenIssues, chat.PMIssueRow{
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
				d.OpenPRs = append(d.OpenPRs, chat.PMPRRow{
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

// extractResult pulls the last `PM-RESULT: ...` line out of the PM's
// stdout. Used purely for logging — the meaningful side effect (any
// new issues filed) already happened via the PM's Bash calls.
func extractResult(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "PM-RESULT:") {
			return s
		}
	}
	return ""
}
