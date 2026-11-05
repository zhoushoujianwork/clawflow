package commands

import (
	"errors"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type issueAdmission struct {
	Author    string    `json:"author"`
	Basis     string    `json:"basis"`
	CheckedAt time.Time `json:"checked_at"`
}

type issueAdmissionCheck func(vcs.Client, string, config.Repo, vcs.Issue) (*issueAdmission, string)

var issueAdmissionFn issueAdmissionCheck = checkIssueAdmission

var errIssueAdmissionDeferred = errors.New("issue author admission deferred")

type issueAdmissionDeferredError struct {
	reason string
}

func (e *issueAdmissionDeferredError) Error() string {
	return errIssueAdmissionDeferred.Error() + ": " + e.reason
}

func (e *issueAdmissionDeferredError) Unwrap() error { return errIssueAdmissionDeferred }

func issueAdmissionReason(err error) string {
	var deferred *issueAdmissionDeferredError
	if errors.As(err, &deferred) {
		return deferred.reason
	}
	return "author_check_failed"
}

func checkIssueAdmission(client vcs.Client, repo string, repoCfg config.Repo, issue vcs.Issue) (*issueAdmission, string) {
	author := strings.TrimSpace(issue.Author)
	if author == "" {
		return nil, "author_check_failed"
	}
	for _, allowed := range repoCfg.AllowedIssueAuthors {
		if strings.EqualFold(strings.TrimSpace(allowed), author) {
			return &issueAdmission{Author: author, Basis: "allowlist", CheckedAt: time.Now().UTC()}, ""
		}
	}
	allowed, err := client.AuthorHasWriteAccess(repo, author)
	if err != nil {
		return nil, "author_check_failed"
	}
	if !allowed {
		return nil, "author_not_admitted:" + author
	}
	return &issueAdmission{Author: author, Basis: "write_permission", CheckedAt: time.Now().UTC()}, ""
}

// executionAdmission decides just before implementation starts. On a coordinator
// worker the controller's record is authoritative: the worker's own config and
// token may differ, and a denial there would requeue the issue every pass and
// spend the task's reopen event. Without a record (a task enqueued before this
// field existed, or a local run) the check runs here against fresh data.
func executionAdmission(j *runJob, issue vcs.Issue) (*issueAdmission, string) {
	if j.hooks != nil && j.admission != nil {
		if strings.EqualFold(strings.TrimSpace(j.admission.Author), strings.TrimSpace(issue.Author)) {
			return j.admission, ""
		}
		return nil, "author_check_failed"
	}
	return issueAdmissionFn(j.client, j.repo, j.repoCfg, issue)
}
