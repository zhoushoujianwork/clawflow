package candidate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StableSnapshot captures what must not change while a candidate runs: the
// stable config and data files, which of its ports accept connections, and
// the identity (pid and start time) of its processes.
type StableSnapshot struct {
	Config      string            `json:"config"`
	Data        string            `json:"data"`
	Listeners   map[int]bool      `json:"listeners"`
	Processes   []string          `json:"processes"`
	ConfigPaths map[string]string `json:"config_paths,omitempty"`
	DataPaths   map[string]string `json:"data_paths,omitempty"`
}

// StableProbe supplies the process view; replaced in tests.
type StableProbe struct {
	// Processes lists "pid:start" identities of stable ClawFlow processes.
	Processes func() ([]string, error)
}

// DefaultStableProbe lists processes whose command line runs a stable
// clawflow service, using only read-only `ps`.
func DefaultStableProbe(stableRoot, candidateBase string) StableProbe {
	return StableProbe{Processes: func() ([]string, error) {
		out, err := exec.Command("ps", "-axo", "pid=,lstart=,command=").Output()
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, line := range strings.Split(string(out), "\n") {
			if !(strings.Contains(line, "clawflow") && (strings.Contains(line, " controller") || strings.Contains(line, " worker") || strings.Contains(line, "ClawFlow.app"))) {
				continue
			}
			if candidateBase != "" && strings.Contains(line, candidateBase) {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 6 {
				continue
			}
			if _, err := strconv.Atoi(f[0]); err != nil {
				continue
			}
			ids = append(ids, f[0]+":"+strings.Join(f[1:6], " "))
		}
		sort.Strings(ids)
		return ids, nil
	}}
}

func digestTree(root string, skip func(rel string) bool, entries ...map[string]string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." || (skip != nil && skip(rel)) {
			if d.IsDir() && rel != "." && skip != nil && skip(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		// Hash exactly the same bytes for the aggregate and the per-path evidence.
		// Neither file contents nor symlink targets are retained in the snapshot.
		entry := sha256.New()
		writer := io.MultiWriter(h, entry)
		defer func() {
			if len(entries) > 0 && entries[0] != nil {
				entries[0][rel] = hex.EncodeToString(entry.Sum(nil))
			}
		}()
		if d.IsDir() {
			fmt.Fprintf(writer, "d %s\n", rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			fmt.Fprintf(writer, "l %s %s\n", rel, target)
			return nil
		}
		fmt.Fprintf(writer, "f %s %d %s\n", rel, info.Size(), info.Mode())
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			content := sha256.New()
			_, readErr := io.Copy(content, file)
			closeErr := file.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			fmt.Fprintf(writer, "c %s %s\n", rel, hex.EncodeToString(content.Sum(nil)))
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// stableActivityData names top-level data entries a healthy stable scheduler
// rewrites on every pass: run history, scan-derived snapshots, HTTP caches and
// scan counters. A candidate cannot reach them through its own HOME, and they
// change whether or not one is running, so they cannot show contamination.
// Configuration, manual queue order and task, acceptance and coordination
// records stay protected.
var stableActivityData = map[string]bool{
	"git-status.json":   true,
	"github-http":       true,
	"harvest":           true,
	"housekeeping.json": true,
	"issues.json":       true,
	"meta.json":         true,
	"operators.json":    true,
	"pending.json":      true,
	"pipeline-prs":      true,
	"pipeline.json":     true,
	"queue-state":       true,
	"repos.json":        true,
	"runs":              true,
	"runs.json":         true,
	"usage.json":        true,
	"vcs-usage.json":    true,
}

// TakeStable records the stable host's observable state. Reads only.
func TakeStable(s Stable, probe StableProbe) (StableSnapshot, error) {
	snap := StableSnapshot{ConfigPaths: map[string]string{}, DataPaths: map[string]string{}}
	var err error
	if snap.Config, err = digestTree(filepath.Join(s.Root, "config"), nil, snap.ConfigPaths); err != nil {
		return snap, err
	}
	if snap.Data, err = digestTree(filepath.Join(s.Root, "data"), func(rel string) bool {
		return stableActivityData[strings.Split(rel, string(filepath.Separator))[0]]
	}, snap.DataPaths); err != nil {
		return snap, err
	}
	snap.Listeners = map[int]bool{}
	for _, port := range s.Ports {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
		snap.Listeners[port] = err == nil
		if c != nil {
			_ = c.Close()
		}
	}
	if probe.Processes != nil {
		if snap.Processes, err = probe.Processes(); err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// Diff lists what changed between two snapshots; empty means unchanged.
func (a StableSnapshot) Diff(b StableSnapshot) []string {
	var d []string
	if a.Config != b.Config {
		d = append(d, "stable config changed")
		d = append(d, changedPaths("config", a.ConfigPaths, b.ConfigPaths)...)
	}
	if a.Data != b.Data {
		d = append(d, "stable data changed")
		d = append(d, changedPaths("data", a.DataPaths, b.DataPaths)...)
	}
	for port, was := range a.Listeners {
		if b.Listeners[port] != was {
			d = append(d, fmt.Sprintf("stable listener on port %d changed", port))
		}
	}
	if strings.Join(a.Processes, "\n") != strings.Join(b.Processes, "\n") {
		d = append(d, "stable process identity changed")
	}
	return d
}

// Legacy snapshots still fail on their aggregate digest; missing path evidence
// cannot be interpreted as an empty tree or used to approve a change.
func changedPaths(kind string, before, after map[string]string) []string {
	if before == nil || after == nil {
		return nil
	}
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	var changed []string
	for k := range keys {
		if before[k] != after[k] {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	var result []string
	for i, k := range changed {
		if i == 50 {
			result = append(result, fmt.Sprintf("stable %s: %d additional changed paths", kind, len(changed)-i))
			break
		}
		action := "modified"
		if _, ok := before[k]; !ok {
			action = "added"
		}
		if _, ok := after[k]; !ok {
			action = "removed"
		}
		result = append(result, fmt.Sprintf("stable %s path %s: %q", kind, action, k))
	}
	return result
}
