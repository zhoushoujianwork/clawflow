package commands

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestDescribeAndUnknownComparison(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"6b1b2ce", "v0.75.0", false}, {"v0.75.0", "v0.75.0-54-ga0ce627", true},
		{"v0.75.0-54-ga0ce627", "v0.75.0", false}, {"v0.75.0-54-ga0ce627", "v0.75.0-55-gb0ce627", true},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", true}, {"v1.0.0-rc.10", "v1.0.0", true},
		{"v1.0.0", "v1.0.0-rc.1", false}, {"v1.0", "v1.1.0", false}, {"v1.0.0-dirty", "bad", false},
	} {
		if got := IsNewerVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("%s -> %s = %v", tc.a, tc.b, got)
		}
	}
}
func TestLargeNumericPrereleaseComparison(t *testing.T) {
	for _, tc := range []struct {
		name, older, newer string
	}{
		{"different lengths", "99999999999999999999", "100000000000000000000"},
		{"same length", "100000000000000000000", "100000000000000000001"},
		{"machine integer boundary", "9", "100000000000000000000"},
		{"numeric before alphanumeric", "100000000000000000000", "100000000000000000000a"},
		{"numeric before hyphen", "100000000000000000000", "-"},
		{"nested identifiers", "rc.99999999999999999999", "rc.100000000000000000000"},
		{"equal large identifier", "100000000000000000000.1", "100000000000000000000.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			older, newer := "v1.0.0-"+tc.older, "v1.0.0-"+tc.newer
			if !IsNewerVersion(older, newer) || IsNewerVersion(newer, older) || IsNewerVersion(newer, newer) {
				t.Errorf("incorrect prerelease ordering: %s < %s", older, newer)
			}
			if err := checkReleaseReplacement(newer, older, "release", false, false); err == nil {
				t.Error("release downgrade accepted without authorization")
			}
			if err := checkReleaseReplacement(older, newer, "release", false, false); err != nil {
				t.Errorf("upgrade rejected: %v", err)
			}
			if err := checkReleaseReplacement(newer, older, "release", false, true); err != nil {
				t.Errorf("explicit downgrade rejected: %v", err)
			}
		})
	}
}

func TestReleaseReplacementPolicy(t *testing.T) {
	for _, tc := range []struct {
		current, channel    string
		explicit, allow, ok bool
	}{
		{"v0.74.0", "release", false, false, true}, {"v0.76.0", "release", false, false, false},
		{"v0.75.0-54-gabcdef0", "source", false, true, false},
		{"v0.75.0-54-gabcdef0", "source", true, false, false},
		{"v0.75.0-54-gabcdef0", "source", true, true, true},
		{"6b1b2ce", "source", true, false, false}, {"dev", "release", false, false, false},
	} {
		if got := checkReleaseReplacement(tc.current, "v0.75.0", tc.channel, tc.explicit, tc.allow) == nil; got != tc.ok {
			t.Errorf("%+v: allowed=%v", tc, got)
		}
	}
}
func TestSourceAncestryRealGit(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s %v", args, b, e)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	git("commit", "--allow-empty", "-m", "first")
	first := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "second")
	head := git("rev-parse", "HEAD")
	git("remote", "add", "origin", dir)
	git("tag", "v9.0.0", first)
	git("tag", "-a", "v10.0.0", "-m", "release", head)
	u := newSelfUpgrader()
	repo := config.Repo{LocalPath: dir, BaseBranch: "main"}
	for _, tc := range []struct {
		commit string
		behind int
		fail   bool
	}{{first, 1, false}, {head, 0, false}, {strings.Repeat("a", 40), 0, true}} {
		u.Commit = tc.commit
		got, n, err := u.SourceStatus(context.Background(), repo)
		if (err != nil) != tc.fail || n != tc.behind || got != head {
			t.Fatalf("%+v: %s %d %v", tc, got, n, err)
		}
	}
	git("checkout", "-b", "diverged", first)
	git("commit", "--allow-empty", "-m", "diverged")
	u.Commit = git("rev-parse", "HEAD")
	if _, _, err := u.SourceStatus(context.Background(), repo); err == nil {
		t.Fatal("diverged commit accepted")
	}
	u.Commit = head
	if err := u.verifyReleaseAncestry(context.Background(), dir, "v9.0.0"); err == nil {
		t.Fatal("numerically newer ancestor release accepted")
	}
	u.Commit = first
	if err := u.verifyReleaseAncestry(context.Background(), dir, "v10.0.0"); err != nil {
		t.Fatalf("annotated descendant release rejected: %v", err)
	}
	u.Commit = ""
	if _, _, err := u.SourceStatus(context.Background(), repo); err == nil {
		t.Fatal("unknown commit accepted")
	}
}
func TestSelfUpgradeRejectsNonAncestorBeforeBuild(t *testing.T) {
	h := newUpgradeHarness(t)
	h.failOn = "merge-base"
	out := h.u.Upgrade(context.Background(), selfUpgradeRepo(t))
	if out.Result != selfUpgradeFailed || h.installed() != "OLD" || h.called("go build") {
		t.Fatalf("unsafe upgrade: %+v", out)
	}
}
func TestSourceStatusEqualHeadDoesNotFetch(t *testing.T) {
	h := newUpgradeHarness(t)
	h.u.Commit = testFullSHA
	_, n, err := h.u.SourceStatus(context.Background(), selfUpgradeRepo(t))
	if err != nil || n != 0 || h.called("git fetch") {
		t.Fatalf("n=%d err=%v calls=%v", n, err, h.calls)
	}
}

func TestSourceBuildMetadata(t *testing.T) {
	flags := sourceBuildFlags("v0.75.0-54-gabcdef0", testFullSHA, "main")
	for _, want := range []string{"Version=v0.75.0-54-gabcdef0", "BuildCommit=" + testFullSHA, "BuildChannel=source", "SourceBranch=main"} {
		if !strings.Contains(flags, want) {
			t.Errorf("missing %s in %s", want, flags)
		}
	}
}
func TestDownloadRefusesSourceBeforeNetwork(t *testing.T) {
	old := BuildChannel
	BuildChannel = "source"
	t.Cleanup(func() { BuildChannel = old })
	if err := downloadLatestBinary(); err == nil || !strings.Contains(err.Error(), "--release") {
		t.Fatalf("expected channel refusal, got %v", err)
	}
}

func TestSelfUpgradeStaleProcessCannotDowngradeInstalledBinary(t *testing.T) {
	h := newUpgradeHarness(t)
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := runSelfUpgradeCmd(context.Background(), dir, "git", args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	git("commit", "--allow-empty", "-m", "A")
	a := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "B")
	b := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "C")
	c := git("rev-parse", "HEAD")
	git("branch", "target", b)
	git("remote", "add", "origin", dir)
	h.u.Version, h.u.Commit = a[:7], a
	// The destination changed while this old process was waiting for the lock.
	h.u.Lock = func() (func(), bool, error) {
		script := "#!/bin/sh\nprintf '%s\\n' '" + c[:7] + "' 'channel=source commit=" + c + " branch=main'\n"
		if err := os.WriteFile(h.dest, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		return func() { h.released++ }, true, nil
	}
	h.u.Run = func(ctx context.Context, cwd, name string, args ...string) ([]byte, error) {
		if len(args) == 1 && args[0] == "version" {
			data, _ := os.ReadFile(name)
			if string(data) == "NEW" {
				return []byte(b[:7] + "\n"), nil
			}
		}
		if name == "git" || name == h.dest {
			return runSelfUpgradeCmd(ctx, cwd, name, args...)
		}
		return h.run(ctx, cwd, name, args...)
	}
	out := h.u.Upgrade(context.Background(), config.Repo{LocalPath: dir, BaseBranch: "target"})
	if out.Result != selfUpgradeFailed || !strings.Contains(out.Reason, "diverged or unknown") {
		t.Fatalf("stale process accepted older target: %+v", out)
	}
	if !strings.Contains(h.installed(), c) || h.called("go build") || h.released != 1 {
		t.Fatalf("destination changed or unnecessary build: %s, calls=%v, releases=%d", h.installed(), h.calls, h.released)
	}
}

func TestSelfUpgradeRejectsUnreadableInstalledIdentity(t *testing.T) {
	for _, output := range []string{"", "unknown\n", "abcdef0\nchannel=source commit=invalid branch=main\n"} {
		t.Run(output, func(t *testing.T) {
			h := newUpgradeHarness(t)
			h.versions = map[string]string{"OLD": output}
			out := h.u.Upgrade(context.Background(), selfUpgradeRepo(t))
			if out.Result != selfUpgradeFailed || h.installed() != "OLD" || h.called("go build") {
				t.Fatalf("unsafe upgrade with unknown installed identity: %+v", out)
			}
		})
	}
}
