package chat

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/claude"
)

// ClaudeCLI implements Provider by shelling out to `claude -p`.
type ClaudeCLI struct{}

func (c *ClaudeCLI) Chat(ctx context.Context, req Request, out io.Writer) error {
	prompt := buildChatPrompt(req)

	args := []string{
		"-p",
		"--verbose",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, claude.Resolve(), args...)
	cmd.Env = claude.CleanedEnv(os.Environ())
	cmd.Stdout = out
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude: %w", err)
	}
	return nil
}

// buildChatPrompt assembles the full prompt from system context + message history.
func buildChatPrompt(req Request) string {
	var b strings.Builder

	if req.System != "" {
		b.WriteString(req.System)
		b.WriteString("\n\n---\n\n")
	}

	if len(req.Messages) > 0 {
		b.WriteString("# Conversation History\n\n")
		for _, m := range req.Messages {
			ts := m.Timestamp.Format(time.RFC3339)
			switch m.Role {
			case "user":
				fmt.Fprintf(&b, "**User** (%s):\n%s\n\n", ts, m.Content)
			case "assistant":
				fmt.Fprintf(&b, "**Assistant** (%s):\n%s\n\n", ts, m.Content)
			}
		}
		b.WriteString("---\n\n")
		b.WriteString("Continue the conversation. Respond to the user's latest message above.\n")
	}

	return b.String()
}
