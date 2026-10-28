package commands

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zhoushoujianwork/clawflow/internal/offloadtest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestConflictRepairDispatchAndSharedBudget(t *testing.T) {
	f, readLog, reg, cfg := repairAcceptanceFixture(t, "passed")
	cfg.CIRequired = false // A conflict needs repair even without CI enrollment.
	f.mergeability = map[int]vcs.MergeStatus{f.pr.Number: vcs.MergeStatusConflict}
	jobs := scanRepair(t, reg, f, cfg, 0)
	if len(jobs) != 1 || !jobs[0].repair.MergeConflict || jobs[0].op.Name != "repair-ci" {
		t.Fatalf("jobs=%v", jobs)
	}
	j := jobs[0]
	now := time.Now().UTC()
	if err := prepareCIRepair(j, now); err != nil {
		t.Fatal(err)
	}
	if j.repair.reason() != "merge_conflict" || j.repair.Attempt != 1 {
		t.Fatalf("repair=%+v", j.repair)
	}
	prompt := j.repair.prompt("go test ./...")
	for _, want := range []string{f.pr.BaseSHA, f.pr.BaseBranch, "git merge origin/<base>", "Never rebase or force-push", "BLOCKED", "go test ./...", "Do not create a new PR"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q: %s", want, prompt)
		}
	}
	logAcceptanceRepair(j, "start")
	logAcceptanceRepair(j, "end", "status", "success")
	if !strings.Contains(readLog(), "run/conflict_repair") || !strings.Contains(readLog(), "base_sha="+f.pr.BaseSHA) {
		t.Fatal(readLog())
	}
	if got := scanRepair(t, reg, f, cfg, 0); len(got) != 0 {
		t.Fatal("cooldown bypassed")
	}
	for i, reason := range []string{repairReasonCI, repairReasonAcceptance} {
		if _, err := reserveRepair(snapshot.DataDir(), "o/r", f.pr, reason, now.Add(time.Duration(i+1)*ciRepairCooldown)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reserveRepair(snapshot.DataDir(), "o/r", f.pr, "merge_conflict", now.Add(time.Hour)); !errors.Is(err, errRepairExhausted) {
		t.Fatalf("budget: %v", err)
	}
	scanRepair(t, reg, f, cfg, 0)
	f.existingComments = append([]string(nil), f.comments...)
	scanRepair(t, reg, f, cfg, 0)
	if len(f.comments) != 1 {
		t.Fatalf("exhaustion comments=%v", f.comments)
	}
}

func TestConflictRepairIgnoresNonConflictsAndRechecks(t *testing.T) {
	f, _, reg, cfg := repairAcceptanceFixture(t, "passed")
	cfg.CIRequired, cfg.AcceptanceRequired = false, false
	for _, status := range []vcs.MergeStatus{vcs.MergeStatusClean, vcs.MergeStatusPending, vcs.MergeStatusUnknown} {
		f.mergeability = map[int]vcs.MergeStatus{f.pr.Number: status}
		if len(scanRepair(t, reg, f, cfg, 0)) != 0 {
			t.Fatalf("dispatched %s", status)
		}
	}
	f.mergeability = map[int]vcs.MergeStatus{f.pr.Number: vcs.MergeStatusConflict}
	jobs := scanRepair(t, reg, f, cfg, 0)
	if len(jobs) != 1 {
		t.Fatal("missing conflict job")
	}
	f.mergeability[f.pr.Number] = vcs.MergeStatusClean
	if err := prepareCIRepair(jobs[0], time.Now()); err == nil {
		t.Fatal("stale conflict dispatched")
	}
	f.mergeability[f.pr.Number] = vcs.MergeStatusConflict
	f.pr.HeadRepo = "fork/r"
	if len(scanRepair(t, reg, f, cfg, 0)) != 0 {
		t.Fatal("fork dispatched")
	}
}

func TestConflictRepairConfirmedHeadPreservesHistory(t *testing.T) {
	p, _ := repairFixture()
	p.BaseSHA = strings.Repeat("b", 40)
	j := &runJob{repo: "o/r", repair: &ciRepairContext{PR: p, MergeConflict: true}}
	old := acceptanceGit
	t.Cleanup(func() { acceptanceGit = old })
	for _, mode := range []string{"merged", "unchanged", "unpublished", "missing-base", "missing-head", "dirty"} {
		t.Run(mode, func(t *testing.T) {
			head := strings.Repeat("c", 40)
			if mode == "unchanged" {
				head = p.HeadSHA
			}
			acceptanceGit = func(_ context.Context, _ string, args ...string) (string, error) {
				switch args[0] {
				case "rev-parse":
					return head, nil
				case "ls-remote":
					if mode == "unpublished" {
						return p.HeadSHA + "\trefs/heads/" + p.HeadBranch, nil
					}
					return head + "\trefs/heads/" + p.HeadBranch, nil
				case "merge-base":
					if (mode == "missing-base" && args[2] == p.BaseSHA) || (mode == "missing-head" && args[2] == p.HeadSHA) {
						return "", errors.New("not ancestor")
					}
					return "", nil
				case "status":
					if mode == "dirty" {
						return "UU file.go", nil
					}
					return "", nil
				}
				t.Fatalf("unexpected git %v", args)
				return "", nil
			}
			got, err := confirmedDevelopmentHead(context.Background(), "unused", j)
			if mode == "merged" {
				if err != nil || got != head {
					t.Fatalf("%s %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}

// Exercise the runner's confirmed-head persistence with a fake successful agent.
func TestConflictRepairRecordsDevelopmentProvenance(t *testing.T) {
	for _, mode := range []string{"merged", "unchanged", "unpublished", "missing-base", "missing-head", "dirty"} {
		t.Run(mode, func(t *testing.T) { checkConflictRepairRun(t, mode) })
	}
}

func checkConflictRepairRun(t *testing.T, mode string) {
	f, readLog, reg, cfg := repairAcceptanceFixture(t, "passed")
	source := newIssue329Repo(t)
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote := git("config", "--get", "remote.origin.url")
	git("push", "origin", "HEAD:refs/heads/"+f.pr.HeadBranch)
	git("remote", "set-url", "origin", "https://github.com/o/r.git")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+remote+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/o/r.git")
	f.pr.HeadSHA = git("rev-parse", "HEAD")
	f.openPRs = []vcs.PR{f.pr}
	f.mergeability = map[int]vcs.MergeStatus{f.pr.Number: vcs.MergeStatusConflict}
	offloadtest.Setup(t)
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "acceptance-profile")
	t.Setenv("OFFLOAD_TEST_TEXT", "Merged base and tested. https://github.com/o/r/pull/11\n<!-- clawflow:outcome=agent-implemented -->")
	jobs := scanRepair(t, reg, f, cfg, 0)
	if len(jobs) != 1 {
		t.Fatalf("jobs=%v", jobs)
	}
	client := &setupRepairFake{acceptanceFake: f}
	jobs[0].client = client
	jobs[0].repoCfg.LocalPath = source
	newHead := strings.Repeat("c", 40)
	if mode == "unchanged" {
		newHead = f.pr.HeadSHA
	}
	oldGit := acceptanceGit
	t.Cleanup(func() { acceptanceGit = oldGit })
	acceptanceGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			if len(args) == 2 && args[1] == "HEAD" {
				return newHead, nil
			}
		case "ls-remote":
			if mode == "unpublished" {
				return f.pr.HeadSHA + "\trefs/heads/" + f.pr.HeadBranch, nil
			}
			return newHead + "\trefs/heads/" + f.pr.HeadBranch, nil
		case "merge-base":
			if (mode == "missing-base" && args[2] == f.pr.BaseSHA) || (mode == "missing-head" && args[2] == f.pr.HeadSHA) {
				return "", errors.New("not ancestor")
			}
			return "", nil
		case "status":
			if mode == "dirty" {
				return "UU file.go", nil
			}
			return "", nil
		}
		return oldGit(ctx, dir, args...)
	}
	fired, limited, failed := runOneOperator(context.Background(), jobs[0], time.Minute)
	if (mode == "merged" && (!fired || limited || failed)) || (mode != "merged" && (fired || limited || !failed)) {
		t.Fatalf("fired=%v limited=%v failed=%v", fired, limited, failed)
	}
	parent := filepath.Dir(snapshot.RunDir("o/r", 42, time.Now()))
	entries, _ := os.ReadDir(parent)
	found := false
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(parent, e.Name(), "meta.json"))
		var meta snapshot.RunMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Operator != "repair-ci" {
			continue
		}
		found = true
		if mode != "merged" {
			if meta.Status != "failed" || meta.HeadSHA != "" || meta.Error == "" {
				t.Fatalf("invalid failure meta=%+v", meta)
			}
			continue
		}
		if meta.Status != "success" || meta.HeadSHA != newHead || meta.RepairReason != "merge_conflict" {
			t.Fatalf("meta=%+v", meta)
		}
	}
	if !found {
		t.Fatal("missing repair run")
	}
	if mode != "merged" {
		if len(client.labels) != 0 || len(f.comments) != 0 || len(f.merged) != 0 || strings.Contains(readLog(), "run/automerge") {
			t.Fatalf("validation failure caused writeback/automation: labels=%v comments=%v log=%s", client.labels, f.comments, readLog())
		}
		_, path := ciRepairLedgerPath(snapshot.DataDir(), "o/r", f.pr.Number)
		ledger, err := loadCIRepairLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		if consumed, infra := ledger.counts(); consumed != 1 || infra != 0 {
			t.Fatalf("consumed=%d infra=%d", consumed, infra)
		}
		return
	}
	if _, err := acceptanceDevelopment("o/r", 42, newHead, f.pr.Number); err != nil {
		t.Fatal(err)
	}
}
