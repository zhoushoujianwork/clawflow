// Package projectgen builds the prompt for a project's context.md and
// invokes `claude -p` to produce it. Shared between the CLI's
// `clawflow project generate` subcommand and the dashboard's
// /api/project/generate-context endpoint so both paths produce
// byte-identical output.
package projectgen

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// Generate scans the project's member repos, builds a prompt, runs
// `claude -p`, writes the result to context.md, and returns the
// content that was written. `model` may be empty to use the default
// chat model from credentials.
//
// `instructions` is optional steering from the user (e.g. "focus on
// API surface", "use bullet points"). When non-empty, the existing
// context.md is also injected into the prompt and Claude is asked to
// preserve manually-edited sections except where the instructions
// override them. Empty instructions trigger the original "produce a
// fresh overview from scratch" behavior — backwards-compatible.
func Generate(name, model, instructions string) (string, error) {
	p, err := project.Get(name)
	if err != nil {
		return "", err
	}
	if len(p.Repos) == 0 {
		return "", fmt.Errorf("project %q has no repos — add one first", name)
	}

	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}

	prompt, scanned := buildPrompt(name, p.Repos, cfg, instructions)
	if scanned == 0 {
		return "", fmt.Errorf("no repos with local_path configured — set local_path in config for at least one member repo")
	}

	if model == "" {
		creds, _ := config.LoadCredentials()
		model = creds.EffectiveChatModel()
	}

	bin := claude.Resolve()
	args := []string{
		"--model", model,
		"--print",
		"-p", prompt,
	}
	cmd := exec.Command(bin, args...)
	creds, _ := config.LoadCredentials()
	apiKey, baseURL := "", ""
	if creds != nil {
		apiKey, baseURL = creds.ClaudeAPIKey, creds.ClaudeBaseURL
	}
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude generate failed: %w", err)
	}

	content := sanitizeOutput(string(output))
	if content == "" {
		return "", fmt.Errorf("claude returned empty output")
	}

	if err := project.WriteContext(name, content); err != nil {
		return "", err
	}
	return content, nil
}

// sanitizeOutput cleans up `claude -p` output that disobeyed the
// "Markdown only, no preamble" instruction. Two common failure modes:
//
//  1. Preamble like "Here is the updated context.md:" before the doc
//  2. Wrapping the entire markdown body inside a ```...``` fence
//     (often with `markdown` or `context.md` as the info string),
//     which then renders as a giant code block in the dashboard
//     instead of as headings/paragraphs.
//
// We extract the largest fenced block if one exists; otherwise we
// strip leading non-markdown chatter up to the first heading/list/
// horizontal-rule marker. If neither pattern matches, we fall back
// to the trimmed raw output — that's the happy path when Claude
// followed instructions.
func sanitizeOutput(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if unwrapped, ok := unwrapFencedDocument(trimmed); ok {
		return strings.TrimSpace(unwrapped)
	}
	return strings.TrimSpace(stripLeadingChatter(trimmed))
}

// unwrapFencedDocument detects the case where Claude wrapped the
// entire document body in a single ```fence```. We accept any info
// string (markdown, context.md, md, …) and require the fence to
// span the whole document modulo leading/trailing chatter.
func unwrapFencedDocument(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	openIdx := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			openIdx = i
			break
		}
	}
	if openIdx == -1 {
		return "", false
	}
	closeIdx := -1
	for i := len(lines) - 1; i > openIdx; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return "", false
	}
	inner := lines[openIdx+1 : closeIdx]
	body := strings.Join(inner, "\n")
	// Heuristic: only treat as a wrapped-document case if the inner
	// body looks like a markdown doc (has at least one heading or
	// list marker). Otherwise it's probably a real code block the
	// user wanted preserved.
	if !looksLikeMarkdown(body) {
		return "", false
	}
	return body, true
}

func looksLikeMarkdown(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") || strings.HasPrefix(t, "## ") || strings.HasPrefix(t, "### ") {
			return true
		}
	}
	return false
}

// stripLeadingChatter drops introductory lines like
// "Here is the updated context.md:" until we hit something that
// looks like a real markdown document start (heading, list, hr, or
// non-empty paragraph after a blank line). Conservative — leaves
// content alone if no chatter pattern matches.
func stripLeadingChatter(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") || strings.HasPrefix(t, "## ") || strings.HasPrefix(t, "### ") || strings.HasPrefix(t, "---") {
			return strings.Join(lines[i:], "\n")
		}
	}
	return s
}

func buildPrompt(name string, repos []string, cfg *config.Config, instructions string) (string, int) {
	var parts []string
	parts = append(parts, fmt.Sprintf("# Project: %s\n", name))
	parts = append(parts, fmt.Sprintf("This project contains %d repositories:\n", len(repos)))

	scanned := 0
	for _, repoName := range repos {
		repoCfg, ok := cfg.Repos[repoName]
		localPath := ""
		if ok {
			localPath = repoCfg.LocalPath
		}
		if localPath == "" {
			parts = append(parts, fmt.Sprintf("\n## %s\n(no local_path configured — skipped)\n", repoName))
			continue
		}

		parts = append(parts, fmt.Sprintf("\n## %s\nLocal path: %s\n", repoName, localPath))

		for _, readme := range []string{"README.md", "readme.md", "README"} {
			data, err := os.ReadFile(fmt.Sprintf("%s/%s", localPath, readme))
			if err == nil {
				content := string(data)
				if len(content) > 3000 {
					content = content[:3000] + "\n... (truncated)"
				}
				parts = append(parts, fmt.Sprintf("\n### README\n```\n%s\n```\n", content))
				break
			}
		}

		configFiles := []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "pom.xml", "build.gradle"}
		for _, cf := range configFiles {
			data, err := os.ReadFile(fmt.Sprintf("%s/%s", localPath, cf))
			if err == nil {
				content := string(data)
				if len(content) > 1500 {
					content = content[:1500] + "\n... (truncated)"
				}
				parts = append(parts, fmt.Sprintf("\n### %s\n```\n%s\n```\n", cf, content))
			}
		}

		entries, err := os.ReadDir(localPath)
		if err == nil {
			var dirs, files []string
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".") {
					continue
				}
				if e.IsDir() {
					dirs = append(dirs, e.Name()+"/")
				} else {
					files = append(files, e.Name())
				}
			}
			parts = append(parts, "\n### Directory structure\n```\n")
			for _, d := range dirs {
				parts = append(parts, d+"\n")
			}
			for _, f := range files {
				parts = append(parts, f+"\n")
			}
			parts = append(parts, "```\n")
		}
		scanned++
	}

	if strings.TrimSpace(instructions) == "" {
		parts = append(parts, `
---

Based on the repository information above, produce a project overview document in Markdown. Include:

1. **Project Overview** — one paragraph describing what this project does
2. **Repository Roles** — for each repo, its role and responsibility (2-3 sentences)
3. **Inter-repo Dependencies** — how the repos depend on and collaborate with each other
4. **Architecture Overview** — high-level architecture description

OUTPUT FORMAT — strict:
- Output ONLY the Markdown document body.
- Do NOT prepend any preamble like "Here is the document:" or "I have generated...".
- Do NOT wrap the document in a `+"```"+` fence. Headings, paragraphs and lists must be at the top level so they render as Markdown, not as a code block.
- Start your response with the first heading (e.g. "# `+name+`") and end with the last paragraph of the document.`)
		return strings.Join(parts, ""), scanned
	}

	// Custom instructions present: include the existing context.md (if any)
	// so Claude can preserve manual edits, and append the user's steering.
	existing, _ := project.ReadContext(name)
	parts = append(parts, "\n---\n")
	if strings.TrimSpace(existing) != "" {
		parts = append(parts, "\nCurrent context.md (treat manual edits as authoritative — preserve unless overridden by the instructions below):\n\n```\n")
		parts = append(parts, existing)
		if !strings.HasSuffix(existing, "\n") {
			parts = append(parts, "\n")
		}
		parts = append(parts, "```\n")
	}
	parts = append(parts, "\nUser instructions:\n\n")
	parts = append(parts, strings.TrimSpace(instructions))
	parts = append(parts, `

Produce an updated complete context.md that follows the user's instructions, stays grounded in the repository state above, and preserves untouched sections of the current context.md where they don't conflict.

OUTPUT FORMAT — strict:
- Output ONLY the Markdown document body.
- Do NOT prepend any preamble like "Here is the updated document:" or "I have updated...".
- Do NOT wrap the document in a `+"```"+` fence. Headings, paragraphs and lists must be at the top level so they render as Markdown, not as a code block.
- Start your response with the first heading and end with the last paragraph of the document.`)

	return strings.Join(parts, ""), scanned
}
