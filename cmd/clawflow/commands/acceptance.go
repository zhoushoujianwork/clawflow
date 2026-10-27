package commands

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

const acceptanceFileLimit = 32 << 20

var acceptanceNativeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// These narrow boundaries allow isolated tests without network or live agents.
var acceptanceClientFactory = newVCSClient
var acceptanceNativeRun = execution.Run
var acceptanceGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.fsmonitor=false"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("acceptance git %s failed", args[0])
	}
	return strings.TrimSpace(string(out)), nil
}

func newAcceptanceCmd() *cobra.Command {
	root := &cobra.Command{Use: "acceptance", Short: "Run or inspect independent local acceptance evidence"}
	for _, run := range []bool{true, false} {
		run := run
		var repo string
		var pr int
		var timeout time.Duration
		var asJSON bool
		name := "status"
		if run {
			name = "run"
		}
		cmd := &cobra.Command{Use: name, Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if pr <= 0 {
					return errors.New("--pr must be positive")
				}
				ctx := cmd.Context()
				if run {
					if timeout <= 0 || timeout > 24*time.Hour {
						return errors.New("--timeout must be positive and at most 24h")
					}
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, timeout)
					defer cancel()
				}
				rc, _, err := acceptanceConfiguration(repo)
				if err != nil {
					return err
				}
				client, err := acceptanceClientFactory(rc)
				if err != nil {
					return errors.New("acceptance provider configuration unavailable")
				}
				if contextual, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
					contextual.SetRequestContext(ctx)
				}
				if run {
					record, err := runAcceptance(ctx, client, repo, pr)
					if record.AttemptID != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "Acceptance PR #%d: %s (attempt %s, planner_isolation=%s)\n", pr, record.Verdict, record.AttemptID, record.Plan.PlannerIsolation)
					}
					return err
				}
				c, record, err := currentAcceptance(ctx, client, repo, pr)
				status, reason := "passed", ""
				if err != nil {
					status, reason = "blocked", err.Error()
				}
				if asJSON {
					if e := json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
						PlannerIsolation string `json:"planner_isolation,omitempty"`
						Status           string `json:"status"`
						Reason           string `json:"reason,omitempty"`
						Head             string `json:"head_sha,omitempty"`
						Attempt          string `json:"attempt_id,omitempty"`
					}{record.Plan.PlannerIsolation, status, reason, c.HeadSHA, record.AttemptID}); e != nil {
						return e
					}
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Acceptance PR #%d: %s (planner_isolation=%s)\n", pr, status, record.Plan.PlannerIsolation)
					if reason != "" {
						fmt.Fprintln(cmd.OutOrStdout(), reason)
					}
				}
				return err
			}}
		cmd.Flags().StringVar(&repo, "repo", "", "Registered GitHub owner/repository")
		cmd.Flags().IntVar(&pr, "pr", 0, "Pull request number")
		_ = cmd.MarkFlagRequired("repo")
		_ = cmd.MarkFlagRequired("pr")
		if run {
			cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Total planning and review timeout")
		} else {
			cmd.Flags().BoolVar(&asJSON, "json", false, "Return current gate status as JSON")
		}
		root.AddCommand(cmd)
	}
	return root
}

// Read canonical configuration without config.Load's migration writes. Resolve
// role and environment precedence once; both phases use this pinned selection.
func acceptanceConfiguration(repo string) (config.Repo, execution.Config, error) {
	var cfg config.Config
	if err := readCheckYAML(config.ConfigPath(), &cfg); err != nil {
		return config.Repo{}, execution.Config{}, errors.New("acceptance requires readable canonical configuration")
	}
	rc, ok := cfg.Repos[repo]
	parts := strings.Split(repo, "/")
	if !ok || len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(repo, "\\\x00\r\n") || strings.Contains(repo, "..") {
		return rc, execution.Config{}, errors.New("acceptance requires a registered owner/repository")
	}
	if rc.Platform != "" && rc.Platform != "github" {
		return rc, execution.Config{}, errors.New("independent acceptance currently supports GitHub only")
	}
	c := execution.WithEnvironment(cfg.Settings.Execution)
	if p := c.RoleProfiles["acceptance"]; p != "" {
		c.Profile = p
	}
	if p, ok := os.LookupEnv("CLAWFLOW_OFFLOAD_PROFILE"); ok {
		c.Profile = p
	}
	c.RoleProfiles = nil
	if err := execution.ValidateConfig(c); err != nil {
		return rc, c, errors.New("acceptance requires an available Offload command and explicit profile")
	}
	command, err := exec.LookPath(c.Command)
	if err != nil {
		return rc, c, errors.New("acceptance execution command unavailable")
	}
	c.Command, err = filepath.Abs(command)
	if err != nil {
		return rc, c, errors.New("acceptance execution command path unavailable")
	}
	return rc, c, nil
}

func acceptanceHash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func acceptancePolicy(repo string, rc config.Repo, ec execution.Config) string {
	return acceptanceHash(struct {
		Version                                              int
		Repository, Platform, BaseURL, BaseBranch, LocalPath string
		CIRequired, AcceptanceRequired                       bool
		Command, Profile, CodexSandbox                       string
	}{1, repo, rc.Platform, rc.BaseURL, rc.BaseBranch, rc.LocalPath, rc.CIRequired, rc.AcceptanceRequired, ec.Command, ec.Profile, ec.CodexSandbox})
}

func acceptanceSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, ch := range s {
		if !(ch >= 'a' && ch <= 'f' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func acceptanceOrigin(ctx context.Context, rc config.Repo, repo string) (string, error) {
	if rc.LocalPath == "" {
		return "", errors.New("acceptance requires a configured local checkout")
	}
	if strings.HasPrefix(rc.LocalPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("acceptance local checkout unavailable")
		}
		rc.LocalPath = filepath.Join(home, rc.LocalPath[2:])
	}
	dir, err := filepath.Abs(rc.LocalPath)
	if err != nil {
		return "", errors.New("acceptance local checkout unavailable")
	}
	origin, err := acceptanceGit(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil || !(strings.HasPrefix(origin, "https://github.com/") || strings.HasPrefix(origin, "git@github.com:")) || !strings.EqualFold(extractOwnerRepo(origin), repo) {
		return "", errors.New("acceptance checkout origin does not match repository")
	}
	return origin, nil
}

func acceptanceContract(ctx context.Context, client vcs.Client, repo string, prNumber int) (acceptance.Contract, config.Repo, execution.Config, vcs.PR, error) {
	var c acceptance.Contract
	rc, ec, err := acceptanceConfiguration(repo)
	if err != nil {
		return c, rc, ec, vcs.PR{}, err
	}
	if err := ctx.Err(); err != nil {
		return c, rc, ec, vcs.PR{}, err
	}
	if _, err := acceptanceOrigin(ctx, rc, repo); err != nil {
		return c, rc, ec, vcs.PR{}, err
	}
	pr, err := client.GetPR(repo, prNumber)
	if err != nil {
		return c, rc, ec, pr, errors.New("acceptance PR unavailable")
	}
	issueNum := extractClosesIssue(pr.Body)
	if pr.Number != prNumber || pr.State != "open" || pr.IsMerged() || pr.Draft || isDraftPR(pr.Title) || !strings.EqualFold(pr.HeadRepo, repo) || issueNum <= 0 || !acceptanceSHA(pr.HeadSHA) || !acceptanceSHA(pr.BaseSHA) || pr.BaseBranch == "" || pr.HeadBranch == "" || (rc.BaseBranch != "" && pr.BaseBranch != rc.BaseBranch) {
		return c, rc, ec, pr, errors.New("acceptance requires an open same-repository non-draft PR with immutable head/base and a closing issue")
	}
	issue, err := client.GetIssue(repo, issueNum)
	if err != nil || issue == nil || issue.Number != issueNum || issue.State != "open" || strings.TrimSpace(issue.Title+issue.Body) == "" {
		return c, rc, ec, pr, errors.New("acceptance requirements issue unavailable or closed")
	}
	requirements := issue.Title + "\n\n" + issue.Body
	if len(requirements) > execution.MaxRequestBytes/2 {
		return c, rc, ec, pr, errors.New("acceptance requirements exceed size limit")
	}
	dev, err := acceptanceDevelopment(repo, issueNum, requirements)
	if err != nil {
		return c, rc, ec, pr, err
	}
	// This is a deterministic local content generation, not a durable counter.
	h := sha256.Sum256([]byte(requirements))
	generation := binary.BigEndian.Uint64(h[:8])
	if generation == 0 {
		generation = 1
	}
	policy := acceptanceHash(struct{ Policy, TargetBranch string }{acceptancePolicy(repo, rc, ec), pr.BaseBranch})
	c = acceptance.Contract{Version: acceptance.Version, TaskID: repo + ":issue:" + strconv.Itoa(issueNum), Generation: generation, Repository: repo, PRNumber: prNumber, HeadSHA: pr.HeadSHA, BaseSHA: pr.BaseSHA, PolicyDigest: policy, Requirements: requirements, Development: dev}
	// IntegrationSHA stays empty: GitHub's merge_commit_sha is not test evidence.
	return c, rc, ec, pr, c.Validate()
}

func acceptanceRead(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errors.New("acceptance local evidence unavailable or oversized")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("acceptance local evidence unreadable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("acceptance local evidence exceeds limit")
	}
	return b, nil
}

func acceptanceEvent(raw []byte) (execution.Event, error) {
	var e execution.Event
	d := json.NewDecoder(bytes.NewReader(raw))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return e, errors.New("invalid native event")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || fields[name] != nil {
			return e, errors.New("ambiguous native event fields")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return e, errors.New("invalid native event field")
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil || json.Unmarshal(raw, &e) != nil {
		return e, errors.New("invalid native event")
	}
	if _, err := d.Token(); err != io.EOF {
		return e, errors.New("trailing native event data")
	}
	if _, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil {
		return e, errors.New("invalid native event timestamp")
	}
	if e.Type == "final" && !bytes.Equal(bytes.TrimSpace(fields["exit_code"]), []byte("0")) {
		return e, errors.New("native final lacks explicit success exit code")
	}
	if e.Type != "start" && e.Type != "progress" && e.Type != "final" {
		return e, errors.New("unknown native event type")
	}
	return e, nil
}

// Only ClawFlow's completed local run metadata and sanitized native event
// sequence supply developer identity. Never fall back past newer failed work.
func acceptanceDevelopment(repo string, issue int, requirements string) (acceptance.Identity, error) {
	parent := filepath.Dir(snapshot.RunDir(repo, issue, time.Now()))
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) > 10000 {
		return acceptance.Identity{}, errors.New("trusted development provenance unavailable")
	}
	var latest snapshot.RunMeta
	var latestDir string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		b, err := acceptanceRead(filepath.Join(parent, entry.Name(), "meta.json"), 1<<20)
		var m snapshot.RunMeta
		if err != nil || json.Unmarshal(b, &m) != nil {
			return acceptance.Identity{}, errors.New("unreadable development run provenance")
		}
		if m.Operator != "implement" && m.Operator != "repair-ci" {
			continue
		}
		if m.Repo != repo || m.IssueNumber != issue || m.StartedAt.IsZero() {
			return acceptance.Identity{}, errors.New("development run identity mismatch")
		}
		if latestDir != "" && m.StartedAt.Equal(latest.StartedAt) {
			return acceptance.Identity{}, errors.New("ambiguous development run provenance")
		}
		if latestDir == "" || m.StartedAt.After(latest.StartedAt) {
			latest, latestDir = m, filepath.Join(parent, entry.Name())
		}
	}
	if latestDir == "" || latest.Status != "success" || latest.EndedAt == nil || latest.EndedAt.Before(latest.StartedAt) {
		return acceptance.Identity{}, errors.New("latest development run is absent, incomplete or unsuccessful")
	}
	metaBytes, err := acceptanceRead(filepath.Join(latestDir, "meta.json"), 1<<20)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(metaBytes, &fields) != nil || fields["issue_body"] == nil || latest.IssueTitle+"\n\n"+latest.IssueBody != requirements {
		return acceptance.Identity{}, errors.New("development requirements snapshot missing or changed; a new managed development run is required")
	}
	b, err := acceptanceRead(filepath.Join(latestDir, "events.jsonl"), acceptanceFileLimit)
	if err != nil {
		return acceptance.Identity{}, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	scanner.Buffer(make([]byte, 4096), execution.MaxEventBytes)
	var start execution.Event
	var session, backend, model string
	seq, finalized := 0, false
	for scanner.Scan() {
		e, err := acceptanceEvent(scanner.Bytes())
		if err != nil || e.Version != 1 || e.Seq != seq+1 || finalized || !acceptanceNativeID.MatchString(e.RunID) || !acceptanceNativeID.MatchString(e.CallerRunID) {
			return acceptance.Identity{}, errors.New("ambiguous native development events")
		}
		if seq == 0 {
			if e.Type != "start" {
				return acceptance.Identity{}, errors.New("missing native development start")
			}
			start = e
		} else if e.Type == "start" || e.RunID != start.RunID || e.CallerRunID != start.CallerRunID {
			return acceptance.Identity{}, errors.New("native development identity changed")
		}
		// Offload can emit start/progress before learning the native session.
		// Once bound, provenance may neither switch nor lose that identity.
		if (e.Session != "" && !acceptanceNativeID.MatchString(e.Session)) || (session != "" && e.Session != session) {
			return acceptance.Identity{}, errors.New("native development session changed or disappeared")
		}
		if (backend != "" && e.Backend != "" && backend != e.Backend) || (model != "" && e.Model != "" && model != e.Model) {
			return acceptance.Identity{}, errors.New("native development backend identity changed")
		}
		if e.Type == "final" {
			if e.Session == "" || (backend != "" && e.Backend != backend) || (model != "" && e.Model != model) || e.Status != "ok" || e.ExitCode != 0 || e.ErrorKind != "" {
				return acceptance.Identity{}, errors.New("native development execution unsuccessful")
			}
			finalized = true
		}
		if e.Session != "" {
			session = e.Session
		}
		if e.Backend != "" {
			backend = e.Backend
		}
		if e.Model != "" {
			model = e.Model
		}
		seq++
	}
	if scanner.Err() != nil || !finalized || seq < 2 || !bytes.HasSuffix(b, []byte("\n")) {
		return acceptance.Identity{}, errors.New("incomplete native development evidence")
	}
	return acceptance.Identity{SessionID: session, RunID: start.RunID}, nil
}

func acceptanceStorePath(repo string, pr int) string {
	return filepath.Join(snapshot.DataDir(), "acceptance", acceptanceHash(repo), strconv.Itoa(pr))
}

// Reuse atomic PID-owned local lockfiles in a separate hashed namespace. This
// excludes concurrent local review/merge; it is not distributed fencing.
func acceptanceLock(repo string, pr int) (func(), error) {
	key := "acceptance-" + acceptanceHash(repo)
	if pr <= 0 {
		return nil, errors.New("acceptance PR must be positive")
	}
	if err := snapshot.AcquireLock(key, pr, "acceptance"); err != nil {
		return nil, errors.New("acceptance review or merge is already active locally")
	}
	return func() { snapshot.ReleaseLock(key, pr) }, nil
}

func acceptanceEnrollment(repo string) (bool, error) {
	var cfg config.Config
	if err := readCheckYAML(config.ConfigPath(), &cfg); err != nil {
		return false, errors.New("merge blocked: repository policy configuration unreadable")
	}
	return cfg.Repos[repo].AcceptanceRequired, nil
}

// Caller holds acceptanceLock across contract capture and every retry. AutoMerge
// enablement is deliberately irrelevant to explicit human-requested merge.
func conditionalAcceptedMerge(ctx context.Context, client vcs.Client, repo string, pr int, accepted acceptance.Contract) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conditional, ok := client.(vcs.ConditionalMerger)
	if !ok {
		return &mergeGateError{reason: mergeReasonConditionalUnsupp, warn: true, err: errors.New("acceptance requires conditional merge capability")}
	}
	rc, _, err := acceptanceConfiguration(repo)
	if err != nil {
		return err
	}
	if rc.CIRequired {
		status, err := client.GetCIStatus(repo, pr)
		if err != nil || status != vcs.CIStatusSuccess {
			return gateBlock(mergeReasonCINotSuccessful, errors.New("acceptance merge blocked: required CI is not successful"))
		}
	}
	status, err := client.GetPRMergeability(repo, pr)
	if err != nil || status != vcs.MergeStatusClean {
		return gateBlock(mergeReasonNotMergeable, errors.New("acceptance merge blocked: PR is not mergeable"))
	}
	fresh, _, err := currentAcceptance(ctx, client, repo, pr)
	if err != nil {
		return gateBlock(acceptanceBlockReason(err), fmt.Errorf("acceptance merge blocked: %w; run clawflow acceptance run --repo %s --pr %d", err, repo, pr))
	}
	oldDigest, err := accepted.Digest()
	if err != nil {
		return err
	}
	newDigest, err := fresh.Digest()
	if err != nil || oldDigest != newDigest {
		return gateBlock(mergeReasonAcceptanceStale, errors.New("acceptance merge blocked: accepted identity changed"))
	}
	sha, err := conditional.MergePRAtHead(repo, pr, accepted.HeadSHA)
	if err == nil && !acceptanceSHA(sha) {
		return errors.New("conditional merge did not confirm a valid merge SHA")
	}
	return err
}

var acceptanceMergeClient = newVCSClientForRepo

func explicitAcceptanceMerge(ctx context.Context, client vcs.Client, repo string, pr int) error {
	required, err := acceptanceEnrollment(repo)
	if err != nil {
		return err
	}
	if !required {
		return client.MergePR(repo, pr)
	}
	unlock, err := acceptanceLock(repo, pr)
	if err != nil {
		return err
	}
	defer unlock()
	accepted, _, err := currentAcceptance(ctx, client, repo, pr)
	if err != nil {
		return fmt.Errorf("acceptance blocked: %w; run clawflow acceptance run --repo %s --pr %d", err, repo, pr)
	}
	return conditionalAcceptedMerge(ctx, client, repo, pr, accepted)
}

// errAcceptanceMissing marks the absence of acceptance evidence for the current
// contract (no private store, or no completed record). It is distinct from
// unreadable or corrupt evidence and from contract-construction failures, none
// of which may trigger a new acceptance run.
var errAcceptanceMissing = errors.New("acceptance evidence missing")

// missingAcceptanceError keeps the historical message text while matching
// errAcceptanceMissing through errors.Is.
type missingAcceptanceError string

func (e missingAcceptanceError) Error() string        { return string(e) }
func (e missingAcceptanceError) Is(target error) bool { return target == errAcceptanceMissing }

func missingAcceptance(msg string) error { return missingAcceptanceError(msg) }

func currentAcceptance(ctx context.Context, client vcs.Client, repo string, pr int) (acceptance.Contract, acceptance.Record, error) {
	c, _, _, _, err := acceptanceContract(ctx, client, repo, pr)
	if err != nil {
		return c, acceptance.Record{}, err
	}
	path := acceptanceStorePath(repo, pr)
	// Status/gate never create storage. OpenStore can mkdir, so reject absent or
	// non-private roots first; existing stores are opened without changing files.
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return c, acceptance.Record{}, missingAcceptance("no private acceptance evidence store")
	}
	store, err := acceptance.OpenStore(path)
	if err != nil {
		return c, acceptance.Record{}, errors.New("acceptance evidence unavailable")
	}
	defer store.Close()
	r, err := store.LatestRecord(c)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, r, missingAcceptance("no current completed acceptance evidence")
		}
		return c, r, errors.New("no current completed acceptance evidence")
	}
	return c, r, acceptance.Evaluate(c, r)
}

// acceptanceRemoveRemotes deletes every configured remote of the review checkout.
func acceptanceRemoveRemotes(ctx context.Context, dir string) error {
	out, err := acceptanceGit(ctx, dir, "remote")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(out) {
		if _, err := acceptanceGit(ctx, dir, "remote", "remove", name); err != nil {
			return err
		}
	}
	return nil
}

// acceptanceReviewerEnv returns env without VCS or ClawFlow token variables.
func acceptanceReviewerEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		switch upper {
		case "GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN":
			continue
		}
		if strings.HasPrefix(upper, "CLAWFLOW_") && strings.Contains(upper, "TOKEN") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func runAcceptance(ctx context.Context, client vcs.Client, repo string, prNumber int) (acceptance.Record, error) {
	unlock, err := acceptanceLock(repo, prNumber)
	if err != nil {
		return acceptance.Record{}, err
	}
	defer unlock()
	c, rc, ec, pr, err := acceptanceContract(ctx, client, repo, prNumber)
	if err != nil {
		return acceptance.Record{}, err
	}
	origin, err := acceptanceOrigin(ctx, rc, repo)
	if err != nil {
		return acceptance.Record{}, err
	}
	scratch, err := os.MkdirTemp("", "clawflow-acceptance-plan-")
	if err != nil {
		return acceptance.Record{}, err
	}
	defer os.RemoveAll(scratch)
	var workspace string
	defer func() {
		if workspace != "" {
			os.RemoveAll(workspace)
		}
	}()
	prepare := func(ctx context.Context) (string, error) {
		workspace, err = os.MkdirTemp("", "clawflow-acceptance-review-")
		if err != nil {
			return "", err
		}
		// An independent detached Git workspace avoids mutating the user's checkout
		// or sharing FETCH_HEAD with implement/repair. Only these temp dirs are removed.
		for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", origin}, {"check-ref-format", "refs/heads/" + pr.HeadBranch}, {"fetch", "--no-tags", "origin", "refs/heads/" + pr.HeadBranch}} {
			if _, err := acceptanceGit(ctx, workspace, args...); err != nil {
				return "", err
			}
		}
		fetched, err := acceptanceGit(ctx, workspace, "rev-parse", "FETCH_HEAD")
		if err != nil || fetched != c.HeadSHA {
			return "", errors.New("acceptance fetched PR head changed")
		}
		if _, err := acceptanceGit(ctx, workspace, "checkout", "--detach", c.HeadSHA); err != nil {
			return "", err
		}
		// The reviewer may run arbitrary commands here, so leave it nothing to push to.
		if err := acceptanceRemoveRemotes(ctx, workspace); err != nil {
			return "", err
		}
		return workspace, nil
	}
	store, err := acceptance.OpenStore(acceptanceStorePath(repo, prNumber))
	if err != nil {
		return acceptance.Record{}, errors.New("cannot open private acceptance store")
	}
	defer store.Close()
	inspect := func(ctx context.Context, workspace string) (acceptance.Source, error) {
		current, _, _, _, err := acceptanceContract(ctx, client, repo, prNumber)
		if err != nil {
			return acceptance.Source{}, err
		}
		digest, _ := current.Digest()
		original, _ := c.Digest()
		if digest != original {
			return acceptance.Source{}, errors.New("acceptance requirements, provenance or policy changed")
		}
		head, err := acceptanceGit(ctx, workspace, "rev-parse", "HEAD")
		if err != nil {
			return acceptance.Source{}, err
		}
		// Build and test artifacts (untracked or modified files) are allowed. The
		// proof is that HEAD is the contract head and no other commit exists.
		extra, err := acceptanceGit(ctx, workspace, "rev-list", "-n", "1", "--all", "--not", c.HeadSHA)
		if err != nil {
			return acceptance.Source{}, err
		}
		return acceptance.Source{HeadSHA: head, BaseSHA: current.BaseSHA, Clean: head == c.HeadSHA && extra == ""}, nil
	}
	execute := func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
		timeout := 30 * time.Minute
		if deadline, ok := ctx.Deadline(); ok {
			timeout = time.Until(deadline)
		}
		if timeout <= 0 {
			return execution.Result{}, context.DeadlineExceeded
		}
		return acceptanceNativeRun(ctx, ec, execution.Request{Version: 1, RequestID: execution.NewSessionKey(), Workspace: opts.Workspace, Prompt: prompt, Instructions: opts.Instructions, PermissionMode: opts.PermissionMode, ToolsDisabled: opts.ToolsDisabled, AllowedTools: opts.AllowedTools, DisallowedTools: opts.DisallowedTools, TimeoutMS: max(1, timeout.Milliseconds())}, acceptanceReviewerEnv(os.Environ()), opts.Events, nil)
	}
	r, err := (acceptance.Runner{Store: store, Execute: execute, Inspect: inspect, Prepare: prepare, TestCommand: rc.TestCommand}).Run(ctx, c, scratch, workspace)
	if err == nil {
		// Run returns nil only after the reviewer report was parsed and the
		// record was saved, so a failed/blocked verdict reaching here is a
		// recorded judgement, not an infrastructure error.
		if err = acceptance.Evaluate(c, r); err != nil && (r.Verdict == acceptance.Failed || r.Verdict == acceptance.Blocked) {
			err = recordedNonpassError{err}
		}
	}
	return r, err
}

// errAcceptanceNonpass marks a recorded reviewer verdict of failed or blocked,
// as opposed to a setup, provider or storage error while producing a verdict.
var errAcceptanceNonpass = errors.New("acceptance recorded a nonpassing verdict")

// recordedNonpassError keeps the Evaluate message and matches errAcceptanceNonpass.
type recordedNonpassError struct{ error }

func (e recordedNonpassError) Is(target error) bool { return target == errAcceptanceNonpass }
func (e recordedNonpassError) Unwrap() error        { return e.error }
