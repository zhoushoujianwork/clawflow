package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// NewDashboardCmd is a JSON-only bridge for the native app. TLS, token-file
// handling and remote routing share the existing platform client.
func NewDashboardCmd() *cobra.Command {
	var settings config.PlatformSettings
	cmd := &cobra.Command{Use: "dashboard", Short: "Native dashboard service bridge", SilenceUsage: true, SilenceErrors: true}
	cmd.PersistentFlags().StringVar(&settings.URL, "url", "", "Controller HTTPS origin (defaults to platform configuration)")
	cmd.PersistentFlags().StringVar(&settings.TokenFile, "token-file", "", "Admin token file")
	cmd.PersistentFlags().StringVar(&settings.Fingerprint, "fingerprint", "", "Pinned SHA-256 certificate fingerprint")
	call := func(cmd *cobra.Command, method, path string, input any) error {
		s := settings
		if s.URL == "" {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s = cfg.Settings.Platform
		}
		var output json.RawMessage
		if err := platformCall(cmd.Context(), s, method, path, input, &output); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(output)
	}
	cmd.AddCommand(&cobra.Command{Use: "snapshot", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return call(c, "GET", "/admin/v1/dashboard", nil) }})
	for _, action := range []string{"invite", "drain", "resume", "remove", "events"} {
		action := action
		cmd.AddCommand(&cobra.Command{Use: action + " NAME", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
			return call(c, "POST", "/admin/v1/dashboard/action", coordinator.DashboardRequest{Action: action, Name: args[0]})
		}})
	}
	// Only monitoring changes here. CI/acceptance/merge remain independent and
	// cannot be weakened by an innocent dashboard pause/resume action.
	for _, action := range []string{"enable", "disable"} {
		action := action
		cmd.AddCommand(&cobra.Command{Use: action + " REPO", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
			return call(c, "POST", "/admin/v1/repos", coordinator.RepoRequest{Action: action, Repo: args[0]})
		}})
	}
	return cmd
}

func (a *platformAdmin) Dashboard(ctx context.Context) (coordinator.Dashboard, error) {
	board, err := a.board(ctx)
	if err != nil {
		return coordinator.Dashboard{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg, err := config.Load()
	if err != nil {
		return coordinator.Dashboard{}, err
	}
	projects, err := project.List()
	if err != nil {
		return coordinator.Dashboard{}, err
	}
	host, _ := os.Hostname()
	out := coordinator.Dashboard{SchemaVersion: 1, Version: Version, Controller: host, Board: board, Projects: []coordinator.DashboardProject{}}
	seen := map[string]bool{}
	for _, p := range projects {
		row := coordinator.DashboardProject{Name: p.Name, Repos: []coordinator.DashboardRepo{}}
		for _, name := range p.Repos {
			if r, ok := cfg.Repos[name]; ok {
				row.Repos = append(row.Repos, dashboardRepo(name, r))
				seen[name] = true
			}
		}
		out.Projects = append(out.Projects, row)
	}
	ungrouped := coordinator.DashboardProject{Name: "Ungrouped repositories", Repos: []coordinator.DashboardRepo{}}
	for name, r := range cfg.Repos {
		if !seen[name] {
			ungrouped.Repos = append(ungrouped.Repos, dashboardRepo(name, r))
		}
	}
	sort.Slice(ungrouped.Repos, func(i, j int) bool { return ungrouped.Repos[i].Name < ungrouped.Repos[j].Name })
	if len(ungrouped.Repos) > 0 {
		out.Projects = append(out.Projects, ungrouped)
	}
	return out, nil
}

func dashboardRepo(name string, r config.Repo) coordinator.DashboardRepo {
	base := "https://github.com"
	if r.Platform == "gitlab" {
		base = r.BaseURL
		if base == "" {
			base = "https://gitlab.com"
		}
	}
	u, err := url.Parse(base)
	link := ""
	if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + name
		link = u.String()
	}
	tags := r.WorkerTags
	if tags == nil {
		tags = []string{}
	}
	return coordinator.DashboardRepo{Name: name, URL: link, Platform: r.Platform, Enabled: r.Enabled, CIRequired: r.CIRequired, AcceptanceRequired: r.AcceptanceRequired, AutoMerge: r.AutoMerge, WorkerTags: tags}
}

func (a *platformAdmin) DashboardAction(ctx context.Context, input coordinator.DashboardRequest) (any, error) {
	if a.workers == nil {
		return nil, fmt.Errorf("worker service unavailable")
	}
	var err error
	switch input.Action {
	case "invite":
		return a.workers.Invite(ctx, input.Name)
	case "events":
		return a.workers.TaskEvents(ctx, input.Name)
	case "drain":
		err = a.workers.SetWorkerPaused(ctx, input.Name, true)
	case "resume":
		err = a.workers.SetWorkerPaused(ctx, input.Name, false)
	case "remove":
		err = a.workers.RemoveWorker(ctx, input.Name)
	default:
		return nil, coordinator.ErrInvalid
	}
	return map[string]bool{"ok": err == nil}, err
}
