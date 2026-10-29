package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

const NoJobEvidence = "no evidence (logs unavailable or no matching failed jobs)"
const evidenceTailBytes = 4096
const evidenceDownloadBytes = 4 << 20

var evidenceSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var evidenceANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
var evidenceSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:gh[pousr]_[a-z0-9_]+|github_pat_[a-z0-9_]+|glpat-[a-z0-9_-]+|AKIA[A-Z0-9]{16})`),
	regexp.MustCompile(`(?i)(?:authorization|password|passwd|secret|token|api[_-]?key|credential)[a-z0-9_-]*["']?\s*[:=]\s*[^\r\n]+`),
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9+/=._-]+`),
	regexp.MustCompile(`(?i)(?:https?|postgres(?:ql)?)://[^\s/@]+:[^\s/@]+@`),
	regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|$)`),
}

// evidenceText redacts before truncating so a boundary cannot expose half a token.
func (c *Client) evidenceText(s string, limit int) string {
	s = evidenceANSI.ReplaceAllString(s, "")
	if c.token != "" {
		s = strings.ReplaceAll(s, c.token, "[redacted]")
	}
	for _, re := range evidenceSecrets {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	s = vcs.FailureExcerpt(s, limit)
	return strings.ToValidUTF8(s, "")
}

// evidenceGET bounds downloads and never forwards API credentials to signed log URLs.
// Errors intentionally omit response bodies and URLs (which can contain secrets).
func (c *Client) evidenceGET(path string, logs bool) ([]byte, error) {
	target := c.baseURL + path
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for hop := 0; hop < 3; hop++ {
		req, err := http.NewRequestWithContext(c.reqContext(), "GET", target, nil)
		if err != nil {
			return nil, fmt.Errorf("evidence request unavailable")
		}
		if hop == 0 {
			req.Header.Set("Authorization", "Bearer "+c.token)
			req.Header.Set("Accept", "application/vnd.github+json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("evidence transport unavailable")
		}
		if logs && resp.StatusCode == http.StatusFound {
			location, err := resp.Location()
			resp.Body.Close()
			if err != nil || location.Scheme != "https" || location.User != nil {
				return nil, fmt.Errorf("evidence redirect unavailable")
			}
			target = location.String()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("evidence unavailable")
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, evidenceDownloadBytes+1))
		resp.Body.Close()
		if err != nil || len(data) > evidenceDownloadBytes {
			return nil, fmt.Errorf("evidence incomplete or oversized")
		}
		return data, nil
	}
	return nil, fmt.Errorf("evidence redirect limit")
}

type evidenceRun struct {
	ID                 int64
	WorkflowID         int64  `json:"workflow_id"`
	RunAttempt         int    `json:"run_attempt"`
	HeadSHA            string `json:"head_sha"`
	Status, Conclusion string
	HeadRepository     struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
}
type evidenceJob struct {
	ID                       int64
	RunID                    int64  `json:"run_id"`
	HeadSHA                  string `json:"head_sha"`
	Name, Status, Conclusion string
	Steps                    []struct {
		Number                   int
		Name, Status, Conclusion string
	}
}

// GetFailedJobEvidence returns advisory text only; it never computes CI status.
// Zero jobID selects up to three failed jobs. A positive ID restricts the same
// revision-bound query, rather than bypassing run/attempt/repository validation.
func (c *Client) GetFailedJobEvidence(repo, sha string, jobID int64) (string, error) {
	if !evidenceSHA.MatchString(sha) || jobID < 0 {
		return NoJobEvidence, fmt.Errorf("full head SHA and nonnegative job ID required")
	}
	ctx, cancel := context.WithTimeout(c.reqContext(), 30*time.Second)
	defer cancel()
	bounded := *c
	bounded.ctx = ctx
	c = &bounded
	data, err := c.evidenceGET("/repos/"+repo+"/actions/runs?head_sha="+url.QueryEscape(sha)+"&per_page=100", false)
	if err != nil {
		return NoJobEvidence, nil
	}
	var runs struct {
		Total int           `json:"total_count"`
		Runs  []evidenceRun `json:"workflow_runs"`
	}
	if json.Unmarshal(data, &runs) != nil || runs.Total != len(runs.Runs) || runs.Total > 100 {
		return NoJobEvidence, nil
	}
	// Retain the newest run for each workflow, including successful/pending reruns.
	latest := map[int64]int64{}
	for _, r := range runs.Runs {
		if r.ID > latest[r.WorkflowID] {
			latest[r.WorkflowID] = r.ID
		}
	}
	var excerpts []string
	inspected := 0
	downloads := 0
	for _, r := range runs.Runs {
		if r.WorkflowID <= 0 || r.ID != latest[r.WorkflowID] || r.HeadSHA != sha || !strings.EqualFold(r.HeadRepository.FullName, repo) || r.Status != "completed" || (r.Conclusion != "failure" && r.Conclusion != "timed_out") || r.RunAttempt < 1 {
			continue
		}
		inspected++
		if inspected > 10 {
			break
		}
		data, err = c.evidenceGET(fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100", repo, r.ID, r.RunAttempt), false)
		if err != nil {
			continue
		}
		var jobs struct {
			Total int `json:"total_count"`
			Jobs  []evidenceJob
		}
		if json.Unmarshal(data, &jobs) != nil || jobs.Total != len(jobs.Jobs) || jobs.Total > 100 {
			continue
		}
		for _, j := range jobs.Jobs {
			if j.ID <= 0 || (jobID > 0 && j.ID != jobID) || j.RunID != r.ID || j.HeadSHA != sha || j.Status != "completed" || (j.Conclusion != "failure" && j.Conclusion != "timed_out") {
				continue
			}
			downloads++
			if downloads > 10 {
				break
			}
			data, err = c.evidenceGET(fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, j.ID), true)
			if err != nil || strings.TrimSpace(string(data)) == "" {
				continue
			}
			steps := ""
			for _, s := range j.Steps {
				if s.Status == "completed" && (s.Conclusion == "failure" || s.Conclusion == "timed_out") {
					steps += fmt.Sprintf("%d: %s; ", s.Number, s.Name)
				}
			}
			// JSON quoting prevents embedded delimiters from impersonating prompt structure.
			item := map[string]any{"run_id": r.ID, "attempt": r.RunAttempt, "job_id": j.ID, "head_sha": sha, "job": c.evidenceText(j.Name, 256), "failed_steps": c.evidenceText(steps, 512), "tail": c.evidenceText(string(data), evidenceTailBytes)}
			encoded, _ := json.Marshal(item)
			excerpts = append(excerpts, string(encoded))
			if len(excerpts) == 3 {
				return strings.Join(excerpts, "\n"), nil
			}
		}
	}
	if len(excerpts) == 0 {
		return NoJobEvidence, nil
	}
	return strings.Join(excerpts, "\n"), nil
}
