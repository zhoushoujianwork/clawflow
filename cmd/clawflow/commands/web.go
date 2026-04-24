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

			addr := fmt.Sprintf("%s:%d", host, port)
			url := fmt.Sprintf("http://%s/", addr)

			mux := http.NewServeMux()
			mux.Handle("/", http.FileServer(http.Dir(snapshot.DashboardRoot())))

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

	return fs.WalkDir(rootmod.EmbeddedDashboard, "web/dashboard", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Strip the "web/dashboard/" prefix so files land at the root of ~/.clawflow/dashboard/.
		rel := strings.TrimPrefix(path, "web/dashboard")
		rel = strings.TrimPrefix(rel, "/")
		dest := filepath.Join(root, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := rootmod.EmbeddedDashboard.ReadFile(path)
		if err != nil {
			return err
		}
		// Don't trample a user-edited file. If it already exists with the
		// same content, skip; if it differs, also skip and trust the user.
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
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
