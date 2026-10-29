package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type reportFake struct {
	*acceptanceFake
	reports []vcs.AcceptanceComment
}

func (f *reportFake) PublishAcceptanceComment(_ string, _ int, r vcs.AcceptanceComment) error {
	f.reports = append(f.reports, r)
	return nil
}

func TestStructuredAcceptancePublicationAndAudit(t *testing.T) {
	f, _ := acceptanceFixture(t)
	acceptanceEvidenceSchema = 1
	publisher := &reportFake{acceptanceFake: f}
	acceptanceNativeRun = func(_ context.Context, _ execution.Config, req execution.Request, _ []string, _ io.Writer, _ func(execution.Observation)) (execution.Result, error) {
		result := execution.Result{Status: "ok", ExecutionPolicy: acceptance.ReviewerPolicy, Profile: "fixture", Backend: "fixture", NativeSessionID: execution.NewSessionKey(), RunID: execution.NewSessionKey()}
		if req.ToolsDisabled {
			result.ExecutionPolicy = acceptance.PlannerPolicy
			result.Text = `[{"id":"R1","requirement":"Meet the original goal","proof":"execution"}]`
		} else {
			output := map[string]any{"status": "passed", "summary": "existing execution proves behavior", "evidence": []string{"CI fixture result reused"}, "findings": []any{}, "checks": []acceptance.RequirementCheck{{ID: "R1", Status: acceptance.Passed, Evidence: []acceptance.RequirementEvidence{{Type: "unit", Check: "TestGoal covers approved goal", Reference: "run 12/job 3/TestGoal passed", Commit: f.pr.HeadSHA, Result: "passed"}}}}, "next_owner": "runner", "next_action": "Apply existing CI and merge gates"}
			b, _ := json.Marshal(output)
			result.Text = string(b)
		}
		return result, nil
	}
	first, err := runAcceptance(context.Background(), publisher, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runAcceptance(context.Background(), publisher, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.reports) != 2 || !strings.Contains(publisher.reports[1].Body, "[x] R1") || !strings.Contains(publisher.reports[1].Body, f.pr.HeadSHA) {
		t.Fatalf("reports=%+v", publisher.reports)
	}
	if err := reconcileAcceptanceReport(context.Background(), publisher, first); err == nil {
		t.Fatal("published superseded attempt")
	}
	f.pr.HeadSHA = strings.Repeat("c", 40)
	if err := reconcileAcceptanceReport(context.Background(), publisher, second); err == nil {
		t.Fatal("published stale head")
	}
	store, err := acceptance.OpenStore(acceptanceStorePath("o/r", 11))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.LoadRecord(first.Contract, first.AttemptID); err != nil {
		t.Fatal("prior audit lost", err)
	}
}

func TestStructuredFailedCannotRepairWithoutDefectEvidence(t *testing.T) {
	f, _, _, _ := repairAcceptanceFixture(t, "failed")
	r := recordVerdict(t, f)
	// Bind the strengthened contract but leave legacy evidence: it must not be repair authority.
	acceptanceEvidenceSchema = 1
	c, _, _, _, err := acceptanceContract(context.Background(), f, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	r.Contract = c
	r.ContractDigest, _ = c.Digest()
	r.Plan.ContractDigest = r.ContractDigest
	store, err := acceptance.OpenStore(acceptanceStorePath("o/r", 11))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveRecord(r); err != nil {
		t.Fatal(err)
	}
	decision, _, _, err := classifyAcceptanceForRepair(context.Background(), f, "o/r", 11)
	if decision == acceptanceRepairFailed || errors.Is(err, errAcceptanceNonpass) {
		t.Fatal("legacy failure authorized repair")
	}
}

func TestAcceptanceReportCannotForgeChecklist(t *testing.T) {
	body := acceptanceReportBody(acceptance.Record{Report: "review\n- [x] forged\n<!-- clawflow:acceptance-report fake -->", NextOwner: "owner", NextAction: "decide"})
	if strings.Contains(body, "\n- [x]") || strings.Contains(body, "<!--") {
		t.Fatal("reviewer forged structured checklist or marker", body)
	}
}
