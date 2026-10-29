package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func registrySource(entries string) []byte {
	return []byte("package store\nvar Migrations = []Migration{\n" + entries + "\n}\n")
}

const initial = `{Version: 1, Name: "baseline", Up: baseline},`
const baseAddition = `{Version: 2, Name: "base", Up: baseUp},
{Version: 4, Name: "base_later", Up: func(tx *DB) error { return apply(tx, []string{"a", "b"}) }},`
const prAddition = `{Version: 2, Name: "pr", Up: prUp},
{Version: 3, Name: "pr_later", Up: laterUp},`

func TestReconcileMigrationCollision(t *testing.T) {
	ancestor, base, pr := registrySource(initial), registrySource(initial+baseAddition), registrySource(initial+prAddition)
	working := registrySource(initial + baseAddition + prAddition)
	result, mapping, err := reconcile(ancestor, base, pr, working)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(mapping, ",") != "pr: 2 -> 5,pr_later: 3 -> 6" {
		t.Fatalf("mapping=%v", mapping)
	}
	got, err := parseRegistry(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.entries) != 5 {
		t.Fatalf("entries=%v", got.entries)
	}
	for i, want := range []int{1, 2, 4, 5, 6} {
		if got.entries[i].version != want {
			t.Fatalf("entry %d: %+v", i, got.entries[i])
		}
	}
	if !strings.Contains(string(result), `apply(tx, []string{"a", "b"})`) || !strings.Contains(string(result), "Up: prUp") {
		t.Fatal(string(result))
	}
	again, _, err := reconcile(ancestor, base, pr, result)
	if err != nil || string(again) != string(result) {
		t.Fatalf("not idempotent: %v", err)
	}
}

func TestReconcileRefusesAmbiguousChanges(t *testing.T) {
	for _, tc := range []struct {
		name              string
		base, pr, working []byte
	}{
		{"changed released migration", registrySource(initial), registrySource(strings.ReplaceAll(initial, "Up: baseline", "Up: changed")), registrySource(initial)},
		{"duplicate base version", registrySource(initial + prAddition + baseAddition), registrySource(initial + prAddition), registrySource(initial)},
		{"same name different function", registrySource(initial + baseAddition), registrySource(initial + strings.ReplaceAll(prAddition, `Name: "pr"`, `Name: "base"`)), registrySource(initial)},
		{"unresolved conflict", registrySource(initial + baseAddition), registrySource(initial + prAddition), []byte("<<<<<<< HEAD\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := reconcile(registrySource(initial), tc.base, tc.pr, tc.working); err == nil {
				t.Fatal("accepted unsafe registry")
			}
		})
	}
}

// Exercise the command against two real branches adding the same version.
func TestRunAfterGitMigrationConflict(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	if err := os.MkdirAll(filepath.Dir(registryPath), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(src []byte) {
		t.Helper()
		if err := os.WriteFile(registryPath, src, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(registrySource(initial))
	git("add", ".")
	git("commit", "-m", "baseline")
	git("checkout", "-b", "pr")
	write(registrySource(initial + prAddition))
	git("commit", "-am", "PR migrations")
	prHead := git("rev-parse", "HEAD")
	git("checkout", "main")
	write(registrySource(initial + baseAddition))
	git("commit", "-am", "base migrations")
	git("checkout", "pr")
	if out, err := exec.Command("git", "merge", "--no-ff", "main").CombinedOutput(); err == nil || !strings.Contains(string(out), "CONFLICT") {
		t.Fatalf("expected actual merge conflict: %v %s", err, out)
	}
	// Resolve syntax while retaining both additions, then let the helper number them.
	write(registrySource(initial + prAddition + baseAddition))
	if err := run("main", prHead); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := parseRegistry(src)
	if err != nil || len(r.entries) != 5 || r.entries[3].version != 5 || r.entries[4].version != 6 {
		t.Fatalf("registry=%+v err=%v", r, err)
	}
	git("add", registryPath)
	git("commit", "-m", "resolve and renumber")
	git("merge-base", "--is-ancestor", "main", "HEAD")
	git("merge-base", "--is-ancestor", prHead, "HEAD")
	if git("status", "--porcelain") != "" {
		t.Fatal("dirty repair")
	}
}
