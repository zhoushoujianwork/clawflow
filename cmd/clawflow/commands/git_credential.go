package commands

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/githubauth"
)

// newGitCredentialCmd implements Git's helper protocol. It must never log its
// input, token or config, and never store credentials in Git's persistent store.
func newGitCredentialCmd() *cobra.Command {
	return &cobra.Command{Use: "git-credential <operation>", Hidden: true, Args: cobra.ExactArgs(1), SilenceErrors: true, SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "get" {
			return nil
		}
		fields := map[string]string{}
		scanner := bufio.NewScanner(io.LimitReader(cmd.InOrStdin(), 65536))
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				break
			}
			k, v, ok := strings.Cut(line, "=")
			if ok {
				fields[k] = v
			}
		}
		if fields["protocol"] != "https" || fields["host"] != "github.com" {
			return nil
		}
		repo := strings.TrimSuffix(fields["path"], ".git")
		if strings.Count(repo, "/") != 1 {
			return nil
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("github_app: local configuration unavailable")
		}
		if !cfg.Settings.GitHubApp.Enabled() {
			return nil
		}
		creds, err := config.LoadCredentials()
		if err != nil {
			return fmt.Errorf("github_app: fallback credentials unavailable")
		}
		identity, err := githubauth.Resolve(cmd.Context(), repo, creds.GHToken, "")
		if err != nil {
			return err
		}
		if identity.Token != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "username=x-access-token\npassword=%s\n\n", identity.Token)
		}
		return nil
	}}
}
