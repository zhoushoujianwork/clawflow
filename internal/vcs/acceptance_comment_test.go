package vcs

import (
	"errors"
	"testing"
	"time"
)

func TestAcceptanceCommentUpdatesOwnershipAndStaleness(t *testing.T) {
	now := time.Now().UTC()
	report := AcceptanceComment{Attempt: "one", Contract: "contract", Head: "head", Started: now, Completed: now.Add(time.Minute), Body: "first"}
	var comments []IssueComment
	posts, updates := 0, 0
	list := func() ([]IssueComment, error) { return comments, nil }
	create := func(body string) error {
		posts++
		comments = append(comments, IssueComment{ID: 7, Author: "bot", Body: body})
		return errors.New("lost response")
	}
	update := func(id int64, body string) error {
		if id != 7 {
			t.Fatal("wrong comment")
		}
		updates++
		comments[len(comments)-1].Body = body
		return nil
	}
	publish := func(r AcceptanceComment) error { return UpsertAcceptanceComment(r, "bot", list, create, update) }
	if err := publish(report); err != nil {
		t.Fatal(err)
	}
	spoof := comments[0]
	spoof.Author = "other"
	comments = append([]IssueComment{spoof}, comments...)
	if err := publish(report); err != nil || posts != 1 || updates != 0 {
		t.Fatalf("not idempotent: %v", err)
	}
	newer := report
	newer.Attempt = "two"
	newer.Started = now.Add(2 * time.Minute)
	newer.Completed = now.Add(3 * time.Minute)
	newer.Body = "second"
	if err := publish(newer); err != nil || updates != 1 || posts != 1 {
		t.Fatalf("not updated: %v", err)
	}
	if err := publish(report); err == nil || updates != 1 {
		t.Fatal("stale result overwrote newer report")
	}
	if comments[0].Body == comments[1].Body {
		t.Fatal("spoof comment updated")
	}
}
