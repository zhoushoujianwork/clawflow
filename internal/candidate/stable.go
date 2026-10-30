package candidate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	Config    string       `json:"config"`
	Data      string       `json:"data"`
	Listeners map[int]bool `json:"listeners"`
	Processes []string     `json:"processes"`
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

func digestTree(root string, skip func(rel string) bool) (string, error) {
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
		if d.IsDir() {
			fmt.Fprintf(h, "d %s\n", rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			fmt.Fprintf(h, "l %s %s\n", rel, target)
			return nil
		}
		fmt.Fprintf(h, "f %s %d\n", rel, info.Size())
		if strings.HasPrefix(rel, "config"+string(filepath.Separator)) || rel == "config" {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			fmt.Fprintf(h, "c %s %s\n", rel, hex.EncodeToString(sum[:]))
		} else {
			fmt.Fprintf(h, "m %s %d\n", rel, info.ModTime().UnixNano())
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// TakeStable records the stable host's observable state. Reads only.
func TakeStable(s Stable, probe StableProbe) (StableSnapshot, error) {
	var snap StableSnapshot
	var err error
	if snap.Config, err = digestTree(filepath.Join(s.Root, "config"), nil); err != nil {
		return snap, err
	}
	if snap.Data, err = digestTree(filepath.Join(s.Root, "data"), func(rel string) bool {
		// Per-run history is appended by normal stable activity and is not
		// part of the configuration or queue state being protected.
		return rel == "runs" || strings.HasPrefix(rel, "runs"+string(filepath.Separator))
	}); err != nil {
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
	}
	if a.Data != b.Data {
		d = append(d, "stable data changed")
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
