package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"gopkg.in/yaml.v3"
)

const githubRepo = "zhoushoujianwork/clawflow"

// installRecord mirrors ~/.clawflow/config/install.yaml. Written by the
// installer; read by `clawflow update` so a dev build knows where the
// original source tree lives for --from-source.
type installRecord struct {
	RepoDir     string `yaml:"repo_dir"`
	InstalledAt string `yaml:"installed_at"`
}

func NewUpdateCmd() *cobra.Command {
	var fromSource, release, allowDowngrade bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update clawflow within its installed channel",
		Long: `Updates the tracked source branch or latest release and replaces the currently
running clawflow binary in place — wherever it lives (/usr/local/bin,
~/.local/bin, ~/.clawflow/bin, ...) — so the updated binary stays on your
PATH and no stale second copy is left behind. Built-in operators ship inside
the binary, so there is nothing else to sync. User operators in
~/.clawflow/skills/ are untouched.

Source builds track their stamped branch (default main) using a verified temporary worktree.
Use --from-source to switch to source, --release to switch to releases.
Older or unknown releases also require --allow-downgrade.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromSource && release {
				return fmt.Errorf("--from-source and --release are mutually exclusive")
			}
			if fromSource || (buildChannel() == "source" && !release) {
				repo, err := sourceRepository()
				if err != nil {
					return err
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), selfUpgradeTimeout)
				defer cancel()
				u := newSelfUpgrader()
				result := u.Upgrade(ctx, repo)
				if result.Result == selfUpgradeFailed {
					return fmt.Errorf("source upgrade: %s", result.Reason)
				}
				fmt.Println(result.Result + ": " + result.Reason)
				return nil
			}
			return downloadReleaseBinary(release, allowDowngrade)
		},
	}

	cmd.Flags().BoolVar(&fromSource, "from-source", false, "Rebuild binary from local source instead of downloading")
	cmd.Flags().BoolVar(&release, "release", false, "Explicitly switch to release assets")
	cmd.Flags().BoolVar(&allowDowngrade, "allow-downgrade", false, "Allow an older or unverified release (source also requires --release)")
	return cmd
}

// loadInstallRecord reads ~/.clawflow/config/install.yaml. Returns nil, nil
// when the file doesn't exist — that's a valid state for curl-installed users.
func loadInstallRecord() (*installRecord, error) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".clawflow", "config", "install.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec installRecord
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// downloadLatestBinary fetches the latest release binary from GitHub.
func downloadLatestBinary() error { return downloadReleaseBinary(false, false) }

func downloadReleaseBinary(explicitRelease, allowDowngrade bool) error {
	if buildChannel() == "source" && !explicitRelease {
		return fmt.Errorf("source build requires --release to switch channels")
	}
	tag, assetURL, err := latestReleaseAsset()
	if err != nil {
		return err
	}

	if err := checkReleaseReplacement(Version, tag, buildChannel(), explicitRelease, allowDowngrade); err != nil {
		return err
	}
	if buildChannel() == "source" && !allowDowngrade {
		repo, err := sourceRepository()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := newSelfUpgrader().verifyReleaseAncestry(ctx, repo.LocalPath, tag); err != nil {
			return err
		}
	}
	if Version == tag {
		return nil
	}

	fmt.Printf("  [dl] downloading %s (%s)...\n", tag, assetURL)

	resp, err := http.Get(assetURL) //nolint:gosec
	if err != nil {
		return fmt.Errorf("HTTP get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return atomicReplace(binaryPath(), resp.Body)
}

// latestReleaseAsset finds the download URL for the current OS/arch binary
// in the latest release.
func latestReleaseAsset() (tag, url string, err error) {
	release, err := fetchLatestRelease(&http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return "", "", err
	}

	want := binaryAssetName()
	for _, a := range release.Assets {
		if a.Name == want || a.Name == "clawflow" {
			return release.TagName, a.BrowserDownloadURL, nil
		}
	}
	return "", "", fmt.Errorf("no binary asset found in release %s (looked for %q)", release.TagName, want)
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// fetchLatestRelease uses the same env-over-file credentials as other GitHub
// commands. Authentication is limited to this API request; asset downloads
// continue without a token because they can redirect to another host.
func fetchLatestRelease(client *http.Client) (githubRelease, error) {
	var release githubRelease
	creds, err := config.LoadCredentials()
	if err != nil {
		return release, fmt.Errorf("load GitHub credentials: %w", err)
	}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return release, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if creds.GHToken != "" {
		req.Header.Set("Authorization", "Bearer "+creds.GHToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return release, fmt.Errorf("fetch latest GitHub release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return release, fmt.Errorf("GitHub API rate limit exceeded (HTTP %d); configure a GitHub token with clawflow config set-token or GH_TOKEN, or retry after the quota resets", resp.StatusCode)
		}
		return release, fmt.Errorf("fetch latest GitHub release: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return release, fmt.Errorf("decode latest GitHub release: %w", err)
	}
	if release.TagName == "" {
		return release, fmt.Errorf("GitHub release response is missing tag_name")
	}
	return release, nil
}

func binaryAssetName() string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("clawflow_%s_%s.exe", runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("clawflow_%s_%s", runtime.GOOS, runtime.GOARCH)
}

// rebuildFromSource builds the binary from the local repo clone.
func rebuildFromSource(repoDir string) error {
	repoDir = expandHomeStr(repoDir)
	fmt.Printf("  [build] rebuilding from %s...\n", repoDir)

	ctx, cancel := context.WithTimeout(context.Background(), selfUpgradeTimeout)
	defer cancel()
	result := newSelfUpgrader().Upgrade(ctx, config.Repo{LocalPath: repoDir, BaseBranch: trackedBranch()})
	if result.Result == selfUpgradeFailed {
		return fmt.Errorf("source upgrade: %s", result.Reason)
	}
	return nil
}

// binaryPath returns the path of the binary that `clawflow update` should
// replace. It resolves the currently-running executable (following symlinks)
// so an update lands in the same location the user originally installed to —
// /usr/local/bin, ~/.local/bin, or ~/.clawflow/bin — keeping it on PATH and
// avoiding a second, stale copy. The legacy ~/.clawflow/bin/clawflow path is
// only used as a fallback when the executable path can't be resolved.
func binaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".clawflow", "bin", "clawflow")
	}
	return resolveBinaryPath(exe)
}

// resolveBinaryPath follows symlinks for exe so we replace the real binary,
// not a symlink that points at it (common when /usr/local/bin/clawflow is a
// link into ~/.clawflow/bin). Falls back to the raw path if EvalSymlinks fails.
func resolveBinaryPath(exe string) string {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// atomicReplace writes src to dest via a temp file, then renames — avoids
// leaving a half-written binary if the download is interrupted.
func atomicReplace(dest string, src io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		if os.IsPermission(err) {
			return permissionDeniedErr(dest, err)
		}
		return err
	}
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()

	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		if os.IsPermission(err) {
			return permissionDeniedErr(dest, err)
		}
		return err
	}
	if err := codesignIfDarwin(dest); err != nil {
		// Don't fail the update for a signing miss — user can still run the
		// binary if Gatekeeper happens to accept it as-is, and they always
		// have the manual `codesign --force --sign - <path>` escape hatch.
		fmt.Printf("  [warn] codesign step skipped: %v\n", err)
	}
	fmt.Printf("  [ok] binary updated → %s\n", dest)
	return nil
}

// permissionDeniedErr wraps a write-permission failure with an actionable
// hint. This is the common case when clawflow was installed into a
// system-owned directory like /usr/local/bin and the user runs `clawflow
// update` without elevated privileges.
func permissionDeniedErr(dest string, cause error) error {
	return fmt.Errorf("no write permission for %s (%w)\n"+
		"      clawflow lives in a directory that needs elevated privileges.\n"+
		"      Re-run with sudo, e.g.:\n"+
		"        sudo clawflow update", dest, cause)
}

// codesignIfDarwin ad-hoc signs `path` on macOS so Gatekeeper doesn't
// SIGKILL the freshly-downloaded binary on first launch. The
// com.apple.provenance attribute that downloads pick up causes
// `spctl --assess` to reject unsigned executables. An ad-hoc signature
// (`codesign --force --sign -`) marks the binary as locally trusted —
// good enough for personal-use binaries that aren't notarized.
//
// No-op on non-Darwin or when the codesign tool isn't on PATH (typical
// on Linux). A failed sign is not fatal — caller logs and moves on.
func codesignIfDarwin(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if _, err := exec.LookPath("codesign"); err != nil {
		return nil // codesign missing (e.g. CLT not installed); nothing to do
	}
	out, err := exec.Command("codesign", "--force", "--sign", "-", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign: %w\n%s", err, out)
	}
	return nil
}

// FetchLatestTag queries GitHub for the latest release tag. Returns empty
// string on any error or timeout (5s).
func FetchLatestTag() string {
	release, err := fetchLatestRelease(&http.Client{Timeout: 5 * time.Second})
	if err != nil {
		return ""
	}
	return release.TagName
}
