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
				return fmt.Errorf("install record not found — run install.sh first: %w", err)
			}

			fmt.Printf("Updating ClawFlow...\n")
			fmt.Printf("  Agent:     %s\n", record.Agent)
			fmt.Printf("  Skill dir: %s\n", record.SkillDir)
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

	c := exec.Command("go", "build", "-o", binaryPath(), "./cmd/clawflow/")
	c.Dir = repoDir
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build: %w\n%s", err, out)
	}
	fmt.Printf("  [ok] binary rebuilt → %s\n", binaryPath())
	return nil
}

// updateSkillMD copies the latest SKILL.md from source repo or GitHub raw.
func updateSkillMD(record *installRecord) error {
	dest := filepath.Join(record.SkillDir, "SKILL.md")

	// Try local repo first
	if record.RepoDir != "" {
		src := filepath.Join(expandHomeStr(record.RepoDir), "skills", "clawflow", "SKILL.md")
		if _, err := os.Stat(src); err == nil {
			data, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				return err
			}
			fmt.Printf("  [ok] SKILL.md updated from local repo → %s\n", dest)
			return nil
		}
	}

	// Fallback: download from GitHub raw
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/main/skills/clawflow/SKILL.md", githubRepo)
	fmt.Printf("  [dl] downloading SKILL.md from GitHub...\n")
	resp, err := http.Get(rawURL) //nolint:gosec
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d fetching SKILL.md", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("  [ok] SKILL.md updated → %s\n", dest)
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
