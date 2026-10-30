package acceptance

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/resourcelease"
)

func TestDevelopmentLeaseHandoffToFreshReview(t *testing.T) {
	r, scratch, workspace := setupRunner(t)
	for _, name := range []string{"development", "review"} {
		if _, err := project.Create(name); err != nil {
			t.Fatal(err)
		}
		repo := "owner/other"
		if name == "review" {
			repo = contract().Repository
		}
		if err := project.AddRepo(name, repo); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(project.ProjectDir(name), "project.yaml")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("shared_resource_keys: [desktop]\n")
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	base := r.Execute
	reviews := 0
	r.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
		if !opts.ToolsDisabled {
			reviews++
			// The independent review acquired a new lease for the exact candidate.
			_, err := project.AcquireResources("owner/other", resourcelease.Holder{Task: "contender"})
			var busy *resourcelease.Blocked
			if !errors.As(err, &busy) || busy.Holder.Phase != "acceptance" || busy.Holder.Candidate != contract().HeadSHA || busy.Holder.Attempt == "dev-run" {
				t.Fatalf("missing independent candidate lease: %v", err)
			}
		}
		return base(ctx, prompt, opts)
	}
	var blockedAttempt string
	_, _, err := operator.Run(context.Background(), &operator.Operator{Name: "implement"}, &operator.Subject{Number: 7}, nil, operator.RunOptions{
		Repo: "owner/other", RunID: "dev-run", RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
			text, outcome, busyErr := operator.Run(context.Background(), &operator.Operator{Name: "implement"}, &operator.Subject{Number: 8}, nil, operator.RunOptions{
				Repo: contract().Repository, RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
					t.Fatal("busy development executed")
					return "", nil
				},
			})
			if busyErr == nil || outcome != "" || !strings.Contains(text, "blocked:") || !strings.Contains(text, "owner/other") {
				t.Fatalf("development silently skipped: %s %s %v", text, outcome, busyErr)
			}
			record, err := r.Run(context.Background(), contract(), scratch, workspace)
			var busy *resourcelease.Blocked
			if !errors.As(err, &busy) || busy.Holder.Repository != "owner/other" || busy.Holder.Task != "#7" || busy.Holder.Attempt != "dev-run" {
				t.Fatalf("busy holder missing: %v", err)
			}
			if record.Verdict != Blocked || record.NextOwner != "scheduling" || !strings.Contains(record.Report, "owner/other") || reviews != 0 {
				t.Fatalf("not visibly blocked: %+v", record)
			}
			blockedAttempt = record.AttemptID
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := r.Run(context.Background(), contract(), scratch, workspace)
	if err != nil || record.Verdict != Passed || reviews != 1 || record.AttemptID == blockedAttempt {
		t.Fatalf("fresh review: verdict=%s reviews=%d err=%v", record.Verdict, reviews, err)
	}
	// Completion releases the host for the next repository, with no max_in_flight.
	release, err := project.AcquireResources("owner/other", resourcelease.Holder{Task: "next"})
	if err != nil {
		t.Fatal(err)
	}
	release()
}
