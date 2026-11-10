package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// NewGitLabCmd manages the registered GitLab sites. Every site keeps its own
// URL, credential, SSH bindings, version and capability declarations;
// changing one never touches another.
func NewGitLabCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gitlab",
		Short: "Manage GitLab sites (several self-hosted GitLab servers side by side)",
	}
	site := &cobra.Command{Use: "site", Short: "Add, change, inspect and remove GitLab sites"}
	site.AddCommand(newGitLabSiteAddCmd(), newGitLabSiteSetCmd(), newGitLabSiteListCmd(), newGitLabSiteShowCmd(), newGitLabSiteRemoveCmd(), newGitLabSiteSetTokenCmd(), newGitLabSiteResolveCmd())
	cmd.AddCommand(site)
	return cmd
}

type siteFlags struct {
	url, tokenEnv, version string
	sshHosts, unsupported  []string
	useLegacyToken         bool
	supported              []string
}

func (f *siteFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.url, "url", "", "GitLab root URL: scheme, host, non-default port and deployment prefix (e.g. https://git.example.com:8443/gitlab)")
	cmd.Flags().StringVar(&f.tokenEnv, "token-env", "", "environment variable that holds this site's token (the token itself is never stored here)")
	cmd.Flags().StringVar(&f.version, "version", "", "GitLab server version, for display (e.g. 11.11)")
	cmd.Flags().StringSliceVar(&f.sshHosts, "ssh-host", nil, "Git SSH host[:port] that belongs to this site; required when it differs from the API host")
	cmd.Flags().StringSliceVar(&f.unsupported, "unsupported", nil, "API capability this server lacks ("+strings.Join(vcs.KnownCapabilities, ", ")+"); repeatable")
	cmd.Flags().StringSliceVar(&f.supported, "supported", nil, "remove a capability from the unsupported list; repeatable")
	cmd.Flags().BoolVar(&f.useLegacyToken, "use-legacy-token", false, "let this site use the pre-existing global gitlab_token (explicit opt-in; one site only)")
}

func newGitLabSiteAddCmd() *cobra.Command {
	var f siteFlags
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a GitLab site",
		Long: `Registers a GitLab site under a short name. The name prefixes repository keys
(name:group/project) so the same path on two sites never collides.

The existing global GitLab token is NOT copied to the new site. Give the site
its own credential with 'clawflow gitlab site set-token <name>' or --token-env.`,
		Example: "  clawflow gitlab site add old --url http://gitlab-a.example.com --version 11.11 --use-legacy-token\n  clawflow gitlab site add new --url https://gitlab-b.example.com:8443/gitlab --version 17.0 --ssh-host ssh.gitlab-b.example.com:2222\n  printf %s \"$TOKEN\" | clawflow gitlab site set-token new --stdin",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.url == "" {
				return fmt.Errorf("--url is required")
			}
			cfg, err := loadOrNewConfig()
			if err != nil {
				return err
			}
			if _, exists := cfg.Settings.GitLabSite(args[0]); exists {
				return fmt.Errorf("GitLab site %q already exists; change it with: clawflow gitlab site set %s", args[0], args[0])
			}
			site := config.GitLabSite{Name: args[0], URL: f.url, TokenEnv: f.tokenEnv, SSHHosts: f.sshHosts, Version: f.version, Unsupported: f.unsupported, UseLegacyToken: f.useLegacyToken}
			if err := cfg.UpsertGitLabSite(site); err != nil {
				return err
			}
			// Repositories whose base_url equals this site's URL are bound to it
			// explicitly; all others stay untouched.
			cfg.BindGitLabRepos()
			if err := cfg.Save(); err != nil {
				return err
			}
			saved, _ := cfg.Settings.GitLabSite(args[0])
			printSite(cmd.OutOrStdout(), saved, cfg)
			fmt.Fprintf(cmd.OutOrStdout(), "next: clawflow gitlab site set-token %s   (this site has no credential until you set one)\n", saved.Name)
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

func newGitLabSiteSetCmd() *cobra.Command {
	var f siteFlags
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Change one GitLab site (other sites are untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			site, ok := cfg.Settings.GitLabSite(args[0])
			if !ok {
				return fmt.Errorf("GitLab site %q is not configured (registered: %s)", args[0], siteNameList(cfg))
			}
			fl := cmd.Flags()
			if fl.Changed("url") {
				site.URL = f.url
			}
			if fl.Changed("token-env") {
				site.TokenEnv = f.tokenEnv
			}
			if fl.Changed("version") {
				site.Version = f.version
			}
			if fl.Changed("ssh-host") {
				site.SSHHosts = f.sshHosts
			}
			if fl.Changed("unsupported") {
				site.Unsupported = append(append([]string(nil), site.Unsupported...), f.unsupported...)
			}
			if fl.Changed("supported") {
				var keep []string
				for _, c := range site.Unsupported {
					drop := false
					for _, s := range f.supported {
						drop = drop || s == c
					}
					if !drop {
						keep = append(keep, c)
					}
				}
				site.Unsupported = keep
			}
			if fl.Changed("use-legacy-token") {
				site.UseLegacyToken = f.useLegacyToken
			}
			if err := cfg.UpsertGitLabSite(site); err != nil {
				return err
			}
			cfg.BindGitLabRepos()
			// A repository bound to this site follows its URL; all others are untouched.
			for key, r := range cfg.Repos {
				if r.Platform == "gitlab" && r.Site == site.Name {
					if saved, ok := cfg.Settings.GitLabSite(site.Name); ok {
						r.BaseURL = saved.URL
						cfg.Repos[key] = r
					}
				}
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			saved, _ := cfg.Settings.GitLabSite(args[0])
			printSite(cmd.OutOrStdout(), saved, cfg)
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

func newGitLabSiteListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List GitLab sites and the repositories bound to each",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			creds, _ := config.LoadCredentials()
			rows := siteRows(cfg, creds)
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No GitLab sites registered. Add one with: clawflow gitlab site add <name> --url <https://host>")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "SITE\tURL\tVERSION\tCREDENTIAL\tREPOS\tUNSUPPORTED")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", r.Name, r.URL, valueOr(r.Version, "-"), r.Credential, r.Repos, valueOr(strings.Join(r.Unsupported, ","), "-"))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

// SiteRow is the credential-free description of a site shown by the CLI and
// the dashboard.
type SiteRow struct {
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	Version     string   `json:"version,omitempty"`
	SSHHosts    []string `json:"ssh_hosts,omitempty"`
	Unsupported []string `json:"unsupported,omitempty"`
	Credential  string   `json:"credential"` // "set (source)" or "missing"; never the token
	Repos       int      `json:"repos"`
}

func siteRows(cfg *config.Config, creds *config.Credentials) []SiteRow {
	rows := make([]SiteRow, 0, len(cfg.Settings.GitLabSites))
	for _, s := range cfg.Settings.GitLabSites {
		row := SiteRow{Name: s.Name, URL: s.URL, Version: s.Version, SSHHosts: s.SSHHosts, Unsupported: s.Unsupported, Credential: "missing"}
		if _, src := creds.SiteToken(s); src != "" {
			row.Credential = "set (" + src + ")"
		}
		for _, r := range cfg.Repos {
			if r.Platform == "gitlab" && r.Site == s.Name {
				row.Repos++
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func newGitLabSiteShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show one GitLab site and its repositories",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			site, ok := cfg.Settings.GitLabSite(args[0])
			if !ok {
				return fmt.Errorf("GitLab site %q is not configured (registered: %s)", args[0], siteNameList(cfg))
			}
			printSite(cmd.OutOrStdout(), site, cfg)
			return nil
		},
	}
}

func newGitLabSiteRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a GitLab site that has no repositories",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := cfg.RemoveGitLabSite(args[0]); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			// Drop only this site's stored credential.
			if creds, cerr := config.LoadCredentials(); cerr == nil {
				if _, had := creds.GitLabTokens[args[0]]; had {
					creds.SetSiteToken(args[0], "")
					_ = config.SaveCredentials(creds)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "GitLab site %q removed\n", args[0])
			return nil
		},
	}
}

func newGitLabSiteSetTokenCmd() *cobra.Command {
	var stdin bool
	cmd := &cobra.Command{
		Use:   "set-token <name> [token]",
		Short: "Store the credential of one GitLab site",
		Long: `Stores a token for exactly one site in credentials.yaml (mode 0600). Other sites
and the legacy global token are not read or changed. Prefer --stdin so the token
stays out of shell history and process listings.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Settings.GitLabSite(args[0]); !ok {
				return fmt.Errorf("GitLab site %q is not configured (registered: %s)", args[0], siteNameList(cfg))
			}
			var token string
			switch {
			case stdin:
				b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 8192))
				if err != nil {
					return err
				}
				token = strings.TrimSpace(string(b))
			case len(args) == 2:
				token = args[1]
			default:
				return fmt.Errorf("pass the token as an argument or use --stdin")
			}
			if token == "" {
				return fmt.Errorf("token cannot be empty")
			}
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			creds.SetSiteToken(args[0], token)
			if err := config.SaveCredentials(creds); err != nil {
				return fmt.Errorf("failed to save token: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "token for GitLab site %q saved (…%s)\n", args[0], lastChars(token, 4))
			return nil
		},
	}
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read the token from standard input")
	return cmd
}

func newGitLabSiteResolveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resolve <repo>",
		Short: "Show which platform, site and project a repo argument selects (no network)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			t, err := cfg.Resolve(args[0])
			if err != nil {
				return err
			}
			state := "not configured (transient)"
			if t.Configured {
				state = "configured"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "key:      %s\ntarget:   %s\nstate:    %s\n", t.Key, t.Describe(), state)
			return nil
		},
	}
}

func printSite(w io.Writer, s config.GitLabSite, cfg *config.Config) {
	creds, _ := config.LoadCredentials()
	cred := "missing"
	if _, src := creds.SiteToken(s); src != "" {
		cred = "set (" + src + ")"
	}
	fmt.Fprintf(w, "site:         %s\n", s.Name)
	fmt.Fprintf(w, "url:          %s\n", s.URL)
	fmt.Fprintf(w, "api:          %s\n", s.APIBase())
	fmt.Fprintf(w, "version:      %s\n", valueOr(s.Version, "(not declared)"))
	fmt.Fprintf(w, "credential:   %s\n", cred)
	fmt.Fprintf(w, "ssh_hosts:    %s\n", valueOr(strings.Join(s.SSHHosts, ", "), "(none)"))
	fmt.Fprintf(w, "unsupported:  %s\n", valueOr(strings.Join(s.Unsupported, ", "), "(none)"))
	var repos []string
	for key, r := range cfg.Repos {
		if r.Platform == "gitlab" && r.Site == s.Name {
			repos = append(repos, key)
		}
	}
	sort.Strings(repos)
	fmt.Fprintf(w, "repos:        %s\n", valueOr(strings.Join(repos, ", "), "(none)"))
}

func siteNameList(cfg *config.Config) string {
	var names []string
	for _, s := range cfg.Settings.GitLabSites {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return valueOr(strings.Join(names, ", "), "none")
}

func lastChars(s string, n int) string {
	if len(s) <= n {
		return strings.Repeat("*", len(s))
	}
	return s[len(s)-n:]
}
