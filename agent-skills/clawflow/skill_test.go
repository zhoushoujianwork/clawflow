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

func TestPlatformEnrollmentContract(t *testing.T) {
	data, err := os.ReadFile("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "## Where ClawFlow runs")
	end := strings.Index(text, "You are an external AI")
	if start < 0 || end <= start {
		t.Fatal("missing platform enrollment guidance")
	}
	guidance := text[start:end]
	for _, rule := range []string{
		"Before any enrollment",
		"clawflow platform status",
		"This machine is not connected to a",
		"Do not enable repositories locally",
		"never fall back to local registration",
		"clawflow repo add <repo-URL> --disabled",
		"clawflow repo enable <namespace/repo>",
		"Report the platform URL printed by the successful enrollment response",
		"No local scheduling",
		"clawflow run",
		"clawflow web",
		"run --local",
		"Add capacity only when explicitly asked",
		"clawflow worker --coordinator",
	} {
		if !strings.Contains(guidance, rule) {
			t.Errorf("missing platform enrollment rule %q", rule)
		}
	}
}
