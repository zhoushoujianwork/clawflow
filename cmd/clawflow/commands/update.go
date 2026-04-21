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
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const githubRepo = "zhoushoujianwork/clawflow"

// installRecord mirrors ~/.clawflow/config/install.yaml.
type installRecord struct {
	Agent       string `yaml:"agent"`
	SkillDir    string `yaml:"skill_dir"`
	RepoDir     string `yaml:"repo_dir"`
	InstalledAt string `yaml:"installed_at"`
}

func NewUpdateCmd() *cobra.Command {
	var fromSource bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update clawflow binary and SKILL.md to the latest release",
		Long: `Pulls the latest release from GitHub, replaces the clawflow binary,
and updates SKILL.md in your agent's skills directory.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			record, err := loadInstallRecord()
			if err != nil {
				// No install record — manual curl install. Use defaults.
				record = defaultInstallRecord()
				fmt.Printf("Updating ClawFlow (no install record found, using defaults)...\n")
			} else {
				fmt.Printf("Updating ClawFlow...\n")
				fmt.Printf("  Agent:     %s\n", record.Agent)
				fmt.Printf("  Skill dir: %s\n", record.SkillDir)
			}
			fmt.Println()

			// ---------- 1. update binary ----------
			if fromSource && record.RepoDir != "" {
				if err := rebuildFromSource(record.RepoDir); err != nil {
					return fmt.Errorf("build from source failed: %w", err)
				}
			} else {
				if err := downloadLatestBinary(); err != nil {
					// Fallback to source rebuild if download fails and repo dir exists
					if record.RepoDir != "" {
						fmt.Printf("  [warn] download failed (%v), trying source rebuild...\n", err)
						if srcErr := rebuildFromSource(record.RepoDir); srcErr != nil {
							return fmt.Errorf("download failed and source rebuild failed: %v / %v", err, srcErr)
						}
					} else {
						return fmt.Errorf("binary download failed: %w", err)
					}
				}
			}

			// ---------- 2. update SKILL.md ----------
			if err := updateSkillMD(record); err != nil {
				return fmt.Errorf("SKILL.md update failed: %w", err)
			}

			fmt.Println("\nClawFlow updated successfully.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&fromSource, "from-source", false, "Rebuild binary from local source instead of downloading")
	return cmd
}

// defaultInstallRecord returns a best-effort record for manual (curl) installs.
func defaultInstallRecord() *installRecord {
	home, _ := os.UserHomeDir()
	// Try to detect agent skill dir from common locations.
	candidates := []struct{ agent, dir string }{
		{"claude", filepath.Join(home, ".claude", "skills", "clawflow")},
		{"openclaw", filepath.Join(home, ".openclaw", "skills", "clawflow")},
	}
	for _, c := range candidates {
		if _, err := os.Stat(c.dir); err == nil {
			return &installRecord{Agent: c.agent, SkillDir: c.dir}
		}
	}
	return &installRecord{}
}

// loadInstallRecord reads ~/.clawflow/config/install.yaml.
func loadInstallRecord() (*installRecord, error) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".clawflow", "config", "install.yaml")
	data, err := os.ReadFile(path)
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
	// Resolve latest release tag
	tag, assetURL, err := latestReleaseAsset()
	if err != nil {
		return err
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

// latestReleaseAsset finds the download URL for the binary in the latest release.
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

	// Look for an asset matching the current OS/arch, or just "clawflow".
	want := binaryAssetName()
	for _, a := range release.Assets {
		if a.Name == want || a.Name == "clawflow" {
			return release.TagName, a.BrowserDownloadURL, nil
		}
	}
	return "", "", fmt.Errorf("no binary asset found in release %s (looked for %q)", release.TagName, want)
}

// binaryAssetName returns the expected asset name for the current platform.
func binaryAssetName() string {
	os_ := runtime.GOOS
	arch := runtime.GOARCH
	if os_ == "windows" {
		return fmt.Sprintf("clawflow_%s_%s.exe", os_, arch)
	}
	return fmt.Sprintf("clawflow_%s_%s", os_, arch)
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

// updateSkillMD syncs all files in skills/clawflow/ from source repo or GitHub.
func updateSkillMD(record *installRecord) error {
	if err := os.MkdirAll(record.SkillDir, 0o755); err != nil {
		return err
	}

	// Try local repo first — copy entire directory
	if record.RepoDir != "" {
		srcDir := filepath.Join(expandHomeStr(record.RepoDir), "skills", "clawflow")
		if entries, err := os.ReadDir(srcDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
				if err != nil {
					return err
				}
				dest := filepath.Join(record.SkillDir, e.Name())
				if err := os.WriteFile(dest, data, 0o644); err != nil {
					return err
				}
				fmt.Printf("  [ok] %s updated from local repo\n", e.Name())
			}
			return nil
		}
	}

	// Fallback: list files via GitHub Contents API, then download each
	return downloadSkillDir(record.SkillDir)
}

// downloadSkillDir fetches all files in skills/clawflow/ from GitHub Contents API.
func downloadSkillDir(destDir string) error {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/skills/clawflow", githubRepo)
	resp, err := http.Get(apiURL) //nolint:gosec
	if err != nil {
		return fmt.Errorf("contents API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("contents API returned %d", resp.StatusCode)
	}

	var entries []struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return fmt.Errorf("decode contents API: %w", err)
	}

	for _, e := range entries {
		if e.Type != "file" || e.DownloadURL == "" {
			continue
		}
		fmt.Printf("  [dl] downloading %s...\n", e.Name)
		fr, err := http.Get(e.DownloadURL) //nolint:gosec
		if err != nil {
			return fmt.Errorf("download %s: %w", e.Name, err)
		}
		data, err := io.ReadAll(fr.Body)
		fr.Body.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name, err)
		}
		if fr.StatusCode != http.StatusOK {
			return fmt.Errorf("download %s: status %d", e.Name, fr.StatusCode)
		}
		dest := filepath.Join(destDir, e.Name)
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("  [ok] %s updated → %s\n", e.Name, dest)
	}
	return nil
}

// binaryPath returns the installed binary path.
func binaryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "bin", "clawflow")
}

// atomicReplace writes src to dest via a temp file, then renames.
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
	fmt.Printf("  [ok] binary updated → %s\n", dest)
	return nil
}

// FetchLatestTag queries GitHub for the latest release tag.
// Returns empty string on any error or timeout (5s).
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
