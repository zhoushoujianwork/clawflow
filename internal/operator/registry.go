package operator

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Registry holds the live set of operators. Loading order matters: call
// LoadEmbedded first, then LoadUserDir — user operators override embedded
// ones by name, so the user-facing "customize by dropping a SKILL.md in
// ~/.clawflow/skills/<name>/" works.
type Registry struct {
	ops map[string]*Operator
}

func NewRegistry() *Registry {
	return &Registry{ops: make(map[string]*Operator)}
}

// IsRetiredBuiltin identifies names reserved by the removed approval pipeline.
// User copies of these operators must not reactivate the retired stages.
func IsRetiredBuiltin(name string) bool {
	switch name {
	case "classify", "evaluate-bug", "evaluate-feat", "decompose":
		return true
	default:
		return false
	}
}

// LoadEmbedded walks `sys` at `root` and parses every <name>/SKILL.md. Used
// for the binary's built-in operators (production passes an embed.FS, which
// satisfies fs.FS), and for tests (pass fstest.MapFS).
func (r *Registry) LoadEmbedded(sys fs.FS, root string) error {
	return fs.WalkDir(sys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, "/SKILL.md") {
			return nil
		}
		data, rerr := fs.ReadFile(sys, path)
		if rerr != nil {
			return fmt.Errorf("read embedded %s: %w", path, rerr)
		}
		op, perr := Parse(data, "embed:"+path)
		if perr != nil {
			return perr
		}
		if !IsRetiredBuiltin(op.Name) {
			r.ops[op.Name] = op
		}
		return nil
	})
}

// LoadUserDir walks `dir` and loads any <name>/SKILL.md found. Missing
// directory is treated as an empty set (not an error) — most users won't
// have custom operators.
func (r *Registry) LoadUserDir(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read user skills dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if IsRetiredBuiltin(e.Name()) {
			warnRetiredUserOperator(e.Name(), filepath.Join(dir, e.Name()))
			continue
		}
		skill := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(skill)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", skill, err)
		}
		if name := skillName(data, skill); IsRetiredBuiltin(name) {
			warnRetiredUserOperator(name, skill)
			continue
		}
		op, err := Parse(data, skill)
		if err != nil {
			return err
		}
		normalizeImplementTrigger(op)
		r.ops[op.Name] = op // overrides embedded with same name
	}
	return nil
}

func warnRetiredUserOperator(name, source string) {
	fmt.Fprintf(os.Stderr, "clawflow: ignored retired operator %q at %q; autonomous implementation replaces this stage\n", name, source)
}

func normalizeImplementTrigger(op *Operator) {
	if op.Name != "implement" {
		return
	}
	changed := false
	removeApproval := func(labels []string) []string {
		return slices.DeleteFunc(labels, func(label string) bool {
			if label == "ready-for-agent" {
				changed = true
				return true
			}
			return false
		})
	}
	op.Trigger.LabelsRequired = removeApproval(op.Trigger.LabelsRequired)
	op.Trigger.LabelsRequiredAny = removeApproval(op.Trigger.LabelsRequiredAny)
	op.Trigger.LabelsConsumed = removeApproval(op.Trigger.LabelsConsumed)
	if changed {
		fmt.Fprintf(os.Stderr, "clawflow: migrated implement operator at %q: removed obsolete ready-for-agent trigger; other constraints preserved\n", op.Source)
	}
}

// All returns operators sorted by name for deterministic iteration.
func (r *Registry) All() []*Operator {
	out := make([]*Operator, 0, len(r.ops))
	for _, op := range r.ops {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get fetches an operator by name.
func (r *Registry) Get(name string) (*Operator, bool) {
	op, ok := r.ops[name]
	return op, ok
}

// Severity classifies a diagnostic finding.
type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
)

func (s Severity) String() string {
	if s == SeverityError {
		return "ERROR"
	}
	return "WARN"
}

// Diagnostic is a single finding from Diagnose().
type Diagnostic struct {
	Operator string
	Severity Severity
	Message  string
}

// Diagnose runs cross-operator validation and returns all findings.
func (r *Registry) Diagnose() []Diagnostic {
	var diags []Diagnostic
	ops := r.All()

	for _, op := range ops {
		if op.Description == "" {
			diags = append(diags, Diagnostic{
				Operator: op.Name,
				Severity: SeverityWarning,
				Message:  "empty description",
			})
		}
		if len(op.Outcomes) == 0 {
			diags = append(diags, Diagnostic{
				Operator: op.Name,
				Severity: SeverityWarning,
				Message:  "no outcomes declared (any label accepted)",
			})
		}
	}

	type triggerKey struct {
		target   string
		applies  string
		required string
		any      string
		excluded string
	}
	labelSet := func(labels []string) string {
		set := make(map[string]bool)
		for _, label := range labels {
			set[label] = true
		}
		sorted := make([]string, 0, len(set))
		for label := range set {
			sorted = append(sorted, label)
		}
		sort.Strings(sorted)
		data, _ := json.Marshal(sorted)
		return string(data)
	}
	seen := make(map[triggerKey]string)
	for _, op := range ops {
		applies := op.Trigger.AppliesTo
		if applies == "" {
			applies = AppliesAny
		}
		k := triggerKey{op.Trigger.Target, applies, labelSet(op.Trigger.LabelsRequired), labelSet(op.Trigger.LabelsRequiredAny), labelSet(op.Trigger.LabelsExcluded)}
		if first, ok := seen[k]; ok {
			diags = append(diags, Diagnostic{
				Operator: op.Name,
				Severity: SeverityError,
				Message:  fmt.Sprintf("duplicate trigger with %q (target=%s, applies_to=%s)", first, k.target, k.applies),
			})
		} else {
			seen[k] = op.Name
		}
	}

	return diags
}
