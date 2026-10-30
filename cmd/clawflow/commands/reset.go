package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	clawsync "github.com/zhoushoujianwork/clawflow/internal/sync"
	"gopkg.in/yaml.v3"
)

type resetResult struct {
	Status     string   `json:"status"`
	Root       string   `json:"root,omitempty"`
	Backup     string   `json:"backup,omitempty"`
	Entries    []string `json:"entries"`
	Moved      []string `json:"moved"`
	Moving     string   `json:"moving,omitempty"`
	SyncPaused bool     `json:"sync_paused"`
	Error      string   `json:"error,omitempty"`
}

func NewResetCmd() *cobra.Command {
	var yes, asJSON bool
	cmd := &cobra.Command{Use: "reset", Short: "Preview or archive local registrations and history (offline maintenance)",
		Long: `Archive local registrations and history, preserving credentials, source clones,
worktrees, skills and binaries. Stop web, runs and external schedulers first.
Default is a read-only preview; --yes applies and leaves local sync paused.
Login and older binaries can bypass the pause: do not run them during maintenance.
This is offline maintenance, not a distributed locking guarantee. Remote issues,
MRs and Gist contents are unchanged. Never automatically resume sync afterward.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, cfg, err := planReset()
			if err == nil && yes && cfg != nil {
				err = applyReset(&result, cfg)
			}
			if err != nil {
				result.Status = "failed"
				result.Error = err.Error()
			}
			if asJSON {
				outErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				if err == nil {
					err = outErr
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Reset: %s\n", result.Status)
				for _, entry := range result.Entries {
					fmt.Fprintln(cmd.OutOrStdout(), "  archive:", entry)
				}
				if result.Backup != "" {
					fmt.Fprintln(cmd.OutOrStdout(), "Backup:", result.Backup)
					fmt.Fprintln(cmd.OutOrStdout(), "Moved:", strings.Join(result.Moved, ", "))
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Sync remains paused after apply. Stop schedulers/web; login and old binaries can bypass this pause.")
			}
			return err
		}}
	cmd.Flags().BoolVar(&yes, "yes", false, "Apply the reset (default: read-only preview)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the structured plan or result, including partial failures")
	return cmd
}

// resetConfig reads without Load's legacy migration side effects.
func resetConfig(root string) (*config.Config, error) {
	cfg := &config.Config{Repos: map[string]config.Repo{}}
	for _, name := range []string{"config.yaml", "repos.yaml"} {
		data, err := os.ReadFile(filepath.Join(root, "config", name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var parsed config.Config
		if err := yaml.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", name, err)
		}
		// The canonical file is authoritative. An unused legacy file may
		// have an older schema; archive its bytes without interpreting it.
		return &parsed, nil
	}
	return cfg, nil
}

func resetSafePaths(root string) error {
	paths := []string{filepath.Dir(root), root, filepath.Join(root, "config"), filepath.Join(root, "backups"), filepath.Join(root, "locks"), filepath.Join(root, "sync-paused")}
	for _, entry := range resetTargets() {
		paths = append(paths, filepath.Join(root, entry))
	}
	for _, path := range paths {
		st, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %s", path)
		}
	}
	return nil
}

func resetTargets() []string {
	return []string{"config/config.yaml", "config/repos.yaml", "projects", "data", "chats", "state", "memory", "logs", "worker.log"}
}

func planReset() (resetResult, *config.Config, error) {
	r := resetResult{Status: "noop", Entries: []string{}, Moved: []string{}}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return r, nil, nil
	}
	if !filepath.IsAbs(home) || filepath.Clean(home) == string(filepath.Separator) {
		return r, nil, fmt.Errorf("a non-root absolute HOME is required")
	}
	r.Root = filepath.Join(filepath.Clean(home), ".clawflow")
	if err := resetSafePaths(r.Root); err != nil {
		return r, nil, err
	}
	if _, err := os.Lstat(r.Root); os.IsNotExist(err) {
		return r, nil, nil
	} else if err != nil {
		return r, nil, err
	}
	cfg, err := resetConfig(r.Root)
	if err != nil {
		return r, nil, err
	}
	if err := resetIdle(false); err != nil {
		return r, nil, err
	}
	for _, entry := range resetTargets() {
		if _, err := os.Lstat(filepath.Join(r.Root, entry)); err == nil {
			r.Entries = append(r.Entries, entry)
		} else if !os.IsNotExist(err) {
			return r, nil, err
		}
	}
	// An already empty canonical config alone does not need another archive.
	if len(r.Entries) == 1 && r.Entries[0] == "config/config.yaml" && len(cfg.Repos) == 0 {
		r.Entries = []string{}
	}
	if len(r.Entries) > 0 {
		r.Status = "planned"
	}
	r.SyncPaused, err = clawsync.IsPaused()
	return r, cfg, err
}

// processInventory is replaceable by isolated tests; failure is conservatively fatal.
var processInventory = func() ([]byte, error) { return exec.Command("ps", "-axo", "pid=,comm=,args=").Output() }

func resetIdle(ownRunLock bool) error {
	out, err := processInventory()
	if err != nil {
		return fmt.Errorf("cannot inspect active processes: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return fmt.Errorf("invalid process inventory")
		}
		if pid == os.Getpid() {
			continue
		}
		name := filepath.Base(fields[1])
		if name != "clawflow" && !strings.HasPrefix(name, "clawflow_") && !strings.HasPrefix(name, "clawflow-") {
			continue
		}
		for _, arg := range fields[3:] {
			if arg == "run" || arg == "web" {
				return fmt.Errorf("stop active clawflow %s process (pid %d) before reset", arg, pid)
			}
		}
	}
	// Any pass (legacy global lock or a per-repo run lock) held by a live
	// process other than this one blocks the reset. The walk below would also
	// refuse, but this names the cause.
	if held, holder := snapshot.AnyRunLockHeldExcept(os.Getpid()); held {
		return fmt.Errorf("stop active clawflow run (pid %d, repo %q) before reset", holder.PID, holder.Repo)
	}
	return filepath.WalkDir(snapshot.LockDir(), func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink lock %s", path)
		}
		if d.IsDir() || !strings.HasSuffix(path, ".lock") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var protocol struct{ Protocol string }
		if json.Unmarshal(data, &protocol) == nil && protocol.Protocol == "flock" && !snapshot.LockHeld(path) {
			return nil
		}
		var lock struct {
			PID int `json:"pid"`
		}
		if err = json.Unmarshal(data, &lock); err != nil || lock.PID <= 0 {
			return fmt.Errorf("cannot establish lock owner: %s", path)
		}
		if ownRunLock && path == snapshot.RunLockPath() && lock.PID == os.Getpid() {
			return nil
		}
		if snapshot.LockHeld(path) {
			return fmt.Errorf("active or inaccessible lock %s (pid %d)", path, lock.PID)
		}
		return nil
	})
}

func writeResetManifest(r *resetResult) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeResetFile(filepath.Join(r.Backup, "manifest.json"), data)
}

func applyReset(r *resetResult, cfg *config.Config) (retErr error) {
	if err := resetSafePaths(r.Root); err != nil {
		return err
	}
	if err := resetIdle(false); err != nil {
		return err
	}
	if err := snapshot.AcquireRunLock(Version); err != nil {
		return err
	}
	defer snapshot.ReleaseRunLock()
	if err := resetIdle(true); err != nil {
		return err
	}
	if err := clawsync.Pause(); err != nil {
		return err
	}
	r.SyncPaused = true
	if len(r.Entries) == 0 {
		// Even an empty local registration set can be repopulated by sync.
		// No archive is needed, but an explicit reset must retain the pause.
		r.Status = "noop"
		return nil
	}
	backups := filepath.Join(r.Root, "backups")
	if err := os.MkdirAll(backups, 0700); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(backups, "reset-"+time.Now().UTC().Format("20060102T150405.000000000Z")+"-")
	if err != nil {
		return err
	}
	r.Backup = backup
	r.Status = "applying"
	defer func() {
		if retErr != nil {
			r.Status = "failed"
			r.Error = retErr.Error()
		}
		if err := writeResetManifest(r); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("persist reset manifest: %w", err))
		}
	}()
	if err := writeResetManifest(r); err != nil {
		return err
	}
	// Recheck immediately before moving state; external schedulers must remain stopped.
	if err := resetSafePaths(r.Root); err != nil {
		return err
	}
	if err := resetIdle(true); err != nil {
		return err
	}
	for _, entry := range r.Entries {
		r.Moving = entry
		if err := writeResetManifest(r); err != nil {
			return err
		}
		dest := filepath.Join(backup, entry)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(r.Root, entry), dest); err != nil {
			return err
		}
		r.Moved = append(r.Moved, entry)
		r.Moving = ""
		if err := writeResetManifest(r); err != nil {
			return err
		}
	}
	if cfg != nil && (containsResetEntry(r.Moved, "config/config.yaml") || containsResetEntry(r.Moved, "config/repos.yaml")) {
		cfg.Repos = map[string]config.Repo{}
		data, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}
		path := filepath.Join(r.Root, "config", "config.yaml")
		if err := writeResetFile(path, data); err != nil {
			return err
		}
	}
	r.Status = "applied"
	return nil
}

func containsResetEntry(entries []string, target string) bool {
	for _, entry := range entries {
		if entry == target {
			return true
		}
	}
	return false
}

// Atomic replacement preserves the last complete recovery manifest on write errors.
func writeResetFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".reset-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
