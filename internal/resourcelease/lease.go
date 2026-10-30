// Package resourcelease serializes cooperating tasks on one stateful host.
// Leases expire when the kernel closes the holder's last descriptor, rather
// than on a wall-clock timeout that could preempt a still-running task.
package resourcelease

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

type Holder struct {
	Repository string `json:"repository"`
	Task       string `json:"task"`
	Phase      string `json:"phase"`
	Candidate  string `json:"candidate,omitempty"`
	Attempt    string `json:"attempt"`
}

// Blocked identifies the task that must finish before this task can run.
type Blocked struct {
	Resource string
	Holder   Holder
}

func (b *Blocked) Error() string {
	return fmt.Sprintf("blocked: resource %q held by %s task=%s phase=%s candidate=%s attempt=%s; next_owner: scheduling", b.Resource, b.Holder.Repository, b.Holder.Task, b.Holder.Phase, b.Holder.Candidate, b.Holder.Attempt)
}

// Acquire obtains all named resources in a common machine-local namespace.
// Partial acquisition is rolled back; no caller may use a resource on error.
// Lock files are never removed, preventing split locks on different inodes.
func Acquire(root string, keys []string, holder Holder) (release func(), err error) {
	var files []*os.File
	release = func() {
		for _, f := range files {
			filelock.Release(f)
		}
		files = nil
	}
	defer func() {
		if err != nil {
			release()
		}
	}()
	keys = slices.Clone(keys)
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		if strings.TrimSpace(key) == "" {
			return release, errors.New("empty shared resource key")
		}
		path := filepath.Join(root, fmt.Sprintf("%x.lock", sha256.Sum256([]byte(key))))
		// Serialize publication and diagnostic reads without holding the guard
		// for the task lifetime. A busy response always sees a complete identity.
		guard, guardErr := filelock.Wait(context.Background(), path+".guard", struct{}{})
		if guardErr != nil {
			return release, guardErr
		}
		f, lockErr := filelock.Try(path, holder)
		if errors.Is(lockErr, filelock.ErrHeld) {
			// The record is diagnostic only; kernel ownership remains authoritative.
			data, readErr := os.ReadFile(path)
			filelock.Release(guard)
			var current Holder
			if readErr != nil {
				return release, fmt.Errorf("blocked: resource %q holder metadata unavailable: %w", key, readErr)
			}
			if json.Unmarshal(data, &current) != nil || current.Task == "" {
				return release, fmt.Errorf("blocked: resource %q holder is publishing identity; retry; next_owner: scheduling", key)
			}
			return release, &Blocked{Resource: key, Holder: current}
		}
		filelock.Release(guard)
		if lockErr != nil {
			return release, lockErr
		}
		files = append(files, f)
	}
	return release, nil
}
