package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// BackfillEntry is one meta.json that BackfillUsage found (or fixed).
type BackfillEntry struct {
	// Kind is "operator" or "pilot".
	Kind string
	// Path is the meta.json path on disk.
	Path string
	// Label identifies the run for humans: "<repo>#<issue>/<operator>" for
	// operator runs, "<project>" for pilot wakes.
	Label     string
	StartedAt time.Time
	Status    string
	// Usage is the recovered figure. Never nil in a returned entry.
	Usage *Usage
	// Written is false in dry-run mode.
	Written bool
}

// BackfillUsage walks both run trees and recovers usage for every meta.json
// whose `usage` is null but whose events.jsonl still carries per-message
// token counts. Historically these rows were unrecoverable: ExtractUsage only
// read the terminal "result" event, which a killed process never writes, so
// the spend of the longest (most expensive) runs vanished from meta.json, the
// dashboard, and usage.json alike — 13 pilot wakes had accumulated that way,
// the largest a single ~11.1M-token wake (issue #322).
//
// Rows still plausibly in flight are left alone: a row is only touched when
// its status is terminal, or when its events.jsonl has been silent past the
// relevant quiet window. Money-affecting rewrites are opt-in — with
// dryRun=true nothing is written and the caller can print the plan first.
//
// Returns the affected entries sorted oldest-first.
func BackfillUsage(dryRun bool) ([]BackfillEntry, error) {
	var out []BackfillEntry

	opRoot := filepath.Join(DataDir(), "runs")
	if err := walkMetaFiles(opRoot, func(path string, data []byte) {
		var m RunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		if m.Usage != nil || m.Status == "" {
			return
		}
		runDir := filepath.Dir(path)
		if !operatorBackfillEligible(m, runDir) {
			return
		}
		u, err := ExtractUsage(filepath.Join(runDir, "events.jsonl"))
		if err != nil || u == nil {
			return
		}
		e := BackfillEntry{
			Kind:      "operator",
			Path:      path,
			Label:     fmt.Sprintf("%s#%d/%s", m.Repo, m.IssueNumber, m.Operator),
			StartedAt: m.StartedAt,
			Status:    m.Status,
			Usage:     u,
		}
		if !dryRun {
			m.Usage = u
			if err := WriteRunMeta(runDir, m); err == nil {
				e.Written = true
			}
		}
		out = append(out, e)
	}); err != nil {
		return out, err
	}

	pilotRoot := filepath.Join(DataDir(), "pilot-runs")
	if err := walkMetaFiles(pilotRoot, func(path string, data []byte) {
		var m PilotRunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		if m.Usage != nil || m.Status == "" {
			return
		}
		runDir := filepath.Dir(path)
		if !pilotBackfillEligible(m, runDir) {
			return
		}
		u, err := ExtractUsage(filepath.Join(runDir, "events.jsonl"))
		if err != nil || u == nil {
			return
		}
		e := BackfillEntry{
			Kind:      "pilot",
			Path:      path,
			Label:     m.Project,
			StartedAt: m.StartedAt,
			Status:    m.Status,
			Usage:     u,
		}
		if !dryRun {
			m.Usage = u
			if err := WritePilotRunMeta(runDir, m); err == nil {
				e.Written = true
			}
		}
		out = append(out, e)
	}); err != nil {
		return out, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

// operatorBackfillEligible mirrors pilotBackfillEligible for operator runs:
// terminal rows always qualify, in-flight rows only once their events.jsonl
// has gone quiet past the operator's window (so a live run is never given a
// partial figure).
func operatorBackfillEligible(m RunMeta, runDir string) bool {
	if m.Status != "running" && m.Status != "finalizing" {
		return true
	}
	qw := quietWindowFor(m.Operator)
	if st, err := os.Stat(filepath.Join(runDir, "events.jsonl")); err == nil {
		return time.Since(st.ModTime()) > qw
	}
	return time.Since(m.StartedAt) > qw
}

// walkMetaFiles calls fn for every meta.json under root. A missing root is
// not an error (fresh install, or pilot never ran).
func walkMetaFiles(root string, fn func(path string, data []byte)) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if isOrphanedRepoDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "meta.json" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		fn(path, data)
		return nil
	})
}
