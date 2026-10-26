package acceptance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func laterRecord(r Record, attempt string, offset time.Duration) Record {
	r.AttemptID = attempt
	r.Plan.AttemptID = attempt
	r.StartedAt = r.StartedAt.Add(offset)
	r.Plan.StartedAt = r.Plan.StartedAt.Add(offset)
	r.Plan.CompletedAt = r.Plan.CompletedAt.Add(offset)
	r.ReviewStartedAt = r.ReviewStartedAt.Add(offset)
	r.CompletedAt = r.CompletedAt.Add(offset)
	return r
}

func TestRepeatedRunnerPreservesEachAttempt(t *testing.T) {
	r, scratch, workspace := setupRunner(t)
	base := r.Execute
	calls := 0
	r.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
		calls++
		result, err := base(ctx, prompt, opts)
		result.NativeSessionID = randomID()
		result.RunID = randomID()
		if opts.ToolsDisabled {
			result.Text += result.RunID
		}
		return result, err
	}
	first, err := r.Run(context.Background(), contract(), scratch, workspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Run(context.Background(), contract(), scratch, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 || first.AttemptID == second.AttemptID || first.Plan.Text == second.Plan.Text {
		t.Fatal("reused a planner or acceptance attempt")
	}
	for _, record := range []Record{first, second} {
		p, err := r.Store.LoadPlan(contract(), record.AttemptID)
		if err != nil || digest(p) != digest(record.Plan) {
			t.Fatalf("plan not preserved: %v", err)
		}
		stored, err := r.Store.LoadRecord(contract(), record.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if err := Evaluate(contract(), stored); err != nil {
			t.Fatal(err)
		}
		if stored.Reviewer.Profile != "test-profile" || stored.Reviewer.Backend != "test-backend" || stored.Reviewer.Model != "test-model" ||
			stored.Plan.Planner.Profile != "test-profile" || stored.Plan.Planner.Backend != "test-backend" || stored.Plan.Planner.Model != "test-model" {
			t.Fatal("lost execution provenance")
		}
	}
	latest, err := r.Store.LatestRecord(contract())
	if err != nil || latest.AttemptID != second.AttemptID {
		t.Fatalf("latest attempt: %v", err)
	}
}

func TestPlanAttemptIsolation(t *testing.T) {
	s := openTestStore(t)
	r := passing()
	if err := s.SavePlan(r.Contract, r.Plan); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadPlan(r.Contract, "another"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reused old plan: %v", err)
	}
	if _, err := s.LoadPlan(r.Contract, ""); err == nil {
		t.Fatal("accepted missing attempt")
	}
	r.Plan.AttemptID = "another"
	if Evaluate(r.Contract, r) == nil {
		t.Fatal("accepted another attempt's plan")
	}
	if err := s.SavePlan(r.Contract, r.Plan); err != nil {
		t.Fatal(err)
	}
	name, _ := planName(r.Contract, "another")
	r.Plan.AttemptID = "wrong"
	if err := s.root.RemoveAll(name); err != nil {
		t.Fatal(err)
	}
	if err := s.write(name, r.Plan); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadPlan(r.Contract, "another"); err == nil {
		t.Fatal("accepted mismatched stored plan attempt")
	}
}

func TestLatestRecordRetainsCancelledRunnerAttemptWithoutPlan(t *testing.T) {
	r, scratch, workspace := setupRunner(t)
	if _, err := r.Run(context.Background(), contract(), scratch, workspace); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, err := r.Run(ctx, contract(), scratch, workspace)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	latest, err := r.Store.LatestRecord(contract())
	if err != nil || latest.AttemptID != cancelled.AttemptID || !latest.Cancelled || latest.Verdict != Blocked || latest.Plan.Text != "" {
		t.Fatalf("lost cancelled attempt without plan: %+v %v", latest, err)
	}
}

func TestRunnerRejectsMissingProvenance(t *testing.T) {
	for _, field := range []string{"profile", "backend"} {
		t.Run(field, func(t *testing.T) {
			r, scratch, workspace := setupRunner(t)
			base := r.Execute
			r.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
				result, err := base(ctx, prompt, opts)
				if !opts.ToolsDisabled {
					if field == "profile" {
						result.Profile = ""
					} else {
						result.Backend = ""
					}
				}
				return result, err
			}
			record, err := r.Run(context.Background(), contract(), scratch, workspace)
			if err == nil || record.Verdict != Blocked {
				t.Fatalf("accepted missing %s", field)
			}
		})
	}
}

func TestLatestRecordKeepsNonpassingOutcome(t *testing.T) {
	for _, mode := range []string{"failed", "blocked", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s := openTestStore(t)
			first := passing()
			second := laterRecord(first, "second", 10*time.Second)
			second.Verdict = Blocked
			if mode == "failed" {
				second.Verdict = Failed
			}
			if mode == "cancelled" {
				second.Cancelled = true
			}
			if err := s.SaveRecord(first); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveRecord(second); err != nil {
				t.Fatal(err)
			}
			incomplete := laterRecord(second, "incomplete", 10*time.Second)
			incomplete.Completed = false
			if err := s.SaveRecord(incomplete); err != nil {
				t.Fatal(err)
			}
			latest, err := s.LatestRecord(first.Contract)
			if err != nil || latest.AttemptID != second.AttemptID || latest.Verdict != second.Verdict || latest.Cancelled != second.Cancelled {
				t.Fatalf("did not retain latest nonpassing attempt: %+v %v", latest, err)
			}
			if Evaluate(first.Contract, latest) == nil {
				t.Fatal("searched backward to a pass")
			}
		})
	}
}

func TestLatestRecordRejectsAmbiguousOrCorruptReferences(t *testing.T) {
	for _, mode := range []string{"start tie", "completion tie", "reversed completion", "incoherent timestamps", "plan timestamps", "review timestamps", "corrupt older", "corrupt newer", "wrong name", "symlink", "plan attempt"} {
		t.Run(mode, func(t *testing.T) {
			s := openTestStore(t)
			first := passing()
			second := laterRecord(first, "second", 10*time.Second)
			switch mode {
			case "start tie":
				second.StartedAt = first.StartedAt
			case "completion tie":
				first.CompletedAt = second.CompletedAt
			case "reversed completion":
				first.CompletedAt = second.CompletedAt.Add(time.Second)
			case "incoherent timestamps":
				second.CompletedAt = second.StartedAt.Add(-time.Second)
			case "plan timestamps":
				second.Plan.StartedAt = second.StartedAt.Add(-time.Second)
			case "review timestamps":
				second.ReviewStartedAt = second.CompletedAt.Add(time.Second)
			case "plan attempt":
				second.Plan.AttemptID = first.AttemptID
			}
			if err := s.SaveRecord(first); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveRecord(second); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "corrupt older", "corrupt newer":
				attempt := first.AttemptID
				if mode == "corrupt newer" {
					attempt = second.AttemptID
				}
				name, _ := recordName(first.Contract, attempt)
				if err := os.WriteFile(filepath.Join(s.path, name, "data.json"), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong name", "symlink":
				name, _ := recordName(first.Contract, "wrong")
				if mode == "wrong name" {
					if err := s.write(name, second); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := s.root.Symlink(".", name); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.LatestRecord(first.Contract); err == nil {
				t.Fatal("accepted ambiguous/corrupt references")
			}
		})
	}
}

func TestLatestRecordExactContractAndNoComplete(t *testing.T) {
	s := openTestStore(t)
	c := contract()
	if _, err := s.LatestRecord(c); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty store: %v", err)
	}
	r := passing()
	r.Completed = false
	if err := s.SaveRecord(r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestRecord(c); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no complete attempts: %v", err)
	}
	other := passing()
	other.Contract.Generation++
	other.ContractDigest, _ = other.Contract.Digest()
	other.Plan.ContractDigest = other.ContractDigest
	if err := s.SaveRecord(other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestRecord(c); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected another contract: %v", err)
	}
}

func TestNativeProvenanceValidation(t *testing.T) {
	for _, field := range []string{"planner profile", "planner backend", "reviewer profile", "reviewer backend"} {
		t.Run(field, func(t *testing.T) {
			r := passing()
			switch field {
			case "planner profile":
				r.Plan.Planner.Profile = ""
			case "planner backend":
				r.Plan.Planner.Backend = ""
			case "reviewer profile":
				r.Reviewer.Profile = ""
			case "reviewer backend":
				r.Reviewer.Backend = ""
			}
			if Evaluate(r.Contract, r) == nil {
				t.Fatal("accepted missing native provenance")
			}
		})
	}
	r := passing()
	r.Plan.Planner.Model = ""
	r.Reviewer.Model = ""
	if err := Evaluate(r.Contract, r); err != nil {
		t.Fatalf("unavailable model rejected: %v", err)
	}
}
