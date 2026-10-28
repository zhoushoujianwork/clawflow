package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// pipelineLocalClient supplies scheduler issues without another provider scan.
// In offline mode it also reuses the last known PR evidence.
type pipelineLocalClient struct {
	vcs.Client
	issues        []snapshot.IssueEntry
	board         pipeline.RepoBoard
	offline       bool
	snapshotAt    time.Time
	missingIssues bool
}

func (c *pipelineLocalClient) SetRequestContext(ctx context.Context) {
	if x, ok := c.Client.(interface{ SetRequestContext(context.Context) }); ok {
		x.SetRequestContext(ctx)
	}
}
func (c *pipelineLocalClient) ListOpenIssues(repo string) ([]vcs.Issue, error) {
	out := []vcs.Issue{}
	haveRepo := false
	for _, i := range c.issues {
		if i.Repo == repo {
			haveRepo = true
		}
		if i.Repo == repo && i.State == "open" {
			out = append(out, vcs.Issue{Number: i.IssueNumber, Title: i.IssueTitle, State: i.State, Labels: i.Labels, UpdatedAt: i.CapturedAt.UTC().Format(time.RFC3339)})
		}
	}
	if !haveRepo && !c.offline {
		return c.Client.ListOpenIssues(repo)
	}
	return out, nil
}
func (c *pipelineLocalClient) ListSubIssues(repo string, n int) ([]vcs.Issue, error) {
	out := []vcs.Issue{}
	for _, i := range c.issues {
		if i.Repo == repo && i.ParentNumber != nil && *i.ParentNumber == n {
			out = append(out, vcs.Issue{Number: i.IssueNumber, Title: i.IssueTitle, State: i.State, Labels: i.Labels})
		}
	}
	return out, nil
}
func (c *pipelineLocalClient) ListRecentPRs(repo, state string, limit int) ([]vcs.PR, error) {
	if state == "open" {
		if captured, err := snapshot.ReadPipelinePRs(repo); err == nil && ((c.offline && !c.snapshotAt.After(captured.CapturedAt)) || (!c.offline && time.Since(captured.CapturedAt) < 20*time.Second)) {
			return captured.PRs, nil
		}
	}
	if !c.offline {
		if l, ok := c.Client.(pipelineRecentPRLister); ok {
			return l.ListRecentPRs(repo, state, limit)
		}
		if state == "open" {
			return c.Client.ListOpenPRs(repo)
		}
		return nil, nil
	}
	out := []vcs.PR{}
	for _, i := range c.board.Items {
		p := i.PR
		if p == nil || (state == "open") != (p.State == "open") {
			continue
		}
		pr := vcs.PR{Body: "Fixes #" + strconv.Itoa(i.Issue), Number: p.Number, URL: p.URL, Title: p.Title, State: p.State, HeadBranch: p.HeadBranch, HeadSHA: p.HeadSHA, Draft: p.Draft, UpdatedAt: p.UpdatedAt}
		if i.Delivery != nil {
			pr.MergedAt = i.Delivery.MergedAt
			pr.MergeCommitSHA = i.Delivery.MergeCommitSHA
		}
		out = append(out, pr)
	}
	return out, nil
}
func (c *pipelineLocalClient) GetCIStatus(repo string, n int) (vcs.CIStatus, error) {
	if !c.offline {
		return c.Client.GetCIStatus(repo, n)
	}
	for _, i := range c.board.Items {
		if i.PR != nil && i.PR.Number == n && c.sameHead(repo, i.PR) {
			return vcs.CIStatus(i.PR.CI), nil
		}
	}
	return vcs.CIStatus("unknown"), nil
}
func (c *pipelineLocalClient) GetPRMergeability(repo string, n int) (vcs.MergeStatus, error) {
	if !c.offline {
		return c.Client.GetPRMergeability(repo, n)
	}
	for _, i := range c.board.Items {
		if i.PR != nil && i.PR.Number == n && c.sameHead(repo, i.PR) {
			return vcs.MergeStatus(i.PR.Mergeable), nil
		}
	}
	return vcs.MergeStatus("unknown"), nil
}

func pipelineSnapshotDeps(d pipelineDeps, offline bool) pipelineDeps {
	old, _ := snapshot.ReadPipeline()
	d.previous = map[string]pipeline.RepoBoard{}
	for _, b := range old.Repos {
		d.previous[b.Repo] = b
	}
	var issues []snapshot.IssueEntry
	data, err := os.ReadFile(filepath.Join(snapshot.DataDir(), "issues.json"))
	haveIssues := err == nil && json.Unmarshal(data, &issues) == nil
	factory := d.newClient
	d.newClient = func(rc config.Repo) (vcs.Client, error) {
		var client vcs.Client
		var err error
		if !offline {
			client, err = factory(rc)
			if err != nil {
				return nil, err
			}
		}
		if !offline && !haveIssues {
			return client, nil
		}
		// buildRepoBoard supplies the repository-specific previous board below.
		return &pipelineLocalClient{Client: client, issues: issues, offline: offline, snapshotAt: pipeline.ParseTime(old.GeneratedAt), missingIssues: !haveIssues}, nil
	}
	if !offline {
		readAcceptance := d.acceptance
		d.acceptance = func(ctx context.Context, client vcs.Client, repo string, rc config.Repo, pr vcs.PR, now time.Time) (*pipeline.AcceptanceInfo, error) {
			if local, ok := client.(*pipelineLocalClient); ok {
				client = local.Client
			}
			return readAcceptance(ctx, client, repo, rc, pr, now)
		}
	}
	if offline {
		d.acceptance = pipelineLocalAcceptance
	}
	return d
}

// writePassPipeline publishes only local evidence; it adds no VCS calls to a pass.
func writePassPipeline() error {
	d, err := defaultPipelineDeps()
	if err != nil {
		return err
	}
	return snapshot.WritePipeline(buildPipeline(context.Background(), pipelineSnapshotDeps(d, true)))
}

// Do not carry CI or mergeability evidence across a scheduler-observed push.
func (c *pipelineLocalClient) sameHead(repo string, p *pipeline.PRInfo) bool {
	captured, err := snapshot.ReadPipelinePRs(repo)
	if err != nil || c.snapshotAt.After(captured.CapturedAt) {
		return true
	}
	for _, pr := range captured.PRs {
		if pr.Number == p.Number {
			return pr.HeadSHA == p.HeadSHA
		}
	}
	return false
}

// pipelineLocalAcceptance reads newly completed records without reconstructing
// the live contract (which requires VCS calls). Valid remains false offline.
func pipelineLocalAcceptance(_ context.Context, _ vcs.Client, repo string, rc config.Repo, pr vcs.PR, _ time.Time) (*pipeline.AcceptanceInfo, error) {
	path := acceptanceStorePath(repo, pr.Number)
	st, err := os.Lstat(path)
	if os.IsNotExist(err) && !rc.AcceptanceRequired {
		return nil, nil
	}
	info := &pipeline.AcceptanceInfo{Required: rc.AcceptanceRequired, MaxInfraRetries: harvestMaxInfra, Running: snapshot.IsLocked("acceptance-"+acceptanceHash(repo), pr.Number)}
	if os.IsNotExist(err) {
		return info, nil
	}
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		info.Error = "acceptance evidence unavailable"
		return info, errors.New(info.Error)
	}
	store, err := acceptance.OpenStore(path)
	if err != nil {
		info.Error = "acceptance evidence unavailable"
		return info, errors.New(info.Error)
	}
	defer store.Close()
	records, err := store.CompleteRecordsAtHead(repo, pr.Number, pr.HeadSHA)
	if err != nil {
		info.Error = "acceptance evidence unreadable"
		return info, errors.New(info.Error)
	}
	return pipelineAcceptanceRecords(info, acceptance.Contract{}, records, false), nil
}

// The scheduler's all-issues snapshot is capped and cannot prove absence.
func (c *pipelineLocalClient) hasSnapshotIssue(repo string, number int) bool {
	for _, issue := range c.issues {
		if issue.Repo == repo && issue.IssueNumber == number {
			return true
		}
	}
	return false
}

func (c *pipelineLocalClient) missingPRIssue(repo string, number int) (*vcs.Issue, error) {
	if !c.offline {
		issue, err := c.Client.GetIssue(repo, number)
		if err != nil {
			return nil, err
		}
		if issue != nil && issue.Number != number {
			return nil, errors.New("issue identity mismatch")
		}
		return issue, nil
	}
	// Pass-end publication performs no VCS reads. Retain previously observed
	// open issues behind active PRs instead of interpreting a capped page as closed.
	for _, item := range c.board.Items {
		if item.Issue == number && item.PR != nil && item.PR.State == "open" {
			return &vcs.Issue{Number: number, Title: item.Title, State: "open", UpdatedAt: item.UpdatedAt}, nil
		}
	}
	return nil, errors.New("issue absent from partial scheduler snapshot")
}
