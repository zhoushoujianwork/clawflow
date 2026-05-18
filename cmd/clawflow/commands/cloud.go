package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	rootmod "github.com/zhoushoujianwork/clawflow"
	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/cloud/auth"
	"github.com/zhoushoujianwork/clawflow/internal/cloud/chat"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// NewCloudCmd contains cloud-config migration and authentication commands.
func NewCloudCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud",
		Short: "Manage ClawFlow SaaS connection",
		Long: `Manage the ClawFlow SaaS connection used by registered workers.

Gist sync remains available under 'clawflow sync' as a legacy migration path.`,
	}
	cmd.AddCommand(newCloudLoginCmd())
	cmd.AddCommand(newCloudServeCmd())
	cmd.AddCommand(newCloudStatusCmd())
	cmd.AddCommand(newCloudImportCmd())
	return cmd
}

func newCloudLoginCmd() *cobra.Command {
	var baseURL string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate this machine against ClawFlow cloud (GitHub device flow)",
		Long: `Run a GitHub App device-flow login against the configured cloud URL.

The cloud prints a one-time user code; open the verification URL in any
browser, enter the code, approve the App, and the CLI saves the issued
personal API token to ~/.clawflow/config/credentials.yaml.

A previous login on this machine is overwritten. Use --url to override the
configured cloud URL.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			cfg := cloud.FromCredentials(creds)
			if baseURL != "" {
				cfg.BaseURL = strings.TrimRight(baseURL, "/")
			}
			if cfg.BaseURL == "" {
				cfg.BaseURL = cloud.DefaultBaseURL
			}

			token, login, err := runDeviceLogin(cmd.Context(), cfg.BaseURL)
			if err != nil {
				return err
			}
			cfg.AccessToken = token
			if err := config.SaveCredentials(cfg.ApplyToCredentials(creds)); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "\nLogged in as %s at %s\n", login, cfg.BaseURL)
			return nil
		},
	}
	cmd.Flags().StringVar(&baseURL, "url", "", "Cloud URL override (default: existing or "+cloud.DefaultBaseURL+")")
	return cmd
}

// runDeviceLogin drives the cloud's /api/v1/auth/device flow end-to-end.
// Returns the issued personal token and the user's github login, or an
// error explaining why the flow could not complete.
func runDeviceLogin(ctx context.Context, baseURL string) (token, login string, err error) {
	start, err := postCloudJSON(ctx, baseURL+"/api/v1/auth/device", nil)
	if err != nil {
		return "", "", fmt.Errorf("start device flow: %w", err)
	}
	userCode, _ := start["user_code"].(string)
	verifyURL, _ := start["verification_uri"].(string)
	interval, _ := start["interval"].(float64)
	if interval < 1 {
		interval = 5
	}
	if userCode == "" || verifyURL == "" {
		return "", "", fmt.Errorf("cloud device response missing user_code / verification_uri")
	}

	fmt.Fprintf(os.Stderr, "\nTo authorize this machine, open:\n  %s\nAnd enter the code:\n  %s\n", verifyURL, userCode)
	openBrowser(verifyURL)

	pollInterval := time.Duration(interval) * time.Second
	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(pollInterval):
		}
		poll, err := postCloudJSON(ctx, baseURL+"/api/v1/auth/device/poll",
			map[string]string{"user_code": userCode})
		if err != nil {
			return "", "", fmt.Errorf("poll device flow: %w", err)
		}
		status, _ := poll["status"].(string)
		switch status {
		case "pending":
			continue
		case "slow_down":
			pollInterval += time.Second
			continue
		case "expired":
			return "", "", fmt.Errorf("device code expired; run `clawflow cloud login` again")
		case "ok":
			tok, _ := poll["token"].(string)
			user, _ := poll["user"].(map[string]any)
			lg := ""
			if user != nil {
				lg, _ = user["login"].(string)
			}
			if tok == "" {
				return "", "", fmt.Errorf("cloud returned status=ok with empty token")
			}
			return tok, lg, nil
		default:
			return "", "", fmt.Errorf("unexpected device poll status %q", status)
		}
	}
}

// postCloudJSON POSTs a JSON body to url and returns the parsed JSON
// response. nil body sends an empty POST. Status codes 2xx are treated as
// success; 4xx/5xx return an error including the response body for
// debuggability.
func postCloudJSON(ctx context.Context, url string, body any) (map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode JSON: %w (body=%q)", err, string(raw))
	}
	return out, nil
}


func newCloudStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local cloud connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cloud.LoadConfig()
			if err != nil {
				return err
			}
			fmt.Printf("cloud_url: %s\n", cfg.BaseURL)
			fmt.Printf("access_token_configured: %t\n", cfg.AccessToken != "")
			fmt.Printf("machine_id: %s\n", cfg.MachineID)
			fmt.Printf("worker_id: %s\n", cfg.WorkerID)
			fmt.Printf("worker_token_configured: %t\n", cfg.WorkerToken != "")
			return nil
		},
	}
}

func newCloudServeCmd() *cobra.Command {
	var (
		host       string
		port       int
		storeFlag  string
		publicURL  string
		appID      int64
		appSlug    string
		clientID   string
		clientSec  string
		sessionKey string
		noAuth     bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the ClawFlow cloud API",
		Long: `Run the ClawFlow cloud API server.

In production, pass GitHub App OAuth credentials so user identity and CLI
device-flow login work. Required flags (or matching env vars) for auth mode:

  --github-app-id              CLAWFLOW_GITHUB_APP_ID
  --github-app-slug            CLAWFLOW_GITHUB_APP_SLUG
  --github-app-client-id       CLAWFLOW_GITHUB_APP_CLIENT_ID
  --github-app-client-secret   CLAWFLOW_GITHUB_APP_CLIENT_SECRET
  --session-key                CLAWFLOW_SESSION_KEY (>=32 random bytes, hex/ascii)
  --public-url                 CLAWFLOW_PUBLIC_URL  (e.g. https://clawflow.daboluo.cc)

For local development without GitHub credentials, pass --no-auth. Worker
endpoints become open and cloud-config endpoints accept any non-empty
Bearer token. Never run --no-auth in production.

Store backends:

  --store memory                          in-memory (default; lost on exit)
  --store sqlite:///path/to/state.db      persistent SQLite file
  --store sqlite://:memory:               ephemeral SQLite (for testing)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openCloudStore(storeFlag)
			if err != nil {
				return err
			}

			var authH cloud.AuthHandler
			if !noAuth {
				appID = orEnvInt(appID, "CLAWFLOW_GITHUB_APP_ID")
				appSlug = orEnv(appSlug, "CLAWFLOW_GITHUB_APP_SLUG")
				clientID = orEnv(clientID, "CLAWFLOW_GITHUB_APP_CLIENT_ID")
				clientSec = orEnv(clientSec, "CLAWFLOW_GITHUB_APP_CLIENT_SECRET")
				sessionKey = orEnv(sessionKey, "CLAWFLOW_SESSION_KEY")
				publicURL = orEnv(publicURL, "CLAWFLOW_PUBLIC_URL")

				if missing := firstEmpty([2]string{"--github-app-client-id", clientID},
					[2]string{"--github-app-client-secret", clientSec},
					[2]string{"--session-key", sessionKey},
					[2]string{"--public-url", publicURL}); missing != "" {
					return fmt.Errorf("%s is required (set the flag, the matching env var, or pass --no-auth for local dev)", missing)
				}
				if len(sessionKey) < 32 {
					return fmt.Errorf("--session-key must be at least 32 bytes")
				}
				authH = auth.NewHandler(store, auth.Config{
					AppID:        appID,
					AppSlug:      appSlug,
					ClientID:     clientID,
					ClientSecret: clientSec,
					PublicURL:    publicURL,
					SessionKey:   []byte(sessionKey),
					SessionTTL:   30 * 24 * time.Hour,
					CookieSecure: strings.HasPrefix(strings.ToLower(publicURL), "https://"),
				})
			}

			// Cloud-side chat: opt-in. Requires Anthropic API key (so the
			// cloud server can spawn `claude -p`); the App private-key path
			// is optional but private-repo clones won't work without it.
			// Reject placeholder values so an unfilled cloud.env template
			// doesn't accidentally enable chat in a half-configured state.
			var extras []cloud.RouteMounter
			anthropicKey := strings.TrimSpace(os.Getenv("CLAWFLOW_ANTHROPIC_API_KEY"))
			if strings.HasPrefix(anthropicKey, "__REPLACE") {
				anthropicKey = ""
			}
			chatMode := "chat=OFF"
			if anthropicKey != "" && authH != nil {
				chatCfg := chat.Config{
					AnthropicAPIKey:         anthropicKey,
					GitHubAppID:             appID,
					GitHubAppPrivateKeyPath: os.Getenv("CLAWFLOW_GITHUB_APP_PRIVATE_KEY_PATH"),
					ClonesDir:               orEnv(os.Getenv("CLAWFLOW_CHAT_CLONES_DIR"), ""),
					Store:                   store,
				}
				chatH, err := chat.NewHandler(chatCfg, authH)
				if err != nil {
					return fmt.Errorf("chat handler: %w", err)
				}
				extras = append(extras, chatH)
				chatMode = "chat=ON"
			}

			// Load embedded operators so /api/cloud/operators and the webhook
			// handler see them. Don't go through loadRegistry — it also probes
			// ~/.clawflow/skills/ which is irrelevant for the cloud server
			// (and fails with EPERM under the unprivileged systemd user).
			reg := operator.NewRegistry()
			if err := reg.LoadEmbedded(rootmod.EmbeddedSkills, "skills"); err != nil {
				fmt.Fprintf(os.Stderr, "warn: embedded operators load: %v\n", err)
			}

			addr := fmt.Sprintf("%s:%d", host, port)
			mode := "auth=github-app"
			if noAuth {
				mode = "auth=NONE (dev)"
			}
			fmt.Fprintf(os.Stderr, "ClawFlow cloud API listening on http://%s (store: %s, %s, %s)\n",
				addr, storeFlag, mode, chatMode)
			return http.ListenAndServe(addr, cloud.NewServerWithExtras(store, reg, authH, extras))
		},
	}
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "Host to bind")
	cmd.Flags().IntVar(&port, "port", 8790, "Port to bind")
	cmd.Flags().StringVar(&storeFlag, "store", "memory",
		`Store backend: "memory" (default) or "sqlite:///path/to/file.db"`)

	cmd.Flags().StringVar(&publicURL, "public-url", "",
		"Externally-visible base URL (e.g. https://clawflow.daboluo.cc); env CLAWFLOW_PUBLIC_URL")
	cmd.Flags().Int64Var(&appID, "github-app-id", 0, "GitHub App ID; env CLAWFLOW_GITHUB_APP_ID")
	cmd.Flags().StringVar(&appSlug, "github-app-slug", "",
		"GitHub App slug (URL fragment); env CLAWFLOW_GITHUB_APP_SLUG")
	cmd.Flags().StringVar(&clientID, "github-app-client-id", "",
		"GitHub App OAuth client ID; env CLAWFLOW_GITHUB_APP_CLIENT_ID")
	cmd.Flags().StringVar(&clientSec, "github-app-client-secret", "",
		"GitHub App OAuth client secret; env CLAWFLOW_GITHUB_APP_CLIENT_SECRET")
	cmd.Flags().StringVar(&sessionKey, "session-key", "",
		"Random secret used to sign session/state cookies (>=32 bytes); env CLAWFLOW_SESSION_KEY")
	cmd.Flags().BoolVar(&noAuth, "no-auth", false,
		"Disable GitHub App auth (DEV ONLY — never use in production)")
	return cmd
}

// orEnv returns flag if non-empty, otherwise os.Getenv(envKey).
func orEnv(flag, envKey string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv(envKey)
}

// orEnvInt is the int64 analogue. Empty / unparseable env yields 0.
func orEnvInt(flag int64, envKey string) int64 {
	if flag != 0 {
		return flag
	}
	v := os.Getenv(envKey)
	if v == "" {
		return 0
	}
	var n int64
	_, _ = fmt.Sscanf(v, "%d", &n)
	return n
}

// firstEmpty returns the first flag name whose value is empty, or "" if all
// values are populated.
func firstEmpty(pairs ...[2]string) string {
	for _, p := range pairs {
		if p[1] == "" {
			return p[0]
		}
	}
	return ""
}

// cloudImporter is the narrow subset of *cloud.Client used by
// importConfigToCloud. Declared as an interface so the helper can be
// unit-tested against an in-memory fake without standing up an HTTP
// server. *cloud.Client satisfies this implicitly.
type cloudImporter interface {
	GetCloudConfig(ctx context.Context) (*cloud.CloudConfigSummary, error)
	CreateProject(ctx context.Context, req cloud.CreateProjectRequest) (*cloud.Project, error)
	CreateRepo(ctx context.Context, req cloud.CreateRepoRequest) (*cloud.Repo, error)
}

// importSummary tracks counts and progress lines emitted by
// importConfigToCloud. Tests inspect Counts directly; the CLI writes
// the Lines slice to stderr in order.
type importSummary struct {
	ProjectsImported int
	ReposImported    int
	Skipped          int
	Lines            []string
}

// importConfigToCloud pushes local projects and repos into the cloud,
// skipping entries whose name already exists in the cloud snapshot.
// The function is intentionally I/O-free apart from the client calls —
// it takes the local project list and repo map as inputs so tests can
// supply fixture data without touching the filesystem.
//
// projectAssoc maps a local repo name ("owner/repo") to the local
// project name it belongs to, or "" when unassociated. The local schema
// stores this relationship on the project side (Project.Repos []string),
// so callers compute the inverse map before invoking this helper.
//
// When dryRun is true no API calls are made; the function only walks
// the inputs and emits the would-do progress lines.
func importConfigToCloud(
	ctx context.Context,
	c cloudImporter,
	localProjects []*project.Project,
	localRepos map[string]config.Repo,
	projectAssoc map[string]string,
	dryRun bool,
) (importSummary, error) {
	var sum importSummary

	// Build name-keyed sets from the existing cloud snapshot so we can
	// skip duplicates. Project IDs returned by the cloud are recorded so
	// repos we're about to create can reference them by ID.
	existingProjectsByName := map[string]string{} // name -> id
	existingRepoNames := map[string]struct{}{}
	if !dryRun {
		snap, err := c.GetCloudConfig(ctx)
		if err != nil {
			return sum, fmt.Errorf("fetch cloud config: %w", err)
		}
		for _, p := range snap.Projects {
			if p == nil {
				continue
			}
			existingProjectsByName[p.Name] = p.ID
		}
		for _, r := range snap.Repos {
			if r == nil {
				continue
			}
			existingRepoNames[r.Name] = struct{}{}
		}
	}

	// Sort projects by name for stable progress output.
	projectsSorted := make([]*project.Project, 0, len(localProjects))
	for _, p := range localProjects {
		if p == nil {
			continue
		}
		projectsSorted = append(projectsSorted, p)
	}
	sort.Slice(projectsSorted, func(i, j int) bool {
		return projectsSorted[i].Name < projectsSorted[j].Name
	})

	// Phase 1: projects. Build localProjectName -> cloud project id map
	// so repo creation in phase 2 can attach project_id.
	projectIDByLocalName := map[string]string{}
	for name, id := range existingProjectsByName {
		projectIDByLocalName[name] = id
	}

	totalProjects := len(projectsSorted)
	for i, p := range projectsSorted {
		idx := i + 1
		if id, ok := existingProjectsByName[p.Name]; ok {
			sum.Skipped++
			sum.Lines = append(sum.Lines,
				fmt.Sprintf("[%d/%d] project %q — already on cloud (%s), skipped", idx, totalProjects, p.Name, id))
			continue
		}
		if dryRun {
			sum.ProjectsImported++
			sum.Lines = append(sum.Lines,
				fmt.Sprintf("[%d/%d] project %q → would create", idx, totalProjects, p.Name))
			projectIDByLocalName[p.Name] = "" // dry-run repos will see empty project_id
			continue
		}
		created, err := c.CreateProject(ctx, cloud.CreateProjectRequest{
			Name: p.Name,
		})
		if err != nil {
			return sum, fmt.Errorf("create project %q: %w", p.Name, err)
		}
		sum.ProjectsImported++
		projectIDByLocalName[p.Name] = created.ID
		sum.Lines = append(sum.Lines,
			fmt.Sprintf("[%d/%d] project %q → %s", idx, totalProjects, p.Name, created.ID))
	}

	// Sort repos by name for stable output.
	repoNames := make([]string, 0, len(localRepos))
	for name := range localRepos {
		repoNames = append(repoNames, name)
	}
	sort.Strings(repoNames)

	totalRepos := len(repoNames)
	for i, name := range repoNames {
		idx := i + 1
		repo := localRepos[name]
		if _, ok := existingRepoNames[name]; ok {
			sum.Skipped++
			sum.Lines = append(sum.Lines,
				fmt.Sprintf("[%d/%d] repo %q — already on cloud, skipped", idx, totalRepos, name))
			continue
		}
		platform := repo.Platform
		if platform == "" {
			platform = "github"
		}
		projectID := projectIDByLocalName[projectAssoc[name]]
		if dryRun {
			sum.ReposImported++
			suffix := ""
			if projectID != "" {
				suffix = fmt.Sprintf(" (project=%s)", projectID)
			}
			sum.Lines = append(sum.Lines,
				fmt.Sprintf("[%d/%d] repo %q → would create%s", idx, totalRepos, name, suffix))
			continue
		}
		created, err := c.CreateRepo(ctx, cloud.CreateRepoRequest{
			Name:       name,
			Platform:   platform,
			BaseBranch: repo.BaseBranch,
			ProjectID:  projectID,
		})
		if err != nil {
			return sum, fmt.Errorf("create repo %q: %w", name, err)
		}
		sum.ReposImported++
		suffix := ""
		if created.ProjectID != "" {
			suffix = fmt.Sprintf(" (project=%s)", created.ProjectID)
		}
		sum.Lines = append(sum.Lines,
			fmt.Sprintf("[%d/%d] repo %q → %s%s", idx, totalRepos, name, created.ID, suffix))
	}

	return sum, nil
}

// buildProjectAssoc inverts the project.Repos []string membership lists
// into a flat "repo name -> project name" lookup. When a repo is listed
// under multiple projects (shouldn't happen given AddRepo's one-project
// guard, but possible from manual yaml edits), the first project wins
// by sort order — deterministic so import output is reproducible.
func buildProjectAssoc(projects []*project.Project) map[string]string {
	out := map[string]string{}
	// Sort by name so iteration order is stable.
	sorted := make([]*project.Project, 0, len(projects))
	for _, p := range projects {
		if p == nil {
			continue
		}
		sorted = append(sorted, p)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, p := range sorted {
		for _, repo := range p.Repos {
			if _, ok := out[repo]; ok {
				continue
			}
			out[repo] = p.Name
		}
	}
	return out
}

func newCloudImportCmd() *cobra.Command {
	var (
		dryRun   bool
		cloudURL string
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Bulk-upload local config.yaml (projects + repos) into the cloud",
		Long: `One-shot migration that pushes every project under ~/.clawflow/projects/
and every entry in ~/.clawflow/config/config.yaml's repos map into the cloud.

The local YAML files are left intact — worker-specific fields like
local_path stay on this machine. Entries whose name already exists on the
cloud are skipped, so the command is safe to re-run.

Machine bindings (which machine handles which repo) are NOT imported.
They are per-machine and need user input; configure them via the cloud
dashboard once the projects and repos land.

Use --dry-run to preview without making API calls. Use --cloud-url to
target a non-default cloud (typically for testing).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			cfg := cloud.FromCredentials(creds)
			if cloudURL != "" {
				cfg.BaseURL = strings.TrimRight(cloudURL, "/")
			}
			if cfg.AccessToken == "" {
				fmt.Fprintln(os.Stderr, "run 'clawflow cloud login' first")
				return fmt.Errorf("not logged in")
			}

			localCfg, err := config.Load()
			if err != nil {
				return err
			}
			localProjects, err := project.List()
			if err != nil {
				return fmt.Errorf("list local projects: %w", err)
			}

			if len(localCfg.Repos) == 0 && len(localProjects) == 0 {
				fmt.Fprintln(os.Stderr, "nothing to import")
				return nil
			}

			assoc := buildProjectAssoc(localProjects)

			var importer cloudImporter
			if !dryRun {
				client, err := cloud.NewClient(cfg)
				if err != nil {
					return err
				}
				importer = client
			}

			sum, err := importConfigToCloud(ctx, importer, localProjects, localCfg.Repos, assoc, dryRun)
			for _, line := range sum.Lines {
				fmt.Fprintln(os.Stderr, line)
			}
			if err != nil {
				return err
			}

			prefix := ""
			if dryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(os.Stderr, "%simported %d projects + %d repos; skipped %d duplicates\n",
				prefix, sum.ProjectsImported, sum.ReposImported, sum.Skipped)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without making API calls")
	cmd.Flags().StringVar(&cloudURL, "cloud-url", "", "Cloud URL override (default: configured value or "+cloud.DefaultBaseURL+")")
	return cmd
}

// openCloudStore returns a Store for the given flag value.
func openCloudStore(storeFlag string) (cloud.Store, error) {
	switch {
	case storeFlag == "" || storeFlag == "memory":
		return cloud.NewMemoryStore(), nil
	case strings.HasPrefix(storeFlag, "sqlite://"):
		path := strings.TrimPrefix(storeFlag, "sqlite://")
		return cloud.NewSQLiteStore(path)
	default:
		return nil, fmt.Errorf("unknown store %q: use \"memory\" or \"sqlite:///path/to/file.db\"", storeFlag)
	}
}
