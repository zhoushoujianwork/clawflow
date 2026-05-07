package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
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
// The SPA bundle is served directly from the binary's embedded FS (no
// on-disk extraction); the JSON snapshots it renders live under
// ~/.clawflow/data/ and are written by `clawflow run` plus a few
// startup refreshes (repos/projects/operators) right here.
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
			// Migrate legacy ~/.clawflow/dashboard/data/ → ~/.clawflow/data/
			// for installs upgrading from the pre-split layout. No-op for
			// fresh installs.
			if moved, err := snapshot.MigrateLegacyDataDir(); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ migrate legacy data dir: %v\n", err)
			} else if moved {
				fmt.Fprintf(os.Stderr, "✓ migrated runtime data ~/.clawflow/dashboard/data → ~/.clawflow/data\n")
			}
			// Drop the old extracted SPA tree under ~/.clawflow/dashboard/
			// — we now serve directly from embed.FS, the directory is
			// purely cosmetic. Best-effort: a non-empty / unrecognized
			// dashboard dir is reported and skipped, never blocks startup.
			if err := snapshot.CleanupLegacyDashboardDir(); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ cleanup legacy dashboard dir: %v\n", err)
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
			// Refresh data/operators.json from embedded + user skills.
			// Operators are derived purely from on-disk skill files (no
			// run history involved) so the Operators page should never
			// be empty just because the user hasn't run a `clawflow run`
			// yet — write the snapshot eagerly here, same as repos /
			// projects above. Best-effort: a malformed user skill is
			// reported on stderr but doesn't block web startup.
			if reg, err := loadRegistry(); err == nil {
				if werr := snapshot.WriteOperators(reg); werr != nil {
					fmt.Fprintf(os.Stderr, "⚠ snapshot operators on startup: %v\n", werr)
				}
			} else {
				fmt.Fprintf(os.Stderr, "⚠ load operator registry on startup: %v\n", err)
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

			// Serve the SPA bundle straight from the embedded FS — no
			// disk extraction. fs.Sub strips the `web/dist/` prefix so
			// requests for `/assets/foo.js` map to `web/dist/assets/foo.js`
			// inside the embed.
			spaFS, err := fs.Sub(rootmod.EmbeddedDashboard, "web/dist")
			if err != nil {
				return fmt.Errorf("embedded SPA fs: %w", err)
			}
			fsrv := http.FileServer(http.FS(spaFS))
			// /data/ is served from a SEPARATE root (~/.clawflow/data/)
			// — runtime snapshots that live on disk and accumulate over
			// time. Mounted explicitly so the SPA fallback below never
			// tries to find data files inside the embedded SPA bundle.
			dataDir := snapshot.DataDir()
			if err := os.MkdirAll(dataDir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ mkdir data dir: %v\n", err)
			}
			dataFsrv := http.FileServer(http.Dir(dataDir))
			mux := http.NewServeMux()
			mux.Handle("/data/", http.StripPrefix("/data/", dataFsrv))
			mux.HandleFunc("/ws/pty", ptyserver.HandlePTY)
			mux.HandleFunc("/api/labels/add", api.HandleAddLabel)
			mux.HandleFunc("/api/labels/remove", api.HandleRemoveLabel)
			mux.HandleFunc("/api/run", api.HandleRun)
			mux.HandleFunc("/api/run/status", api.HandleRunStatus)
			mux.HandleFunc("/api/run/pause", api.HandleRunPause)
			mux.HandleFunc("/api/version", api.HandleVersion)
			mux.HandleFunc("/api/update", api.HandleUpdate)
			mux.HandleFunc("/api/repo/config", api.HandleRepoConfig)
			mux.HandleFunc("/api/repo/remove", api.HandleRepoRemove)
			mux.HandleFunc("/api/repo/clone", api.HandleClone)
			mux.HandleFunc("/api/repo/refresh-issues", api.HandleRepoRefreshIssues)
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
			mux.HandleFunc("/api/project/generate-deployment", api.HandleProjectGenerateDeployment)
			mux.HandleFunc("/api/project/generate-deployment/status", api.HandleProjectGenerateDeploymentStatus)
			mux.HandleFunc("/api/project/automation", api.HandleProjectAutomation)
			mux.HandleFunc("/api/project/get", api.HandleProjectGet)
			mux.HandleFunc("/api/project/health-check/run", api.HandleProjectHealthCheckRun)
			mux.HandleFunc("/api/project/health-check/status", api.HandleProjectHealthCheckStatus)
			mux.HandleFunc("/api/project/health-check/apply", api.HandleProjectHealthCheckApply)
			mux.HandleFunc("/api/project/pm-runs", api.HandleProjectPMRuns)
			mux.HandleFunc("/api/sync/status", api.HandleSyncStatus)
			mux.HandleFunc("/api/sync/push", api.HandleSyncPush)
			mux.HandleFunc("/api/sync/pull", api.HandleSyncPull)
			mux.HandleFunc("/api/login", api.HandleLogin)
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				// SPA fallback: if the requested path maps to a real
				// asset inside the embedded bundle (assets/, favicon.svg,
				// logo.svg, etc.) serve it from spaFS. Otherwise hand
				// back index.html so the client-side router can resolve
				// /dashboard, /repos, /runs/… on hard-refresh. /data/
				// is mounted separately above and never reaches here.
				reqPath := strings.TrimPrefix(r.URL.Path, "/")
				if reqPath == "" {
					fsrv.ServeHTTP(w, r)
					return
				}
				if f, err := spaFS.Open(reqPath); err == nil {
					_ = f.Close()
					fsrv.ServeHTTP(w, r)
					return
				}
				// index.html is small (under a kilobyte); reading it on
				// every SPA-route request is cheaper than a syscall.
				indexHTML, err := fs.ReadFile(spaFS, "index.html")
				if err != nil {
					http.Error(w, "index.html missing from embed", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				_, _ = w.Write(indexHTML)
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
