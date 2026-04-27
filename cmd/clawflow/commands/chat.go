package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func NewChatCmd() *cobra.Command {
	var (
		repo    string
		issue   int
		model   string
		resume  string
	)
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Interactive AI chat with repo/issue context",
		Long: `Start an interactive chat session with AI, automatically injecting
repository or issue context. The AI can help evaluate issues, suggest
labels, create new issues, and more.

Examples:
  clawflow chat --repo owner/repo
  clawflow chat --repo owner/repo --issue 42
  clawflow chat --repo owner/repo --issue 42 --model sonnet`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			return runChat(cmd.Context(), repo, issue, model, resume)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository (owner/repo)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number for issue-level chat")
	cmd.Flags().StringVar(&model, "model", "haiku", "claude model to use")
	cmd.Flags().StringVar(&resume, "resume", "", "session ID to resume")
	return cmd
}

func runChat(ctx context.Context, repo string, issueNum int, model, resumeID string) error {
	client, repoCfg, err := newVCSClientForRepo(repo)
	if err != nil {
		return err
	}

	var systemCtx string
	if issueNum > 0 {
		systemCtx, err = buildIssueChatContext(client, repo, issueNum)
	} else {
		systemCtx, err = buildRepoChatContext(client, repo, repoCfg.Platform, repoCfg.BaseBranch)
	}
	if err != nil {
		return fmt.Errorf("build context: %w", err)
	}

	var sessionDir string
	if resumeID != "" {
		slug := repoSlug(repo)
		sessionDir = chat.SessionPath(slug, resumeID)
		if _, err := os.Stat(sessionDir); err != nil {
			return fmt.Errorf("session %q not found", resumeID)
		}
		fmt.Printf("Resuming session %s\n", resumeID)
	} else {
		_, dir, err := chat.NewSession(repo, issueNum, model)
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		sessionDir = dir
	}

	if issueNum > 0 {
		fmt.Printf("Chat: %s #%d (model: %s)\n", repo, issueNum, model)
	} else {
		fmt.Printf("Chat: %s (model: %s)\n", repo, model)
	}
	fmt.Println()

	provider := &chat.ClaudeCLI{}
	return chat.RunREPL(ctx, chat.REPLConfig{
		Repo:        repo,
		IssueNumber: issueNum,
		Model:       model,
		Provider:    provider,
		VCS:         client,
		System:      systemCtx,
		SessionDir:  sessionDir,
	})
}

func buildIssueChatContext(client vcs.Client, repo string, issueNum int) (string, error) {
	issues, err := client.ListOpenIssues(repo)
	if err != nil {
		return "", err
	}
	var issue vcs.Issue
	found := false
	for _, iss := range issues {
		if iss.Number == issueNum {
			issue = iss
			found = true
			break
		}
	}
	if !found {
		// Try closed issues too
		allIssues, err := client.ListIssues(repo, "all", nil)
		if err != nil {
			return "", fmt.Errorf("issue #%d not found", issueNum)
		}
		for _, iss := range allIssues {
			if iss.Number == issueNum {
				issue = iss
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("issue #%d not found", issueNum)
		}
	}

	comments, _ := client.ListIssueCommentsDetail(repo, issueNum)
	return chat.BuildIssueContext(repo, issue, comments), nil
}

func buildRepoChatContext(client vcs.Client, repo, platform, baseBranch string) (string, error) {
	if platform == "" {
		platform = "github"
	}
	if baseBranch == "" {
		baseBranch = "main"
	}
	issues, err := client.ListOpenIssues(repo)
	if err != nil {
		return "", err
	}
	return chat.BuildRepoContext(repo, platform, baseBranch, issues), nil
}

func repoSlug(repo string) string {
	return filepath.Base(repo)
}
