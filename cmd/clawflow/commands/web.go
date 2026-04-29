package commands

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	rootmod "github.com/zhoushoujianwork/clawflow"
	"github.com/zhoushoujianwork/clawflow/internal/api"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	ptyserver "github.com/zhoushoujianwork/clawflow/internal/pty"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// NewWebCmd exposes `clawflow web`, a zero-dependency local dashboard.
// The data it renders is whatever was persisted to ~/.clawflow/dashboard/
// by previous `clawflow run` invocations — this command does not fetch
// anything from the VCS itself.
func NewWebCmd() *cobra.Command {
	var (
		port     int
		host     string
		openFlag bool
	)
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Serve the local ClawFlow dashboard on localhost",
		Long: `Starts a tiny static file server at http://<host>:<port>/ backed by
~/.clawflow/dashboard/. The dashboard renders snapshots written by
previous 'clawflow run' invocations (repos.json, operators.json,
runs.json, plus per-run events.jsonl for replay). No VCS calls happen
here — run 'clawflow run' first if you want fresh data.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureDashboardExtracted(); err != nil {
				return fmt.Errorf("extract dashboard assets: %w", err)
			}

			// Wire version info for the API
			api.VersionInfo.Current = Version
			api.VersionInfo.Fetch = FetchLatestTag
			api.VersionInfo.IsNewer = IsNewerVersion

			// Refresh data/repos.json from the live config on startup
			// so the dashboard never serves a stale snapshot left over
			// from the last `clawflow run`. Best-effort; if config fails
			// to load we let the existing snapshot stand.
			if cfg, err := config.Load(); err == nil {
				_ = snapshot.WriteRepos(cfg)
			}

			// Reconcile any "running" entries left over from an
			// interrupted `clawflow run` (Ctrl-C, SIGTERM, machine
			// sleep), then regenerate runs.json from the cleaned tree.
			// Without this, restarting the dashboard would still show a
			// frozen "running" row pointing at a long-dead process —
			// and any deleted run dirs would linger in the index until
			// the next `clawflow run` rewrote it. Both cases are common
			// during local dev; doing it on startup means a restart is
			// the obvious fix the user already reaches for.
			//
			// 60 min staleAfter mirrors the default per-operator
			// timeout — anything older than that is definitively dead
			// regardless of events.jsonl activity.
			if n, err := snapshot.ReconcileStaleRuns(60 * time.Minute); err == nil && n > 0 {
				fmt.Fprintf(os.Stderr, "✓ reconciled %d stale run(s) on startup\n", n)
			}
			if _, err := snapshot.WriteRunsIndex(50); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ snapshot runs index on startup: %v\n", err)
			}

			// Background reconcile: every minute, sweep for orphaned
			// running entries (events.jsonl gone quiet for >2 min) and
			// regenerate runs.json so the dashboard's poll picks up the
			// flip. Without this loop, an interrupt taken AFTER web
			// starts wouldn't be reflected until the next `clawflow run`
			// or web restart.
			go func() {
				tick := time.NewTicker(time.Minute)
				defer tick.Stop()
				for range tick.C {
					n, err := snapshot.ReconcileStaleRuns(60 * time.Minute)
					if err != nil {
						continue
					}
					if n > 0 {
						fmt.Fprintf(os.Stderr, "✓ reconciled %d orphaned run(s)\n", n)
						_, _ = snapshot.WriteRunsIndex(50)
					}
				}
			}()

			addr := fmt.Sprintf("%s:%d", host, port)
			url := fmt.Sprintf("http://%s/", addr)

			root := snapshot.DashboardRoot()
			fsrv := http.FileServer(http.Dir(root))
			mux := http.NewServeMux()
			mux.HandleFunc("/ws/pty", ptyserver.HandlePTY)
			mux.HandleFunc("/api/labels/add", api.HandleAddLabel)
			mux.HandleFunc("/api/labels/remove", api.HandleRemoveLabel)
			mux.HandleFunc("/api/run", api.HandleRun)
			mux.HandleFunc("/api/run/status", api.HandleRunStatus)
			mux.HandleFunc("/api/version", api.HandleVersion)
			mux.HandleFunc("/api/update", api.HandleUpdate)
			mux.HandleFunc("/api/repo/config", api.HandleRepoConfig)
			mux.HandleFunc("/api/repo/clone", api.HandleClone)
			mux.HandleFunc("/api/settings", api.HandleGetSettings)
			mux.HandleFunc("/api/settings/claude", api.HandleUpdateClaudeSettings)
			mux.HandleFunc("/api/settings/tokens", api.HandleUpdateTokens)
			mux.HandleFunc("/api/settings/global", api.HandleUpdateGlobalSettings)
			mux.HandleFunc("/api/settings/claude/test", api.HandleTestClaude)
			mux.HandleFunc("/api/settings/reveal", api.HandleRevealSecret)
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				// SPA fallback: if the requested path maps to a real file
				// (or lives under /data/ or /assets/ which tanstack-router
				// wouldn't own anyway), serve it. Otherwise hand back
				// index.html so the client-side router can resolve
				// /dashboard, /repos, /runs/… on hard-refresh.
				reqPath := strings.TrimPrefix(r.URL.Path, "/")
				if reqPath == "" {
					fsrv.ServeHTTP(w, r)
					return
				}
				if _, err := os.Stat(filepath.Join(root, reqPath)); err == nil {
					fsrv.ServeHTTP(w, r)
					return
				}
				http.ServeFile(w, r, filepath.Join(root, "index.html"))
			})

			srv := &http.Server{
				Addr:              addr,
				Handler:           mux,
				ReadHeaderTimeout: 5 * time.Second,
			}

			fmt.Printf("ClawFlow dashboard → %s\n", url)
			fmt.Printf("  data dir: %s\n", snapshot.DataDir())
			fmt.Printf("  Ctrl-C to stop.\n\n")

			if openFlag {
				go openBrowser(url)
			}
			return srv.ListenAndServe()
		},
	}
	cmd.Flags().IntVar(&port, "port", 8080, "TCP port to bind")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "host/IP to bind — 127.0.0.1 by default so the dashboard stays off the LAN")
	cmd.Flags().BoolVar(&openFlag, "open", false, "open the dashboard in your default browser")
	return cmd
}

// ensureDashboardExtracted materializes the embedded dashboard SPA into
// ~/.clawflow/dashboard/. If an existing file has the same content we skip
// it; if the user hand-edited it we leave their version alone.
func ensureDashboardExtracted() error {
	root := snapshot.DashboardRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}

	return fs.WalkDir(rootmod.EmbeddedDashboard, "web/dist", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Strip the "web/dist/" prefix so files land at the root of ~/.clawflow/dashboard/.
		rel := strings.TrimPrefix(path, "web/dist")
		rel = strings.TrimPrefix(rel, "/")
		dest := filepath.Join(root, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := rootmod.EmbeddedDashboard.ReadFile(path)
		if err != nil {
			return err
		}
		// Overwrite unconditionally. Upgrades need fresh dashboard bundles;
		// if a user wants to hand-edit they should fork the repo's web/
		// directory and build their own rather than patching the extracted
		// copy.
		return os.WriteFile(dest, data, 0o644)
	})
}

// openBrowser opens url in the user's default browser. Silent on failure;
// users can always copy the URL from stdout.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	_ = cmd.Start()
}
