package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func isolatedContract() Contract {
	c := contract()
	c.IsolatedRun = true
	return c
}

func cleanIsolated(c Contract) IsolatedRun {
	now := time.Now().UTC()
	r := IsolatedRun{Commit: c.HeadSHA, RuntimeID: "0123456789ab", Status: IsolatedPassed, Triggers: []string{"internal/store/store.go"},
		Summary: "isolated run passed of candidate 0123456789ab", Digest: strings.Repeat("a", 64), StartedAt: now, CompletedAt: now.Add(time.Second)}
	for _, n := range isolatedStepNames {
		r.Steps = append(r.Steps, IsolatedStep{Name: n, Passed: true, Evidence: n + " observed"})
	}
	r.ResidualProcesses, r.ResidualLocks = []string{}, []string{}
	return r
}

func failedIsolated(c Contract) IsolatedRun {
	r := cleanIsolated(c)
	r.Status = IsolatedFailed
	r.ResidualProcesses = []string{"sleep (pid 9)"}
	r.Failure = ""
	r.Summary = "isolated run FAILED of candidate 0123456789ab; residual processes: 1"
	return r
}

// The status of a run must follow from its steps, residue and failure, so an
// edited record cannot report a pass over a leftover process or a failed step.
func TestIsolatedRunStatusMustFollowItsFacts(t *testing.T) {
	c := isolatedContract()
	if err := cleanIsolated(c).Validate(c); err != nil {
		t.Fatal(err)
	}
	if err := failedIsolated(c).Validate(c); err != nil {
		t.Fatalf("a coherent failed run must validate: %v", err)
	}
	cases := map[string]func(*IsolatedRun){
		"passed over a leftover process": func(r *IsolatedRun) { r.ResidualProcesses = []string{"x"} },
		"passed over a leftover lock":    func(r *IsolatedRun) { r.ResidualLocks = []string{"x.lock"} },
		"passed over a failed step":      func(r *IsolatedRun) { r.Steps[2].Passed = false },
		"passed over a recorded failure": func(r *IsolatedRun) { r.Failure = "boom" },
		"passed with a missing step":     func(r *IsolatedRun) { r.Steps = r.Steps[:3] },
		"passed with reordered steps":    func(r *IsolatedRun) { r.Steps[0], r.Steps[1] = r.Steps[1], r.Steps[0] },
		"another commit":                 func(r *IsolatedRun) { r.Commit = "other" },
		"no triggers":                    func(r *IsolatedRun) { r.Triggers = nil },
		"unknown status":                 func(r *IsolatedRun) { r.Status = "blocked" },
		"no evidence digest":             func(r *IsolatedRun) { r.Digest = "" },
		"time travel":                    func(r *IsolatedRun) { r.CompletedAt = r.StartedAt.Add(-time.Second) },
	}
	for name, mutate := range cases {
		r := cleanIsolated(c)
		mutate(&r)
		if r.Validate(c) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A failed status over a clean run is also incoherent.
	r := cleanIsolated(c)
	r.Status = IsolatedFailed
	if r.Validate(c) == nil {
		t.Error("failed status over a clean run accepted")
	}
}

func passingIsolatedRecord(t *testing.T) Record {
	t.Helper()
	rec := structuredRecord()
	rec.Contract.IsolatedRun = true
	d, _ := rec.Contract.Digest()
	rec.ContractDigest, rec.Plan.ContractDigest = d, d
	run := cleanIsolated(rec.Contract)
	rec.IsolatedRun = &run
	rec.Evidence = append(rec.Evidence, isolatedEvidenceLine(run))
	return rec
}

func TestEvaluateRequiresAPassedIsolatedRunWhenTheContractDoes(t *testing.T) {
	rec := passingIsolatedRecord(t)
	c := rec.Contract
	if err := Evaluate(c, rec); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Record){
		"no run":          func(r *Record) { r.IsolatedRun = nil },
		"failed run":      func(r *Record) { run := failedIsolated(c); r.IsolatedRun = &run },
		"run of old head": func(r *Record) { r.IsolatedRun.Commit = "old" },
		"forged status":   func(r *Record) { r.IsolatedRun.ResidualProcesses = []string{"x"} },
	}
	for name, mutate := range cases {
		r := passingIsolatedRecord(t)
		mutate(&r)
		if Evaluate(c, r) == nil {
			t.Errorf("%s: a passing record was accepted", name)
		}
	}
	// A contract that does not require a run must not carry one.
	plain := structuredRecord()
	run := cleanIsolated(plain.Contract)
	plain.IsolatedRun = &run
	if err := validateIsolated(plain.Contract, plain); err == nil {
		t.Error("a run attached to a contract that did not ask for it was accepted")
	}
	if err := Evaluate(structuredRecord().Contract, structuredRecord()); err != nil {
		t.Fatalf("a contract without the requirement must be unaffected: %v", err)
	}
}

func TestIsolatedFlagChangesTheContractIdentityOnlyWhenSet(t *testing.T) {
	plain, _ := contract().Digest()
	set, _ := isolatedContract().Digest()
	if plain == set {
		t.Fatal("requiring a run must change the contract digest, so an old pass cannot satisfy it")
	}
	again, _ := contract().Digest()
	if plain != again {
		t.Fatal("digest of an unflagged contract changed")
	}
	if strings.Contains(string(mustJSON(contract())), "isolated_run") {
		t.Fatal("the flag leaked into existing contracts, changing their digests")
	}
}

// schemaOneContract is the production contract shape: schema 1 with a saved
// criteria plan, which is what the reviewer's checks are validated against.
func schemaOneContract() Contract {
	c := isolatedContract()
	c.EvidenceSchema = 1
	c.Requirements = "Required behavior"
	return c
}

const isolatedPassReport = `{"status":"passed","summary":"verified","evidence":["behavior check passed"],` +
	`"findings":[],"checks":[{"id":"R1","status":"passed","evidence":[{"type":"unit","check":"TestRequiredBehavior covers required behavior","reference":"CI run 7/job 9/TestRequiredBehavior: PASS","commit":"head","result":"passed"}],"missing":""}],` +
	`"next_action":"Apply CI and merge gates","next_owner":"runner"}`

func schemaOneRunner(t *testing.T, c Contract, report string) (Runner, string, string) {
	t.Helper()
	r, scratch, workspace := setupRunnerFor(t, c)
	r.Execute = func(_ context.Context, prompt string, o execution.Options) (execution.Result, error) {
		if o.Workspace == scratch {
			res := successful("planner", "plan-run")
			res.Text = `[{"id":"R1","requirement":"Required behavior","proof":"execution"}]`
			return res, nil
		}
		if o.Workspace != workspace || o.ToolsDisabled || o.PermissionMode != "bypassPermissions" {
			t.Fatal("reviewer workspace/tools/policy mismatch")
		}
		res := reviewerResult("reviewer", "review-run")
		res.Text = report
		return res, nil
	}
	return r, scratch, workspace
}

func runIsolated(t *testing.T, hook func(context.Context, Contract, string) (IsolatedRun, error), report string) (Record, error) {
	t.Helper()
	if report == "" {
		report = isolatedPassReport
	}
	c := schemaOneContract()
	r, scratch, workspace := schemaOneRunner(t, c, report)
	r.Isolated = hook
	return r.Run(context.Background(), c, scratch, workspace)
}

func TestRunnerRunsTheIsolatedRunFirstAndTheVerdictCitesIt(t *testing.T) {
	c := schemaOneContract()
	var calls int
	order := []string{}
	r, scratch, workspace := schemaOneRunner(t, c, isolatedPassReport)
	base := r.Execute
	r.Execute = func(ctx context.Context, p string, o execution.Options) (execution.Result, error) {
		if o.Workspace == workspace {
			order = append(order, "review")
			if !strings.Contains(p, "Isolated real-run evidence") || !strings.Contains(p, "status passed") {
				t.Error("the reviewer was not shown the run")
			}
		}
		return base(ctx, p, o)
	}
	r.Isolated = func(_ context.Context, got Contract, w string) (IsolatedRun, error) {
		calls++
		order = append(order, "run")
		if w != workspace || got.HeadSHA != c.HeadSHA {
			t.Error("run did not receive the review checkout and contract")
		}
		return cleanIsolated(got), nil
	}
	rec, err := r.Run(context.Background(), c, scratch, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || strings.Join(order, ",") != "run,review" {
		t.Fatalf("calls=%d order=%v", calls, order)
	}
	if rec.IsolatedRun == nil || rec.Verdict != Passed {
		t.Fatalf("verdict %s run %+v", rec.Verdict, rec.IsolatedRun)
	}
	cited := false
	for _, e := range rec.Evidence {
		cited = cited || strings.HasPrefix(e, "isolated-run: ") && strings.Contains(e, strings.Repeat("a", 64))
	}
	if !cited {
		t.Fatalf("verdict does not cite the run: %v", rec.Evidence)
	}
	if err := Evaluate(c, rec); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUpload(c, rec); err != nil {
		t.Fatalf("the cited record is not uploadable: %v", err)
	}
	stored, err := r.Store.LoadRecord(c, rec.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.IsolatedRun == nil || Evaluate(c, stored) != nil {
		t.Fatal("the stored record lost or invalidated the run")
	}
}

// A failed run decides the verdict. The reviewer approving the code, or even
// reporting a pass, cannot override evidence that the head misbehaves when run.
func TestAFailedIsolatedRunYieldsFailedWhateverTheReviewerSays(t *testing.T) {
	c := schemaOneContract()
	for name, reviewer := range map[string]string{
		"reviewer passes": "",
		"reviewer blocks": `{"status":"blocked","summary":"cannot tell","evidence":["x"],"findings":[],` +
			`"checks":[{"id":"R1","status":"blocked","evidence":[],"missing":"needs a human"}],"next_action":"human","next_owner":"owner"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := runIsolated(t, func(_ context.Context, got Contract, _ string) (IsolatedRun, error) {
				return failedIsolated(got), nil
			}, reviewer)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Verdict != Failed {
				t.Fatalf("verdict %s, want failed", rec.Verdict)
			}
			if Evaluate(c, rec) == nil {
				t.Fatal("a failed record passed the gate")
			}
			if rec.NextOwner != "implementer" {
				t.Fatalf("a failed run must go to the repair flow, next owner %q", rec.NextOwner)
			}
			var defect *Finding
			for i := range rec.Findings {
				if rec.Findings[i].Observation != nil && strings.HasPrefix(rec.Findings[i].Observation.Reference, "isolated run ") {
					defect = &rec.Findings[i]
				}
			}
			if defect == nil || !defect.Failing() || defect.Observation.Commit != c.HeadSHA || defect.Observation.Result != "failed" {
				t.Fatalf("no exact-head failing finding for the run: %+v", rec.Findings)
			}
			if !strings.Contains(strings.Join(rec.Evidence, "\n"), "isolated-run: ") {
				t.Fatalf("a failed verdict must still cite the run: %v", rec.Evidence)
			}
			// The existing repair flow only acts on a stored record that passes upload checks.
			if err := ValidateUpload(c, rec); err != nil {
				t.Fatalf("the failed record is not storable for repair: %v", err)
			}
		})
	}
}

func TestIsolatedRunInfrastructureErrorIsNotAVerdict(t *testing.T) {
	rec, err := runIsolated(t, func(context.Context, Contract, string) (IsolatedRun, error) {
		return IsolatedRun{}, errors.New("port busy")
	}, "")
	if err == nil {
		t.Fatal("an unattempted run produced a verdict")
	}
	if IsInfrastructure(rec) != true || rec.Verdict != Blocked || len(rec.Evidence) != 0 {
		t.Fatalf("an unattempted run must be an evidence-free infrastructure attempt: verdict=%s evidence=%v", rec.Verdict, rec.Evidence)
	}
	if rec.Verdict == Failed {
		t.Fatal("an environment problem was reported as a code defect")
	}
}

func TestMissingIsolatedHookNeverPasses(t *testing.T) {
	_, err := runIsolated(t, nil, "")
	if err == nil {
		t.Fatal("a contract requiring a run passed with no runner")
	}
}

func TestRunWithoutRequirementDoesNotCallTheHook(t *testing.T) {
	r, scratch, workspace := setupRunner(t)
	r.Isolated = func(context.Context, Contract, string) (IsolatedRun, error) {
		t.Error("the hook ran for a contract that does not require a run")
		return IsolatedRun{}, nil
	}
	rec, err := r.Run(context.Background(), contract(), scratch, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if rec.IsolatedRun != nil || rec.Verdict != Passed {
		t.Fatalf("verdict %s, run %v", rec.Verdict, rec.IsolatedRun)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
