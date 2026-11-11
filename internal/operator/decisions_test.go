package operator

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	clawflow "github.com/zhoushoujianwork/clawflow"
)

const decisionsDir = "../../docs/decisions"

var recordFile = regexp.MustCompile(`^(\d{4})-[a-z0-9]+(-[a-z0-9]+)*\.md$`)

func decisionRecords(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(decisionsDir)
	if err != nil {
		t.Fatalf("decision records directory missing: %v", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "README.md" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestDecisionRecordsBackfillCount(t *testing.T) {
	if n := len(decisionRecords(t)); n < 8 || n > 10 {
		t.Fatalf("expected 8 to 10 backfilled records, got %d", n)
	}
}

func TestDecisionRecordsShape(t *testing.T) {
	sections := []string{"## 背景", "## 决定", "## 理由", "## 放弃的方案", "## 相关 issue"}
	seen := map[string]string{}
	for _, name := range decisionRecords(t) {
		m := recordFile.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("%s: name must be NNNN-kebab-case.md", name)
			continue
		}
		if prev, dup := seen[m[1]]; dup {
			t.Errorf("%s: number %s already used by %s", name, m[1], prev)
		}
		seen[m[1]] = name
		data, err := os.ReadFile(filepath.Join(decisionsDir, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.HasPrefix(text, "# ADR-"+m[1]+"：") {
			t.Errorf("%s: title must start with '# ADR-%s：'", name, m[1])
		}
		for _, field := range []string{"- 状态：", "- 日期："} {
			if !strings.Contains(text, "\n"+field) {
				t.Errorf("%s: missing %q", name, field)
			}
		}
		last := -1
		for _, s := range sections {
			i := strings.Index(text, "\n"+s+"\n")
			if i < 0 {
				t.Errorf("%s: missing section %q", name, s)
				continue
			}
			if i < last {
				t.Errorf("%s: section %q out of order", name, s)
			}
			last = i
			body := text[i+len(s)+2:]
			if next := strings.Index(body, "\n## "); next >= 0 {
				body = body[:next]
			}
			if strings.TrimSpace(body) == "" {
				t.Errorf("%s: section %q is empty", name, s)
			}
		}
		if !regexp.MustCompile(`#\d+`).MatchString(text[strings.Index(text, "\n## 相关 issue"):]) &&
			!strings.Contains(text[strings.Index(text, "\n## 相关 issue"):], "提交 `") {
			t.Errorf("%s: related-issue section names no issue or commit", name)
		}
	}
}

func TestDecisionRecordsIndexMatchesFiles(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(decisionsDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	index := string(data)
	linked := map[string]bool{}
	for _, m := range regexp.MustCompile(`\]\((\d{4}-[^)]+\.md)\)`).FindAllStringSubmatch(index, -1) {
		linked[m[1]] = true
	}
	for _, name := range decisionRecords(t) {
		if !linked[name] {
			t.Errorf("index does not list %s", name)
		}
		delete(linked, name)
	}
	for name := range linked {
		t.Errorf("index lists %s but the file is missing", name)
	}
	// Every index row carries a non-empty "read when" cell so readers can pick records.
	for _, line := range strings.Split(index, "\n") {
		if !strings.HasPrefix(line, "| [0") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "| "), "|")
		if len(cells) < 4 || strings.TrimSpace(cells[len(cells)-1]) == "" {
			t.Errorf("index row lacks a read-when cell: %s", line)
		}
	}
}

func TestImplementSkillReferencesDecisionRecords(t *testing.T) {
	data, err := clawflow.EmbeddedSkills.ReadFile("skills/implement/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(clawflow.EmbeddedSkills, "skills"); err != nil {
		t.Fatal(err)
	}
	op, ok := reg.Get("implement")
	if !ok {
		t.Fatal("implement operator missing")
	}
	prompt := BuildSystemPrompt(op, "owner/repo", "")
	for _, want := range []string{
		"docs/decisions/README.md",
		"only the records whose",
		"Do not read the whole directory",
		"background, not requirements",
		"Record major decisions",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("implement prompt lacks %q", want)
		}
	}
	if !strings.Contains(string(data), "docs/decisions/") {
		t.Fatal("embedded implement skill does not mention docs/decisions/")
	}
}

func TestImplementSkillStaysWithinSizeLimit(t *testing.T) {
	data, err := clawflow.EmbeddedSkills.ReadFile("skills/implement/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "\n"); n > 500 {
		t.Fatalf("SKILL.md has %d lines; the limit is 500", n)
	}
}
