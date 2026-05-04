package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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
			// Refresh data/projects.json so the dashboard picks up any
			// project changes made via the CLI since the last web start.
			_ = snapshot.WriteProjects()

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
			// Regenerate usage.json with period data on startup so the
			// dashboard has fresh billing breakdowns without waiting for
			// the next `clawflow run`.
			if allEntries, err := snapshot.WriteRunsIndex(0); err == nil {
				billingDay := 1
				if cfg, err := config.Load(); err == nil {
					billingDay = cfg.Settings.BillingCycleDay
				}
				if err := snapshot.WriteUsageSummary(allEntries, billingDay); err != nil {
					fmt.Fprintf(os.Stderr, "⚠ snapshot usage summary on startup: %v\n", err)
				}
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

			// Periodic auto-runner: when settings.run_interval_minutes > 0,
			// fire `clawflow run` on a recurring tick. The ticker shares
			// the runActive mutex with the manual Run button (via
			// api.TriggerRun), so the two can never overlap. settings
			// are re-read every tick — bumping run_interval_minutes or
			// flipping run_paused via the dashboard takes effect on the
			// next tick without restarting web.
			go runPeriodicScheduler()

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
			mux.HandleFunc("/api/run/pause", api.HandleRunPause)
			mux.HandleFunc("/api/version", api.HandleVersion)
			mux.HandleFunc("/api/update", api.HandleUpdate)
			mux.HandleFunc("/api/repo/config", api.HandleRepoConfig)
			mux.HandleFunc("/api/repo/clone", api.HandleClone)
			mux.HandleFunc("/api/repos/list-remote", api.HandleListRemoteRepos)
			mux.HandleFunc("/api/repos/add-remote", api.HandleAddRemoteRepo)
			mux.HandleFunc("/api/settings", api.HandleGetSettings)
			mux.HandleFunc("/api/settings/claude", api.HandleUpdateClaudeSettings)
			mux.HandleFunc("/api/settings/tokens", api.HandleUpdateTokens)
			mux.HandleFunc("/api/settings/global", api.HandleUpdateGlobalSettings)
			mux.HandleFunc("/api/settings/claude/test", api.HandleTestClaude)
			mux.HandleFunc("/api/settings/reveal", api.HandleRevealSecret)
			mux.HandleFunc("/api/settings/verify-token", api.HandleVerifyToken)
			mux.HandleFunc("/api/browse-directory", api.HandleBrowseDirectory)
			mux.HandleFunc("/api/chat/spawn", api.HandleChatSpawn)
			mux.HandleFunc("/api/project/create", api.HandleProjectCreate)
			mux.HandleFunc("/api/project/delete", api.HandleProjectDelete)
			mux.HandleFunc("/api/project/add-repo", api.HandleProjectAddRepo)
			mux.HandleFunc("/api/project/remove-repo", api.HandleProjectRemoveRepo)
			mux.HandleFunc("/api/project/generate-context", api.HandleProjectGenerateContext)
			mux.HandleFunc("/api/project/generate-context/status", api.HandleProjectGenerateContextStatus)
			mux.HandleFunc("/api/project/automation", api.HandleProjectAutomation)
			mux.HandleFunc("/api/project/get", api.HandleProjectGet)
			mux.HandleFunc("/api/project/health-check/run", api.HandleProjectHealthCheckRun)
			mux.HandleFunc("/api/project/health-check/status", api.HandleProjectHealthCheckStatus)
			mux.HandleFunc("/api/project/health-check/apply", api.HandleProjectHealthCheckApply)
			mux.HandleFunc("/api/project/pm-runs", api.HandleProjectPMRuns)
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

			// Wire the dashboard's Upgrade button: after a successful
			// `clawflow update` the old binary is on disk, but this
			// running process is still the pre-update one. Respawn
			// spawns a detached helper that waits for our port to
			// free, then execs `clawflow web` again — picking up the
			// new binary. We then gracefully shut ourselves down.
			//
			// Helper failures must NOT take down the running web —
			// fall through silently and let the user restart manually.
			api.VersionInfo.Respawn = func() {
				self, err := os.Executable()
				if err != nil {
					fmt.Fprintf(os.Stderr, "respawn: locate self: %v\n", err)
					return
				}
				helper := exec.Command(self, "__respawn", "--addr", addr) //nolint:gosec
				helper.Stdin = nil
				helper.Stdout = os.Stdout
				helper.Stderr = os.Stderr
				// Setsid detaches the helper from our process group so
				// it survives our os.Exit and any SIGINT the user sends
				// to the terminal.
				helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				if err := helper.Start(); err != nil {
					fmt.Fprintf(os.Stderr, "respawn: start helper: %v\n", err)
					return
				}
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					_ = srv.Shutdown(ctx)
				}()
			}

			fmt.Printf("ClawFlow dashboard → %s\n", url)
			fmt.Printf("  data dir: %s\n", snapshot.DataDir())
			fmt.Printf("  Ctrl-C to stop.\n\n")

			if openFlag {
				go openBrowser(url)
			}
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 8090, "TCP port to bind")
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

// runPeriodicScheduler is the goroutine that fires `clawflow run` on a
// recurring interval set in settings.run_interval_minutes. It re-reads
// the config at the start of every tick so the user can change the
// interval / pause state from the dashboard without restarting web.
//
// The actual run is dispatched through api.TriggerRun, which shares
// the same in-memory mutex with the manual Run button — periodic and
// manual triggers can never overlap, and a long-running operator
// passes through quietly because TriggerRun returns false until the
// previous subprocess exits.
//
// Tick cadence: we drive a 30s coarse ticker and check elapsed time
// against the configured interval each tick. This way changes to
// run_interval_minutes take effect within at most 30s, without any
// fancy ticker reset logic. 30s is also fine-grained enough that the
// dashboard countdown (`next in M:SS`) never lags noticeably.
func runPeriodicScheduler() {
	const checkEvery = 30 * time.Second
	tick := time.NewTicker(checkEvery)
	defer tick.Stop()

	var lastFire time.Time
	for range tick.C {
		cfg, err := config.Load()
		if err != nil {
			api.SetSchedulerNextFire(time.Time{})
			continue
		}
		interval := cfg.Settings.RunIntervalMinutes
		if interval <= 0 {
			// Auto-run disabled — clear the published next-fire time
			// so the dashboard hides the countdown.
			api.SetSchedulerNextFire(time.Time{})
			lastFire = time.Time{}
			continue
		}
		intervalDur := time.Duration(interval) * time.Minute

		// Initialize lastFire on the first iteration after the
		// scheduler "turns on" so we don't fire immediately on web
		// startup (the user explicitly clicks Run if they want a kick
		// right after start).
		if lastFire.IsZero() {
			lastFire = time.Now()
		}
		nextFire := lastFire.Add(intervalDur)
		api.SetSchedulerNextFire(nextFire)

		if cfg.Settings.RunPaused {
			// Paused: keep the published next-fire time as-is so the
			// dashboard can render "paused · would have fired in 1:23".
			// Do NOT advance lastFire — once the user resumes, we
			// pick up exactly where we left off.
			continue
		}
		if time.Now().Before(nextFire) {
			continue
		}

		// Time to fire. TriggerRun returns false if the previous run
		// is still going — that's fine, we just skip and try again
		// next tick.
		if api.TriggerRun("", 0) {
			lastFire = time.Now()
			fmt.Fprintf(os.Stderr, "[scheduler] auto-fire at %s (next in %s)\n",
				lastFire.Format("15:04:05"), intervalDur)
		}
	}
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
