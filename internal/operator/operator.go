// Package operator loads, matches, and executes operator SKILL.md files.
//
// An operator is a self-contained YAML+Markdown file declaring when it should
// run (via label-based triggers on issues/PRs) and containing the prompt
// handed to `claude -p` when the trigger fires. The operator package is the
// ClawFlow extension model: new behavior = new SKILL.md, no Go changes.
package operator

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Operator is one parsed SKILL.md.
type Operator struct {
	Name        string
	Description string
	Trigger     Trigger
	// LockLabel is parsed from SKILL.md but no longer used by the runtime.
	// Cross-process locking is handled by local lockfiles in ~/.clawflow/locks/;
	// the field is kept for back-compat with SKILL.md files that still declare
	// `lock_label:`. New operators can safely omit it.
	LockLabel string
	// Outcomes is the allow-list of labels the operator may declare via the
	// `<!-- clawflow:outcome=<label> -->` marker in its stdout. The runner
	// adds the outcome label after posting the comment. An empty list means
	// "accept any label the operator emits" — back-compat for older skills
	// that don't declare an explicit outcome set.
	Outcomes []string
	// Prompt is the SKILL.md body (everything after the frontmatter).
	// The runner prepends runtime context before handing it to `claude -p`.
	Prompt string
	// Source is diagnostic-only: "embed:skills/<name>/SKILL.md" for built-ins,
	// absolute path for user operators.
	Source string
}

// AppliesTo enumerates the sub-issue structure a trigger requires. It is a
// deterministic, structure-aware gate layered on top of label matching:
// "parent" fires only on issues that have sub-issues, "leaf" only on issues
// that have none, and "any" (the default) ignores structure entirely.
//
// This lets coordinator operators (track-progress) run exclusively on tracking
// parents while work operators (evaluate-*, decompose, implement) stay on
// leaves — turning the previous prompt-level soft convention ("skip if I'm a
// parent") into a hard matcher constraint.
const (
	AppliesAny    = "any"
	AppliesParent = "parent"
	AppliesLeaf   = "leaf"
)

// Trigger gates when an operator fires on a given issue/PR.
type Trigger struct {
	Target            string
	LabelsRequired    []string
	LabelsRequiredAny []string // OR semantics: at least one must be present (empty = no constraint)
	LabelsExcluded    []string
	// AppliesTo is the sub-issue structure constraint: "" or "any" = no
	// constraint (back-compat), "parent" = subject must have sub-issues,
	// "leaf" = subject must have none. See the AppliesTo constants.
	AppliesTo string
}

// frontmatter mirrors the YAML shape inside the SKILL.md.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Operator    struct {
		Trigger struct {
			Target            string   `yaml:"target"`
			LabelsRequired    []string `yaml:"labels_required"`
			LabelsRequiredAny []string `yaml:"labels_required_any"`
			LabelsExcluded    []string `yaml:"labels_excluded"`
			AppliesTo         string   `yaml:"applies_to"`
		} `yaml:"trigger"`
		LockLabel string   `yaml:"lock_label"`
		Outcomes  []string `yaml:"outcomes"`
	} `yaml:"operator"`
}

// Parse decodes a SKILL.md into an Operator. `source` is used for error
// context and stored on the returned Operator.Source.
func Parse(data []byte, source string) (*Operator, error) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, fmt.Errorf("%s: missing '---' frontmatter opener", source)
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return nil, fmt.Errorf("%s: frontmatter not closed with '---'", source)
	}
	fmText := rest[:end]
	body := rest[end+5:]

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return nil, fmt.Errorf("%s: yaml parse: %w", source, err)
	}

	if fm.Name == "" {
		return nil, fmt.Errorf("%s: operator name required", source)
	}
	tgt := fm.Operator.Trigger.Target
	if tgt != "issue" && tgt != "pr" {
		return nil, fmt.Errorf("%s: operator.trigger.target must be \"issue\" or \"pr\", got %q", source, tgt)
	}
	appliesTo := fm.Operator.Trigger.AppliesTo
	switch appliesTo {
	case "", AppliesAny, AppliesParent, AppliesLeaf:
		// ok ("" normalized below to AppliesAny)
	default:
		return nil, fmt.Errorf("%s: operator.trigger.applies_to must be \"any\", \"parent\", or \"leaf\", got %q", source, appliesTo)
	}
	if appliesTo == "" {
		appliesTo = AppliesAny
	}
	// lock_label was previously required as the issue-side concurrency
	// gate; it is now ignored at runtime (see Operator.LockLabel doc).
	// Tolerate both presence and absence so old and new SKILL.md files
	// parse identically.

	return &Operator{
		Name:        fm.Name,
		Description: fm.Description,
		Trigger: Trigger{
			Target:            tgt,
			LabelsRequired:    fm.Operator.Trigger.LabelsRequired,
			LabelsRequiredAny: fm.Operator.Trigger.LabelsRequiredAny,
			LabelsExcluded:    fm.Operator.Trigger.LabelsExcluded,
			AppliesTo:         appliesTo,
		},
		LockLabel: fm.Operator.LockLabel,
		Outcomes:  fm.Operator.Outcomes,
		Prompt:    body,
		Source:    source,
	}, nil
}
