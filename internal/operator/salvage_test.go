package operator

import (
	"context"
	"io"
	"os"
	"path/filepath"
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
		name      string
		op        string
		outcomes  []string
		body      string
		threshold float64 // zero value in the table means "use the default 7.0"; the explicit-zero-threshold case below uses 0.0001 to stay distinguishable
		want      string
		wantConf  float64
		wantOK    bool
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
			// Issue #336: a configured threshold of 6 lets a 6.0 score pass
			// that the historical hardcoded 7.0 would have skipped.
			name: "custom threshold 6, score above it", op: "evaluate-bug", outcomes: evalOutcomes,
			body: evalBody("6.0"), threshold: 6, want: "agent-evaluated", wantConf: 6, wantOK: true,
		},
		{
			// A configured threshold of 8 rejects a 7.9 score the default
			// 7.0 would have accepted.
			name: "custom threshold 8, score below it", op: "evaluate-bug", outcomes: evalOutcomes,
			body: evalBody("7.9"), threshold: 8, want: "agent-skipped", wantConf: 7.9, wantOK: true,
		},
		{
			// Exactly at a non-default threshold still passes (>=, not >).
			name: "custom threshold 8, score exactly at it", op: "evaluate-bug", outcomes: evalOutcomes,
			body: evalBody("8.0"), threshold: 8, want: "agent-evaluated", wantConf: 8, wantOK: true,
		},
		{
			// Issue #336's explicit-0 case: every valid numeric score clears
			// the bar. This does not bypass the dimension/Confidence
			// well-formedness gates — those still ran above.
			name: "explicit zero threshold accepts any valid score", op: "evaluate-bug", outcomes: evalOutcomes,
			body: evalBody("0.1"), threshold: 0.0001, want: "agent-evaluated", wantConf: 0.1, wantOK: true,
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
			threshold := tt.threshold
			if threshold == 0 {
				threshold = defaultConfidenceThreshold
			}
			op := &Operator{Name: tt.op, Outcomes: tt.outcomes}
			got, conf, ok := salvageOutcome(op, tt.body, threshold)
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

// TestSalvageOutcome_Issue313RealBody is a regression test built from a real,
// already-paid-for run artifact rather than a hand-written fixture.
//
// testdata/issue-313-evaluate-bug-no-marker.md is the verbatim
// meta.json.summary of clawflow run
// runs/zhoushoujianwork__clawflow/issue-313/2026-09-11T16-50-30Z: a 6472-char
// evaluate-bug body, complete with all three dimension lines and
// "**Confidence:** 8.7/10", that stopped one line short of the outcome marker
// and was discarded as status=no-marker for $1.21 — the second of two such
// losses on #313, and occurrence 3 and 4 of the bug after PR #310 landed the
// fix (issue #314).
//
// Its value is anchoring salvage's judgement to a body that a real model
// really produced: prose in mixed Chinese/English, inline code spans, `%(...)`
// git format placeholders and nested backticks that a hand-rolled fixture
// would not think to include. A future tightening of confidenceRE or
// dimensionScores that regresses on real-world formatting fails here.
func TestSalvageOutcome_Issue313RealBody(t *testing.T) {
	path := filepath.Join("testdata", "issue-313-evaluate-bug-no-marker.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	body := string(raw)

	// Guard the fixture itself: if it is ever truncated or regenerated from a
	// different run, the assertions below would silently test nothing.
	if !strings.Contains(body, "**Confidence:** 8.7/10") {
		t.Fatalf("fixture lost its Confidence line (%d bytes)", len(body))
	}

	op := &Operator{Name: "evaluate-bug", Outcomes: []string{"agent-evaluated", "agent-skipped"}}
	outcome, conf, ok := salvageOutcome(op, body, defaultConfidenceThreshold)
	if !ok {
		t.Fatalf("salvageOutcome ok = false, want true: this body was discarded for $1.21 (issue #314)")
	}
	if outcome != "agent-evaluated" {
		t.Errorf("outcome = %q, want agent-evaluated", outcome)
	}
	if conf != 8.7 {
		t.Errorf("confidence = %v, want 8.7", conf)
	}
}

// TestRun_Issue313RealBody_SalvagedEndToEnd runs the same real #313 body
// through Run, so the assertion covers the full path the money was lost on:
// no marker on stdout → comment posted → agent-evaluated applied → nil error.
// Before PR #310 this returned ErrNoOutcomeMarker and threw the body away.
func TestRun_Issue313RealBody_SalvagedEndToEnd(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "issue-313-evaluate-bug-no-marker.md"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	op := &Operator{
		Name:      "evaluate-bug",
		LockLabel: "agent-running",
		Prompt:    "evaluate",
		Outcomes:  []string{"agent-evaluated", "agent-skipped"},
	}
	sub := &Subject{Number: 313, Labels: []string{"bug"}}
	v := newFakeVCS()

	var recoveredOutcome string
	var recoveredConf float64

	_, outcome, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:    "zhoushoujianwork/clawflow",
		Workdir: t.TempDir(),
		Timeout: time.Second,
		RunFunc: func(_ context.Context, _, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
			return string(raw), nil
		},
		MarkerRecovered: func(o string, c float64) {
			recoveredOutcome, recoveredConf = o, c
		},
	})
	if err != nil {
		t.Fatalf("Run returned error, want nil (body must be salvaged): %v", err)
	}
	if outcome != "agent-evaluated" {
		t.Errorf("outcome = %q, want agent-evaluated", outcome)
	}
	if recoveredOutcome != "agent-evaluated" || recoveredConf != 8.7 {
		t.Errorf("MarkerRecovered got (%q, %v), want (agent-evaluated, 8.7)", recoveredOutcome, recoveredConf)
	}
	if len(v.comments) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(v.comments))
	}
	if posted := v.comments[0].body; !strings.Contains(posted, "**Confidence:** 8.7/10") {
		t.Errorf("posted comment lost the evaluation body (%d bytes)", len(posted))
	}
	if got := v.labels[313]; !slices.Contains(got, "agent-evaluated") {
		t.Errorf("labels = %v, want agent-evaluated applied", got)
	}
}

// TestRun_Issue326RealBody_SalvagedEndToEnd runs the actual stdout of the
// evaluate-bug run on clawflow#323 — the body that cost $1.67 and applied no
// label. It quotes the marker placeholder in a repro step and never emits a
// real trailing marker, so the old unanchored regex captured the literal "..."
// as the verdict: outcomeAllowed rejected it, no label landed, the trigger
// labels stayed put, and run/end still logged status=success outcome=...
//
// Post-#326 the quoted placeholder is prose, so the body falls through to the
// #307 salvage path it should have taken all along: agent-evaluated derived
// from its Confidence 8.3/10, comment posted, label applied.
func TestRun_Issue326RealBody_SalvagedEndToEnd(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "issue-326-quoted-marker-body.md"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	body := string(raw)

	// Guard the fixture: both properties below are what make this a #326 case.
	if !strings.Contains(body, "<!-- clawflow:outcome=... -->") {
		t.Fatalf("fixture lost its quoted marker placeholder (%d bytes)", len(body))
	}
	if !strings.Contains(body, "**Confidence:** 8.3/10") {
		t.Fatalf("fixture lost its Confidence line (%d bytes)", len(body))
	}

	// The old behaviour, asserted directly so a regression is unambiguous.
	if label, _ := parseOutcome(body); label != "" {
		t.Errorf("parseOutcome label = %q, want empty — a quoted placeholder is not a verdict", label)
	}

	op := &Operator{
		Name:      "evaluate-bug",
		LockLabel: "agent-running",
		Prompt:    "evaluate",
		Outcomes:  []string{"agent-evaluated", "agent-skipped"},
	}
	sub := &Subject{Number: 323, Labels: []string{"bug"}}
	v := newFakeVCS()

	out, outcome, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:    "zhoushoujianwork/clawflow",
		Workdir: t.TempDir(),
		Timeout: time.Second,
		RunFunc: func(_ context.Context, _, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error, want nil (body must be salvaged): %v", err)
	}
	if outcome != "agent-evaluated" {
		t.Errorf("outcome = %q, want agent-evaluated (salvaged from Confidence 8.3)", outcome)
	}
	if got := v.labels[323]; !slices.Contains(got, "agent-evaluated") {
		t.Errorf("labels = %v, want agent-evaluated applied", got)
	}
	if slices.Contains(v.labels[323], "...") {
		t.Errorf("literal %q label was applied: %v", "...", v.labels[323])
	}
	// The quoted placeholder must survive into the posted comment: stripping it
	// is what mangled the real comment's repro step to a pair of empty backticks.
	if !strings.Contains(out, "<!-- clawflow:outcome=... -->") {
		t.Error("quoted marker was stripped from the body — the posted comment would be mangled")
	}
}

// TestRun_ConfidenceThreshold_ConsistentAcrossMarkerAndSalvage is the issue
// #336 end-to-end contract: the same Confidence score must land on the same
// outcome label whether the operator emits an explicit marker or drops it
// (routing through salvage), and both paths must honour a configured
// threshold instead of the historical hardcoded 7.0.
func TestRun_ConfidenceThreshold_ConsistentAcrossMarkerAndSalvage(t *testing.T) {
	customThreshold := 6.0

	newOp := func() *Operator {
		return &Operator{
			Name:      "evaluate-bug",
			LockLabel: "agent-running",
			Prompt:    "evaluate",
			Outcomes:  []string{"agent-evaluated", "agent-skipped"},
		}
	}

	// 6.0 is below the historical hardcoded 7.0 but at-or-above a configured
	// threshold of 6 — both the marker path and the salvage path must agree
	// it clears the bar.
	t.Run("marker path honours custom threshold", func(t *testing.T) {
		op := newOp()
		sub := &Subject{Number: 1, Labels: []string{"bug"}}
		v := newFakeVCS()

		body := "## Eval\n\n**Confidence:** 6.0/10\n\n<!-- clawflow:outcome=agent-evaluated -->\n"
		_, outcome, err := Run(context.Background(), op, sub, v, RunOptions{
			Repo:                "r",
			ConfidenceThreshold: &customThreshold,
			RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
				return body, nil
			},
		})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if outcome != "agent-evaluated" {
			t.Errorf("outcome = %q, want agent-evaluated", outcome)
		}
	})

	t.Run("salvage path honours the same custom threshold", func(t *testing.T) {
		op := newOp()
		sub := &Subject{Number: 2, Labels: []string{"bug"}}
		v := newFakeVCS()

		_, outcome, err := Run(context.Background(), op, sub, v, RunOptions{
			Repo:                "r",
			ConfidenceThreshold: &customThreshold,
			RunFunc: func(_ context.Context, _, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
				return evalBody("6.0"), nil // full template, no marker → salvage
			},
		})
		if err != nil {
			t.Fatalf("Run returned error, want nil (body should be salvaged): %v", err)
		}
		if outcome != "agent-evaluated" {
			t.Errorf("outcome = %q, want agent-evaluated (6.0 clears a configured threshold of 6)", outcome)
		}
	})

	// A score that would have passed the default 7.0 must NOT be salvaged as
	// a pass once the configured threshold is raised above it.
	t.Run("salvage does not wrongly pass a score below a raised threshold", func(t *testing.T) {
		op := newOp()
		sub := &Subject{Number: 3, Labels: []string{"bug"}}
		v := newFakeVCS()
		raised := 8.0

		_, outcome, err := Run(context.Background(), op, sub, v, RunOptions{
			Repo:                "r",
			ConfidenceThreshold: &raised,
			RunFunc: func(_ context.Context, _, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
				return evalBody("7.5"), nil // above the old 7.0 default, below the configured 8
			},
		})
		if err != nil {
			t.Fatalf("Run returned error, want nil: %v", err)
		}
		if outcome != "agent-skipped" {
			t.Errorf("outcome = %q, want agent-skipped (7.5 must not clear a configured threshold of 8)", outcome)
		}
	})
}

// TestRun_ConfidenceThreshold_Unset_KeepsHistoricalDefault verifies that
// omitting RunOptions.ConfidenceThreshold (the zero-value RunOptions every
// pre-#336 caller and test uses) still applies the historical 7.0 bar,
// preserving back-compat for callers that don't route through config.
func TestRun_ConfidenceThreshold_Unset_KeepsHistoricalDefault(t *testing.T) {
	op := &Operator{
		Name:      "evaluate-bug",
		LockLabel: "agent-running",
		Prompt:    "evaluate",
		Outcomes:  []string{"agent-evaluated", "agent-skipped"},
	}
	sub := &Subject{Number: 4, Labels: []string{"bug"}}
	v := newFakeVCS()

	_, outcome, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(_ context.Context, _, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
			return evalBody("6.9"), nil // below 7.0, would pass a threshold of 6
		},
	})
	if err != nil {
		t.Fatalf("Run returned error, want nil: %v", err)
	}
	if outcome != "agent-skipped" {
		t.Errorf("outcome = %q, want agent-skipped (unset RunOptions must keep the 7.0 default)", outcome)
	}
}
