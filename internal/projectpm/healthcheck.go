package projectpm

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	clawflow "github.com/zhoushoujianwork/clawflow"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// HealthCheck is a project-level operator that audits each repo's
// CLAUDE.md plus the project's context.md / testing.md against a
// fixed-dimension rubric, then emits proposed file changes the user
// reviews and applies via the dashboard.
//
// Unlike issue/PR operators, this is dashboard-triggered (no label
// scan) and produces structured propose blocks instead of an outcome
// label. The runner is shared: same `claude -p` invocation primitive
// (operator.RunClaude), same workdir convention (project dir), same
// stream-json parsing.
//
// Output flow:
//
//	dashboard click → RunHealthCheck →
//	  build prompt (skill body + project state) →
//	  operator.RunClaude →
//	  parse <!-- clawflow:propose ... --> blocks →
//	  return ProposedChange list to frontend →
//	  user reviews diff and clicks Apply →
//	  ApplyHealthCheckChanges (separate entrypoint) writes + commits.

// ProposedChange is one file the operator wants to update (or create).
// The runner enriches each change with the current on-disk content so
// the frontend can render a side-by-side diff without a second round
// trip.
type ProposedChange struct {
	// Target is "repo" or "project". Determines how Path is resolved
	// when applying: repo → <LocalPath>/<Path>; project → ProjectDir/<Path>.
	Target string `json:"target"`
	// RepoID is the repo identifier (e.g. "owner/name") when Target=="repo",
	// empty otherwise.
	RepoID string `json:"repo_id,omitempty"`
	// Path is the file path *within* the target. Always relative.
	Path string `json:"path"`
	// Action is "create" (file does not exist) or "update".
	Action string `json:"action"`
	// ProposedContent is the FULL final content the operator wants to write.
	ProposedContent string `json:"proposed_content"`
	// CurrentContent is what's currently on disk (empty for create).
	CurrentContent string `json:"current_content"`
}

// HealthCheckResult is what the dashboard renders. Outcome distinguishes
// "nothing to do" from "review these changes". RawOutput is included
// for debugging when parsing edge cases bite.
type HealthCheckResult struct {
	Outcome   string           `json:"outcome"` // "healthy" | "changes-proposed"
	Summary   string           `json:"summary"`
	Changes   []ProposedChange `json:"changes"`
	RawOutput string           `json:"raw_output,omitempty"`
}

// RunHealthCheck builds the prompt for project p, invokes claude,
// parses the structured output, and returns the result. Side-effect
// free: it does NOT write any files. ApplyHealthCheckChanges does that.
func RunHealthCheck(ctx context.Context, p *project.Project, cfg *config.Config, model string, timeout time.Duration) (*HealthCheckResult, error) {
	skillBody, err := loadProjectSkill("pm-health-check")
	if err != nil {
		return nil, fmt.Errorf("load skill: %w", err)
	}

	repos := collectRepoInputs(p, cfg)
	contextMD, _ := project.ReadContext(p.Name)
	testingMD, _ := project.ReadTesting(p.Name)

	prompt := buildHealthCheckPrompt(p.Name, contextMD, testingMD, repos, skillBody)
	workdir := project.ProjectDir(p.Name)

	output, err := operator.RunClaude(ctx, prompt, workdir, timeout, nil, model)
	if err != nil {
		return nil, fmt.Errorf("run claude: %w", err)
	}

	result := parseHealthCheckOutput(output)
	enrichWithCurrentContent(result, p, cfg)
	result.RawOutput = output
	return result, nil
}

// repoInput is one row in the prompt's per-repo section. LocalPath is
// the absolute path on disk (so the operator could in principle reason
// about file layout, though the prompt currently only feeds CLAUDE.md
// content). FetchError surfaces missing-clone / unreadable-file cases
// to the operator so it can flag them in the summary instead of
// silently treating the repo as "no CLAUDE.md".
type repoInput struct {
	ID         string
	LocalPath  string
	ClaudeMD   string
	FetchError string
}

func collectRepoInputs(p *project.Project, cfg *config.Config) []repoInput {
	out := make([]repoInput, 0, len(p.Repos))
	for _, name := range p.Repos {
		ri := repoInput{ID: name}
		rc, ok := cfg.Repos[name]
		if !ok {
			ri.FetchError = "repo not in clawflow config"
			out = append(out, ri)
			continue
		}
		if rc.LocalPath == "" {
			ri.FetchError = "no local clone (local_path empty)"
			out = append(out, ri)
			continue
		}
		ri.LocalPath = rc.LocalPath
		claudePath := filepath.Join(rc.LocalPath, "CLAUDE.md")
		data, err := os.ReadFile(claudePath)
		switch {
		case os.IsNotExist(err):
			// Empty CLAUDE.md is the create-from-scratch case. Operator
			// sees empty string + this signal via Action=create later.
		case err != nil:
			ri.FetchError = "read CLAUDE.md: " + err.Error()
		default:
			ri.ClaudeMD = string(data)
		}
		out = append(out, ri)
	}
	return out
}

// buildHealthCheckPrompt prepends a structured INPUT section to the
// SKILL.md body. The operator reads this section verbatim — the
// section headers must match what the SKILL.md prose describes
// ("Project context.md", "Project testing.md", per-repo blocks).
func buildHealthCheckPrompt(projectName, contextMD, testingMD string, repos []repoInput, skillBody string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# INPUT\n\n")
	fmt.Fprintf(&b, "Project name: %s\n\n", projectName)

	fmt.Fprintf(&b, "## Project context.md\n\n")
	if strings.TrimSpace(contextMD) == "" {
		b.WriteString("(empty / not yet generated)\n\n")
	} else {
		b.WriteString(contextMD)
		ensureTrailingBlank(&b)
	}

	fmt.Fprintf(&b, "## Project testing.md\n\n")
	if strings.TrimSpace(testingMD) == "" {
		b.WriteString("(empty / not yet generated)\n\n")
	} else {
		b.WriteString(testingMD)
		ensureTrailingBlank(&b)
	}

	fmt.Fprintf(&b, "## Repos (%d)\n\n", len(repos))
	for _, r := range repos {
		fmt.Fprintf(&b, "### Repo: %s\n", r.ID)
		if r.LocalPath != "" {
			fmt.Fprintf(&b, "Local path: %s\n", r.LocalPath)
		}
		if r.FetchError != "" {
			fmt.Fprintf(&b, "FetchError: %s\n", r.FetchError)
		}
		fmt.Fprintf(&b, "\nCurrent CLAUDE.md:\n")
		if strings.TrimSpace(r.ClaudeMD) == "" {
			b.WriteString("(empty / file does not exist)\n\n")
		} else {
			b.WriteString("```markdown\n")
			b.WriteString(r.ClaudeMD)
			ensureTrailingNewline(&b)
			b.WriteString("```\n\n")
		}
	}

	fmt.Fprintf(&b, "---\n\n# YOUR INSTRUCTIONS\n\n")
	b.WriteString(skillBody)
	return b.String()
}

func ensureTrailingBlank(b *strings.Builder) {
	s := b.String()
	if !strings.HasSuffix(s, "\n\n") {
		if strings.HasSuffix(s, "\n") {
			b.WriteString("\n")
		} else {
			b.WriteString("\n\n")
		}
	}
}

func ensureTrailingNewline(b *strings.Builder) {
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
}

// loadProjectSkill reads <name>/SKILL.md, preferring the user override
// at ~/.clawflow/project-skills/<name>/SKILL.md, falling back to the
// embedded project-skills/. Returns the prompt body (frontmatter
// stripped). Mirrors the precedence in operator.Registry.LoadUserDir.
func loadProjectSkill(name string) (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, ".clawflow", "project-skills", name, "SKILL.md")
		if data, err := os.ReadFile(userPath); err == nil {
			return stripFrontmatter(string(data))
		}
	}
	embedPath := filepath.ToSlash(filepath.Join("project-skills", name, "SKILL.md"))
	data, err := fs.ReadFile(clawflow.EmbeddedProjectSkills, embedPath)
	if err != nil {
		return "", fmt.Errorf("project skill %q: %w", name, err)
	}
	return stripFrontmatter(string(data))
}

// stripFrontmatter removes a leading `---\n...\n---\n` YAML block.
// Project skills don't currently use the parsed frontmatter at runtime
// (trigger is dashboard-driven, outcomes are read from the marker line
// in stdout) — so we only need the body. If the file lacks
// frontmatter, return it as-is.
func stripFrontmatter(s string) (string, error) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return s, nil
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return "", fmt.Errorf("frontmatter not closed with '---'")
	}
	return rest[end+5:], nil
}

// proposeBlockRE matches one full propose block. Captures:
//
//	1: target (repo:<id> or project)
//	2: path
//	3: action (create | update)
//	4: content (everything between header and propose-end)
//
// (?s) is needed because content spans newlines.
var proposeBlockRE = regexp.MustCompile(`(?s)<!--\s*clawflow:propose\s+target=(\S+)\s+path=(\S+)\s+action=(\S+)\s*-->\n(.*?)\n<!--\s*clawflow:propose-end\s*-->`)

// outcomeRE pulls the project-outcome marker. The runner takes the
// last occurrence (mirrors the issue-operator outcome convention).
var outcomeRE = regexp.MustCompile(`<!--\s*clawflow:project-outcome=([a-zA-Z0-9_-]+)\s*-->`)

// summarySectionRE captures everything between "## Health summary"
// and the next "## " heading or the first propose block. Used purely
// for the Summary field shown in the dashboard.
var summarySectionRE = regexp.MustCompile(`(?s)##\s+Health summary\s*\n(.*?)(?:\n##\s|\n<!--\s*clawflow:propose\s|$)`)

// parseHealthCheckOutput pulls the structured pieces out of claude's
// stdout. Tolerates extra prose around the markers; the operator is a
// model and may add framing the SKILL.md didn't ask for.
func parseHealthCheckOutput(output string) *HealthCheckResult {
	res := &HealthCheckResult{
		Outcome: "changes-proposed",
		Changes: []ProposedChange{},
	}

	if m := outcomeRE.FindAllStringSubmatch(output, -1); len(m) > 0 {
		// Last marker wins, same as issue-operator outcome rule.
		res.Outcome = m[len(m)-1][1]
	}

	if m := summarySectionRE.FindStringSubmatch(output); len(m) >= 2 {
		res.Summary = strings.TrimSpace(m[1])
	}

	for _, m := range proposeBlockRE.FindAllStringSubmatch(output, -1) {
		target, path, action, content := m[1], m[2], m[3], m[4]
		change := ProposedChange{
			Path:            path,
			Action:          action,
			ProposedContent: content,
		}
		if strings.HasPrefix(target, "repo:") {
			change.Target = "repo"
			change.RepoID = strings.TrimPrefix(target, "repo:")
		} else {
			change.Target = target
		}
		res.Changes = append(res.Changes, change)
	}

	if res.Outcome == "healthy" {
		res.Changes = nil
	}
	return res
}

// enrichWithCurrentContent fills CurrentContent on each ProposedChange
// so the frontend can diff without another fetch. Mismatches between
// claimed `action` and on-disk reality (e.g. operator says "create" but
// the file exists) are normalized: we trust the disk state.
func enrichWithCurrentContent(res *HealthCheckResult, p *project.Project, cfg *config.Config) {
	if res == nil {
		return
	}
	for i := range res.Changes {
		c := &res.Changes[i]
		path, err := resolveChangePath(c, p, cfg)
		if err != nil {
			// Path can't be resolved — likely an unknown repo. Leave
			// CurrentContent empty; the apply step will reject it
			// with the same error.
			continue
		}
		data, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err):
			c.Action = "create"
			c.CurrentContent = ""
		case err == nil:
			c.Action = "update"
			c.CurrentContent = string(data)
		}
	}
}

// resolveChangePath maps a ProposedChange to an absolute filesystem
// path. Returns an error when the change references an unknown
// repo or a repo without a local clone.
func resolveChangePath(c *ProposedChange, p *project.Project, cfg *config.Config) (string, error) {
	cleanPath := filepath.Clean(c.Path)
	if filepath.IsAbs(cleanPath) || strings.HasPrefix(cleanPath, "..") {
		return "", fmt.Errorf("invalid path %q (must be relative, no parent traversal)", c.Path)
	}
	switch c.Target {
	case "project":
		return filepath.Join(project.ProjectDir(p.Name), cleanPath), nil
	case "repo":
		if c.RepoID == "" {
			return "", fmt.Errorf("repo target missing repo_id")
		}
		rc, ok := cfg.Repos[c.RepoID]
		if !ok {
			return "", fmt.Errorf("repo %q not in clawflow config", c.RepoID)
		}
		if rc.LocalPath == "" {
			return "", fmt.Errorf("repo %q has no local clone", c.RepoID)
		}
		return filepath.Join(rc.LocalPath, cleanPath), nil
	default:
		return "", fmt.Errorf("unknown target %q", c.Target)
	}
}
