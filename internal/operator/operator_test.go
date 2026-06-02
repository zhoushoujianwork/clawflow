package operator

import (
	"slices"
	"strings"
	"testing"
)

const validSkill = `---
name: test-op
description: a test operator
operator:
  trigger:
    target: issue
    labels_required: [bug]
    labels_excluded: [skip, wip]
  lock_label: agent-running
---

This is the prompt body.

It can have multiple paragraphs.`

func TestParse_Valid(t *testing.T) {
	op, err := Parse([]byte(validSkill), "test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op.Name != "test-op" {
		t.Errorf("Name: got %q want %q", op.Name, "test-op")
	}
	if op.Description != "a test operator" {
		t.Errorf("Description: got %q", op.Description)
	}
	if op.Trigger.Target != "issue" {
		t.Errorf("Target: got %q", op.Trigger.Target)
	}
	if !slices.Equal(op.Trigger.LabelsRequired, []string{"bug"}) {
		t.Errorf("LabelsRequired: got %v", op.Trigger.LabelsRequired)
	}
	if !slices.Equal(op.Trigger.LabelsExcluded, []string{"skip", "wip"}) {
		t.Errorf("LabelsExcluded: got %v", op.Trigger.LabelsExcluded)
	}
	if op.LockLabel != "agent-running" {
		t.Errorf("LockLabel: got %q", op.LockLabel)
	}
	if !strings.Contains(op.Prompt, "This is the prompt body.") {
		t.Errorf("Prompt missing body content; got: %q", op.Prompt)
	}
	if !strings.Contains(op.Prompt, "multiple paragraphs") {
		t.Errorf("Prompt truncated; got: %q", op.Prompt)
	}
	if op.Source != "test.md" {
		t.Errorf("Source: got %q", op.Source)
	}
}

func TestParse_Errors(t *testing.T) {
	cases := map[string]struct {
		input   string
		wantSub string
	}{
		"missing opener": {
			input:   "name: foo\n---\nbody",
			wantSub: "missing '---'",
		},
		"unclosed frontmatter": {
			input:   "---\nname: foo\nbody with no closing",
			wantSub: "not closed",
		},
		"invalid yaml": {
			input:   "---\n  :::not valid yaml\n---\nbody",
			wantSub: "yaml parse",
		},
		"missing name": {
			input: "---\ndescription: x\n" +
				"operator:\n  trigger:\n    target: issue\n  lock_label: running\n" +
				"---\nbody",
			wantSub: "name required",
		},
		"bad target": {
			input: "---\nname: foo\n" +
				"operator:\n  trigger:\n    target: banana\n  lock_label: x\n" +
				"---\nbody",
			wantSub: `must be "issue" or "pr"`,
		},
		"missing target": {
			input: "---\nname: foo\n" +
				"operator:\n  lock_label: x\n" +
				"---\nbody",
			wantSub: `must be "issue" or "pr"`,
		},
		// `lock_label` was previously required but is now optional —
		// the in-process per-issue mutex replaced the issue-side label
		// lock. No error expected for its absence; the field is simply
		// ignored at runtime.
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.input), "test")
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantSub)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), c.wantSub)
			}
		})
	}
}

func TestParse_CRLF(t *testing.T) {
	input := strings.ReplaceAll(validSkill, "\n", "\r\n")
	op, err := Parse([]byte(input), "test")
	if err != nil {
		t.Fatalf("unexpected error on CRLF input: %v", err)
	}
	if op.Name != "test-op" {
		t.Errorf("Name: got %q", op.Name)
	}
}

func TestParse_Outcomes(t *testing.T) {
	const skillWithOutcomes = `---
name: evaluate-bug
description: Evaluate a bug
operator:
  trigger:
    target: issue
    labels_required: [bug]
  lock_label: agent-running
  outcomes: [agent-evaluated, agent-skipped]
---

Body.`

	op, err := Parse([]byte(skillWithOutcomes), "evaluate-bug/SKILL.md")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(op.Outcomes, []string{"agent-evaluated", "agent-skipped"}) {
		t.Errorf("Outcomes = %v, want [agent-evaluated agent-skipped]", op.Outcomes)
	}
}

func TestParse_OutcomesOmitted_DefaultsToEmpty(t *testing.T) {
	op, err := Parse([]byte(validSkill), "test.md")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(op.Outcomes) != 0 {
		t.Errorf("Outcomes should default to empty slice when omitted; got %v", op.Outcomes)
	}
}

func TestParse_AppliesTo(t *testing.T) {
	const skill = `---
name: track-progress
operator:
  trigger:
    target: issue
    applies_to: parent
    labels_required: [progress-check]
---

Body.`
	op, err := Parse([]byte(skill), "track-progress/SKILL.md")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if op.Trigger.AppliesTo != AppliesParent {
		t.Errorf("AppliesTo = %q, want %q", op.Trigger.AppliesTo, AppliesParent)
	}
}

func TestParse_AppliesToDefaultsToAny(t *testing.T) {
	op, err := Parse([]byte(validSkill), "test.md")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if op.Trigger.AppliesTo != AppliesAny {
		t.Errorf("AppliesTo should default to %q when omitted; got %q", AppliesAny, op.Trigger.AppliesTo)
	}
}

func TestParse_AppliesToInvalid(t *testing.T) {
	const skill = `---
name: foo
operator:
  trigger:
    target: issue
    applies_to: banana
---

Body.`
	_, err := Parse([]byte(skill), "test")
	if err == nil {
		t.Fatal("expected error for invalid applies_to, got nil")
	}
	if !strings.Contains(err.Error(), "applies_to") {
		t.Errorf("error = %q, want substring %q", err.Error(), "applies_to")
	}
}

func TestParse_PRTarget(t *testing.T) {
	input := `---
name: review-pr
operator:
  trigger:
    target: pr
  lock_label: reviewing
---

prompt`
	op, err := Parse([]byte(input), "test")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if op.Trigger.Target != "pr" {
		t.Errorf("Target: got %q", op.Trigger.Target)
	}
}
