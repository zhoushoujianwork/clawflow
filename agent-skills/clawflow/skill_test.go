package clawflow_test

import (
	"os"
	"strings"
	"testing"
	"unicode"

	"gopkg.in/yaml.v3"
)

func TestSkillDiscoveryContract(t *testing.T) {
	data, err := os.ReadFile("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	parts := strings.SplitN(text, "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatal("missing YAML frontmatter")
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Name != "clawflow" || !strings.HasPrefix(meta.Description, "Inspect GitLab CI failures, job/pipeline URLs and job logs, including self-hosted GitLab;") {
		t.Fatal("skill discovery must lead with GitLab CI diagnosis")
	}
	firstSection := strings.Index(parts[2], "\n## ")
	if firstSection < 0 || !strings.HasPrefix(parts[2][firstSection:], "\n## GitLab job or pipeline diagnosis (read-only)\n") {
		t.Fatal("read-only GitLab diagnosis must be the first section")
	}
	if lines := len(strings.Split(strings.TrimSuffix(text, "\n"), "\n")); lines >= 500 {
		t.Fatalf("skill has %d lines; must stay under 500", lines)
	}
	if strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		t.Fatal("skill must remain English-only")
	}
}
