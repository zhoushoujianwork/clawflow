// Package github wraps the gh CLI for ClawFlow operations.
package github

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Issue represents a GitHub issue.
type Issue struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []Label  `json:"labels"`
}

// Label represents a GitHub label.
type Label struct {
	Name string `json:"name"`
}

// PR represents a GitHub pull request.
type PR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	Body        string `json:"body"`
	State       string `json:"state"`
}

// HasLabel reports whether an issue has a given label.
func (i Issue) HasLabel(name string) bool {
	for _, l := range i.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

// ListOpenIssues returns all open issues for a repo.
func ListOpenIssues(repo string) ([]Issue, error) {
	out, err := gh("issue", "list", "-R", repo, "--state", "open",
		"--json", "number,title,body,labels", "--limit", "200")
	if err != nil {
		return nil, err
	}
	var issues []Issue
	return issues, json.Unmarshal(out, &issues)
}

// ListOpenPRs returns all open PRs for a repo.
func ListOpenPRs(repo string) ([]PR, error) {
	out, err := gh("pr", "list", "-R", repo, "--state", "open",
		"--json", "number,title,headRefName,body", "--limit", "100")
	if err != nil {
		return nil, err
	}
	var prs []PR
	return prs, json.Unmarshal(out, &prs)
}

// PRExistsForIssue checks whether an open PR already targets this issue.
func PRExistsForIssue(repo string, issueNumber int) (bool, error) {
	prs, err := ListOpenPRs(repo)
	if err != nil {
		return false, err
	}
	needle := fmt.Sprintf("issue-%d", issueNumber)
	fixes := fmt.Sprintf("Fixes #%d", issueNumber)
	for _, pr := range prs {
		if strings.Contains(pr.HeadRefName, needle) ||
			strings.Contains(pr.Body, fixes) {
			return true, nil
		}
	}
	return false, nil
}

// AddLabel adds one or more labels to an issue.
func AddLabel(repo string, issueNumber int, labels ...string) error {
	_, err := gh("issue", "edit",
		fmt.Sprint(issueNumber), "-R", repo,
		"--add-label", strings.Join(labels, ","))
	return err
}

// RemoveLabel removes one or more labels from an issue.
func RemoveLabel(repo string, issueNumber int, labels ...string) error {
	_, err := gh("issue", "edit",
		fmt.Sprint(issueNumber), "-R", repo,
		"--remove-label", strings.Join(labels, ","))
	return err
}

// gh runs a gh CLI subcommand and returns stdout.
// It injects GH_TOKEN from credentials.yaml if not already set in the environment.
func gh(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	injectToken(cmd)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, ee.Stderr)
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// injectToken sets GH_TOKEN on the command if not already in the environment.
func injectToken(cmd *exec.Cmd) {
	if os.Getenv("GH_TOKEN") != "" {
		return // already set, nothing to do
	}
	home, _ := os.UserHomeDir()
	credsPath := filepath.Join(home, ".clawflow", "config", "credentials.yaml")
	data, err := os.ReadFile(credsPath)
	if err != nil {
		return
	}
	// Simple extraction — avoid importing config to prevent import cycle.
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "gh_token:") {
			token := strings.TrimSpace(strings.TrimPrefix(line, "gh_token:"))
			token = strings.Trim(token, `"'`)
			if token != "" {
				cmd.Env = append(os.Environ(), "GH_TOKEN="+token)
			}
			return
		}
	}
}
