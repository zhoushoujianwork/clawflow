package cloud

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// handleWebhookGitHub is registered on POST /api/v1/github/app/webhook
// (must match the GitHub App's Webhook URL exactly).
//
// It verifies the X-Hub-Signature-256 HMAC, dispatches on X-GitHub-Event,
// and enqueues one JobSpec per (issue/PR, matching operator) pair.
// Duplicate deliveries are silently collapsed by the existing dedupe map.
//
// Security notes:
//   - Body is read into memory once and used for both HMAC verification and
//     JSON decode to avoid TOCTOU.
//   - The webhook secret is never written to logs or response bodies.
//   - Unknown events return 204 without touching the store.
func (s *server) handleWebhookGitHub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	if eventType == "" {
		writeCloudError(w, http.StatusBadRequest, "missing X-GitHub-Event header")
		return
	}

	// Read body once for HMAC verification and JSON decode.
	body, err := io.ReadAll(io.LimitReader(r.Body, 25<<20)) // 25 MiB limit
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	// Derive repo early so we can look up the connection and its secret.
	repo, target, number, labels, err := extractGitHubPayload(eventType, body)
	if err != nil {
		// Unsupported event type or irrelevant action — ignore cleanly.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Look up the VCSConnection to get the webhook secret.
	conn := s.store.GetConnectionByRepo(repo)
	if conn == nil || conn.GitHubApp == nil || conn.GitHubApp.WebhookSecret == "" {
		// No connection registered; accept the delivery but do nothing.
		// This prevents enumeration of registered repos via 401 differences.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Verify HMAC-SHA256 signature.
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !verifyGitHubSignature(body, conn.GitHubApp.WebhookSecret, sigHeader) {
		// Generic error — do not leak secret or signature detail.
		writeCloudError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	// Determine whether this is a PR or issue target.
	isPR := target == "pr"
	sub := &operator.Subject{
		Number: number,
		Labels: labels,
		IsPR:   isPR,
		State:  "open",
	}

	// Match operators against this subject using the server's registry.
	var enqueued int
	for _, op := range s.operators.All() {
		if !operator.Matches(sub, op) {
			continue
		}
		dedupeKey := fmt.Sprintf("github:%s:%s:%d:operator:%s", repo, target, number, op.Name)
		spec := JobSpec{
			DedupeKey: dedupeKey,
			Repo:      repo,
			Platform:  "github",
			Operator:  op.Name,
			Target:    target,
			Number:    number,
			Labels:    append([]string(nil), labels...),
			State:     "open",
		}
		if conn.BoundMachineID != "" {
			spec.Binding = conn.BoundMachineID
		}
		if _, enqErr := s.store.EnqueueJob(spec, conn.BoundMachineID); enqErr != nil {
			// Log but do not abort — try to enqueue the remaining operators.
			_ = enqErr
		} else {
			enqueued++
		}
	}

	_ = enqueued
	w.WriteHeader(http.StatusNoContent)
}

// verifyGitHubSignature checks that sigHeader ("sha256=<hex>") matches the
// HMAC-SHA256 of body computed with secret. Uses constant-time comparison.
func verifyGitHubSignature(body []byte, secret, sigHeader string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	gotHex := sigHeader[len(prefix):]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(gotHex), []byte(expected))
}

// ghIssuePayload is the minimal shape shared by the GitHub issues event.
type ghIssuePayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number int `json:"number"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"issue"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// ghPRPayload is the minimal shape shared by the GitHub pull_request event.
type ghPRPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number int `json:"number"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// ghIssueCommentPayload is the minimal shape for the issue_comment event.
type ghIssueCommentPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number     int `json:"number"`
		State      string `json:"state"`
		Labels     []struct {
			Name string `json:"name"`
		} `json:"labels"`
		PullRequest *struct{} `json:"pull_request"` // non-nil when the issue is a PR
	} `json:"issue"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// extractGitHubPayload parses the minimal fields we need from a GitHub
// webhook payload. It returns an error for unsupported event types or
// actions that ClawFlow intentionally ignores (e.g. closed/deleted issues).
func extractGitHubPayload(eventType string, body []byte) (repo, target string, number int, labels []string, err error) {
	switch eventType {
	case "issues":
		var p ghIssuePayload
		if err = json.Unmarshal(body, &p); err != nil {
			return
		}
		// Only process actions that change trigger-relevant state.
		switch p.Action {
		case "opened", "reopened", "labeled", "unlabeled", "edited":
			// continue
		default:
			err = fmt.Errorf("unsupported issues action: %s", p.Action)
			return
		}
		repo = p.Repository.FullName
		target = "issue"
		number = p.Issue.Number
		for _, l := range p.Issue.Labels {
			labels = append(labels, l.Name)
		}

	case "pull_request":
		var p ghPRPayload
		if err = json.Unmarshal(body, &p); err != nil {
			return
		}
		switch p.Action {
		case "opened", "reopened", "labeled", "unlabeled", "synchronize", "edited":
			// continue
		default:
			err = fmt.Errorf("unsupported pull_request action: %s", p.Action)
			return
		}
		repo = p.Repository.FullName
		target = "pr"
		number = p.PullRequest.Number
		for _, l := range p.PullRequest.Labels {
			labels = append(labels, l.Name)
		}

	case "issue_comment":
		var p ghIssueCommentPayload
		if err = json.Unmarshal(body, &p); err != nil {
			return
		}
		// Only "created" comments on open issues/PRs are relevant.
		if p.Action != "created" {
			err = fmt.Errorf("unsupported issue_comment action: %s", p.Action)
			return
		}
		repo = p.Repository.FullName
		if p.Issue.PullRequest != nil {
			target = "pr"
		} else {
			target = "issue"
		}
		number = p.Issue.Number
		for _, l := range p.Issue.Labels {
			labels = append(labels, l.Name)
		}

	default:
		err = fmt.Errorf("unsupported GitHub event: %s", eventType)
		return
	}

	if repo == "" || number == 0 {
		err = fmt.Errorf("incomplete payload: repo=%q number=%d", repo, number)
	}
	return
}
