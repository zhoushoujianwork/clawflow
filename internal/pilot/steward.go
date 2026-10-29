package pilot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

const DefaultPrompt = "review platform state"
const Platform = "platform"

var runWakeOffload = operator.RunOffload

// StewardResult carries the authoritative answer and audited effects.
type StewardResult struct {
	Answer  string
	Actions []ActionRecord
}

func CharterPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "pilot", "charter.md")
}

// EnsureCharter seeds the global charter once, preserving project context as
// implementation context. O_EXCL ensures concurrent first sessions never overwrite it.
func EnsureCharter() (string, error) {
	path := CharterPath()
	if data, err := os.ReadFile(path); err == nil {
		return string(data), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	projects, err := project.List()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Platform steward charter\n\nServe the whole enabled platform on user request. Preserve repository policy gates and owner decisions.\n")
	for _, p := range projects {
		body, err := project.ReadContext(p.Name)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if strings.TrimSpace(body) != "" {
			fmt.Fprintf(&b, "\n## Initial context: %s\n\n%s\n", p.Name, body)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		data, err := os.ReadFile(path)
		return string(data), err
	}
	if err != nil {
		return "", err
	}
	_, writeErr := f.WriteString(b.String())
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	return b.String(), closeErr
}

// BuildPlatformContext uses cached evidence, so starting a conversation does not
// depend on every provider being reachable. Missing evidence is stated explicitly.
func BuildPlatformContext(cfg *config.Config, charter, message string) (string, []string) {
	repos := make([]string, 0, len(cfg.EnabledRepos()))
	for name := range cfg.EnabledRepos() {
		repos = append(repos, name)
	}
	slices.Sort(repos)
	board, boardErr := snapshot.ReadPipeline()
	filtered := pipeline.Snapshot{GeneratedAt: board.GeneratedAt}
	for _, r := range board.Repos {
		if slices.Contains(repos, r.Repo) {
			filtered.Repos = append(filtered.Repos, r)
		}
	}
	for _, q := range board.GlobalQueue {
		if slices.Contains(repos, q.Repo) {
			filtered.GlobalQueue = append(filtered.GlobalQueue, q)
		}
	}
	events, _ := pipeline.ReadEventsTail(filepath.Join(filepath.Dir(snapshot.DataDir()), "logs", "run.log"), 1<<20)
	for _, e := range events {
		if slices.Contains(repos, e.Fields["repo"]) {
			filtered.Events = append(filtered.Events, e)
		}
	}
	runs := []snapshot.RunMeta{}
	for _, r := range snapshot.ReadRunsIndex() {
		if slices.Contains(repos, r.Repo) {
			// Include results/usage, never duplicate arbitrary issue bodies in history.
			r.IssueBody = ""
			runs = append(runs, r.RunMeta)
			if len(runs) == 50 {
				break
			}
		}
	}
	evidence := struct {
		Repos      []string                `json:"enabled_repos"`
		Board      pipeline.Snapshot       `json:"board"`
		BoardError string                  `json:"board_error,omitempty"`
		Runs       []snapshot.RunMeta      `json:"recent_runs_and_usage"`
		Recent     []chat.PilotWakeSummary `json:"recent_steward_results"`
	}{Repos: repos, Board: filtered, Runs: runs, Recent: pilotRecentSummaries(Platform, 5)}
	if boardErr != nil {
		evidence.BoardError = boardErr.Error()
	}
	raw, _ := json.Marshal(evidence)
	return chat.BuildStewardContext(charter, message, string(raw), cfg.Settings.Language), repos
}

// Ask is the sole invocation path, shared by CLI conversations and user checks.
// There is deliberately no scheduling, cooldown, or project automation admission.
func Ask(ctx context.Context, message string, ops int, timeout time.Duration) (StewardResult, error) {
	var result StewardResult
	if strings.TrimSpace(message) == "" || ops <= 0 || timeout <= 0 {
		return result, fmt.Errorf("message, positive ops budget and timeout are required")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		return result, err
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		return result, err
	}
	charter, err := EnsureCharter()
	if err != nil {
		return result, err
	}
	prompt, repos := BuildPlatformContext(cfg, charter, message)
	start := time.Now().UTC()
	id := fmt.Sprintf("%x", start.UnixNano())
	prompt += fmt.Sprintf("\nOps budget: %d actions (existing daily/creation limits also apply).\nWake run_id: %s\n", ops, id)
	dir := snapshot.PilotRunDir(Platform, start) + "-" + id
	if err := os.MkdirAll(dir, 0700); err != nil {
		return result, err
	}
	meta := snapshot.PilotRunMeta{Project: Platform, StartedAt: start, Status: "running"}
	if err := snapshot.WritePilotRunMeta(dir, meta); err != nil {
		return result, err
	}
	events, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return result, err
	}
	defer events.Close()
	ctx = operator.WithExecutionResultObserver(ctx, func(r execution.Result) {
		meta.Profile, meta.Backend, meta.Model = r.Profile, r.Backend, r.Model
		meta.Usage = snapshot.UsageFromResult(r)
	})
	result.Answer, err = runWakeOffload(ctx, prompt, filepath.Dir(CharterPath()), timeout, events, config.RolePilot)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && strings.Contains(result.Answer, "```pilot-actions") {
		var set ActionSet
		set, err = ParseActions(result.Answer)
		if err == nil {
			set.RunID = id
			executor := NewActionExecutor(cfg, creds, repos, "pilot")
			executor.MaxWake = ops
			executor.Context = ctx
			baseClient := executor.Client
			executor.Client = func(repo string) (vcs.Client, error) {
				c, err := baseClient(repo)
				if bounded, ok := c.(interface{ SetRequestContext(context.Context) }); ok {
					bounded.SetRequestContext(ctx)
				}
				return c, err
			}
			result.Actions, err = executor.Apply(set, false)
			for _, r := range result.Actions {
				if r.Outcome != "applied" && r.Outcome != "already_applied" {
					err = fmt.Errorf("pilot action %s: %s (%s)", r.Action.ID, r.Outcome, r.Reason)
				}
			}
		}
	}
	end := time.Now().UTC()
	meta.EndedAt, meta.Summary, meta.Result = &end, result.Answer, extractResult(result.Answer)
	if meta.Result == "" {
		meta.Result = strings.TrimSpace(result.Answer)
		if len(meta.Result) > 500 {
			meta.Result = meta.Result[:500]
		}
	}
	meta.Status = "success"
	if err != nil {
		meta.Status, meta.Error = classifyWakeStatus(err, result.Answer), err.Error()
	}
	if saveErr := snapshot.WritePilotRunMeta(dir, meta); saveErr != nil {
		return result, saveErr
	}
	return result, err
}

// WakeOne keeps old callers source compatible; its project argument is ignored.
func WakeOne(ctx context.Context, _ string, timeout time.Duration) error {
	_, err := Ask(ctx, DefaultPrompt, 5, timeout)
	return err
}
