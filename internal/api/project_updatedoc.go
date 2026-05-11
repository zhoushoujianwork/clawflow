package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// updateDocTimeout caps how long claude can spend rewriting one
// project-level doc. Five minutes is generous for a 1-5KB document but
// short enough that a misbehaving model can't pin an HTTP connection
// open indefinitely.
const updateDocTimeout = 5 * time.Minute

// allowedDocFiles whitelists the project-level files the
// /api/project/update-doc handler can write. Anything else (CLAUDE.md,
// project.yaml, .gitignore, ...) is auto-managed or out of scope.
var allowedDocFiles = map[string]struct{}{
	"context.md":    {},
	"testing.md":    {},
	"deployment.md": {},
}

type projectUpdateDocRequest struct {
	Project      string `json:"project"`
	File         string `json:"file"`
	Instructions string `json:"instructions"`
	Model        string `json:"model,omitempty"` // optional override
}

type projectUpdateDocResponse struct {
	OK         bool   `json:"ok"`
	NewContent string `json:"new_content"`
	CharCount  int    `json:"char_count"`
}

// HandleProjectUpdateDoc rewrites one project-level markdown file
// (context.md / testing.md / deployment.md) according to the user's
// free-form instructions. The handler is synchronous: it blocks until
// claude returns or the timeout fires, then writes the result and
// responds.
//
// The blocking design is deliberate — users explicitly asked for the
// simplest possible flow, and the doc-update task is short (one claude
// turn producing a 1-5KB fenced block). Background-job tracking like
// HealthCheck has would be overkill here.
func HandleProjectUpdateDoc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectUpdateDocRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	name := strings.TrimSpace(req.Project)
	file := strings.TrimSpace(req.File)
	instructions := strings.TrimSpace(req.Instructions)
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "project is required"})
		return
	}
	if _, ok := allowedDocFiles[file]; !ok {
		writeJSON(w, 400, map[string]string{
			"error": fmt.Sprintf("file %q is not updatable (allowed: context.md, testing.md, deployment.md)", file),
		})
		return
	}
	if instructions == "" {
		writeJSON(w, 400, map[string]string{"error": "instructions is required"})
		return
	}

	p, err := project.Get(name)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}

	current, err := readDocFile(name, file)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "read current doc: " + err.Error()})
		return
	}

	model := req.Model
	if model == "" {
		creds, _ := config.LoadCredentials()
		if creds != nil {
			model = creds.EffectiveOperatorModel()
		}
	}

	prompt := buildDocUpdatePrompt(p.Name, file, current, instructions)
	workdir := project.ProjectDir(p.Name)

	ctx, cancel := context.WithTimeout(r.Context(), updateDocTimeout)
	defer cancel()

	output, err := operator.RunClaude(ctx, prompt, workdir, updateDocTimeout, nil, model)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "run claude: " + err.Error()})
		return
	}

	newContent := chat.ExtractFencedDoc(output, file)
	if strings.TrimSpace(newContent) == "" {
		writeJSON(w, 502, map[string]string{
			"error": fmt.Sprintf("claude did not emit a fenced ```%s block; raw output: %s", file, truncate(output, 800)),
		})
		return
	}

	if err := writeDocFile(name, file, newContent); err != nil {
		writeJSON(w, 500, map[string]string{"error": "write doc: " + err.Error()})
		return
	}

	_ = snapshot.WriteProjects()

	writeJSON(w, 200, projectUpdateDocResponse{
		OK:         true,
		NewContent: newContent,
		CharCount:  len(newContent),
	})
}

// readDocFile dispatches by filename to the matching project.Read* helper.
// allowedDocFiles is checked upstream so the default branch is unreachable;
// the explicit error keeps the linter and future maintainers honest.
func readDocFile(projectName, file string) (string, error) {
	switch file {
	case "context.md":
		return project.ReadContext(projectName)
	case "testing.md":
		return project.ReadTesting(projectName)
	case "deployment.md":
		return project.ReadDeployment(projectName)
	default:
		return "", fmt.Errorf("unsupported doc %q", file)
	}
}

// writeDocFile dispatches by filename to the matching project.Write* helper.
// Same shape as readDocFile.
func writeDocFile(projectName, file, content string) error {
	switch file {
	case "context.md":
		return project.WriteContext(projectName, content)
	case "testing.md":
		return project.WriteTesting(projectName, content)
	case "deployment.md":
		return project.WriteDeployment(projectName, content)
	default:
		return fmt.Errorf("unsupported doc %q", file)
	}
}

// buildDocUpdatePrompt is the pure prompt-builder. Separated from the
// HTTP handler so it can be unit-tested without a running claude.
func buildDocUpdatePrompt(projectName, file, current, instructions string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Update `%s` for project `%s`\n\n", file, projectName)

	fmt.Fprintln(&b, "## Current content")
	fmt.Fprintln(&b)
	if strings.TrimSpace(current) == "" {
		fmt.Fprintln(&b, "_(file is empty — this is a fresh authoring task)_")
	} else {
		fmt.Fprintln(&b, current)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## User instructions")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, instructions)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Output protocol")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Apply the user's instructions to the current content. Emit the COMPLETE\n")
	fmt.Fprintf(&b, "updated document inside a fenced code block whose info string is\n")
	fmt.Fprintf(&b, "**literally** `%s`:\n\n", file)
	fmt.Fprintf(&b, "    ```%s\n", file)
	fmt.Fprintln(&b, "    <full updated document content>")
	fmt.Fprintln(&b, "    ```")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Rules:")
	fmt.Fprintf(&b, "- The opening fence MUST be exactly ```%s on its own line.\n", file)
	fmt.Fprintln(&b, "- Emit the COMPLETE document, not a diff or excerpt — the runner")
	fmt.Fprintln(&b, "  replaces the file wholesale with whatever's inside the block.")
	fmt.Fprintln(&b, "- Preserve correct existing content. Only change what the user asked")
	fmt.Fprintln(&b, "  for, plus minimal edits needed for consistency.")
	fmt.Fprintln(&b, "- Emit at most ONE fenced block per response.")
	fmt.Fprintln(&b, "- Do not add commentary outside the block.")

	return b.String()
}

// truncate returns at most n runes of s, suffixed with "..." when truncated.
// Used to keep error payloads small when claude emits a wall of text instead
// of a fenced block.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
