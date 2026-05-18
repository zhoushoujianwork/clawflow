package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/cloud/auth"
	"github.com/zhoushoujianwork/clawflow/internal/config"
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

			addr := fmt.Sprintf("%s:%d", host, port)
			mode := "auth=github-app"
			if noAuth {
				mode = "auth=NONE (dev)"
			}
			fmt.Fprintf(os.Stderr, "ClawFlow cloud API listening on http://%s (store: %s, %s)\n", addr, storeFlag, mode)
			return http.ListenAndServe(addr, cloud.NewServerWithAuth(store, nil, authH))
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
