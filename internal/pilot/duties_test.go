package pilot

import (
	"testing"
)

func TestExtractDuties_happyPath(t *testing.T) {
	output := "Some prose before.\n\n" +
		"```pilot-duties\n" +
		"duties:\n" +
		"  pr_triage:\n" +
		"    status: action_taken\n" +
		"    actions:\n" +
		"      - \"PR #133: rebased onto main\"\n" +
		"  monitoring:\n" +
		"    status: ok\n" +
		"  doc_sync:\n" +
		"    status: flagged\n" +
		"    note: \"CLAUDE.md mentions removed health-check feature\"\n" +
		"  issue_digest:\n" +
		"    summary: |\n" +
		"      Two new bug reports about login flow; #42 closed as dup.\n" +
		"  backlog_hygiene:\n" +
		"    status: ok\n" +
		"```\n\n" +
		"PILOT-RESULT: 1 action — rebased PR #133\n"

	d := ExtractDuties(output)
	if d == nil {
		t.Fatal("expected duties to parse, got nil")
	}
	if d.PRTriage.Status != "action_taken" {
		t.Errorf("pr_triage status = %q, want action_taken", d.PRTriage.Status)
	}
	if len(d.PRTriage.Actions) != 1 || d.PRTriage.Actions[0] != "PR #133: rebased onto main" {
		t.Errorf("pr_triage actions = %#v", d.PRTriage.Actions)
	}
	if d.Monitoring.Status != "ok" {
		t.Errorf("monitoring status = %q, want ok", d.Monitoring.Status)
	}
	if d.DocSync.Status != "flagged" || d.DocSync.Note == "" {
		t.Errorf("doc_sync = %+v", d.DocSync)
	}
	if d.IssueDigest.Summary == "" {
		t.Error("issue_digest summary should be populated")
	}
}

func TestExtractDuties_missingBlock(t *testing.T) {
	out := "Just a free-form pilot response with no duties block.\n\nPILOT-RESULT: no-action — nothing\n"
	if d := ExtractDuties(out); d != nil {
		t.Errorf("expected nil for missing block, got %+v", d)
	}
}

func TestExtractDuties_unknownStatusNormalized(t *testing.T) {
	output := "```pilot-duties\n" +
		"duties:\n" +
		"  pr_triage:\n" +
		"    status: SUCCESS\n" + // not in the vocabulary
		"  monitoring:\n" +
		"    status: ok\n" +
		"  doc_sync:\n" +
		"    status: ok\n" +
		"  issue_digest:\n" +
		"    summary: \"\"\n" +
		"  backlog_hygiene:\n" +
		"    status: ok\n" +
		"```\n"
	d := ExtractDuties(output)
	if d == nil {
		t.Fatal("expected duties, got nil")
	}
	if d.PRTriage.Status != "ok" {
		t.Errorf("unknown status should normalize to 'ok', got %q", d.PRTriage.Status)
	}
	if d.PRTriage.Note == "" {
		t.Error("unknown status should leave a diagnostic in Note")
	}
}

func TestStripDutiesBlock(t *testing.T) {
	out := "before\n```pilot-duties\nduties: {}\n```\nafter"
	got := StripDutiesBlock(out)
	if got == out {
		t.Error("expected block to be stripped")
	}
	if !contains(got, "before") || !contains(got, "after") {
		t.Errorf("expected surrounding prose to survive, got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
