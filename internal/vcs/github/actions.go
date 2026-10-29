package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/actions"
)

type actionsRepo struct {
	ID       int64
	FullName string `json:"full_name"`
}
type actionsPR struct {
	Number int
	Head   struct {
		SHA  string
		Repo actionsRepo
	}
	Base struct {
		SHA  string
		Repo actionsRepo
	}
}
type actionsRun struct {
	ID                              int64
	WorkflowID                      int64  `json:"workflow_id"`
	Attempt                         int    `json:"run_attempt"`
	HeadSHA                         string `json:"head_sha"`
	SuiteID                         int64  `json:"check_suite_id"`
	URL                             string `json:"html_url"`
	Path, Event, Status, Conclusion string
	Repository                      actionsRepo
	HeadRepository                  actionsRepo `json:"head_repository"`
	PRs                             []actionsPR `json:"pull_requests"`
	StartedAt                       time.Time   `json:"run_started_at"`
}
type actionsJob struct {
	ID                       int64
	RunID                    int64  `json:"run_id"`
	Attempt                  int    `json:"run_attempt"`
	HeadSHA                  string `json:"head_sha"`
	CheckURL                 string `json:"check_run_url"`
	Name, Status, Conclusion string
	Steps                    []struct {
		Name, Status, Conclusion string
		Started                  time.Time `json:"started_at"`
		Ended                    time.Time `json:"completed_at"`
	}
}

// actionsJSON never retries/sleeps on throttling and uses the bounded, redacted
// evidence transport. The next scheduler pass reattaches to the same run.
func (c *Client) actionsJSON(path string, out any) error {
	b, err := c.evidenceGET(path, false)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errors.New("invalid Actions response")
	}
	return nil
}

// actionsPages bounds pagination and rejects unstable totals and conflicting
// duplicate identities. Repeated delivery of an identical item is a no-op.
func actionsPages[T any](c *Client, path, field string, id func(T) int64) ([]T, error) {
	seen := map[int64]string{}
	var out []T
	total := -1
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page <= 10; page++ {
		var raw map[string]json.RawMessage
		if err := c.actionsJSON(fmt.Sprintf("%s%sper_page=100&page=%d", path, sep, page), &raw); err != nil {
			return nil, err
		}
		var n int
		var items []T
		if json.Unmarshal(raw["total_count"], &n) != nil || json.Unmarshal(raw[field], &items) != nil || n < 0 || n > 1000 || (total >= 0 && n != total) {
			return nil, errors.New("incomplete Actions pagination")
		}
		total = n
		for _, item := range items {
			key := id(item)
			b, _ := json.Marshal(item)
			if key <= 0 {
				return nil, errors.New("missing Actions identity")
			}
			if old, ok := seen[key]; ok {
				if old != string(b) {
					return nil, errors.New("conflicting Actions identity")
				}
				continue
			}
			seen[key] = string(b)
			out = append(out, item)
		}
		if len(out) == total {
			return out, nil
		}
		if len(items) < 100 || len(out) > total {
			return nil, errors.New("missing Actions page")
		}
	}
	return nil, errors.New("Actions pagination limit")
}

func (p actionsPR) matches(r actions.Request) bool {
	c := r.Contract
	return p.Number == c.PRNumber && p.Head.Repo.ID > 0 && p.Head.Repo.FullName != "" && p.Head.SHA == c.HeadSHA && p.Base.SHA == c.BaseSHA && p.Base.Repo.ID == r.Policy.RepositoryID && strings.EqualFold(p.Base.Repo.FullName, c.Repository)
}

// ObserveActions observes existing pull_request runs only. No API in this path
// dispatches, reruns, comments, labels, merges or approves policy changes.
func (c *Client) ObserveActions(ctx context.Context, r actions.Request, previous actions.Observation) (actions.Observation, error) {
	key, err := r.Key()
	if err != nil {
		return actions.Observation{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	bounded := *c
	bounded.ctx = ctx
	c = &bounded
	o := actions.Observation{Key: key, Request: r, State: "unknown"}
	stop := func(reason string) (actions.Observation, error) {
		o.State = "unknown"
		o.Reason = reason
		return o, nil
	}
	prefix := "/repos/" + r.Contract.Repository
	prPath := fmt.Sprintf("%s/pulls/%d", prefix, r.Contract.PRNumber)
	var pr actionsPR
	if err := c.actionsJSON(prPath, &pr); err != nil {
		return o, err
	}
	if !pr.matches(r) {
		return stop("PR repository/head/base changed")
	}
	// Discover newer runs even after restart: a later run or rerun supersedes
	// earlier success. Reattachment never implies redispatch.
	runs, err := actionsPages(c, fmt.Sprintf("%s/actions/workflows/%d/runs?event=pull_request&head_sha=%s", prefix, r.Policy.WorkflowID, url.QueryEscape(r.Contract.HeadSHA)), "workflow_runs", func(v actionsRun) int64 { return v.ID })
	if err != nil {
		return o, err
	}
	var selected actionsRun
	for _, run := range runs {
		if run.Repository.ID != r.Policy.RepositoryID || !strings.EqualFold(run.Repository.FullName, r.Contract.Repository) || run.WorkflowID != r.Policy.WorkflowID || run.Path != r.Policy.WorkflowPath || run.Event != "pull_request" || run.HeadSHA != r.Contract.HeadSHA {
			continue
		}
		// Do not select an old base's run just because its head is unchanged.
		matched := false
		for _, p := range run.PRs {
			if p.Number == r.Contract.PRNumber && p.Head.SHA == r.Contract.HeadSHA && p.Base.SHA == r.Contract.BaseSHA {
				matched = true
			}
		}
		if matched && run.ID > selected.ID {
			selected = run
		}
	}
	if selected.ID == 0 {
		if previous.Key == key && previous.RunID > 0 {
			// A disappeared run is missing evidence, never permission to start one.
			o.RunID, o.Attempt, o.RunURL = previous.RunID, previous.Attempt, previous.RunURL
			return stop("previously attached run is missing")
		}
		o.State, o.Reason = "waiting_external", "no matching PR workflow run yet"
		return o, nil
	}
	if previous.Key == key && (selected.ID < previous.RunID || (selected.ID == previous.RunID && selected.Attempt < previous.Attempt)) {
		return stop("Actions run or attempt regressed")
	}
	// Read the authoritative run, rather than mixing list snapshots with jobs.
	var run actionsRun
	runPath := fmt.Sprintf("%s/actions/runs/%d", prefix, selected.ID)
	o.RunID, o.Attempt, o.RunURL = selected.ID, selected.Attempt, selected.URL
	if err := c.actionsJSON(runPath, &run); err != nil {
		return o, err
	}
	o.RunID, o.Attempt, o.RunURL, o.RunHeadSHA = run.ID, run.Attempt, run.URL, run.HeadSHA
	o.Status, o.Conclusion = run.Status, run.Conclusion
	if run.ID != selected.ID || run.Attempt < 1 || run.Attempt != selected.Attempt || run.WorkflowID != r.Policy.WorkflowID || run.Repository.ID != r.Policy.RepositoryID || !strings.EqualFold(run.Repository.FullName, r.Contract.Repository) || run.HeadRepository.ID != pr.Head.Repo.ID || !strings.EqualFold(run.HeadRepository.FullName, pr.Head.Repo.FullName) || run.HeadSHA != r.Contract.HeadSHA || run.Path != r.Policy.WorkflowPath || run.Event != "pull_request" || run.SuiteID <= 0 {
		return stop("untrusted or changed workflow run identity")
	}
	if run.Status != "completed" {
		o.State = "waiting_external"
		return o, nil
	}
	if run.Conclusion != "success" {
		o.State = "failed"
		o.Reason = "workflow conclusion is not success"
		return o, nil
	}
	jobs, err := actionsPages(c, fmt.Sprintf("%s/attempts/%d/jobs", runPath, run.Attempt), "jobs", func(v actionsJob) int64 { return v.ID })
	if err != nil {
		return o, err
	}
	for _, required := range r.Policy.Jobs {
		var found []actionsJob
		for _, j := range jobs {
			if j.Name == required.Name {
				found = append(found, j)
			}
		}
		if len(found) != 1 {
			return stop("missing or ambiguous required job: " + required.Name)
		}
		j := found[0]
		if j.RunID != run.ID || (j.Attempt != 0 && j.Attempt != run.Attempt) || j.HeadSHA != run.HeadSHA {
			return stop("job belongs to another run, attempt or SHA")
		}
		checkURL, err := url.Parse(j.CheckURL)
		if err != nil {
			return stop("missing check identity")
		}
		checkPath := prefix + "/check-runs/"
		checkID, err := strconv.ParseInt(strings.TrimPrefix(checkURL.Path, checkPath), 10, 64)
		if err != nil || checkID <= 0 || !strings.HasPrefix(checkURL.Path, checkPath) {
			return stop("missing check identity")
		}
		var check struct {
			ID                 int64
			HeadSHA            string `json:"head_sha"`
			Status, Conclusion string
			Suite              struct{ ID int64 } `json:"check_suite"`
			App                struct{ Slug string }
		}
		if err := c.actionsJSON(checkPath+strconv.FormatInt(checkID, 10), &check); err != nil {
			return o, err
		}
		if check.ID != checkID || check.Suite.ID != run.SuiteID || check.App.Slug != "github-actions" || check.HeadSHA != run.HeadSHA {
			return stop("untrusted check provenance")
		}
		evidence := actions.Job{ID: j.ID, RunID: j.RunID, Attempt: run.Attempt, CheckID: checkID, HeadSHA: j.HeadSHA, CheckSuiteID: check.Suite.ID, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion}
		for _, s := range j.Steps {
			evidence.Steps = append(evidence.Steps, actions.Step{Name: s.Name, Status: s.Status, Conclusion: s.Conclusion})
		}
		o.Jobs = append(o.Jobs, evidence)
		if j.Status != "completed" || j.Conclusion != "success" || check.Status != "completed" || check.Conclusion != "success" {
			o.State = "failed"
			o.Reason = "required job/check is not successful"
			return o, nil
		}
		for _, name := range required.Steps {
			count := 0
			for _, s := range j.Steps {
				if s.Name == name {
					count++
					if s.Status != "completed" || s.Conclusion != "success" {
						o.State = "failed"
						o.Reason = "required step is not successful: " + name
						return o, nil
					}
				}
			}
			if count != 1 {
				return stop("missing or ambiguous required step: " + name)
			}
		}
		// Only checkout-generated lines within the provider-reported checkout
		// step's timestamp interval may establish the actually tested SHA.
		logs, err := c.evidenceGET(fmt.Sprintf("%s/actions/jobs/%d/logs", prefix, j.ID), true)
		if err != nil {
			return o, err
		}
		checkoutCount := 0
		for _, s := range j.Steps {
			if s.Name != "Run actions/checkout@v4" || s.Status != "completed" || s.Conclusion != "success" {
				continue
			}
			checkoutCount++
			o.Jobs[len(o.Jobs)-1].CheckoutLogURL = fmt.Sprintf("%s%s/actions/jobs/%d/logs", c.baseURL, prefix, j.ID)
			o.Jobs[len(o.Jobs)-1].CheckoutStartedAt = s.Started
			o.Jobs[len(o.Jobs)-1].CheckoutEndedAt = s.Ended
			if !checkoutSHA(string(logs), s.Started, s.Ended, r.Contract.IntegrationSHA) {
				return stop("actual checkout SHA not proven by checkout step")
			}
		}
		if checkoutCount != 1 {
			return stop("missing or ambiguous checkout step")
		}
	}
	o.TestedSHA = r.Contract.IntegrationSHA
	// Prove the synthetic merge against the immutable contract, not today's
	// mutable refs/pull/N/merge value.
	var commit struct {
		SHA     string
		Parents []struct{ SHA string }
	}
	if err := c.actionsJSON(prefix+"/git/commits/"+o.TestedSHA, &commit); err != nil {
		return o, err
	}
	if commit.SHA != o.TestedSHA || len(commit.Parents) != 2 || commit.Parents[0].SHA != r.Contract.BaseSHA || commit.Parents[1].SHA != r.Contract.HeadSHA {
		return stop("test merge does not prove submitted head/base tuple")
	}
	var file struct{ SHA, Encoding, Content string }
	if err := c.actionsJSON(prefix+"/contents/"+r.Policy.WorkflowPath+"?ref="+o.TestedSHA, &file); err != nil {
		return o, err
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	sum := sha256.Sum256(content)
	if err != nil || file.Encoding != "base64" || !evidenceSHA.MatchString(file.SHA) || hex.EncodeToString(sum[:]) != r.Policy.WorkflowDigest {
		return stop("tested workflow differs from independently trusted version")
	}
	o.WorkflowBlob = file.SHA
	artifacts, err := actionsPages(c, runPath+"/artifacts", "artifacts", func(v actions.Artifact) int64 { return v.ID })
	if err != nil {
		return o, err
	}
	o.Artifacts = artifacts
	var attemptStart time.Time
	if len(r.Policy.Artifacts) > 0 {
		var attempt struct {
			ID        int64
			Number    int       `json:"run_attempt"`
			StartedAt time.Time `json:"run_started_at"`
		}
		if err := c.actionsJSON(fmt.Sprintf("%s/attempts/%d", runPath, run.Attempt), &attempt); err != nil {
			return o, err
		}
		if attempt.ID != run.ID || attempt.Number != run.Attempt || attempt.StartedAt.IsZero() {
			return stop("missing artifact attempt provenance")
		}
		attemptStart = attempt.StartedAt
	}
	for _, name := range r.Policy.Artifacts {
		count := 0
		for _, a := range artifacts {
			if a.Name == name {
				count++
				if a.Expired || a.URL == "" || a.Digest == "" || a.WorkflowRun.ID != run.ID || a.WorkflowRun.HeadSHA != run.HeadSHA || a.CreatedAt.Before(attemptStart) {
					return stop("required artifact is expired or lacks provenance")
				}
			}
		}
		if count != 1 {
			return stop("missing or ambiguous required artifact: " + name)
		}
	}
	// Close head/base and rerun races. No cached pass is reused on restart.
	var after actionsPR
	var latest actionsRun
	if err := c.actionsJSON(prPath, &after); err != nil {
		return o, err
	}
	if err := c.actionsJSON(runPath, &latest); err != nil {
		return o, err
	}
	if !after.matches(r) || !reflect.DeepEqual(latest, run) {
		return stop("PR or workflow attempt changed during observation")
	}
	newest, err := actionsPages(c, fmt.Sprintf("%s/actions/workflows/%d/runs?event=pull_request&head_sha=%s", prefix, r.Policy.WorkflowID, url.QueryEscape(r.Contract.HeadSHA)), "workflow_runs", func(v actionsRun) int64 { return v.ID })
	if err != nil {
		return o, err
	}
	for _, newer := range newest {
		if newer.WorkflowID != run.WorkflowID || newer.Repository.ID != run.Repository.ID || newer.Path != run.Path || newer.Event != run.Event || newer.HeadSHA != run.HeadSHA {
			continue
		}
		for _, p := range newer.PRs {
			if p.Number == r.Contract.PRNumber && p.Head.SHA == r.Contract.HeadSHA && p.Base.SHA == r.Contract.BaseSHA && (newer.ID > run.ID || (newer.ID == run.ID && newer.Attempt > run.Attempt)) {
				return stop("newer workflow execution supersedes observation")
			}
		}
	}
	o.State = "passed"
	return o, nil
}

func checkoutSHA(log string, started, ended time.Time, expected string) bool {
	if started.IsZero() || ended.Before(started) {
		return false
	}
	wantNext := false
	count := 0
	for _, line := range strings.Split(log, "\n") {
		ts, text, ok := strings.Cut(strings.TrimSuffix(line, "\r"), " ")
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		// Provider step boundaries have second precision, log lines have nanos.
		if err != nil || t.Before(started) || !t.Before(ended.Add(time.Second)) {
			continue
		}
		if strings.HasSuffix(text, "[command]/usr/bin/git log -1 --format=%H") {
			wantNext = true
			continue
		}
		if wantNext {
			if text != expected {
				return false
			}
			count++
			wantNext = false
		}
	}
	return count == 1 && !wantNext
}
