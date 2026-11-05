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

// NoJobEvidence prefixes every "nothing collected" result; a reason list follows
// so callers and the repair prompt can tell why (HTTP status, no matching job...).
const NoJobEvidence = "no evidence (logs unavailable or no matching failed jobs)"

func noEvidence(reasons []string) string {
	if len(reasons) == 0 {
		return NoJobEvidence
	}
	return NoJobEvidence + "; reasons: " + strings.Join(reasons, "; ")
}

// IsNoEvidence reports whether s is a NoJobEvidence result, with or without reasons.
func IsNoEvidence(s string) bool { return strings.HasPrefix(s, NoJobEvidence) }

var evidenceErrorLine = regexp.MustCompile(`##\[error\][^\r\n]*`)

// logErrorLines returns up to max GitHub "##[error]" annotations, which name the
// failing command's outcome, plus the last "##[group]" title before the first one
// (the step that was running). Input must already be redacted.
func logErrorLines(log string, max int) (step string, errs []string) {
	idx := evidenceErrorLine.FindAllStringIndex(log, -1)
	for _, m := range idx {
		if len(errs) == max {
			break
		}
		errs = append(errs, strings.TrimSpace(log[m[0]:m[1]]))
	}
	if len(idx) > 0 {
		before := log[:idx[0][0]]
		if g := strings.LastIndex(before, "##[group]"); g >= 0 {
			line := before[g+len("##[group]"):]
			if nl := strings.IndexByte(line, '\n'); nl >= 0 {
				line = line[:nl]
			}
			step = strings.TrimSpace(line)
		}
	}
	return step, errs
}

const evidenceTailBytes = 4096
const evidenceDownloadBytes = 4 << 20
const evidenceStreamBytes = 256 << 20 // upper bound on bytes read from one log

// readTail reads r and returns at most keep trailing bytes. It fails if r exceeds
// maxTotal bytes, so a hostile endpoint cannot stream forever.
func readTail(r io.Reader, keep, maxTotal int) ([]byte, error) {
	buf := make([]byte, 0, 64<<10)
	chunk := make([]byte, 64<<10)
	total := 0
	for {
		n, err := r.Read(chunk)
		total += n
		if total > maxTotal {
			return nil, fmt.Errorf("log over %d bytes", maxTotal)
		}
		buf = append(buf, chunk[:n]...)
		if len(buf) > 2*keep {
			buf = append(buf[:0], buf[len(buf)-keep:]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("log read incomplete: %v", err)
		}
	}
	if len(buf) > keep {
		buf = buf[len(buf)-keep:]
	}
	return buf, nil
}

var evidenceSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var evidenceANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
var evidenceSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:gh[pousr]_[a-z0-9_.-]+|github_pat_[a-z0-9_]+|glpat-[a-z0-9_-]+|AKIA[A-Z0-9]{16})`),
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
	selected, err := c.authenticated(path)
	if err != nil {
		return nil, err
	}
	c = selected
	target := c.baseURL + path
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for hop := 0; hop < 3; hop++ {
		req, err := http.NewRequestWithContext(c.reqContext(), "GET", target, nil)
		if err != nil {
			return nil, fmt.Errorf("request could not be built")
		}
		if hop == 0 {
			req.Header.Set("Authorization", "Bearer "+c.token)
			req.Header.Set("Accept", "application/vnd.github+json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("transport error")
		}
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			if !logs {
				resp.Body.Close()
				return nil, fmt.Errorf("unexpected HTTP %d redirect", resp.StatusCode)
			}
			location, err := resp.Location()
			resp.Body.Close()
			if err != nil || location.Scheme != "https" || location.User != nil {
				return nil, fmt.Errorf("HTTP %d redirect with missing or non-https location", resp.StatusCode)
			}
			target = location.String()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if logs {
			// Job logs routinely exceed the cap (verbose -race runs); the failure is
			// at the end, so stream the body and keep only its tail.
			data, err := readTail(resp.Body, evidenceDownloadBytes, evidenceStreamBytes)
			resp.Body.Close()
			if err != nil {
				return nil, err
			}
			return data, nil
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, evidenceDownloadBytes+1))
		resp.Body.Close()
		if err != nil || len(data) > evidenceDownloadBytes {
			return nil, fmt.Errorf("response incomplete or over %d bytes", evidenceDownloadBytes)
		}
		return data, nil
	}
	return nil, fmt.Errorf("too many redirects")
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
	selected, authErr := c.authenticated("/repos/" + repo + "/")
	if authErr != nil {
		return "", authErr
	}
	c = selected
	if !evidenceSHA.MatchString(sha) || jobID < 0 {
		return NoJobEvidence, fmt.Errorf("full head SHA and nonnegative job ID required")
	}
	ctx, cancel := context.WithTimeout(c.reqContext(), 30*time.Second)
	defer cancel()
	bounded := *c
	bounded.ctx = ctx
	c = &bounded
	var reasons []string
	why := func(f string, a ...any) {
		if len(reasons) < 8 {
			reasons = append(reasons, fmt.Sprintf(f, a...))
		}
	}
	data, err := c.evidenceGET("/repos/"+repo+"/actions/runs?head_sha="+url.QueryEscape(sha)+"&per_page=100", false)
	if err != nil {
		return noEvidence([]string{"listing workflow runs failed: " + err.Error()}), nil
	}
	var runs struct {
		Total int           `json:"total_count"`
		Runs  []evidenceRun `json:"workflow_runs"`
	}
	if json.Unmarshal(data, &runs) != nil {
		return noEvidence([]string{"workflow run list was not valid JSON"}), nil
	}
	if len(runs.Runs) == 0 {
		return noEvidence([]string{"no workflow runs for head " + sha[:12]}), nil
	}
	if runs.Total > len(runs.Runs) {
		// More runs than one page: inspect the newest page instead of giving up.
		why("run list truncated (%d of %d runs)", len(runs.Runs), runs.Total)
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
		// 已取消的 workflow 仍可能包含先前失败的 job；仅下方失败 job 的日志可作为证据。
		if r.WorkflowID <= 0 || r.ID != latest[r.WorkflowID] || r.HeadSHA != sha || !strings.EqualFold(r.HeadRepository.FullName, repo) || r.Status != "completed" || (r.Conclusion != "failure" && r.Conclusion != "timed_out" && r.Conclusion != "cancelled") || r.RunAttempt < 1 {
			if r.ID == latest[r.WorkflowID] {
				why("run %d skipped (status=%s conclusion=%s head_repo=%s)", r.ID, r.Status, r.Conclusion, r.HeadRepository.FullName)
			}
			continue
		}
		inspected++
		if inspected > 10 {
			break
		}
		data, err = c.evidenceGET(fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100", repo, r.ID, r.RunAttempt), false)
		if err != nil {
			why("run %d jobs: %v", r.ID, err)
			continue
		}
		var jobs struct {
			Total int `json:"total_count"`
			Jobs  []evidenceJob
		}
		if json.Unmarshal(data, &jobs) != nil {
			why("run %d jobs response was not valid JSON", r.ID)
			continue
		}
		if jobs.Total > len(jobs.Jobs) {
			why("run %d job list truncated (%d of %d)", r.ID, len(jobs.Jobs), jobs.Total)
		}
		matched := 0
		for _, j := range jobs.Jobs {
			if j.ID <= 0 || (jobID > 0 && j.ID != jobID) || j.RunID != r.ID || j.HeadSHA != sha || j.Status != "completed" || (j.Conclusion != "failure" && j.Conclusion != "timed_out") {
				continue
			}
			matched++
			downloads++
			if downloads > 10 {
				break
			}
			data, err = c.evidenceGET(fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, j.ID), true)
			if err != nil {
				why("job %d logs: %v", j.ID, err)
				continue
			}
			if strings.TrimSpace(string(data)) == "" {
				why("job %d logs were empty", j.ID)
				continue
			}
			steps := ""
			for _, s := range j.Steps {
				if s.Status == "completed" && (s.Conclusion == "failure" || s.Conclusion == "timed_out") {
					steps += fmt.Sprintf("%d: %s; ", s.Number, s.Name)
				}
			}
			// JSON quoting prevents embedded delimiters from impersonating prompt structure.
			redacted := c.evidenceText(string(data), evidenceTailBytes)
			item := map[string]any{"run_id": r.ID, "attempt": r.RunAttempt, "job_id": j.ID, "head_sha": sha, "job": c.evidenceText(j.Name, 256), "failed_steps": c.evidenceText(steps, 512), "tail": redacted}
			// Error annotations come from the redacted full-size tail, then are re-bounded.
			if step, errs := logErrorLines(c.evidenceText(string(data), evidenceDownloadBytes), 5); len(errs) > 0 {
				item["log_step"] = c.evidenceText(step, 256)
				item["error_lines"] = c.evidenceText(strings.Join(errs, "\n"), 1024)
			}
			encoded, _ := json.Marshal(item)
			excerpts = append(excerpts, string(encoded))
			if len(excerpts) == 3 {
				return strings.Join(excerpts, "\n"), nil
			}
		}
		if matched == 0 && jobID == 0 {
			why("run %d attempt %d has no failed job", r.ID, r.RunAttempt)
		}
		if matched == 0 && jobID > 0 {
			why("run %d has no failed job with id %d", r.ID, jobID)
		}
	}
	if len(excerpts) == 0 {
		if inspected == 0 {
			why("no completed failed workflow run for this head")
		}
		return noEvidence(reasons), nil
	}
	return strings.Join(excerpts, "\n"), nil
}
