package operator

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

// evalBody builds an evaluate-bug body in the exact shape observed in the
// salvaged run artifacts of issue #307: full template, footer last, no marker.
func evalBody(conf string) string {
	return `## 🔍 ClawFlow Bug Evaluation

**Reproducibility:** 10/10 — reproduced locally from the run artifacts.
**Root cause:** 10/10 — traced to internal/operator/runner.go.
**Fix difficulty:** 8/10 — one new function plus a branch.

**Confidence:** ` + conf + `/10 ✅ above threshold

### Repro steps
scan the run metas

### Root cause analysis
the guard keys off the marker

### Suggested fix
salvage the body

---

👉 If this plan looks right, add the ` + "`ready-for-agent`" + ` label to kick off automatic implementation.`
}

func TestSalvageOutcome(t *testing.T) {
	evalOutcomes := []string{"agent-evaluated", "agent-skipped"}

	featBody := `## 🔍 ClawFlow Feature Evaluation

**Clarity:** 8/10 — clear ask.
**Scope:** 6/10 — touches three modules.
**Architecture fit:** 8/10 — slots in.

**Confidence:** 7.3/10 ✅ 高于阈值

### Summary of the ask
do the thing

---

👉 If this plan looks right, add the ` + "`ready-for-agent`" + ` label to kick off automatic implementation.`

	tests := []struct {
		name     string
		op       string
		outcomes []string
		body     string
		want     string
		wantConf float64
		wantOK   bool
	}{
		{
			name: "evaluate-bug above threshold", op: "evaluate-bug", outcomes: evalOutcomes,
			body: evalBody("9.3"), want: "agent-evaluated", wantConf: 9.3, wantOK: true,
		},
		{
			// easyeda-agent#152: full 7185-char body, Confidence below the bar.
			// Salvage must honour the threshold, not assume a pass.
			name: "evaluate-bug below threshold", op: "evaluate-bug", outcomes: evalOutcomes,
			body: evalBody("6.7"), want: "agent-skipped", wantConf: 6.7, wantOK: true,
		},
		{
			name: "integer confidence", op: "evaluate-bug", outcomes: evalOutcomes,
			body: evalBody("9"), want: "agent-evaluated", wantConf: 9, wantOK: true,
		},
		{
			// Exactly at the documented 7.0 threshold → evaluated, matching the
			// SKILL.md wording ("below 7.0 → agent-skipped").
			name: "exactly at threshold", op: "evaluate-bug", outcomes: evalOutcomes,
			body: evalBody("7.0"), want: "agent-evaluated", wantConf: 7, wantOK: true,
		},
		{
			// evaluate-feat uses different dimension names (issue #307 comment):
			// hardcoding evaluate-bug's names would drop this one.
			name: "evaluate-feat dimensions", op: "evaluate-feat", outcomes: evalOutcomes,
			body: featBody, want: "agent-evaluated", wantConf: 7.3, wantOK: true,
		},
		{
			// clawflow#308 dropped the Confidence line together with the
			// marker. All three dimensions are present, so Confidence is
			// recomputed as their average per the SKILL.md definition:
			// (9 + 10 + 8) / 3 = 9.0.
			name: "confidence derived from full dimension set", op: "evaluate-bug", outcomes: evalOutcomes,
			body: `**Reproducibility:** 9/10 — ok
**Root cause:** 10/10 — ok
**Fix difficulty:** 8/10 — ok

### Suggested fix
do it`,
			want: "agent-evaluated", wantConf: 9, wantOK: true,
		},
		{
			// Derived average below the bar → agent-skipped: (6+5+7)/3 = 6.0.
			name: "derived confidence below threshold", op: "evaluate-bug", outcomes: evalOutcomes,
			body: `**Reproducibility:** 6/10 — ok
**Root cause:** 5/10 — ok
**Fix difficulty:** 7/10 — ok`,
			want: "agent-skipped", wantConf: 6, wantOK: true,
		},
		{
			// No Confidence line AND an incomplete dimension set: averaging two
			// of three would misstate the score, so keep discarding.
			name: "no confidence and partial dimensions", op: "evaluate-bug", outcomes: evalOutcomes,
			body:   "**Reproducibility:** 9/10 — ok\n**Root cause:** 8/10 — ok",
			wantOK: false,
		},
		{
			// The #143 form: model self-posted, stdout is a one-liner. Must NOT
			// be salvaged — posting it would accumulate meta-comment noise.
			name: "short self-post summary", op: "evaluate-bug", outcomes: evalOutcomes,
			body: "评估已完成。", wantOK: false,
		},
		{
			name: "confidence but no dimensions", op: "evaluate-bug", outcomes: evalOutcomes,
			body: "**Confidence:** 9.0/10 ✅ above threshold", wantOK: false,
		},
		{
			// Only one dimension line present: too weak a signal, could be
			// prose quoting a score.
			name: "single dimension", op: "evaluate-bug", outcomes: evalOutcomes,
			body: "**Reproducibility:** 9/10 — ok\n\n**Confidence:** 9/10", wantOK: false,
		},
		{
			// Non-evaluator operators have no Confidence contract to infer from.
			name: "non-eval operator", op: "implement", outcomes: []string{"agent-implemented"},
			body: evalBody("9.3"), wantOK: false,
		},
		{
			// Salvage may only pick labels the operator is allowed to apply.
			name: "eval name but foreign outcomes", op: "evaluate-bug", outcomes: []string{"agent-implemented"},
			body: evalBody("9.3"), wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := &Operator{Name: tt.op, Outcomes: tt.outcomes}
			got, conf, ok := salvageOutcome(op, tt.body)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (outcome %q)", ok, tt.wantOK, got)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Errorf("outcome = %q, want %q", got, tt.want)
			}
			if conf != tt.wantConf {
				t.Errorf("confidence = %v, want %v", conf, tt.wantConf)
			}
		})
	}
}

// TestRun_MarkerMissing_EvaluationBodySalvaged is the end-to-end contract of
// issue #307: an evaluate-bug run whose stdout stops one line short of the
// marker must still post its comment, apply the inferred label, and report no
// error — instead of discarding 4000+ characters of paid-for analysis.
func TestRun_MarkerMissing_EvaluationBodySalvaged(t *testing.T) {
	op := &Operator{
		Name:      "evaluate-bug",
		LockLabel: "agent-running",
		Prompt:    "evaluate",
		Outcomes:  []string{"agent-evaluated", "agent-skipped"},
	}
	sub := &Subject{Number: 307, Labels: []string{"bug"}}
	v := newFakeVCS()

	var recoveredOutcome string
	var recoveredConf float64

	body, outcome, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:    "zhoushoujianwork/clawflow",
		Workdir: t.TempDir(),
		Timeout: time.Second,
		RunFunc: func(_ context.Context, _, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
			return evalBody("9.3"), nil
		},
		MarkerRecovered: func(o string, c float64) {
			recoveredOutcome, recoveredConf = o, c
		},
	})
	if err != nil {
		t.Fatalf("Run returned error, want nil (body should be salvaged): %v", err)
	}
	if outcome != "agent-evaluated" {
		t.Errorf("outcome = %q, want agent-evaluated", outcome)
	}
	if recoveredOutcome != "agent-evaluated" || recoveredConf != 9.3 {
		t.Errorf("MarkerRecovered got (%q, %v), want (agent-evaluated, 9.3)", recoveredOutcome, recoveredConf)
	}
	if len(v.comments) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(v.comments))
	}
	posted := v.comments[0].body
	if !strings.Contains(posted, "**Root cause:** 10/10") {
		t.Errorf("posted comment lost the evaluation body: %q", posted)
	}
	if !strings.Contains(posted, "未产出 outcome marker") {
		t.Errorf("posted comment missing the inferred-label notice: %q", posted)
	}
	if !strings.Contains(body, "未产出 outcome marker") {
		t.Errorf("returned body missing the inferred-label notice: %q", body)
	}
	if got := v.labels[307]; !slices.Contains(got, "agent-evaluated") {
		t.Errorf("labels = %v, want agent-evaluated applied", got)
	}
}

// TestRun_MarkerMissing_BelowThreshold_SalvagedAsSkipped locks the threshold
// direction: a complete body scoring under 7.0 salvages to agent-skipped.
func TestRun_MarkerMissing_BelowThreshold_SalvagedAsSkipped(t *testing.T) {
	op := &Operator{
		Name:      "evaluate-bug",
		LockLabel: "agent-running",
		Prompt:    "evaluate",
		Outcomes:  []string{"agent-evaluated", "agent-skipped"},
	}
	sub := &Subject{Number: 152, Labels: []string{"bug"}}
	v := newFakeVCS()

	_, outcome, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:    "acme/webapp",
		Workdir: t.TempDir(),
		Timeout: time.Second,
		RunFunc: func(_ context.Context, _, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
			return evalBody("6.7"), nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error, want nil: %v", err)
	}
	if outcome != "agent-skipped" {
		t.Errorf("outcome = %q, want agent-skipped", outcome)
	}
	if got := v.labels[152]; !slices.Contains(got, "agent-skipped") {
		t.Errorf("labels = %v, want agent-skipped applied", got)
	}
}
