// Package housekeeping bounds ClawFlow-owned disk use. Unknown paths are retained.
package housekeeping

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Disk struct {
	Total       uint64  `json:"total_bytes"`
	Available   uint64  `json:"available_bytes"`
	UsedPercent float64 `json:"used_percent"`
	Warning     bool    `json:"warning"`
}

func Probe(path string, warning float64) (Disk, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return Disk{}, err
	}
	total, available := uint64(s.Blocks)*uint64(s.Bsize), uint64(s.Bavail)*uint64(s.Bsize)
	d := Disk{Total: total, Available: available}
	if total > 0 {
		d.UsedPercent = 100 * float64(total-available) / float64(total)
	}
	d.Warning = d.UsedPercent >= warning
	return d, nil
}

// Gate coordinates cleanup with all local managed runs, including workers with
// different state roots. Shared owners may run concurrently. Cleanup never waits.
func Gate(ctx context.Context, temp string, exclusive bool) (func(), error) {
	path := filepath.Join(temp, "clawflow-housekeeping.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if exclusive || !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

type Result struct {
	At      time.Time `json:"at"`
	DryRun  bool      `json:"dry_run"`
	Paths   []string  `json:"paths"`
	Bytes   int64     `json:"bytes"`
	Skipped string    `json:"skipped,omitempty"`
	Errors  []string  `json:"errors,omitempty"`
}

// SafeDir rejects symlink components below a trusted root. The root itself may
// be the OS's canonical temp alias (/var on macOS).
func SafeDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	p := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func Size(path string) (int64, error) {
	var n int64
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Type()&os.ModeSymlink == 0 {
			i, err := d.Info()
			if err != nil {
				return err
			}
			n += i.Size()
		}
		return nil
	})
	return n, err
}

func (r *Result) Remove(root, path string) {
	if !SafeDir(root, path) {
		return
	}
	n, err := Size(path)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		return
	}
	if !r.DryRun {
		// Root confines even a concurrent symlink substitution during recursive removal.
		dir, err := os.OpenRoot(root)
		if err != nil {
			r.Errors = append(r.Errors, err.Error())
			return
		}
		rel, _ := filepath.Rel(root, path)
		err = dir.RemoveAll(rel)
		dir.Close()
		if err != nil {
			r.Errors = append(r.Errors, err.Error())
			return
		}
	}
	r.Paths = append(r.Paths, path)
	r.Bytes += n
}

// Temps only recognizes names created by ClawFlow; generic go-build and
// offload-prompt directories may belong to unrelated programs and are excluded.
func (r *Result) Temps(root string, age time.Duration, inUse func(string) bool) {
	r.oldDirs(root, age, inUse, []string{"clawflow-op-", "clawflow-offload-", "clawflow-acceptance-", "clawflow-capability-", "clawflow-selfupgrade-"})
}

// ReviewTemps only visits the private reviewer temp root, never generic OS Go output.
func (r *Result) ReviewTemps(trustedRoot, root string, age time.Duration) {
	if SafeDir(trustedRoot, root) {
		r.oldDirs(root, age, func(string) bool { return false }, []string{"go-build"})
	}
}

func (r *Result) oldDirs(root string, age time.Duration, inUse func(string) bool, prefixes []string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		return
	}
	for _, e := range entries {
		owned := false
		for _, prefix := range prefixes {
			owned = owned || strings.HasPrefix(e.Name(), prefix)
		}
		if !owned || !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name())
		info, err := e.Info()
		if err == nil && r.At.Sub(info.ModTime()) >= age && !inUse(p) {
			r.Remove(root, p)
		}
	}
}

// Cache trims oldest top-level build-cache buckets, never the review temp root.
// The caller holds the exclusive gate, so no reviewer can still use the cache.
func (r *Result) Cache(trustedRoot, root string, limit int64) {
	if !SafeDir(trustedRoot, root) {
		return
	}
	total, err := Size(root)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		a, e1 := entries[i].Info()
		b, e2 := entries[j].Info()
		return e1 == nil && e2 == nil && a.ModTime().Before(b.ModTime())
	})
	for _, e := range entries {
		if total <= limit {
			break
		}
		// Go build cache buckets are exactly two lowercase hexadecimal digits.
		name := e.Name()
		if len(name) != 2 || strings.Trim(name, "0123456789abcdef") != "" || !e.IsDir() {
			continue
		}
		p := filepath.Join(root, name)
		n, err := Size(p)
		if err != nil {
			continue
		}
		before := len(r.Paths)
		r.Remove(trustedRoot, p)
		if len(r.Paths) > before {
			total -= n
		}
	}
}
