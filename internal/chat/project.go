package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProjectDir returns ~/.clawflow/projects/<name>.
func ProjectDir(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".clawflow", "projects", name), nil
}

// ProjectContextPath returns ~/.clawflow/projects/<name>/context.md.
func ProjectContextPath(name string) (string, error) {
	dir, err := ProjectDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "context.md"), nil
}

// LoadProjectContext reads context.md for the named project.
// Returns ("", nil) if the file does not exist yet.
func LoadProjectContext(name string) (string, error) {
	p, err := ProjectContextPath(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SaveProjectContext writes content to context.md, creating the directory
// if needed.
func SaveProjectContext(name, content string) error {
	p, err := ProjectContextPath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// ListProjects returns the names of all projects that have a context.md.
func ListProjects() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(home, ".clawflow", "projects")
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ctxPath := filepath.Join(base, e.Name(), "context.md")
		if _, err := os.Stat(ctxPath); err == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// BuildProjectChatContext assembles the system prompt for project-level chat.
// It instructs Claude to work with the context.md document and output the
// final version when the user is satisfied.
func BuildProjectChatContext(name, contextMD string) string {
	var b strings.Builder

	fmt.Fprintln(&b, "# Project Chat Context")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Project: %s\n", name)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Current context.md")
	fmt.Fprintln(&b)
	if strings.TrimSpace(contextMD) == "" {
		fmt.Fprintln(&b, "_(empty — this is a new project)_")
	} else {
		fmt.Fprintln(&b, contextMD)
	}

	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, `## Your role

You are a project context assistant. You help the user refine and maintain
the project's context.md — a living document that captures the project's
purpose, architecture, conventions, and current state.

## Important: outputting the final document

When the conversation reaches a natural conclusion, or when the user asks
you to finalize, output the complete updated context.md inside a fenced
code block with the `+"`"+`context.md`+"`"+` info string, like this:

`+"```"+`context.md
# Project Name
... full document content ...
`+"```"+`

This is how the tool captures your changes for write-back. Always output
the COMPLETE document, not a partial diff. If the user hasn't asked for
changes, you don't need to output it.

## Hard constraints

- Do NOT run git commands that mutate the working tree.
- Do NOT edit, create, or delete files. This is a planning session.
- Focus on the context.md document — its structure, accuracy, and
  completeness.`)

	return b.String()
}

// streamJSONMessage is the minimal projection of a claude --output-format
// stream-json assistant message event. We only care about extracting text
// content from assistant messages.
type streamJSONMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	// For content_block events
	ContentBlock *contentBlock `json:"content_block,omitempty"`
	// For message events with content array
	Message *messagePayload `json:"message,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type messagePayload struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

// ExtractLastContextMD scans a stream-json output (one JSON object per line)
// and returns the content of the last fenced code block tagged "context.md"
// found in assistant messages. Returns "" if none found.
func ExtractLastContextMD(output string) string {
	// Collect all assistant text from the stream-json output.
	// Claude's stream-json format emits various event types; we look for
	// assistant message text in multiple possible shapes.
	var allText strings.Builder

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg streamJSONMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		// assistant message with inline content string
		if msg.Role == "assistant" && msg.Content != "" {
			allText.WriteString(msg.Content)
			continue
		}
		// content_block with text
		if msg.ContentBlock != nil && msg.ContentBlock.Type == "text" {
			allText.WriteString(msg.ContentBlock.Text)
			continue
		}
		// message wrapper with content array
		if msg.Message != nil && msg.Message.Role == "assistant" {
			for _, cb := range msg.Message.Content {
				if cb.Type == "text" {
					allText.WriteString(cb.Text)
				}
			}
			continue
		}
	}

	return extractFencedBlock(allText.String(), "context.md")
}

// extractFencedBlock finds the last fenced code block with the given info
// string in text. Returns the content between the fences, or "" if not found.
func extractFencedBlock(text, infoString string) string {
	var last string
	lines := strings.Split(text, "\n")
	inBlock := false
	var blockLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			// Look for opening fence: ```context.md or ``` context.md
			if (strings.HasPrefix(trimmed, "```"+infoString) || strings.HasPrefix(trimmed, "~~~ "+infoString)) &&
				len(trimmed) <= len("```"+infoString)+1 { // allow trailing whitespace
				inBlock = true
				blockLines = nil
				continue
			}
			// Also match with a space: "``` context.md"
			if strings.HasPrefix(trimmed, "``` "+infoString) {
				inBlock = true
				blockLines = nil
				continue
			}
		} else {
			// Look for closing fence
			if trimmed == "```" || trimmed == "~~~" {
				inBlock = false
				last = strings.Join(blockLines, "\n")
				continue
			}
			blockLines = append(blockLines, line)
		}
	}

	return last
}
