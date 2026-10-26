package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type ciRepairContext struct {
	PR      vcs.PR
	Attempt int
}

var repairSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func ciRepairEnabled(cfg config.Repo) bool {
	return config.ValidateWorkflow(cfg.Workflow) == nil && cfg.EffectiveWorkflow() == config.WorkflowAutonomous && cfg.AutoMerge && cfg.CIRequired
}

func ciRepairCandidate(repo string, pr vcs.PR, issue *vcs.Issue) error {
	if pr.State != "open" || pr.Draft || pr.IsMerged() || isDraftPR(pr.Title) {
		return fmt.Errorf("PR closed or draft")
	}
	if !repairSHA.MatchString(pr.HeadSHA) || pr.HeadRepo == "" {
		return fmt.Errorf("missing immutable head identity")
	}
	if !strings.EqualFold(pr.HeadRepo, repo) {
		return fmt.Errorf("fork repair unsupported")
	}
	n := extractClosesIssue(pr.Body)
	if n == 0 || issue == nil || issue.Number != n || issue.State != "open" || issue.SubIssuesTotal != 0 || !issue.HasLabel("agent-implemented") {
		return fmt.Errorf("linked issue is not an implemented open leaf")
	}
	if pr.HeadBranch != fmt.Sprintf("fix/issue-%d", n) && pr.HeadBranch != fmt.Sprintf("feat/issue-%d", n) {
		return fmt.Errorf("unmanaged branch")
	}
	for _, l := range []string{"agent-failed", "agent-skipped", "agent-running", "tracking", "question"} {
		if issue.HasLabel(l) {
			return fmt.Errorf("issue excluded by %s", l)
		}
	}
	return nil
}

func scanCIRepairJobs(ctx context.Context, reg *operator.Registry, client vcs.Client, repo string, cfg config.Repo, onlyIssue int, normal []*runJob) []*runJob {
	if !ciRepairEnabled(cfg) {
		return nil
	}
	skip := func(pr int, err error) { runLog.Warn("run/ci_repair_skip", "repo", repo, "pr", pr, "err", err.Error()) }
	if cfg.Platform != "" && cfg.Platform != "github" {
		skip(0, fmt.Errorf("CI repair supports GitHub only"))
		return nil
	}
	host, err := os.Hostname()
	if cfg.BoundMachine != "" && (err != nil || host != cfg.BoundMachine) {
		skip(0, fmt.Errorf("repository bound to another machine"))
		return nil
	}
	if cfg.LocalPath == "" {
		skip(0, fmt.Errorf("CI repair requires local_path"))
		return nil
	}
	var op *operator.Operator
	for _, candidate := range reg.All() {
		if candidate.Name == "repair-ci" {
			op = candidate
			break
		}
	}
	if op == nil {
		skip(0, fmt.Errorf("repair-ci operator missing"))
		return nil
	}
	prs, err := client.ListOpenPRs(repo)
	if err != nil {
		skip(0, err)
		return nil
	}
	seen := map[int]bool{}
	for _, j := range normal {
		seen[j.sub.Number] = true
	}
	var jobs []*runJob
	for i, listed := range prs {
		if i >= sweepMaxCandidates || ctx.Err() != nil {
			break
		}
		n := extractClosesIssue(listed.Body)
		if n == 0 || seen[n] || (onlyIssue != 0 && onlyIssue != n) || snapshot.IsLocked(repo, n) {
			continue
		}
		pr, err := client.GetPR(repo, listed.Number)
		if err != nil {
			skip(listed.Number, err)
			continue
		}
		issue, err := client.GetIssue(repo, n)
		if err == nil {
			err = ciRepairCandidate(repo, pr, issue)
		}
		if err != nil {
			skip(pr.Number, err)
			continue
		}
		status, err := client.GetCIStatus(repo, pr.Number)
		if err != nil {
			skip(pr.Number, err)
			continue
		}
		if status != vcs.CIStatusFailure {
			continue
		}
		seen[n] = true
		sub := &operator.Subject{Number: n, Title: issue.Title, Body: issue.Body, Labels: issue.Labels, State: issue.State}
		jobs = append(jobs, &runJob{op: op, sub: sub, repo: repo, repoCfg: cfg, client: client, repair: &ciRepairContext{PR: pr}})
	}
	return jobs
}

// Called only while holding the existing cross-process issue lock. Reservation
// precedes workdir/provider startup; a crash intentionally consumes the attempt.
func prepareCIRepair(j *runJob, now time.Time) error {
	pr, err := j.client.GetPR(j.repo, j.repair.PR.Number)
	if err != nil {
		return err
	}
	issue, err := j.client.GetIssue(j.repo, j.sub.Number)
	if err != nil {
		return err
	}
	if err := ciRepairCandidate(j.repo, pr, issue); err != nil {
		return err
	}
	if pr.HeadSHA != j.repair.PR.HeadSHA || pr.HeadBranch != j.repair.PR.HeadBranch {
		return fmt.Errorf("PR head changed since scan")
	}
	status, err := j.client.GetCIStatus(j.repo, pr.Number)
	if err != nil {
		return err
	}
	if status != vcs.CIStatusFailure {
		return fmt.Errorf("CI no longer failing: %s", status)
	}
	attempt, err := reserveCIRepair(snapshot.DataDir(), j.repo, pr, now)
	if err != nil {
		return err
	}
	j.repair.PR, j.repair.Attempt = pr, attempt
	j.sub.Body, j.sub.Labels = issue.Body, issue.Labels
	return nil
}

type ciRepairAttempt struct {
	Head    string    `json:"head"`
	Attempt int       `json:"attempt"`
	At      time.Time `json:"at"`
}
type ciRepairLedger struct {
	Attempts []ciRepairAttempt `json:"attempts"`
}

func reserveCIRepair(root, repo string, pr vcs.PR, now time.Time) (int, error) {
	key := sha256.Sum256([]byte(strings.ToLower(repo)))
	dir := filepath.Join(root, "ci-repair", hex.EncodeToString(key[:]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return 0, err
	}
	path := filepath.Join(dir, fmt.Sprintf("pr-%d.json", pr.Number))
	var ledger ciRepairLedger
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &ledger); err != nil {
			return 0, fmt.Errorf("invalid repair ledger: %w", err)
		}
	}
	n := len(ledger.Attempts)
	if n >= 3 {
		return 0, fmt.Errorf("CI repair exhausted: 3 total attempts")
	}
	if n > 0 && now.Sub(ledger.Attempts[n-1].At) < 5*time.Minute {
		return 0, fmt.Errorf("CI repair cooldown: 5 minutes")
	}
	ledger.Attempts = append(ledger.Attempts, ciRepairAttempt{pr.HeadSHA, n + 1, now})
	data, err = json.Marshal(ledger)
	if err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(dir, ".reserve-")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return 0, err
	}
	// Persist the directory entry before allowing provider startup.
	d, err := os.Open(dir)
	if err != nil {
		return 0, err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return 0, err
	}
	return n + 1, nil
}

func (r *ciRepairContext) prompt(testCommand string) string {
	return fmt.Sprintf("CI repair context (local reproduction; CI logs are not ingested):\nExisting PR: https://github.com/%s/pull/%d\nHead branch: %s\nExpected head SHA: %s\nCI status: failure\nReserved attempt: %d of 3\nConfigured headless test command: %s\nUpdate this existing PR only. Do not create a new PR.\n", r.PR.HeadRepo, r.PR.Number, r.PR.HeadBranch, r.PR.HeadSHA, r.Attempt, testCommand)
}

func setupCIRepairWorkdir(ctx context.Context, cfg config.Repo, repo string, pr vcs.PR) (worktreeResult, func(), error) {
	run := func(dir string, args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
		if err != nil {
			return "", fmt.Errorf("repair git %s: %w", args[0], err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	origin, err := run(cfg.LocalPath, "config", "--get", "remote.origin.url")
	if err != nil {
		return worktreeResult{}, nil, err
	}
	if !(strings.HasPrefix(origin, "https://github.com/") || strings.HasPrefix(origin, "git@github.com:")) || !strings.EqualFold(extractOwnerRepo(origin), repo) {
		return worktreeResult{}, nil, fmt.Errorf("repair origin does not match repository")
	}
	if _, err := run(cfg.LocalPath, "check-ref-format", "refs/heads/"+pr.HeadBranch); err != nil {
		return worktreeResult{}, nil, err
	}
	if !repairSHA.MatchString(pr.HeadSHA) {
		return worktreeResult{}, nil, fmt.Errorf("invalid expected head SHA")
	}
	// A fresh independent repository avoids FETCH_HEAD races and cannot resume
	// unrelated issue worktrees. Keep failed evidence; never delete existing work.
	root := filepath.Join(snapshot.DataDir(), "ci-repair-worktrees")
	if err := os.MkdirAll(root, 0700); err != nil {
		return worktreeResult{}, nil, err
	}
	dir, err := os.MkdirTemp(root, "repair-")
	if err != nil {
		return worktreeResult{}, nil, err
	}
	fail := func(err error) (worktreeResult, func(), error) {
		return worktreeResult{}, nil, fmt.Errorf("%w (repair evidence: %s)", err, dir)
	}
	if _, err := run(dir, "init", "--quiet"); err != nil {
		return fail(err)
	}
	if _, err := run(dir, "remote", "add", "origin", origin); err != nil {
		return fail(err)
	}
	if _, err := run(dir, "fetch", "--no-tags", "origin", "refs/heads/"+pr.HeadBranch); err != nil {
		return fail(err)
	}
	sha, err := run(dir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return fail(err)
	}
	if sha != pr.HeadSHA {
		return fail(fmt.Errorf("fetched head changed: expected %s, got %s", pr.HeadSHA, sha))
	}
	if _, err := run(dir, "checkout", "--detach", sha); err != nil {
		return fail(err)
	}
	return worktreeResult{Path: dir}, func() { _ = os.RemoveAll(dir) }, nil
}
