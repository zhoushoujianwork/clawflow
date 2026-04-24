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
	LockLabel   string
	// Prompt is the SKILL.md body (everything after the frontmatter).
	// The runner prepends runtime context before handing it to `claude -p`.
	Prompt string
	// Source is diagnostic-only: "embed:skills/<name>/SKILL.md" for built-ins,
	// absolute path for user operators.
	Source string
}

// Trigger gates when an operator fires on a given issue/PR.
type Trigger struct {
	Target         string
	LabelsRequired []string
	LabelsExcluded []string
}

// frontmatter mirrors the YAML shape inside the SKILL.md.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Operator    struct {
		Trigger struct {
			Target         string   `yaml:"target"`
			LabelsRequired []string `yaml:"labels_required"`
			LabelsExcluded []string `yaml:"labels_excluded"`
		} `yaml:"trigger"`
		LockLabel string `yaml:"lock_label"`
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
	if fm.Operator.LockLabel == "" {
		return nil, fmt.Errorf("%s: operator.lock_label required", source)
	}

	return &Operator{
		Name:        fm.Name,
		Description: fm.Description,
		Trigger: Trigger{
			Target:         tgt,
			LabelsRequired: fm.Operator.Trigger.LabelsRequired,
			LabelsExcluded: fm.Operator.Trigger.LabelsExcluded,
		},
		LockLabel: fm.Operator.LockLabel,
		Prompt:    body,
		Source:    source,
	}, nil
}
