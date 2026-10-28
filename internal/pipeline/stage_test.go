package pipeline

import (
	"strings"
	"testing"
	"time"
)

func openPR(ci string) *PRInfo { return &PRInfo{Number: 7, State: "open", CI: ci, Mergeable: "clean"} }

func TestDeriveStages(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(10 * time.Minute).Format(time.RFC3339)
	past := now.Add(-10 * time.Minute).Format(time.RFC3339)
	passed := &AcceptanceInfo{Required: true, Attempts: 1, Verdict: "passed", Valid: true}
	cases := []struct {
		name      string
		ev        Evidence
		want      Stage
		whyHas    string
		evidenced string
	}{
		{"queued without anything", Evidence{IssueState: "open"}, StageQueued, "next run", ""},
		{"implementing", Evidence{IssueState: "open", Run: &RunInfo{Operator: "implement", Status: "running", Stage: "offload-started", Live: true}}, StageImplementing, "offload-started", "implement run"},
		{"stale running meta is failed", Evidence{IssueState: "open", Run: &RunInfo{Operator: "implement", Status: "running"}}, StageFailed, "runner is gone", ""},
		{"run failed", Evidence{IssueState: "open", Run: &RunInfo{Operator: "implement", Status: "failed"}}, StageFailed, "failed", ""},
		{"agent-failed label", Evidence{IssueState: "open", Labels: []string{"agent-failed"}}, StageFailed, "agent-failed", ""},
		{"rate limited is blocked", Evidence{IssueState: "open", Run: &RunInfo{Operator: "implement", Status: "rate-limited"}}, StageBlocked, "rate limit", ""},
		{"agent-skipped is blocked", Evidence{IssueState: "open", Labels: []string{"agent-skipped"}}, StageBlocked, "more information", ""},
		{"implemented without PR", Evidence{IssueState: "open", Labels: []string{"agent-implemented"}}, StageBlocked, "no pull request", ""},
		{"closed issue without PR", Evidence{IssueState: "closed"}, StageBlocked, "closed", ""},
		{"pr open, nothing required", Evidence{IssueState: "open", PR: openPR("success")}, StagePROpen, "open", "PR #7"},
		{"ci pending", Evidence{IssueState: "open", PR: openPR("pending")}, StageCIPending, "still running", "ci=pending"},
		{"required ci reports no checks", Evidence{IssueState: "open", CIRequired: true, PR: openPR("no-checks")}, StageCIPending, "no checks", ""},
		{"ci failed", Evidence{IssueState: "open", PR: openPR("failure")}, StageCIFailed, "CI failed", "ci=failure"},
		{"ci failed with repair running", Evidence{IssueState: "open", PR: openPR("failure"), Run: &RunInfo{Operator: "repair-ci", Status: "running", Live: true}}, StageCIFailed, "repair-ci is running", ""},
		{"ci unreadable while required", Evidence{IssueState: "open", CIRequired: true, PR: &PRInfo{Number: 7, State: "open", CI: "unknown", CIError: "HTTP 502"}}, StageBlocked, "HTTP 502", ""},
		{"merge conflict", Evidence{IssueState: "open", PR: &PRInfo{Number: 7, State: "open", CI: "success", Mergeable: "conflict"}}, StageBlocked, "conflicts", ""},
		{"draft", Evidence{IssueState: "open", PR: &PRInfo{Number: 7, State: "open", Draft: true}}, StageBlocked, "draft", ""},
		{"acceptance pending without record", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("success")}, StageAcceptancePending, "waiting", ""},
		{"acceptance pending with empty info", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: &AcceptanceInfo{Required: true}}, StageAcceptancePending, "waiting", ""},
		{"acceptance overlapping CI", Evidence{IssueState: "open", CIRequired: true, AcceptanceRequired: true, PR: openPR("pending"), Acceptance: &AcceptanceInfo{Required: true, Running: true}}, StageAcceptanceRunning, "acceptance running (CI pending)", "ci=pending"},
		{"acceptance running", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: &AcceptanceInfo{Required: true, Running: true}}, StageAcceptanceRunning, "reviewing", "in progress"},
		{"accepted awaiting auto-merge", Evidence{IssueState: "open", AcceptanceRequired: true, AutoMerge: true, PR: openPR("success"), Acceptance: passed}, StageAccepted, "auto-merge", "verdict=passed"},
		{"accepted awaiting manual merge", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: passed}, StageAccepted, "manual merge", ""},
		{"stale pass is not accepted", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: &AcceptanceInfo{Required: true, Attempts: 1, Verdict: "passed"}}, StageAcceptancePending, "no longer matches", ""},
		{"rejected failed", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: &AcceptanceInfo{Required: true, Attempts: 1, Verdict: "failed"}}, StageRejected, "failed", "verdict=failed"},
		{"rejected blocked", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: &AcceptanceInfo{Required: true, Attempts: 2, Verdict: "blocked"}}, StageRejected, "blocked", ""},
		{"infra retry with future time", Evidence{Now: now, IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: &AcceptanceInfo{Required: true, Attempts: 1, InfraAttempts: 1, LatestIsInfra: true, MaxInfraRetries: 3, NextRetryAt: future}}, StageInfraRetry, "next retry at " + future, "infrastructure=1"},
		{"infra retry due", Evidence{Now: now, IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: &AcceptanceInfo{Required: true, Attempts: 2, InfraAttempts: 2, LatestIsInfra: true, MaxInfraRetries: 3, NextRetryAt: past}}, StageInfraRetry, "due", ""},
		{"infra exhausted is blocked", Evidence{Now: now, IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: &AcceptanceInfo{Required: true, Attempts: 3, InfraAttempts: 3, LatestIsInfra: true, MaxInfraRetries: 3, NextRetryAt: past}}, StageBlocked, "retries stopped", ""},
		{"acceptance contract error", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("success"), Acceptance: &AcceptanceInfo{Required: true, Error: "no development provenance"}}, StageBlocked, "no development provenance", ""},
		{"ci failure wins over accepted", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("failure"), Acceptance: passed}, StageCIFailed, "CI failed", ""},
		{"running reviewer wins over ci", Evidence{IssueState: "open", AcceptanceRequired: true, PR: openPR("failure"), Acceptance: &AcceptanceInfo{Running: true}}, StageAcceptanceRunning, "", ""},
		{"merged", Evidence{IssueState: "closed", PR: &PRInfo{Number: 7, State: "merged"}}, StageMerged, "merged", "PR #7 is merged"},
		{"merged wins over failed run", Evidence{IssueState: "closed", Run: &RunInfo{Operator: "implement", Status: "failed"}, PR: &PRInfo{Number: 7, State: "merged"}}, StageMerged, "", ""},
		{"closed PR", Evidence{IssueState: "open", PR: &PRInfo{Number: 7, State: "closed"}}, StageBlocked, "closed without merging", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why, ev := Derive(tc.ev)
			if got != tc.want {
				t.Fatalf("stage = %q (%s), want %q", got, why, tc.want)
			}
			if why == "" {
				t.Fatal("empty why")
			}
			if !strings.Contains(why, tc.whyHas) {
				t.Fatalf("why %q lacks %q", why, tc.whyHas)
			}
			if tc.evidenced != "" && !strings.Contains(strings.Join(ev, "\n"), tc.evidenced) {
				t.Fatalf("evidence %v lacks %q", ev, tc.evidenced)
			}
		})
	}
}

func TestLatest(t *testing.T) {
	if got := Latest("", "garbage", "2026-01-02T03:04:05Z", "2026-01-02T03:04:06+00:00"); got != "2026-01-02T03:04:06Z" {
		t.Fatalf("got %q", got)
	}
	if Latest("", "x") != "" {
		t.Fatal("expected empty")
	}
}
