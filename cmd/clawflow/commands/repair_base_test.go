package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestAcceptanceRepairOlderContractBaseProceeds(t *testing.T) {
	f, _, reg, cfg := repairAcceptanceFixture(t, "failed")
	recordVerdict(t, f)
	jobs := scanRepair(t, reg, f, cfg, 0)
	if len(jobs) != 1 {
		t.Fatalf("jobs=%v", jobs)
	}
	if err := prepareCIRepair(jobs[0], time.Now()); err != nil {
		t.Fatal(err)
	}
	// The PR API can retain the contract base while the actual branch advances.
	checkRepairFetchedBase(t, jobs[0], false)
}

func TestConflictRepairOlderContractBaseUsesFetchedBase(t *testing.T) {
	f, _, reg, cfg := repairAcceptanceFixture(t, "passed")
	f.mergeability = map[int]vcs.MergeStatus{f.pr.Number: vcs.MergeStatusConflict}
	jobs := scanRepair(t, reg, f, cfg, 0)
	if len(jobs) != 1 {
		t.Fatalf("jobs=%v", jobs)
	}
	if err := prepareCIRepair(jobs[0], time.Now()); err != nil {
		t.Fatal(err)
	}
	checkRepairFetchedBase(t, jobs[0], true)
}

func checkRepairFetchedBase(t *testing.T, j *runJob, conflictRepair bool) {
	t.Helper()
	oldGit := acceptanceGit
	t.Cleanup(func() { acceptanceGit = oldGit })
	acceptanceGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "conflict"}[conflict], func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			write := func(name, content string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-q", "-b", "main")
			git("config", "user.name", "Test")
			git("config", "user.email", "test@example.com")
			write("code", "original\n")
			git("add", ".")
			git("commit", "-qm", "initial")
			stale := git("rev-parse", "HEAD")
			git("checkout", "-qb", "repair")
			write("code", "head\n")
			git("commit", "-qam", "head")
			head := git("rev-parse", "HEAD")
			git("checkout", "-q", "main")
			if conflict {
				write("code", "base\n")
			} else {
				write("other", "base\n")
			}
			git("add", ".")
			git("commit", "-qm", "base")
			current := git("rev-parse", "HEAD")
			git("checkout", "-q", "repair")
			git("remote", "add", "origin", dir)
			j.repair.PR.BaseBranch, j.repair.PR.BaseSHA = "main", stale
			j.repair.PR.HeadBranch = "repair"
			if err := refreshRepairBase(context.Background(), dir, j); err != nil {
				t.Fatal(err)
			}
			if j.repair.PR.BaseSHA != current {
				t.Fatalf("base=%s want=%s", j.repair.PR.BaseSHA, current)
			}
			prompt := j.repair.prompt("go test ./...")
			for _, want := range []string{current, "information only", "log the mismatch and continue", "Never rebase or force-push"} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("missing %q: %s", want, prompt)
				}
			}
			if strings.Contains(prompt, stale) {
				t.Fatal("prompt retained stale base")
			}
			cmd := exec.Command("git", "-C", dir, "merge-tree", "--write-tree", "HEAD", "origin/main")
			_, err := cmd.CombinedOutput()
			if conflict {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
					t.Fatalf("expected conflict: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if conflict || conflictRepair {
				cmd = exec.Command("git", "-C", dir, "merge", "--no-ff", "--no-edit", "origin/main")
				out, err := cmd.CombinedOutput()
				if conflict {
					if err == nil {
						t.Fatal("expected merge conflict")
					}
					write("code", "head and base\n")
					git("add", "code")
					git("commit", "-qm", "resolve")
				} else if err != nil {
					t.Fatalf("merge: %v %s", err, out)
				}
				git("merge-base", "--is-ancestor", current, "HEAD")
				git("merge-base", "--is-ancestor", head, "HEAD")
			} else if git("rev-parse", "HEAD") != head {
				t.Fatal("clean acceptance repair created a merge")
			}
		})
	}
}

func TestRepairSkillBaseMovementPolicy(t *testing.T) {
	data, err := os.ReadFile("../../../skills/repair-ci/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Base movement alone is not a reason to stop",
		"git merge-tree --write-tree HEAD origin/<base>",
		"exit 0 means clean and requires",
		"exit 1 means conflicts and requires the merge",
		"git merge --no-ff origin/<base>",
		"no base merge was needed",
		"never force or force-with-lease",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing base movement policy %q", want)
		}
	}
}

func TestRepairSkillMigrationCollisionPolicy(t *testing.T) {
	data, err := os.ReadFile("../../../skills/repair-ci/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Preserve every base migration's version", "next free version above the base maximum", "preserve base 5/6", "addition version 7", "Run migration validation tests", "never renumber an"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing migration collision policy %q", want)
		}
	}
}
