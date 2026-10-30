package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

const noJobEvidence = "no evidence (logs unavailable or no matching failed jobs)"

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

// evidenceGET bounds diagnostic downloads and refuses redirects so PRIVATE-TOKEN
// cannot escape to a trace storage host. Errors never contain response bodies.
func (c *Client) evidenceGET(path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(c.reqContext(), "GET", c.baseURL+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("evidence request unavailable")
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("evidence transport unavailable")
	}
	defer resp.Body.Close()
	const limit = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) > limit {
		return nil, resp.StatusCode, fmt.Errorf("evidence incomplete or oversized")
	}
	return data, resp.StatusCode, nil
}

// mrHeadPipeline binds the MR's selected pipeline to the current source head.
// A stale, missing or synthetic merge pipeline is not source-head evidence.
func (c *Client) mrHeadPipeline(repo string, number int, expected string) (Pipeline, error) {
	data, status, err := c.evidenceGET(fmt.Sprintf("/projects/%s/merge_requests/%d", projectID(repo), number))
	if err != nil {
		return Pipeline{}, err
	}
	if status != 200 {
		return Pipeline{}, fmt.Errorf("gitlab get MR: HTTP %d", status)
	}
	var mr struct {
		SHA             string    `json:"sha"`
		SourceProjectID int64     `json:"source_project_id"`
		TargetProjectID int64     `json:"target_project_id"`
		HeadPipeline    *Pipeline `json:"head_pipeline"`
	}
	if err := json.Unmarshal(data, &mr); err != nil {
		return Pipeline{}, err
	}
	if !canonicalMergeSHA(mr.SHA) || (expected != "" && mr.SHA != expected) || mr.SourceProjectID <= 0 || mr.SourceProjectID != mr.TargetProjectID || mr.HeadPipeline == nil || mr.HeadPipeline.ID <= 0 || mr.HeadPipeline.SHA != mr.SHA {
		return Pipeline{}, nil
	}
	p, err := c.GetPipeline(repo, mr.HeadPipeline.ID)
	if err != nil {
		return Pipeline{}, err
	}
	if p.ID != mr.HeadPipeline.ID || p.SHA != mr.SHA {
		return Pipeline{}, nil
	}
	return p, nil
}

// GetCIStatusAtHead uses pipeline API status, never job-log contents.
// An empty expected SHA selects the current MR head for GetCIStatus callers.
func (c *Client) GetCIStatusAtHead(repo string, number int, expected string) (vcs.CIStatus, error) {
	p, err := c.mrHeadPipeline(repo, number, expected)
	if err != nil || p.ID == 0 {
		return vcs.CIStatusNone, err
	}
	switch p.Status {
	case "success":
		return vcs.CIStatusSuccess, nil
	case "failed":
		return vcs.CIStatusFailure, nil
	default:
		return vcs.CIStatusPending, nil
	}
}

// GetMRFailedJobEvidence reuses the CI query and trace reader, with no authority
// over CI status. Every excerpt is JSON-quoted, redacted and revision-bound.
func (c *Client) GetMRFailedJobEvidence(repo string, number int, sha string) (string, error) {
	if !canonicalMergeSHA(sha) {
		return noJobEvidence, fmt.Errorf("full head SHA required")
	}
	ctx, cancel := context.WithTimeout(c.reqContext(), 30*time.Second)
	defer cancel()
	bounded := *c
	bounded.ctx = ctx
	c = &bounded
	p, err := c.mrHeadPipeline(repo, number, sha)
	if err != nil || p.ID == 0 || p.Status != "failed" {
		return noJobEvidence, nil
	}
	jobs, err := c.ListPipelineJobs(repo, p.ID)
	if err != nil {
		return noJobEvidence, nil
	}
	var excerpts []string
	downloads := 0
	for _, job := range jobs {
		if job.ID <= 0 || job.Status != "failed" {
			continue
		}
		downloads++
		if downloads > 10 {
			break
		}
		trace, err := c.GetJobLog(repo, job.ID)
		if err != nil || strings.TrimSpace(trace) == "" {
			continue
		}
		item := map[string]any{"pipeline_id": p.ID, "job_id": job.ID, "head_sha": sha, "job": c.evidenceText(job.Name, 256), "tail": c.evidenceText(trace, 4096)}
		encoded, _ := json.Marshal(item)
		excerpts = append(excerpts, string(encoded))
		if len(excerpts) == 3 {
			break
		}
	}
	if len(excerpts) == 0 {
		return noJobEvidence, nil
	}
	return strings.Join(excerpts, "\n"), nil
}
