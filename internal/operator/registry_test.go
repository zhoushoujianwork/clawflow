package operator

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func makeSkill(name, target, lock string, required, excluded []string) string {
	var b strings.Builder
	b.WriteString("---\nname: ")
	b.WriteString(name)
	b.WriteString("\noperator:\n  trigger:\n    target: ")
	b.WriteString(target)
	b.WriteString("\n    labels_required: [")
	b.WriteString(strings.Join(required, ","))
	b.WriteString("]\n    labels_excluded: [")
	b.WriteString(strings.Join(excluded, ","))
	b.WriteString("]\n  lock_label: ")
	b.WriteString(lock)
	b.WriteString("\n---\n\nprompt for ")
	b.WriteString(name)
	return b.String()
}

func TestRegistry_LoadEmbedded(t *testing.T) {
	sys := fstest.MapFS{
		"skills/alpha/SKILL.md": {Data: []byte(makeSkill("alpha", "issue", "running", []string{"bug"}, nil))},
		"skills/beta/SKILL.md":  {Data: []byte(makeSkill("beta", "pr", "reviewing", nil, nil))},
		"skills/README.md":      {Data: []byte("# not a skill; should be ignored")},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if _, ok := reg.Get("alpha"); !ok {
		t.Error("alpha should be registered")
	}
	if _, ok := reg.Get("beta"); !ok {
		t.Error("beta should be registered")
	}
	if len(reg.All()) != 2 {
		t.Errorf("expected 2 ops, got %d", len(reg.All()))
	}
	// Source should indicate embed origin
	alpha, _ := reg.Get("alpha")
	if !strings.HasPrefix(alpha.Source, "embed:") {
		t.Errorf("alpha.Source should start with 'embed:', got %q", alpha.Source)
	}
}

func TestRegistry_AllSortedByName(t *testing.T) {
	sys := fstest.MapFS{
		"skills/charlie/SKILL.md": {Data: []byte(makeSkill("charlie", "issue", "l", nil, nil))},
		"skills/alpha/SKILL.md":   {Data: []byte(makeSkill("alpha", "issue", "l", nil, nil))},
		"skills/bravo/SKILL.md":   {Data: []byte(makeSkill("bravo", "issue", "l", nil, nil))},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	ops := reg.All()
	if len(ops) != 3 {
		t.Fatalf("want 3 ops got %d", len(ops))
	}
	names := []string{ops[0].Name, ops[1].Name, ops[2].Name}
	if names[0] != "alpha" || names[1] != "bravo" || names[2] != "charlie" {
		t.Errorf("All() not sorted: %v", names)
	}
}

func TestRegistry_UserOverridesEmbedded(t *testing.T) {
	sys := fstest.MapFS{
		"skills/evaluate-custom/SKILL.md": {Data: []byte(makeSkill("evaluate-custom", "issue", "running", []string{"bug"}, nil))},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatal(err)
	}

	// Create a user override with different trigger requirements.
	userDir := t.TempDir()
	opDir := filepath.Join(userDir, "evaluate-custom")
	if err := os.MkdirAll(opDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userSkill := makeSkill("evaluate-custom", "issue", "running", []string{"bug", "urgent"}, nil)
	if err := os.WriteFile(filepath.Join(opDir, "SKILL.md"), []byte(userSkill), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := reg.LoadUserDir(userDir); err != nil {
		t.Fatal(err)
	}

	got, ok := reg.Get("evaluate-custom")
	if !ok {
		t.Fatal("evaluate-custom missing")
	}
	if len(got.Trigger.LabelsRequired) != 2 || got.Trigger.LabelsRequired[0] != "bug" || got.Trigger.LabelsRequired[1] != "urgent" {
		t.Errorf("user version did not override embedded; required=%v", got.Trigger.LabelsRequired)
	}
	if strings.HasPrefix(got.Source, "embed:") {
		t.Errorf("Source should be file path after user override, got %q", got.Source)
	}
}

func TestRegistry_LoadUserDir_Missing(t *testing.T) {
	reg := NewRegistry()
	err := reg.LoadUserDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Errorf("missing user dir should be a no-op, got: %v", err)
	}
}

func TestRegistry_RetiredUserOperatorsIgnored(t *testing.T) {
	userDir := t.TempDir()
	for _, name := range []string{"classify", "evaluate-bug", "evaluate-feat", "decompose", "evaluate-custom"} {
		dir := filepath.Join(userDir, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(makeSkill(name, "issue", "running", nil, nil)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reg := NewRegistry()
	if err := reg.LoadUserDir(userDir); err != nil {
		t.Fatal(err)
	}
	ops := reg.All()
	if len(ops) != 1 || ops[0].Name != "evaluate-custom" {
		t.Fatalf("loaded operators: %+v", ops)
	}
}

func captureUserLoad(t *testing.T, reg *Registry, dir string) string {
	t.Helper()
	log, err := os.CreateTemp(t.TempDir(), "operator-notices")
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = log
	defer func() { os.Stderr = stderr; log.Close() }()
	if err := reg.LoadUserDir(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRegistry_RetiredDiagnosticsAndAliases(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"classify":      "malformed content must not abort custom loads",
		"evaluate-bug":  makeSkill("evaluate-custom", "issue", "", nil, nil),
		"alias":         makeSkill("evaluate-feat", "invalid-old-target", "", nil, nil),
		"another-alias": makeSkill("decompose", "issue", "", nil, nil),
		"custom":        makeSkill("evaluate-custom", "issue", "", nil, nil),
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reg := NewRegistry()
	notices := captureUserLoad(t, reg, dir)
	if ops := reg.All(); len(ops) != 1 || ops[0].Name != "evaluate-custom" {
		t.Fatalf("loaded operators: %+v", ops)
	}
	for _, name := range []string{"classify", "evaluate-bug", "evaluate-feat", "decompose"} {
		if !strings.Contains(notices, "ignored retired operator \""+name+"\"") {
			t.Fatalf("missing diagnostic for %s: %s", name, notices)
		}
	}
}

func TestRegistry_LegacyImplementTriggerMigration(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "copied", true: "custom-constraints"}[custom], func(t *testing.T) {
			dir := t.TempDir()
			content := "---\nname: implement\ndescription: custom implementation\noperator:\n  trigger:\n    target: issue\n    applies_to: leaf\n    labels_required: [ready-for-agent]\n    labels_required_any: [ready-for-agent]\n    labels_consumed: [ready-for-agent]\n    labels_excluded: [blocked]\n  outcomes: [custom-done]\n---\nKeep the user's implementation prompt.\n"
			if custom {
				content = strings.Replace(content, "labels_required: [ready-for-agent]", "labels_required: [ready-for-agent, custom]", 1)
				content = strings.Replace(content, "labels_required_any: [ready-for-agent]", "labels_required_any: [ready-for-agent, one, two]", 1)
				content = strings.Replace(content, "labels_consumed: [ready-for-agent]", "labels_consumed: [ready-for-agent, custom-flow]", 1)
			}
			path := filepath.Join(dir, "implement", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			original, err := Parse([]byte(content), path)
			if err != nil {
				t.Fatal(err)
			}
			reg := NewRegistry()
			if notice := captureUserLoad(t, reg, dir); !strings.Contains(notice, "removed obsolete ready-for-agent trigger") {
				t.Fatalf("missing migration notice: %s", notice)
			}
			op, ok := reg.Get("implement")
			if !ok {
				t.Fatal("implement missing")
			}
			if !custom {
				if !Matches(&Subject{State: "open"}, op) {
					t.Fatal("copied implementation still requires approval")
				}
				if len(op.Trigger.LabelsConsumed) != 0 {
					t.Fatal("obsolete consumed label retained")
				}
			} else {
				if Matches(&Subject{State: "open"}, op) || Matches(&Subject{State: "open", Labels: []string{"custom"}}, op) {
					t.Fatal("custom constraints lost")
				}
				if !Matches(&Subject{State: "open", Labels: []string{"custom", "one"}}, op) {
					t.Fatal("custom eligible issue rejected")
				}
				if !reflect.DeepEqual(op.Trigger.LabelsConsumed, []string{"custom-flow"}) {
					t.Fatalf("consumed labels: %v", op.Trigger.LabelsConsumed)
				}
			}
			original.Trigger.LabelsRequired = op.Trigger.LabelsRequired
			original.Trigger.LabelsRequiredAny = op.Trigger.LabelsRequiredAny
			original.Trigger.LabelsConsumed = op.Trigger.LabelsConsumed
			if !reflect.DeepEqual(original, op) {
				t.Fatal("migration changed unrelated operator fields")
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, []byte(content)) {
				t.Fatal("migration rewrote the user file")
			}
		})
	}
	// Approval-like labels remain a valid custom extension convention.
	dir := t.TempDir()
	path := filepath.Join(dir, "evaluate-custom", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	content := makeSkill("evaluate-custom", "issue", "", []string{"ready-for-agent"}, nil)
	content = strings.Replace(content, "labels_required: [ready-for-agent]", "labels_required: [ready-for-agent]\n    labels_required_any: [ready-for-agent, custom]\n    labels_consumed: [ready-for-agent, custom-flow]", 1)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if notice := captureUserLoad(t, reg, dir); notice != "" {
		t.Fatalf("custom operator migrated: %s", notice)
	}
	op, _ := reg.Get("evaluate-custom")
	want, err := Parse([]byte(content), path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(op, want) {
		t.Fatal("non-implement custom metadata changed")
	}
	if Matches(&Subject{State: "open"}, op) {
		t.Fatal("custom approval trigger stripped")
	}
}

func TestRegistry_LoadUserDir_InvalidSkill(t *testing.T) {
	reg := NewRegistry()
	userDir := t.TempDir()
	opDir := filepath.Join(userDir, "broken")
	_ = os.MkdirAll(opDir, 0o755)
	if err := os.WriteFile(filepath.Join(opDir, "SKILL.md"), []byte("not yaml, no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := reg.LoadUserDir(userDir)
	if err == nil {
		t.Error("invalid user SKILL.md should return an error")
	}
}

func TestRegistry_Get(t *testing.T) {
	reg := NewRegistry()
	if _, ok := reg.Get("nonexistent"); ok {
		t.Error("Get on empty registry should return ok=false")
	}
}

func makeSkillFull(name, desc, target, lock string, required, excluded, outcomes []string) string {
	var b strings.Builder
	b.WriteString("---\nname: ")
	b.WriteString(name)
	if desc != "" {
		b.WriteString("\ndescription: ")
		b.WriteString(desc)
	}
	b.WriteString("\noperator:\n  trigger:\n    target: ")
	b.WriteString(target)
	b.WriteString("\n    labels_required: [")
	b.WriteString(strings.Join(required, ","))
	b.WriteString("]\n    labels_excluded: [")
	b.WriteString(strings.Join(excluded, ","))
	b.WriteString("]\n  lock_label: ")
	b.WriteString(lock)
	if len(outcomes) > 0 {
		b.WriteString("\n  outcomes: [")
		b.WriteString(strings.Join(outcomes, ","))
		b.WriteString("]")
	}
	b.WriteString("\n---\n\nprompt for ")
	b.WriteString(name)
	return b.String()
}

func TestDiagnose_Clean(t *testing.T) {
	sys := fstest.MapFS{
		"skills/a/SKILL.md": {Data: []byte(makeSkillFull("a", "does A", "issue", "lock-a", []string{"bug"}, nil, []string{"done"}))},
		"skills/b/SKILL.md": {Data: []byte(makeSkillFull("b", "does B", "pr", "lock-b", []string{"review"}, nil, []string{"reviewed"}))},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatal(err)
	}
	diags := reg.Diagnose()
	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics, got %d: %+v", len(diags), diags)
	}
}

func TestDiagnose_EmptyDescription(t *testing.T) {
	sys := fstest.MapFS{
		"skills/a/SKILL.md": {Data: []byte(makeSkillFull("a", "", "issue", "lock", []string{"bug"}, nil, []string{"done"}))},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatal(err)
	}
	diags := reg.Diagnose()
	found := false
	for _, d := range diags {
		if d.Operator == "a" && d.Severity == SeverityWarning && strings.Contains(d.Message, "empty description") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning about empty description; got %+v", diags)
	}
}

func TestDiagnose_NoOutcomes(t *testing.T) {
	sys := fstest.MapFS{
		"skills/a/SKILL.md": {Data: []byte(makeSkillFull("a", "desc", "issue", "lock", []string{"bug"}, nil, nil))},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatal(err)
	}
	diags := reg.Diagnose()
	found := false
	for _, d := range diags {
		if d.Operator == "a" && strings.Contains(d.Message, "no outcomes") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning about no outcomes; got %+v", diags)
	}
}

func TestDiagnose_DuplicateTriggers(t *testing.T) {
	sys := fstest.MapFS{
		"skills/a/SKILL.md": {Data: []byte(makeSkillFull("a", "desc", "issue", "lock-a", []string{"bug", "urgent"}, nil, []string{"done"}))},
		"skills/b/SKILL.md": {Data: []byte(makeSkillFull("b", "desc", "issue", "lock-b", []string{"urgent", "bug"}, nil, []string{"done"}))},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatal(err)
	}
	diags := reg.Diagnose()
	found := false
	for _, d := range diags {
		if d.Severity == SeverityError && strings.Contains(d.Message, "duplicate trigger") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error about duplicate triggers; got %+v", diags)
	}
}

func TestDiagnose_DifferentTargetNoOverlap(t *testing.T) {
	sys := fstest.MapFS{
		"skills/a/SKILL.md": {Data: []byte(makeSkillFull("a", "desc", "issue", "lock-a", []string{"bug"}, nil, []string{"done"}))},
		"skills/b/SKILL.md": {Data: []byte(makeSkillFull("b", "desc", "pr", "lock-b", []string{"bug"}, nil, []string{"done"}))},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatal(err)
	}
	diags := reg.Diagnose()
	for _, d := range diags {
		if d.Severity == SeverityError {
			t.Errorf("same labels on different targets should not conflict; got %+v", d)
		}
	}
}

func TestDiagnose_TriggerConstraints(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*Operator)
		duplicate bool
	}{
		{"equivalent_sets", func(op *Operator) {
			op.Trigger.LabelsRequired = []string{"b", "a", "a"}
			op.Trigger.LabelsRequiredAny = []string{"y", "x", "x"}
			op.Trigger.LabelsExcluded = []string{"n", "m", "m"}
			op.Trigger.AppliesTo = ""
		}, true},
		{"different_any", func(op *Operator) { op.Trigger.LabelsRequiredAny = []string{"z"} }, false},
		{"different_excluded", func(op *Operator) { op.Trigger.LabelsExcluded = []string{"z"} }, false},
		{"different_applies", func(op *Operator) { op.Trigger.AppliesTo = AppliesLeaf }, false},
		{"different_target", func(op *Operator) { op.Trigger.Target = "pr" }, false},
		{"consumed_and_outcomes_ignored", func(op *Operator) {
			op.Trigger.LabelsConsumed = []string{"a"}
			op.Outcomes = []string{"other"}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Operator{Name: "a", Description: "a", Outcomes: []string{"done"}, Trigger: Trigger{Target: "issue", AppliesTo: AppliesAny, LabelsRequired: []string{"a", "b"}, LabelsRequiredAny: []string{"x", "y"}, LabelsExcluded: []string{"m", "n"}}}
			b := *a
			b.Name = "b"
			tc.change(&b)
			r := NewRegistry()
			r.ops[a.Name], r.ops[b.Name] = a, &b
			found := false
			for _, d := range r.Diagnose() {
				if d.Severity == SeverityError {
					found = true
				}
			}
			if found != tc.duplicate {
				t.Fatalf("duplicate=%t, diagnostics=%+v", tc.duplicate, r.Diagnose())
			}
		})
	}
}

func TestDiagnose_BuiltinLifecycle(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"implement", "repair-ci", "track-progress", "reply-question", "reply-comment"} {
		path := filepath.Join("..", "..", "skills", name, "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		op, err := Parse(data, path)
		if err != nil {
			t.Fatal(err)
		}
		r.ops[op.Name] = op
	}
	for _, d := range r.Diagnose() {
		if d.Severity == SeverityError {
			t.Fatalf("builtins conflict: %+v", d)
		}
	}
}
