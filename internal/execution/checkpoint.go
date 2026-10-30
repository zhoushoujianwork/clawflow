package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

// Checkpoint is private recovery metadata, not execution evidence or authority.
type Checkpoint struct {
	Version                                              int
	Workspace, Profile, Command, Session, Backend, RunID string
	ArtifactDir                                          string
	UpdatedAt                                            time.Time
	Completed                                            bool
}
type checkpointOptions struct {
	Path   string
	Resume bool
}
type checkpointKey struct{}

func WithCheckpoint(ctx context.Context, path string, resume bool) context.Context {
	return context.WithValue(ctx, checkpointKey{}, checkpointOptions{path, resume})
}

func RecoveryWorkspace(ctx context.Context) (string, bool) {
	option, _ := ctx.Value(checkpointKey{}).(checkpointOptions)
	if !option.Resume {
		return "", false
	}
	c, err := ReadCheckpoint(option.Path)
	return c.Workspace, err == nil
}

func ReadCheckpoint(path string) (Checkpoint, error) {
	var c Checkpoint
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Workspace == "" || c.Profile == "" {
		return c, errors.New("invalid execution checkpoint")
	}
	return c, nil
}

func writeCheckpoint(path string, c Checkpoint) error {
	c.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func recoveryCheckpoint(ctx context.Context, c Config, req *Request) (string, *Checkpoint, error) {
	option, _ := ctx.Value(checkpointKey{}).(checkpointOptions)
	if option.Path == "" || req.PermissionMode != "bypassPermissions" || req.ToolsDisabled {
		return "", nil, nil
	}
	cp := Checkpoint{Version: 1, Workspace: req.Workspace, Profile: c.Profile, Command: c.Command}
	if option.Resume {
		previous, err := ReadCheckpoint(option.Path)
		if err != nil {
			return "", nil, err
		}
		if previous.Completed || previous.Workspace != req.Workspace || previous.Profile != c.Profile || previous.Command != c.Command || !idPattern.MatchString(previous.Session) {
			return "", nil, errors.New("recorded execution cannot safely resume")
		}
		req.Resume, req.SessionKey = true, previous.Session
		req.Prompt = "Recover the interrupted task in this same session and worktree. Verify existing edits, commits and external effects before continuing; do not repeat completed actions.\n\n" + req.Prompt
		cp = previous
	}
	return option.Path, &cp, nil
}

// A child inherits this descriptor so a manager/executor crash cannot release
// recovery ownership while the original Offload process still runs.
func checkpointLock(path string) (*os.File, error) {
	return filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
}
