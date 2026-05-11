package pilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// digestWindow is the lookback for the issue digest counts. 24h
// matches the most natural "what happened today" cadence; making it
// configurable would complicate the dashboard without obvious value.
const digestWindow = 24 * time.Hour

// FillIssueDigestCounts populates the count fields on Duties.IssueDigest
// from the locally-cached issues.json so Pilot doesn't have to count.
// Pilot owns Summary (the prose); the runner owns numbers — that
// separation keeps the YAML schema short and the numbers always
// accurate.
//
// memberRepos restricts the count to the project's own repos so a
// project with one repo doesn't see counts inflated by other
// projects' repos in the same issues.json.
//
// Returns silently on any read/parse failure: an empty digest is
// better than no wake at all.
func FillIssueDigestCounts(duties *snapshot.PilotDuties, memberRepos []string) {
	if duties == nil {
		return
	}
	duties.IssueDigest.SinceHours = int(digestWindow / time.Hour)

	path := filepath.Join(snapshot.DataDir(), "issues.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var entries []snapshot.IssueEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return
	}

	memberSet := make(map[string]struct{}, len(memberRepos))
	for _, r := range memberRepos {
		memberSet[r] = struct{}{}
	}

	cutoff := time.Now().UTC().Add(-digestWindow)
	var newCount, closedCount int
	for _, e := range entries {
		if len(memberSet) > 0 {
			if _, ok := memberSet[e.Repo]; !ok {
				continue
			}
		}
		// new in last 24h — counts both still-open and closed-since-open
		// issues, as long as the create timestamp is fresh enough.
		if e.CreatedAt != "" {
			if t, perr := time.Parse(time.RFC3339, e.CreatedAt); perr == nil && t.After(cutoff) {
				newCount++
			}
		}
		// closed in last 24h — use the VCS's own closed_at when
		// available; only fall back to nothing otherwise. Earlier
		// versions proxied via CapturedAt, but CapturedAt is rewritten
		// by every scan (state-change-agnostic), so that proxy
		// counted ALL still-closed issues as "recently closed" after
		// a fresh scan. Wrong number is worse than no number.
		if e.State == "closed" && e.ClosedAt != "" {
			if t, perr := time.Parse(time.RFC3339, e.ClosedAt); perr == nil && t.After(cutoff) {
				closedCount++
			}
		}
	}

	duties.IssueDigest.New = newCount
	duties.IssueDigest.Closed = closedCount
	// labeled / commented require event tracking we don't currently
	// capture locally; left at zero so the dashboard renders them as
	// "—" rather than misleading data. Adding these later means adding
	// an events log on the scanner side, not changing this contract.
}
