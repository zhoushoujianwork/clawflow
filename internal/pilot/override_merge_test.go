package pilot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"gopkg.in/yaml.v3"
)

func diffFixture(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("one\ntwo\nthree\nfour\nfive\n")
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	write("one\nchanged\nthree\nfour\nfive\nadded\n")
	git("add", ".")
	git("commit", "-m", "head")
	return dir, base, git("rev-parse", "HEAD")
}
func TestOutsideDiffRealGit(t *testing.T) {
	dir, base, head := diffFixture(t)
	for _, tt := range []struct {
		line int
		want string
	}{{1, ""}, {2, "finding_inside_diff"}, {3, ""}, {5, ""}, {6, "finding_inside_diff"}, {7, "invalid_location"}} {
		err := outsideDiff(dir, base, head, []acceptance.Finding{{Locations: []acceptance.FindingLocation{{Path: "file.txt", Line: tt.line, Side: "head"}}}})
		if (err == nil) != (tt.want == "") || err != nil && err.Error() != tt.want {
			t.Fatalf("line %d: %v", tt.line, err)
		}
	}
	for _, locations := range [][]acceptance.FindingLocation{nil, {{Path: "../file.txt", Line: 1, Side: "head"}}, {{Path: "file.txt", Line: 1, Side: "base"}}, {{Path: "file.txt", Line: 1, Side: "head"}, {Path: "file.txt", Line: 2, Side: "head"}}} {
		if err := outsideDiff(dir, base, head, []acceptance.Finding{{Locations: locations}}); err == nil {
			t.Fatal("unsafe locations accepted")
		}
	}
}

type overrideClient struct {
	actionClient
	pr         vcs.PR
	ci         vcs.CIEvidence
	commentErr bool
	changed    bool
	gets       int
}

func (c *overrideClient) GetPR(string, int) (vcs.PR, error) {
	c.gets++
	p := c.pr
	if c.changed && c.gets > 1 {
		p.HeadSHA = strings.Repeat("b", 40)
	}
	return p, nil
}
func (c *overrideClient) GetCIEvidence(string, int) (vcs.CIEvidence, error) { return c.ci, nil }
func (c *overrideClient) PostIssueCommentIdempotent(_ string, _ int, body, _ string) error {
	c.calls = append(c.calls, "audit:"+body)
	if c.commentErr {
		return errors.New("audit failed")
	}
	return nil
}
func (c *overrideClient) MergePRAtHead(_ string, _ int, head string) (string, error) {
	if head != c.pr.HeadSHA {
		return "", errors.New("wrong head")
	}
	c.calls = append(c.calls, "merge")
	return "merged", nil
}

func TestOverrideMergeGuards(t *testing.T) {
	dir, base, head := diffFixture(t)
	for _, mode := range []string{"absent", "absent_infra_exhausted", "no_positive_reason", "no_reason_error", "infra_records_no_reason", "failed_outside", "failed_inside", "failed_then_infra", "legacy", "blocked", "passed", "incomplete", "corrupt", "missing_store", "disabled", "ci_failed", "ci_stale", "head_changed", "missing_followup", "audit_failed", "day_limit", "dry_run"} {
		t.Run(mode, func(t *testing.T) {
			e, ac := testExecutor(t)
			e.Config = &config.Config{Repos: map[string]config.Repo{"o/r": {PilotOverrideMerge: true, LocalPath: dir, SelfUpgrade: true}}}
			c := &overrideClient{actionClient: *ac, pr: vcs.PR{Number: 11, State: "open", HeadRepo: "o/r", HeadSHA: head, BaseSHA: base, BaseBranch: "main"}, ci: vcs.CIEvidence{HeadSHA: head, Status: vcs.CIStatusSuccess, CheckIDs: []int64{1}}}
			e.Client = func(string) (vcs.Client, error) { return c, nil }
			store, err := acceptance.OpenStore(overrideStorePath("o/r", 11))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "absent_infra_exhausted" || mode == "infra_records_no_reason" {
				contract := acceptance.Contract{Version: acceptance.Version, TaskID: "task", Generation: 1, Repository: "o/r", PRNumber: 11, HeadSHA: head, BaseSHA: base, PolicyDigest: "policy", Requirements: "requirements", Development: acceptance.Identity{SessionID: "dev", RunID: "dev-run"}}
				digest, err := contract.Digest()
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().Add(-time.Hour)
				for i := 0; i < 3; i++ {
					// Infrastructure attempts: completed, blocked, no reviewer evidence.
					r := acceptance.Record{Version: acceptance.Version, Contract: contract, ContractDigest: digest, AttemptID: fmt.Sprintf("infra%d", i), Role: "acceptance", Completed: true, Verdict: acceptance.Blocked, Report: "provider unavailable", StartedAt: now.Add(time.Duration(i) * time.Minute), CompletedAt: now.Add(time.Duration(i)*time.Minute + time.Second)}
					if err := store.SaveRecord(r); err != nil {
						t.Fatal(err)
					}
				}
			}
			if strings.HasPrefix(mode, "failed_") || mode == "legacy" || mode == "blocked" || mode == "passed" || mode == "incomplete" {
				contract := acceptance.Contract{Version: acceptance.Version, TaskID: "task", Generation: 1, Repository: "o/r", PRNumber: 11, HeadSHA: head, BaseSHA: base, PolicyDigest: "policy", Requirements: "requirements", Development: acceptance.Identity{SessionID: "dev", RunID: "dev-run"}}
				digest, err := contract.Digest()
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now()
				r := acceptance.Record{Version: acceptance.Version, Contract: contract, ContractDigest: digest, AttemptID: "attempt", Role: "acceptance", Completed: true, Verdict: acceptance.Failed, Report: "failed", Evidence: []string{"proof"}, StartedAt: now, CompletedAt: now.Add(time.Second), Findings: []acceptance.Finding{{Kind: "defect", Summary: "defect", Defect: "defect", Evidence: "evidence", Locations: []acceptance.FindingLocation{{Path: "file.txt", Line: 1, Side: "head"}}}}}
				switch mode {
				case "failed_inside":
					r.Findings[0].Locations[0].Line = 2
				case "legacy":
					r.Findings = nil
				case "blocked":
					r.Verdict = acceptance.Blocked
				case "passed":
					r.Verdict = acceptance.Passed
				case "incomplete":
					r.Completed = false
				}
				if err := store.SaveRecord(r); err != nil {
					t.Fatal(err)
				}
				if mode == "failed_then_infra" {
					r.AttemptID = "later-infra"
					r.Verdict = acceptance.Blocked
					r.Evidence = nil
					r.Findings = nil
					r.Report = "provider unavailable"
					r.StartedAt = now.Add(2 * time.Minute)
					r.CompletedAt = now.Add(3 * time.Minute)
					if !acceptance.IsInfrastructure(r) {
						t.Fatal("fixture must be infrastructure-only")
					}
					if err := store.SaveRecord(r); err != nil {
						t.Fatal(err)
					}
				}
			}
			store.Close()
			switch mode {
			case "corrupt":
				os.WriteFile(filepath.Join(overrideStorePath("o/r", 11), "bad.record"), []byte("bad"), 0600)
			case "missing_store":
				os.RemoveAll(overrideStorePath("o/r", 11))
			case "disabled":
				rc := e.Config.Repos["o/r"]
				rc.PilotOverrideMerge = false
				e.Config.Repos["o/r"] = rc
			case "ci_failed":
				c.ci.Status = vcs.CIStatusFailure
			case "ci_stale":
				c.ci.HeadSHA = strings.Repeat("c", 40)
			case "head_changed":
				c.changed = true
			case "audit_failed":
				c.commentErr = true
			case "day_limit":
				e.MaxDay = 1
			}
			oldReason := OverrideNoVerdictReason
			defer func() { OverrideNoVerdictReason = oldReason }()
			OverrideNoVerdictReason = func(context.Context, vcs.Client, string, int) (string, error) {
				switch mode {
				case "absent_infra_exhausted":
					return NoVerdictInfraBudget, nil
				case "no_positive_reason", "infra_records_no_reason":
					return "", nil
				case "no_reason_error":
					return NoVerdictProvenance, errors.New("contract unavailable")
				}
				return NoVerdictProvenance, nil
			}
			old := OverrideMerged
			upgraded := false
			OverrideMerged = func(string) { upgraded = true }
			defer func() { OverrideMerged = old }()
			followups := `["follow"]`
			if mode == "missing_followup" {
				followups = `[]`
			}
			actions := actionSet("wake", `{"id":"follow","type":"create_issue","title":"root cause","body":"follow-up"}`, fmt.Sprintf(`{"id":"merge","type":"override_merge","pr":11,"head":%q,"followups":%s}`, head, followups))
			records, err := e.Apply(actions, mode == "dry_run")
			if err != nil {
				t.Fatal(err)
			}
			r := records[1]
			success := mode == "absent" || mode == "absent_infra_exhausted"
			if success {
				if r.Outcome != "applied" || len(c.calls) != 3 || !strings.HasPrefix(c.calls[1], "audit:") || c.calls[2] != "merge" || !upgraded {
					t.Fatalf("%+v calls=%v upgrade=%v", r, c.calls, upgraded)
				}
				replay, err := e.Apply(actions, false)
				if err != nil || replay[1].Outcome != "already_applied" || len(c.calls) != 3 {
					t.Fatalf("replay: %+v %v", replay, err)
				}
			} else {
				if r.Outcome == "applied" || upgraded {
					t.Fatalf("unsafe merge: %+v", r)
				}
				for _, call := range c.calls {
					if call == "merge" {
						t.Fatal("merged")
					}
				}
			}
			if strings.HasPrefix(mode, "failed_") || mode == "legacy" {
				if r.Reason != "reviewer_verdict_present" {
					t.Fatalf("expected reviewer refusal, got %+v", r)
				}
				for _, call := range c.calls {
					if strings.HasPrefix(call, "audit:") {
						t.Fatal("audited a prohibited override")
					}
				}
			}
			if mode == "dry_run" && (r.Outcome != "valid" || len(c.calls) != 0) {
				t.Fatalf("dry run %+v %v", r, c.calls)
			}
			if mode == "no_positive_reason" || mode == "no_reason_error" || mode == "infra_records_no_reason" {
				if r.Reason != "no_positive_reason" {
					t.Fatalf("%+v", r)
				}
			}
			if mode == "absent" || mode == "absent_infra_exhausted" {
				want := "no_verdict:" + NoVerdictProvenance
				if mode == "absent_infra_exhausted" {
					want = "no_verdict:" + NoVerdictInfraBudget
				}
				if !strings.Contains(c.calls[1], "Bypassed gate: `"+want+"`") {
					t.Fatalf("audit lacks reason %s: %s", want, c.calls[1])
				}
			}
			if mode == "blocked" || mode == "passed" || mode == "incomplete" || mode == "corrupt" || mode == "missing_store" {
				if r.Reason != "verdict_present_or_unreadable" {
					t.Fatalf("%+v", r)
				}
			}
		})
	}
}
func TestOverrideConfigDefault(t *testing.T) {
	var rc config.Repo
	if rc.PilotOverrideMerge {
		t.Fatal("enabled by default")
	}
	if err := yaml.Unmarshal([]byte("pilot_override_merge: on"), &rc); err != nil || !rc.PilotOverrideMerge {
		t.Fatalf("opt-in: %+v %v", rc, err)
	}
}
