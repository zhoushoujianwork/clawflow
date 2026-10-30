package commands

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

type legacyExecutionImport struct {
	TaskID         string          `json:"task_id"`
	Worker         string          `json:"worker"`
	Epoch          int64           `json:"epoch"`
	PID            int             `json:"pid"`
	Workspace      string          `json:"workspace"`
	Session        string          `json:"session"`
	Profile        string          `json:"profile"`
	Backend        string          `json:"backend"`
	RunID          string          `json:"run_id"`
	Options        workerOptions   `json:"options"`
	ExpectedReport json.RawMessage `json:"expected_report"`
}

func newWorkerLegacyImportCmd() *cobra.Command {
	var manifest string
	var apply bool
	cmd := &cobra.Command{Use: "import-legacy", Short: "Offline adoption of explicitly stopped legacy sessions (plan by default)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		lock, err := controllerLock()
		if err != nil {
			return fmt.Errorf("stop controller before legacy import: %w", err)
		}
		defer filelock.Release(lock)
		raw, err := os.ReadFile(manifest)
		if err != nil {
			return err
		}
		var rows []legacyExecutionImport
		if json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
			return fmt.Errorf("invalid legacy manifest")
		}
		db, _, err := openDB(cmd)
		if err != nil {
			return err
		}
		defer db.Close()
		store := coordinator.New(db.DB())
		for _, row := range rows {
			if row.PID <= 0 || row.Epoch <= 0 || row.Session == "" || row.Worker == "" || row.Options.TokenFile == "" || row.Options.StateDir == "" || !row.Options.Durable {
				return fmt.Errorf("incomplete legacy identity")
			}
			if err := syscall.Kill(row.PID, 0); err == nil {
				return fmt.Errorf("legacy process %d is still alive; import refused", row.PID)
			} else if err != syscall.ESRCH {
				return err
			}
			task, err := store.Task(cmd.Context(), row.TaskID)
			if err != nil {
				return err
			}
			if task.Kind != "implement" && task.Kind != "repair" {
				return fmt.Errorf("legacy task kind cannot resume")
			}
			canonical, err := filepath.EvalSymlinks(row.Workspace)
			if err != nil {
				return err
			}
			origin, err := sessionGit(cmd.Context(), canonical, "config", "--get", "remote.origin.url")
			if err != nil || !strings.EqualFold(extractOwnerRepo(origin), task.Repo) {
				return fmt.Errorf("legacy worktree repository mismatch")
			}
			if _, err := sessionGit(cmd.Context(), canonical, "rev-parse", "HEAD"); err != nil {
				return err
			}
			if configuredDevelopmentProfile(roleForOperator(task.Operator)) != row.Profile {
				return fmt.Errorf("legacy profile changed")
			}
			// The token's server identity must match the captured lease owner.
			token, err := os.ReadFile(row.Options.TokenFile)
			if err != nil {
				return err
			}
			identity, err := store.VerifyToken(cmd.Context(), strings.TrimSpace(string(token)))
			if err != nil || identity.Name != row.Worker || task.LeaseOwner != row.Worker {
				return fmt.Errorf("legacy worker credential mismatch")
			}
			if !apply {
				fmt.Fprintf(cmd.OutOrStdout(), "ready: %s #%d session=%s worktree=%s\n", task.Repo, task.Issue, row.Session, canonical)
				continue
			}
			root := workerJournalRoot(row.Options)
			path := filepath.Join(root, task.ID, "journal.json")
			journalLock, err := filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
			if err != nil {
				return err
			}
			err = func() error {
				if existing, err := readWorkerJournal(path); err == nil && existing.State != "legacy_preparing" {
					return fmt.Errorf("legacy journal already exists; use normal recovery")
				} else if err != nil && !os.IsNotExist(err) {
					return err
				}
				executable, err := os.Executable()
				if err != nil {
					return err
				}
				cli, err := os.ReadFile(executable)
				if err != nil {
					return err
				}
				pinned := filepath.Join(root, "generations", fmt.Sprintf("%x", sha256.Sum256(cli)), "clawflow")
				if _, err := os.Stat(pinned); os.IsNotExist(err) {
					if err := writeDurableFile(pinned, cli, 0700); err != nil {
						return err
					}
				}
				ec, err := execution.LoadConfig()
				if err != nil {
					return err
				}
				command, err := exec.LookPath(ec.Command)
				if err != nil {
					return err
				}
				binary, err := os.ReadFile(command)
				if err != nil {
					return err
				}
				offload := filepath.Join(root, "runtimes", fmt.Sprintf("%x", sha256.Sum256(binary)), "offload")
				if _, err := os.Stat(offload); os.IsNotExist(err) {
					if err := writeDurableFile(offload, binary, 0700); err != nil {
						return err
					}
				}
				row.Options.Executable = pinned
				journal := &workerJournal{Version: 1, Task: *task, Options: row.Options, Executable: pinned, OffloadCommand: offload, State: "legacy_preparing", Resume: true, LegacyReport: row.ExpectedReport}
				if err := writeWorkerJournal(path, journal); err != nil {
					return err
				}
				checkpoint := execution.Checkpoint{Version: 1, Workspace: canonical, Profile: row.Profile, Command: offload, Session: row.Session, Backend: row.Backend, RunID: row.RunID}
				data, _ := json.Marshal(checkpoint)
				if err := writeDurableFile(filepath.Join(filepath.Dir(path), "execution.json"), data, 0600); err != nil {
					return err
				}
				recovered, err := store.AdoptLegacyInterruption(cmd.Context(), row.TaskID, row.Worker, row.Epoch, row.ExpectedReport)
				if err != nil {
					return err
				}
				journal.Task = *recovered
				journal.State = "prepared"
				if err := writeWorkerJournal(path, journal); err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "adopted: %s #%d epoch=%d journal=%s\n", task.Repo, task.Issue, recovered.FencingToken, path)
				return err
			}()
			filelock.Release(journalLock)
			if err != nil {
				return err
			}
		}
		return nil
	}}
	cmd.Flags().StringVar(&manifest, "manifest", "", "Private captured legacy execution manifest")
	cmd.Flags().BoolVar(&apply, "apply", false, "Adopt stopped interruption records; controller must be offline")
	_ = cmd.MarkFlagRequired("manifest")
	return cmd
}
