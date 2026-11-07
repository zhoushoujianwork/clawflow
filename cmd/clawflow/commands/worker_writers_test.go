package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const lsofSample = "p100\nclogin\nfcwd\na \nn/work/repo/sub\n" + // cwd inside
	"p200\ncwriter\nfcwd\nn/\nf3\nau\nn/work/repo/out.log\nf4\nar\nn/work/repo/in.txt\n" + // writes inside, reads inside
	"p300\ncreader\nfcwd\nn/\nf5\nar\nn/work/repo/in.txt\nf6\naw\nn/tmp/elsewhere\n" + // only reads inside
	"p400\ncsibling\nfcwd\nn/work/repo-old\nf3\naw\nn/work/repo-old/x\n" + // prefix lookalike
	"p500\ncself\nfcwd\nn/work/repo\n" + // the worker itself
	"p600\ncboth\nf3\naw\nn/work/repo/a\nfcwd\nn/work/repo\n" // cwd wins over a write fd

func TestParseLsofHoldersCountsCwdAndWriteDescriptorsOnly(t *testing.T) {
	got := parseLsofHolders([]byte(lsofSample), "/work/repo", 500)
	byPID := map[int]worktreeHolder{}
	for _, h := range got {
		byPID[h.PID] = h
	}
	if len(byPID) != 3 {
		t.Fatalf("holders=%+v", got)
	}
	if h := byPID[100]; h.Name != "login" || h.Reason != holderReasonCWD {
		t.Fatalf("cwd holder=%+v", h)
	}
	if h := byPID[200]; h.Name != "writer" || h.Reason != "open for writing: out.log" {
		t.Fatalf("write holder=%+v", h)
	}
	if h := byPID[600]; h.Reason != holderReasonCWD {
		t.Fatalf("cwd should take precedence: %+v", h)
	}
	for _, pid := range []int{300, 400, 500} {
		if _, ok := byPID[pid]; ok {
			t.Fatalf("pid %d must not be reported: %+v", pid, got)
		}
	}
}

func TestInsideWorktreeRequiresPathBoundary(t *testing.T) {
	for p, want := range map[string]bool{
		"/work/repo":        true,
		"/work/repo/a/b":    true,
		"/work/repo-old":    false,
		"/work/repo-old/a":  false,
		"/work":             false,
		"/work/repo2/a":     false,
		"/other/work/repo/": false,
	} {
		if got := insideWorktree("/work/repo", p); got != want {
			t.Errorf("insideWorktree(%q)=%v want %v", p, got, want)
		}
	}
}

func fakeProcEntry(t *testing.T, procRoot string, pid string, comm string, cwd string, fds map[string]struct{ target, flags string }) {
	t.Helper()
	dir := filepath.Join(procRoot, pid)
	for _, sub := range []string{"fd", "fdinfo"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
		t.Fatal(err)
	}
	for fd, spec := range fds {
		if err := os.Symlink(spec.target, filepath.Join(dir, "fd", fd)); err != nil {
			t.Fatal(err)
		}
		if spec.flags != "" {
			if err := os.WriteFile(filepath.Join(dir, "fdinfo", fd), []byte("pos:\t0\nflags:\t"+spec.flags+"\nmnt_id:\t1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestScanProcHoldersReadsCwdAndWriteFlags(t *testing.T) {
	proc := t.TempDir()
	type fdSpec = struct{ target, flags string }
	fakeProcEntry(t, proc, "10", "inside-cwd", "/work/repo/sub", nil)
	fakeProcEntry(t, proc, "20", "writer", "/", map[string]fdSpec{"3": {"/work/repo/out.log", "0100001"}})
	fakeProcEntry(t, proc, "21", "readwrite", "/", map[string]fdSpec{"4": {"/work/repo/db", "0100002"}})
	fakeProcEntry(t, proc, "30", "reader", "/", map[string]fdSpec{"3": {"/work/repo/in.txt", "0100000"}})
	fakeProcEntry(t, proc, "31", "unknown-mode", "/", map[string]fdSpec{"3": {"/work/repo/x", ""}})
	fakeProcEntry(t, proc, "40", "lookalike", "/work/repo-old", map[string]fdSpec{"3": {"/work/repo-old/x", "0100001"}})
	fakeProcEntry(t, proc, "50", "self", "/work/repo", nil)
	if err := os.MkdirAll(filepath.Join(proc, "sys"), 0o700); err != nil { // non-numeric entries are ignored
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proc, "60"), 0o700); err != nil { // unreadable: no cwd, no fd directory
		t.Fatal(err)
	}
	got, err := scanProcHolders(proc, "/work/repo", 50)
	if err != nil {
		t.Fatal(err)
	}
	pids := map[int]string{}
	for _, h := range got {
		pids[h.PID] = h.Reason
	}
	want := map[int]string{
		10: holderReasonCWD,
		20: "open for writing: out.log",
		21: "open for writing: db",
		31: "open for writing: x", // unreadable mode fails closed
	}
	if len(pids) != len(want) {
		t.Fatalf("holders=%v want %v", pids, want)
	}
	for pid, reason := range want {
		if pids[pid] != reason {
			t.Fatalf("pid %d reason=%q want %q (all=%v)", pid, pids[pid], reason, pids)
		}
	}
}

func TestScanProcHoldersFailsWhenProcCannotBeListed(t *testing.T) {
	if _, err := scanProcHolders(filepath.Join(t.TempDir(), "missing"), "/work/repo", 1); err == nil {
		t.Fatal("expected an error when the process table is unavailable")
	}
}

func TestEnsureWorktreeHasNoWritersReportsReasonAndPostponesOnScanFailure(t *testing.T) {
	old := scanWorktreeHolders
	defer func() { scanWorktreeHolders = old }()

	scanWorktreeHolders = func(context.Context, string) ([]worktreeHolder, error) { return nil, nil }
	if err := ensureWorktreeHasNoWriters(context.Background(), "/work/repo"); err != nil {
		t.Fatalf("clean worktree: %v", err)
	}

	scanWorktreeHolders = func(context.Context, string) ([]worktreeHolder, error) {
		var hs []worktreeHolder
		for pid := 9; pid >= 1; pid-- {
			hs = append(hs, worktreeHolder{PID: pid, Name: "tool", Reason: holderReasonCWD})
		}
		return hs, nil
	}
	err := ensureWorktreeHasNoWriters(context.Background(), "/work/repo")
	if !errors.Is(err, errWorktreeInUse) || !strings.Contains(err.Error(), "recovery is postponed") || !strings.Contains(err.Error(), "pid 1 (tool): "+holderReasonCWD) || !strings.Contains(err.Error(), "and 4 more") || strings.Contains(err.Error(), "pid 9") {
		t.Fatalf("err=%v", err)
	}

	scanWorktreeHolders = func(context.Context, string) ([]worktreeHolder, error) { return nil, errors.New("lsof not found") }
	err = ensureWorktreeHasNoWriters(context.Background(), "/work/repo")
	if err == nil || errors.Is(err, errWorktreeInUse) || !strings.Contains(err.Error(), "recovery is postponed") || !strings.Contains(err.Error(), "lsof not found") {
		t.Fatalf("scan failure must postpone with its cause: %v", err)
	}
}

func TestRecoveryIsPostponedWhenWorktreeScanFails(t *testing.T) {
	path, _, api, calls := libraryRecoveryFixture(t, false)
	oldAbandon, oldScan := abandonOrphanedRun, scanWorktreeHolders
	abandonOrphanedRun = func(context.Context, string) (AbandonState, error) { return AbandonEnded, nil }
	scanWorktreeHolders = func(context.Context, string) ([]worktreeHolder, error) {
		return nil, errors.New("process table unreadable")
	}
	defer func() { abandonOrphanedRun, scanWorktreeHolders = oldAbandon, oldScan }()
	err := prepareWorkerRecovery(context.Background(), api, path)
	if err == nil || !strings.Contains(err.Error(), "process table unreadable") || *calls != 0 {
		t.Fatalf("err=%v calls=%d", err, *calls)
	}
	j, readErr := readWorkerJournal(path)
	if readErr != nil || j.State != "needs_recovery" || j.Resume {
		t.Fatalf("journal=%+v err=%v", j, readErr)
	}
}
