package chat

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// REPLConfig holds configuration for the interactive chat loop.
type REPLConfig struct {
	Repo        string
	IssueNumber int
	Model       string
	Provider    Provider
	VCS         vcs.Client
	System      string // pre-built system context
	SessionDir  string // where to persist messages
}

// RunREPL starts the interactive chat loop.
func RunREPL(ctx context.Context, cfg REPLConfig) error {
	messages, err := LoadMessages(cfg.SessionDir)
	if err != nil {
		return fmt.Errorf("load history: %w", err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Type your message (or /quit to exit, /context to show context)")
	fmt.Println()

	for {
		fmt.Print("you> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		switch {
		case input == "/quit" || input == "/exit":
			fmt.Println("Bye!")
			return nil
		case input == "/context":
			fmt.Println(cfg.System)
			continue
		case input == "/history":
			for _, m := range messages {
				fmt.Printf("[%s] %s: %s\n\n", m.Timestamp.Format("15:04"), m.Role, m.Content)
			}
			continue
		}

		userMsg := Message{
			Role:      "user",
			Content:   input,
			Timestamp: time.Now(),
		}
		messages = append(messages, userMsg)
		_ = AppendMessage(cfg.SessionDir, userMsg)

		req := Request{
			System:   cfg.System,
			Messages: messages,
			Model:    cfg.Model,
		}

		fmt.Print("\nassistant> ")
		var buf bytes.Buffer
		w := io.MultiWriter(os.Stdout, &buf)

		if err := cfg.Provider.Chat(ctx, req, w); err != nil {
			fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			continue
		}
		fmt.Println()

		response := buf.String()
		assistantMsg := Message{
			Role:      "assistant",
			Content:   response,
			Timestamp: time.Now(),
		}
		messages = append(messages, assistantMsg)
		_ = AppendMessage(cfg.SessionDir, assistantMsg)

		actions := ParseActions(response)
		if len(actions) > 0 && cfg.VCS != nil {
			fmt.Println()
			for _, a := range actions {
				desc := a.Describe(cfg.IssueNumber)
				fmt.Printf("  Action: %s\n", desc)
				fmt.Print("  Apply? [y/N] ")
				if !scanner.Scan() {
					break
				}
				answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
				if answer == "y" || answer == "yes" {
					if err := a.Execute(cfg.VCS, cfg.Repo, cfg.IssueNumber); err != nil {
						fmt.Fprintf(os.Stderr, "  Failed: %v\n", err)
					} else {
						fmt.Println("  Done.")
					}
				} else {
					fmt.Println("  Skipped.")
				}
			}
		}
		fmt.Println()
	}

	return scanner.Err()
}
