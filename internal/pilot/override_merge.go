package pilot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type overrideRefusal string

func (r *overrideRefusal) Error() string { return string(*r) }
func refuseOverride(reason string) error { r := overrideRefusal(reason); return &r }

// OverrideMerged is wired by the CLI to the normal self-upgrade merge trigger.
var OverrideMerged = func(string) {}

// Reasons ClawFlow may positively establish for a missing acceptance verdict.
const (
	NoVerdictProvenance  = "provenance"
	NoVerdictInfraBudget = "infra_exhausted"
)

// OverrideNoVerdictReason is wired by the CLI to the acceptance contract and
// harvest infrastructure budget checks. It returns NoVerdictProvenance when the
// contract for the current head fails with the development-provenance error,
// NoVerdictInfraBudget when the infrastructure budget is exhausted, and "" when
// no positive reason exists. The default establishes nothing, so case (a) fails
// closed unless the CLI wiring is present.
var OverrideNoVerdictReason = func(context.Context, vcs.Client, string, int) (string, error) { return "", nil }

func overrideStorePath(repo string, pr int) string {
	b, _ := json.Marshal(repo)
	return filepath.Join(snapshot.DataDir(), "acceptance", fmt.Sprintf("%x", sha256.Sum256(b)), strconv.Itoa(pr))
}

func (e ActionExecutor) overrideMerge(a Action, run string, prior map[string]ActionRecord) error {
	rc := e.Config.Repos[a.Repo]
	if !rc.PilotOverrideMerge {
		return refuseOverride("override_merge_disabled")
	}
	key := "acceptance-"
	b, _ := json.Marshal(a.Repo)
	key += fmt.Sprintf("%x", sha256.Sum256(b))
	if err := snapshot.AcquireLock(key, a.PR, "acceptance"); err != nil {
		return refuseOverride("acceptance_busy")
	}
	defer snapshot.ReleaseLock(key, a.PR)
	c, err := e.Client(a.Repo)
	if err != nil {
		return err
	}
	merger, ok := c.(vcs.ConditionalMerger)
	if !ok {
		return refuseOverride("unsupported_platform")
	}
	ci, ok := c.(vcs.CIEvidenceProvider)
	if !ok {
		return refuseOverride("unsupported_platform")
	}
	pr, err := c.GetPR(a.Repo, a.PR)
	if err != nil {
		return err
	}
	base := rc.BaseBranch
	if base == "" {
		base = "main"
	}
	if pr.Number != a.PR || pr.State != "open" || pr.Draft || pr.IsMerged() || !strings.EqualFold(pr.HeadRepo, a.Repo) || pr.BaseBranch != base || !rerunHead.MatchString(pr.BaseSHA) {
		return refuseOverride("unmanaged_pr")
	}
	if pr.HeadSHA != a.Head {
		return refuseOverride("head_changed")
	}
	evidence, err := ci.GetCIEvidence(a.Repo, a.PR)
	if err != nil || !evidence.SuccessfulFor(a.Head) {
		return refuseOverride("ci_not_successful")
	}
	path := overrideStorePath(a.Repo, a.PR)
	// Do not let OpenStore create an absent evidence store and prove false absence.
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return refuseOverride("verdict_present_or_unreadable")
	}
	store, err := acceptance.OpenStore(path)
	if err != nil {
		return refuseOverride("verdict_present_or_unreadable")
	}
	defer store.Close()
	records, err := store.RecordsAtHead(a.Repo, a.PR, a.Head)
	if err != nil {
		return refuseOverride("verdict_present_or_unreadable")
	}
	// Infrastructure-only attempts never produced a reviewer verdict, so they
	// do not count as one. Any other record, including an incomplete one, does.
	verdicts := 0
	for _, r := range records {
		if !acceptance.IsInfrastructure(r) {
			verdicts++
		}
	}
	gate := "no_verdict"
	var findings []acceptance.Finding
	if verdicts == 0 {
		// No record is necessary but not sufficient: ClawFlow itself must show
		// that provenance or the infrastructure budget explains the absence.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		reason, err := OverrideNoVerdictReason(ctx, c, a.Repo, a.PR)
		cancel()
		if err != nil || (reason != NoVerdictProvenance && reason != NoVerdictInfraBudget) {
			return refuseOverride("no_positive_reason")
		}
		gate = "no_verdict:" + reason
	} else {
		r := records[len(records)-1]
		if !r.Completed || r.Cancelled || r.Verdict != acceptance.Failed {
			return refuseOverride("verdict_present_or_unreadable")
		}
		if r.Contract.BaseSHA != pr.BaseSHA {
			return refuseOverride("acceptance_stale")
		}
		for _, f := range r.Findings {
			if f.Failing() {
				findings = append(findings, f)
			}
		}
		if len(findings) == 0 {
			return refuseOverride("findings_without_locations")
		}
		if err := outsideDiff(rc.LocalPath, pr.BaseSHA, a.Head, findings); err != nil {
			return err
		}
		gate = "failed_outside_diff"
	}
	required := len(findings)
	if required == 0 {
		required = 1
	}
	if len(a.Followups) != required {
		return refuseOverride("followups_required")
	}
	seen := map[int]bool{}
	links := []int{}
	for _, id := range a.Followups {
		r, ok := prior[run+"/"+id]
		if !ok || r.Outcome != "applied" || r.Action.Type != "create_issue" || r.Action.Repo != a.Repo || r.CreatedIssue <= 0 || seen[r.CreatedIssue] {
			return refuseOverride("followups_required")
		}
		seen[r.CreatedIssue] = true
		links = append(links, r.CreatedIssue)
	}
	audit := fmt.Sprintf("Pilot override_merge\n\nHead: `%s`\nBase: `%s`\nBypassed gate: `%s`\nActor: %s\nRun: %s\n", a.Head, pr.BaseSHA, gate, e.Actor, run)
	for i, n := range links {
		audit += fmt.Sprintf("\nFollow-up: #%d", n)
		if len(findings) > 0 {
			loc, _ := json.Marshal(findings[i].Locations)
			audit += fmt.Sprintf("; finding %d: all locations outside changed head hunks: `%s`", i+1, loc)
		}
	}
	current, err := c.GetPR(a.Repo, a.PR)
	if err != nil {
		return err
	}
	if current.HeadSHA != a.Head || current.BaseSHA != pr.BaseSHA || current.State != "open" || current.Draft || current.IsMerged() {
		return refuseOverride("head_changed")
	}
	evidence, err = ci.GetCIEvidence(a.Repo, a.PR)
	if err != nil || !evidence.SuccessfulFor(a.Head) {
		return refuseOverride("ci_not_successful")
	}
	id := fmt.Sprintf("pilot-override-%x", sha256.Sum256([]byte(run+"/"+a.ID)))
	if err := c.PostIssueCommentIdempotent(a.Repo, a.PR, audit, id); err != nil {
		return err
	}
	if _, err := merger.MergePRAtHead(a.Repo, a.PR, a.Head); err != nil {
		return err
	}
	if rc.SelfUpgrade {
		OverrideMerged(a.Repo)
	}
	return nil
}

var headHunk = regexp.MustCompile(`(?m)^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,([0-9]+))? @@`)

// Check each path independently, so quoted/unusual filenames never affect hunk
// association. Immutable commits and literal pathspecs avoid revision injection.
func outsideDiff(dir, base, head string, findings []acceptance.Finding) error {
	if dir == "" || !rerunHead.MatchString(base) || !rerunHead.MatchString(head) {
		return refuseOverride("diff_unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		return cmd.Output()
	}
	for _, f := range findings {
		if len(f.Locations) == 0 {
			return refuseOverride("findings_without_locations")
		}
		for _, l := range f.Locations {
			if l.Validate() != nil {
				return refuseOverride("invalid_location")
			}
			content, err := git("cat-file", "blob", head+":"+l.Path)
			if err != nil || strings.ContainsRune(string(content), 0) {
				return refuseOverride("invalid_location")
			}
			lines := strings.Count(string(content), "\n")
			if len(content) > 0 && content[len(content)-1] != '\n' {
				lines++
			}
			if l.Line > lines {
				return refuseOverride("invalid_location")
			}
			diff, err := git("diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=0", base+"..."+head, "--", ":(literal)"+l.Path)
			if err != nil {
				return refuseOverride("diff_unavailable")
			}
			if strings.Contains(string(diff), "Binary files ") {
				return refuseOverride("diff_unavailable")
			}
			for _, m := range headHunk.FindAllStringSubmatch(string(diff), -1) {
				start, e1 := strconv.Atoi(m[1])
				count := 1
				var e2 error
				if m[2] != "" {
					count, e2 = strconv.Atoi(m[2])
				}
				if e1 != nil || e2 != nil {
					return errors.New("invalid git hunk")
				}
				if l.Line >= start && l.Line-start < count {
					return refuseOverride("finding_inside_diff")
				}
			}
		}
	}
	return nil
}
