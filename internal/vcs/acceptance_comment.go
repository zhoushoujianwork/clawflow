package vcs

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AcceptanceCommentPublisher is optional for adapters. Callers serialize all
// writers for one PR and revalidate the authoritative contract before publishing.
type AcceptanceCommentPublisher interface {
	PublishAcceptanceComment(repo string, pr int, report AcceptanceComment) error
}

type AcceptanceComment struct {
	Attempt   string    `json:"attempt"`
	Contract  string    `json:"contract"`
	Head      string    `json:"head"`
	Started   time.Time `json:"started"`
	Completed time.Time `json:"completed"`
	Body      string    `json:"-"`
}

const acceptanceCommentMarker = "<!-- clawflow:acceptance-report "

// UpsertAcceptanceComment only trusts markers owned by the authenticated actor.
// Creation is reconciled after a lost response; updates are idempotent writes.
func UpsertAcceptanceComment(report AcceptanceComment, actor string, list func() ([]IssueComment, error), create func(string) error, update func(int64, string) error) error {
	if actor == "" || report.Attempt == "" || report.Contract == "" || report.Head == "" || report.Started.IsZero() || report.Completed.Before(report.Started) {
		return errors.New("invalid acceptance publication")
	}
	comments, err := list()
	if err != nil {
		return err
	}
	var target *IssueComment
	for i := range comments {
		comment := &comments[i]
		if comment.Author != actor {
			continue
		}
		_, tail, ok := strings.Cut(comment.Body, acceptanceCommentMarker)
		if !ok {
			continue
		}
		encoded, _, ok := strings.Cut(tail, " -->")
		if !ok {
			return errors.New("invalid owned acceptance marker")
		}
		var previous AcceptanceComment
		if json.Unmarshal([]byte(encoded), &previous) != nil || previous.Started.IsZero() {
			return errors.New("invalid owned acceptance revision")
		}
		if target != nil {
			return errors.New("multiple owned acceptance comments")
		}
		target = comment
		if previous.Attempt == report.Attempt && previous.Contract == report.Contract {
			return nil
		}
		if !report.Started.After(previous.Started) || !report.Completed.After(previous.Completed) {
			return errors.New("stale acceptance publication")
		}
	}
	encoded, _ := json.Marshal(report)
	body := strings.NewReplacer("<!--", "", "-->", "").Replace(report.Body) + "\n\n" + acceptanceCommentMarker + string(encoded) + " -->"
	if len(body) > 60000 {
		return errors.New("acceptance report exceeds comment size limit")
	}
	if target != nil {
		return update(target.ID, body)
	}
	if err := create(body); err != nil {
		comments, readErr := list()
		if readErr == nil {
			for _, c := range comments {
				if c.Author == actor && c.Body == body {
					return nil
				}
			}
		}
		return err
	}
	return nil
}
