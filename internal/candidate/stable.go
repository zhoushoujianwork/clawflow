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
	// Activity and Executors record stable activity that is deliberately not
	// compared: per-top-level summaries of unprotected data/ and executor processes.
	Activity  map[string]string `json:"activity,omitempty"`
	Executors []string          `json:"executors,omitempty"`
}

// StableProbe supplies the process view; replaced in tests.
type StableProbe struct {
	// Processes lists "pid:start" identities of stable ClawFlow processes.
	Processes func() ([]string, error)
	// Executors lists "pid:start" identities of task executor processes the
	// stable worker starts and ends during normal dispatch. Optional.
	Executors func() ([]string, error)
}

// DefaultStableProbe lists processes whose command line runs a stable
// clawflow service, using only read-only `ps`.
func DefaultStableProbe(stableRoot, candidateBase string) StableProbe {
	list := func(executors bool) ([]string, error) {
		out, err := exec.Command("ps", "-axo", "pid=,lstart=,command=").Output()
		if err != nil {
			return nil, err
		}
		return parseProcesses(string(out), stableRoot, candidateBase, executors), nil
	}
	return StableProbe{
		Processes: func() ([]string, error) { return list(false) },
		Executors: func() ([]string, error) { return list(true) },
	}
}

// parseProcesses extracts "pid:start" identities from `ps -axo pid=,lstart=,command=`
// output. Services are processes whose executable is named clawflow and whose
// first subcommand is controller or worker (other than "worker execute", which
// is a task executor), or the ClawFlow.app bundle. Words that merely appear in
// arguments or paths (a shell cd-ing into a clawflow checkout, grep, git) do
// not count. Identity stays pid + start time + command line.
func parseProcesses(out, stableRoot, candidateBase string, executors bool) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		if _, err := strconv.Atoi(f[0]); err != nil {
			continue
		}
		if candidateBase != "" && strings.Contains(line, candidateBase) {
			continue
		}
		// The command starts after the pid and the five lstart fields.
		command := commandAfter(line, 6)
		sub, exe := clawflowSubcommand(command)
		if sub != "" && !underStableRoot(exe, stableRoot) {
			// A clawflow process from a temp dir, test dir or another checkout
			// is not a stable service or executor.
			sub = ""
		}
		isExec := strings.Contains(line, "worker-executions") || sub == "worker execute"
		if isExec != executors {
			continue
		}
		if !executors && sub != "controller" && sub != "worker" && !strings.Contains(line, "ClawFlow.app") {
			continue
		}
		ids = append(ids, f[0]+":"+strings.Join(f[1:6], " ")+":"+command)
	}
	sort.Strings(ids)
	return ids
}

// commandAfter returns line with its first n whitespace-separated fields removed.
func commandAfter(line string, n int) string {
	rest := strings.TrimSpace(line)
	for i := 0; i < n; i++ {
		idx := strings.IndexAny(rest, " \t")
		if idx < 0 {
			return ""
		}
		rest = strings.TrimSpace(rest[idx:])
	}
	return rest
}

// underStableRoot reports whether the executable path lies strictly inside
// stableRoot, comparing both lexically and with symlinks resolved.
func underStableRoot(exe, stableRoot string) bool {
	if exe == "" || stableRoot == "" || !filepath.IsAbs(exe) {
		return false
	}
	inside := func(root, p string) bool {
		rel, err := filepath.Rel(root, p)
		return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	exe = filepath.Clean(exe)
	root := filepath.Clean(stableRoot)
	if inside(root, exe) {
		return true
	}
	if r, err := filepath.EvalSymlinks(root); err == nil && inside(r, exe) {
		return true
	}
	return false
}

// clawflowSubcommand returns "controller", "worker", "worker execute" or ""
// (plus the executable path, empty for a bare name) when command does not run
// a clawflow executable at its start. Paths may
// contain spaces, so the executable is the last "/clawflow " whose preceding
// path has no option-looking token.
func clawflowSubcommand(command string) (string, string) {
	var rest, exe string
	switch {
	case strings.HasPrefix(command, "clawflow "):
		rest = command[len("clawflow "):]
		exe = "clawflow"
	case strings.HasPrefix(command, "/"):
		idx := strings.LastIndex(command, "/clawflow ")
		if idx < 0 || strings.Contains(command[:idx], " -") {
			return "", ""
		}
		exe = command[:idx+len("/clawflow")]
		rest = command[idx+len("/clawflow "):]
	default:
		return "", ""
	}
	args := strings.Fields(rest)
	if len(args) == 0 || (args[0] != "controller" && args[0] != "worker") {
		return "", ""
	}
	if args[0] == "worker" && len(args) > 1 && args[1] == "execute" {
		return "worker execute", exe
	}
	return args[0], exe
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

// protectedData lists the only data/ files compared. The stable scheduler and
// its SQLite database (clawflow.sqlite3, -wal, -shm) write data/ continuously,
// so everything else there is treated as stable activity and never affects the
// verdict. Candidate isolation relies on its own HOME and CLAWFLOW_STATE_DIR
// and on devmode.CheckWritePath, not on this comparison.
var protectedData = map[string]bool{
	"queue.json": true,
}

func isProtectedData(rel string) bool { return protectedData[rel] }

// TakeStable records the stable host's observable state. Reads only.
func TakeStable(s Stable, probe StableProbe) (StableSnapshot, error) {
	snap := StableSnapshot{ConfigPaths: map[string]string{}, DataPaths: map[string]string{}}
	var err error
	if snap.Config, err = digestTree(filepath.Join(s.Root, "config"), nil, snap.ConfigPaths); err != nil {
		return snap, err
	}
	if snap.Data, err = digestTree(filepath.Join(s.Root, "data"), func(rel string) bool { return !isProtectedData(rel) }, snap.DataPaths); err != nil {
		return snap, err
	}
	snap.Activity = stableActivity(filepath.Join(s.Root, "data"))
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
	if probe.Executors != nil {
		if snap.Executors, err = probe.Executors(); err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// stableActivity summarizes ignored data/ content per top-level entry as
// "paths:bytes:latest-mtime", so evidence stays small however many files exist.
func stableActivity(dataRoot string) map[string]string {
	type agg struct {
		n, size, mtime int64
	}
	sums := map[string]*agg{}
	_ = filepath.WalkDir(dataRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dataRoot, path)
		if rel == "." || d.IsDir() || isProtectedData(rel) {
			return nil
		}
		top := strings.Split(rel, string(filepath.Separator))[0]
		if info, ierr := d.Info(); ierr == nil {
			a := sums[top]
			if a == nil {
				a = &agg{}
				sums[top] = a
			}
			a.n++
			a.size += info.Size()
			if m := info.ModTime().UnixNano(); m > a.mtime {
				a.mtime = m
			}
		}
		return nil
	})
	act := map[string]string{}
	for k, a := range sums {
		act[k] = fmt.Sprintf("%d:%d:%d", a.n, a.size, a.mtime)
	}
	return act
}

// Ignored lists stable scheduler activity seen between two snapshots. It is
// informational and never part of Diff.
func (a StableSnapshot) Ignored(b StableSnapshot) []string {
	var out []string
	if tops := changedKeys(a.Activity, b.Activity); len(tops) > 0 {
		out = append(out, fmt.Sprintf("stable data activity (ignored): %d top-level entries changed: %s", len(tops), strings.Join(tops, ", ")))
	}
	before := map[string]bool{}
	for _, e := range a.Executors {
		before[e] = true
	}
	after := map[string]bool{}
	for _, e := range b.Executors {
		after[e] = true
	}
	started, ended := 0, 0
	for e := range after {
		if !before[e] {
			started++
		}
	}
	for e := range before {
		if !after[e] {
			ended++
		}
	}
	if started+ended > 0 {
		out = append(out, fmt.Sprintf("stable task executor processes (ignored): %d started, %d exited", started, ended))
	}
	return out
}

func changedKeys(a, b map[string]string) []string {
	var keys []string
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			keys = append(keys, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	// Keep evidence small: name at most 20 entries.
	if len(keys) > 20 {
		keys = append(keys[:20], fmt.Sprintf("... %d more", len(keys)-20))
	}
	return keys
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
