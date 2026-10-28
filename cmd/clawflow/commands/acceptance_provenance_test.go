package commands

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// The origin transport is redirected locally; all graph, fetch and review
// checkout operations use real Git repositories.
func provenanceGitFixture(t *testing.T, f *acceptanceFake) (func(...string) string, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	git("commit", "--allow-empty", "-m", "root")
	git("checkout", "-b", f.pr.HeadBranch)
	git("commit", "--allow-empty", "-m", "development")
	proven := git("rev-parse", "HEAD")
	run := writeAcceptanceDevelopment(t, "success", time.Now().Add(-2*time.Minute), nil)
	updateDevelopmentHead(t, run, proven)
	acceptanceGit = func(ctx context.Context, workspace string, args ...string) (string, error) {
		if args[0] == "config" {
			return "https://github.com/o/r.git", nil
		}
		args = append([]string(nil), args...)
		if len(args) == 4 && args[0] == "remote" && args[1] == "add" {
			args[3] = dir
		}
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, args...)...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	return git, proven
}

func TestAcceptanceBaseMergeProvenance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		merges int
		extra  string
		want   bool
	}{
		{"base merge", 1, "", true},
		{"five merges", 5, "", true},
		{"six merges", 6, "", false},
		{"arbitrary branch", 1, "arbitrary", false},
		{"non merge on top", 1, "commit", false},
		{"octopus", 1, "octopus", false},
		{"running development", 1, "running", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := acceptanceFixture(t)
			git, proven := provenanceGitFixture(t, f)
			for i := 0; i < tc.merges; i++ {
				git("checkout", "main")
				git("commit", "--allow-empty", "-m", "base update")
				git("checkout", f.pr.HeadBranch)
				git("merge", "--no-ff", "main", "-m", "merge")
			}
			switch tc.extra {
			case "arbitrary", "octopus":
				git("checkout", "-b", "unrelated", proven)
				git("commit", "--allow-empty", "-m", "unrelated")
				other := git("rev-parse", "HEAD")
				git("checkout", f.pr.HeadBranch)
				parents := []string{"commit-tree", "HEAD^{tree}", "-p", proven, "-p", other}
				if tc.extra == "octopus" {
					parents = append(parents, "-p", "main")
				}
				merge := git(append(parents, "-m", "Merge main")...)
				git("reset", "--hard", merge)
			case "commit":
				git("commit", "--allow-empty", "-m", "outside development")
			case "running":
				writeAcceptanceDevelopment(t, "running", time.Now(), nil)
			}
			f.pr.HeadSHA = git("rev-parse", "HEAD")
			f.pr.BaseSHA = git("rev-parse", "main")
			c, _, _, _, err := acceptanceContract(context.Background(), f, "o/r", 11)
			if (err == nil) != tc.want {
				t.Fatalf("contract error = %v, want success %v", err, tc.want)
			}
			if tc.want && (c.HeadSHA != f.pr.HeadSHA || c.Development.RunID != "dev-run") {
				t.Fatalf("wrong inherited contract: %+v", c)
			}
		})
	}
}

func TestHarvestAcceptsBaseMergeProvenance(t *testing.T) {
	f, logs, calls := harvestFixture(t)
	git, proven := provenanceGitFixture(t, f)
	f.pr.HeadSHA = proven
	f.pr.BaseSHA = git("rev-parse", "main")
	produceAcceptance(t, f)
	git("checkout", "main")
	git("commit", "--allow-empty", "-m", "base update")
	git("checkout", f.pr.HeadBranch)
	git("merge", "--no-ff", "main", "-m", "merge")
	f.pr.HeadSHA = git("rev-parse", "HEAD")
	f.pr.BaseSHA = git("rev-parse", "main")
	f.openPRs = []vcs.PR{f.pr}
	if _, _, err := currentAcceptance(context.Background(), f, "o/r", 11); !errors.Is(err, errAcceptanceMissing) {
		t.Fatalf("old-head acceptance must not satisfy merged head: %v", err)
	}
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 1 {
		t.Fatalf("failures=%v calls=%d", failed, *calls)
	}
	if len(f.expected) != 1 || f.expected[0] != f.pr.HeadSHA {
		t.Fatalf("accepted heads=%v", f.expected)
	}
	if log := logs(); !strings.Contains(log, "acceptance/provenance_inherited") || !strings.Contains(log, proven) || !strings.Contains(log, f.pr.HeadSHA) {
		t.Fatalf("missing inheritance log: %s", log)
	}
}
