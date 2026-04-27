package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
	var fromSource bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the clawflow binary to the latest release",
		Long: `Fetches the latest release from GitHub and replaces the clawflow binary
at ~/.clawflow/bin/clawflow. Built-in operators ship inside the binary, so
there is nothing else to sync. User operators in ~/.clawflow/skills/ are
untouched.

Use --from-source to rebuild from a local repo clone instead (dev flow).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			record, _ := loadInstallRecord() // missing record is fine for curl-installed users

			if fromSource {
				if record == nil || record.RepoDir == "" {
					return fmt.Errorf("--from-source requires an install record with repo_dir; install via ./install.sh first")
				}
				return rebuildFromSource(record.RepoDir)
			}

			if err := downloadLatestBinary(); err != nil {
				if record != nil && record.RepoDir != "" {
					fmt.Printf("  [warn] download failed (%v), trying source rebuild...\n", err)
					return rebuildFromSource(record.RepoDir)
				}
				return fmt.Errorf("binary download failed: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&fromSource, "from-source", false, "Rebuild binary from local source instead of downloading")
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
func downloadLatestBinary() error {
	tag, assetURL, err := latestReleaseAsset()
	if err != nil {
		return err
	}

	// Short-circuit when the installed binary is already at (or ahead of) the
	// published release. A dev build (Version == "dev") parses to 0.0.0 and
	// therefore never matches — `clawflow update` on a dev build always
	// downloads, which is the intended way to swap a local build for the
	// released one.
	if !IsNewerVersion(Version, tag) {
		fmt.Printf("  [ok] already at %s — nothing to do\n", Version)
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
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	resp, err := http.Get(apiURL) //nolint:gosec
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
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

	version := "dev"
	if tagOut, err := exec.Command("git", "-C", repoDir, "describe", "--tags", "--abbrev=0").Output(); err == nil {
		version = strings.TrimSpace(string(tagOut))
	}
	ldflags := fmt.Sprintf("-s -w -X github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands.Version=%s", version)
	c := exec.Command("go", "build", "-ldflags", ldflags, "-o", binaryPath(), "./cmd/clawflow/")
	c.Dir = repoDir
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build: %w\n%s", err, out)
	}
	fmt.Printf("  [ok] binary rebuilt → %s\n", binaryPath())
	return nil
}

func binaryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "bin", "clawflow")
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

// IsNewerVersion reports whether `latest` is strictly newer than `current`,
// comparing major.minor.patch triples only. Leading "v" is optional; any
// pre-release or git-describe suffix (e.g. "-5-g1234abc", "-rc1") is stripped
// before comparison, so a dev build ahead of the tag is treated as equal
// to that tag rather than older. An unparseable input counts as zero — we'd
// rather stay silent than nag the user with bogus "new version" hints.
func IsNewerVersion(current, latest string) bool {
	c := parseSemverTriple(current)
	l := parseSemverTriple(latest)
	for i := range 3 {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseSemverTriple(s string) [3]int {
	s = strings.TrimPrefix(s, "v")
	if dash := strings.Index(s, "-"); dash >= 0 {
		s = s[:dash]
	}
	parts := strings.SplitN(s, ".", 3)
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		out[i], _ = strconv.Atoi(parts[i])
	}
	return out
}

// FetchLatestTag queries GitHub for the latest release tag. Returns empty
// string on any error or timeout (5s).
func FetchLatestTag() string {
	client := &http.Client{Timeout: 5 * time.Second}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	resp, err := client.Get(apiURL) //nolint:gosec
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return ""
	}
	return release.TagName
}
