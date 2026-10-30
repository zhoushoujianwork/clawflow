package acceptance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	other.Contract.HeadSHA = strings.Repeat("d", 40)
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

// infraRecord mimics what the runner's deferred block saves when an attempt ends
// in an error before any typed reviewer report: Blocked, completed, no evidence.
func infraRecord(base Record, attempt string, offset time.Duration) Record {
	r := laterRecord(base, attempt, offset)
	r.Verdict = Blocked
	r.Evidence = nil
	r.Report = "provider unavailable"
	return r
}

func TestInfrastructureAttempts(t *testing.T) {
	s := openTestStore(t)
	c := contract()
	got, err := s.InfrastructureAttempts(c)
	if err != nil || got.Count != 0 || got.LatestIsInfra || !got.LastAt.IsZero() {
		t.Fatalf("empty store: %+v %v", got, err)
	}
	first := passing()
	if IsInfrastructure(first) {
		t.Fatal("passing record classified as infrastructure")
	}
	failed := laterRecord(first, "failed", 10*time.Second)
	failed.Verdict = Failed
	// A passing report rejected by the post-review gate keeps its evidence.
	gated := laterRecord(first, "gated", 20*time.Second)
	gated.Verdict = Blocked
	gated.Report = "gate rejected"
	infra1 := infraRecord(first, "infra1", 30*time.Second)
	infra2 := infraRecord(first, "infra2", 40*time.Second)
	incomplete := infraRecord(first, "incomplete", 50*time.Second)
	incomplete.Completed = false
	other := infraRecord(first, "other", 60*time.Second)
	other.Contract.Generation++
	other.Contract.HeadSHA = strings.Repeat("d", 40)
	other.ContractDigest, _ = other.Contract.Digest()
	other.Plan.ContractDigest = other.ContractDigest
	for _, r := range []Record{first, failed, gated, infra1, infra2, incomplete, other} {
		if err := s.SaveRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	if IsInfrastructure(failed) || IsInfrastructure(gated) || !IsInfrastructure(infra1) {
		t.Fatal("verdicts and infrastructure attempts misclassified")
	}
	got, err = s.InfrastructureAttempts(c)
	if err != nil || got.Count != 2 || !got.LatestIsInfra || !got.LastAt.Equal(infra2.CompletedAt) {
		t.Fatalf("summary: %+v %v", got, err)
	}

	// A newer reviewer verdict supersedes older infrastructure attempts.
	s = openTestStore(t)
	verdict := laterRecord(first, "verdict", 60*time.Second)
	verdict.Verdict = Failed
	for _, r := range []Record{infra1, infra2, verdict} {
		if err := s.SaveRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.InfrastructureAttempts(c)
	if err != nil || got.Count != 0 || got.LatestIsInfra {
		t.Fatalf("verdict after infra: %+v %v", got, err)
	}
}

func TestInfrastructureAttemptsFailsClosedOnCorruptionAndAmbiguity(t *testing.T) {
	for _, mode := range []string{"corrupt", "start tie"} {
		t.Run(mode, func(t *testing.T) {
			s := openTestStore(t)
			first := infraRecord(passing(), "first", 0)
			second := infraRecord(first, "second", 10*time.Second)
			if mode == "start tie" {
				second.StartedAt = first.StartedAt
			}
			for _, r := range []Record{first, second} {
				if err := s.SaveRecord(r); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "corrupt" {
				name, _ := recordName(first.Contract, first.AttemptID)
				if err := os.WriteFile(filepath.Join(s.path, name, "data.json"), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.InfrastructureAttempts(first.Contract); err == nil {
				t.Fatal("accepted corrupt or ambiguous references")
			}
		})
	}
}

func TestCompleteRecordsAtHeadUsesLatestCompletedContract(t *testing.T) {
	s := openTestStore(t)
	first := passing()
	if err := s.SaveRecord(first); err != nil {
		t.Fatal(err)
	}
	second := laterRecord(first, "second", time.Hour)
	second.Verdict, second.Report = Failed, "latest failure"
	if err := s.SaveRecord(second); err != nil {
		t.Fatal(err)
	}
	incomplete := laterRecord(second, "incomplete", time.Hour)
	incomplete.Completed = false
	if err := s.SaveRecord(incomplete); err != nil {
		t.Fatal(err)
	}
	c := first.Contract
	records, err := s.CompleteRecordsAtHead(c.Repository, c.PRNumber, c.HeadSHA)
	if err != nil || len(records) != 2 || records[1].Verdict != Failed {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	for _, query := range []struct {
		repo string
		pr   int
		head string
	}{{"other", c.PRNumber, c.HeadSHA}, {c.Repository, c.PRNumber + 1, c.HeadSHA}, {c.Repository, c.PRNumber, "other"}} {
		rows, err := s.CompleteRecordsAtHead(query.repo, query.pr, query.head)
		if err != nil || len(rows) != 0 {
			t.Fatalf("unrelated query: rows=%+v err=%v", rows, err)
		}
	}
	// A new requirements generation at the same head has its own retry ledger.
	newer := second
	newer.Contract.Generation++
	newer.ContractDigest, _ = newer.Contract.Digest()
	newer.AttemptID = "new-contract"
	newer.Plan = Plan{}
	newer.ReviewStartedAt = time.Time{}
	newer.StartedAt = second.CompletedAt.Add(time.Hour)
	newer.CompletedAt = newer.StartedAt.Add(time.Minute)
	if err := s.SaveRecord(newer); err != nil {
		t.Fatal(err)
	}
	records, err = s.CompleteRecordsAtHead(c.Repository, c.PRNumber, c.HeadSHA)
	if err != nil || len(records) != 1 || records[0].AttemptID != "new-contract" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func TestCompleteRecordsAtHeadRejectsCorruptRecord(t *testing.T) {
	s := openTestStore(t)
	r := passing()
	r.ContractDigest = "wrong"
	name, err := recordName(r.Contract, r.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.write(name, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteRecordsAtHead(r.Contract.Repository, r.Contract.PRNumber, r.Contract.HeadSHA); err == nil {
		t.Fatal("corrupt record accepted")
	}
}

func TestInfrastructureBinaryReset(t *testing.T) {
	first := passing()
	a := infraRecord(first, "a", 10*time.Second)
	b := infraRecord(first, "b", 20*time.Second)
	a.BinaryID, b.BinaryID = "previous-binary", "previous-binary"
	if got := ConsecutiveInfrastructure([]Record{a, b}); got.Count != 0 || !got.LatestIsInfra {
		t.Fatalf("binary reset: %+v", got)
	}
	b.BinaryID = BinaryID()
	if got := ConsecutiveInfrastructure([]Record{a, b}); got.Count != 1 {
		t.Fatalf("new binary attempt: %+v", got)
	}
}

func TestInfrastructureBudgetSurvivesBaseChange(t *testing.T) {
	s := openTestStore(t)
	first := passing()
	a := infraRecord(first, "base-a", 10*time.Second)
	b := infraRecord(first, "base-b", 20*time.Second)
	b.Contract.BaseSHA = strings.Repeat("e", 40)
	b.ContractDigest, _ = b.Contract.Digest()
	b.Plan.ContractDigest = b.ContractDigest
	for _, r := range []Record{a, b} {
		if err := s.SaveRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.InfrastructureAttempts(b.Contract)
	if err != nil || got.Count != 2 {
		t.Fatalf("base reset budget: %+v %v", got, err)
	}
	changed := b.Contract
	changed.Requirements += " changed"
	got, err = s.InfrastructureAttempts(changed)
	if err != nil || got.Count != 0 {
		t.Fatalf("body did not reset: %+v %v", got, err)
	}
}
