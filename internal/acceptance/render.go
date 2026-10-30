package acceptance

import (
	"encoding/json"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Render execution belongs on the Linux CI runner, outside the review sandbox.
func renderReviewInstruction(head string, ci vcs.CIEvidence) string {
	status := "blocked"
	var checks []vcs.CICheck
	if head != "" && ci.HeadSHA == head {
		for _, check := range ci.Checks {
			if check.Name == "Web render" && check.HeadSHA == head && check.ID > 0 && check.App.Slug == "github-actions" {
				checks = append(checks, check)
			}
		}
	}
	// Ambiguous identities cannot establish proof. The provider requests latest checks.
	if len(checks) == 1 && checks[0].Status == "completed" && checks[0].Conclusion != nil {
		switch *checks[0].Conclusion {
		case "success":
			status = "passed"
		case "failure", "timed_out", "action_required":
			status = "failed"
		}
	}
	evidence, _ := json.Marshal(checks)
	return " UI render evidence policy: map UI layout (including 1440px/390px), grouping and interaction criteria to the dedicated GitHub Actions job Web render in .github/workflows/ci.yml, which runs all web/scripts/check-*-render.mjs fixtures. Do not launch Chromium, Playwright or another browser inside the agent sandbox. Inspect the exact-head fixtures to establish coverage of each criterion; a green aggregate or unrelated fixture is not proof. Authoritative exact-head Web render status: " + status + "; checks: " + string(evidence) + ". A successful covered render job is integration execution proof, just like relevant unit-test CI; cite its check ID, submitted head and fixture in criterion evidence. A failed render job means failed for the covered criterion, with the failed job as observation and a finding quoting the requirement. A missing, stale, ambiguous, skipped, cancelled or unfinished job means blocked for criteria needing render execution; request the exact-head Web render job. These render evidence gaps must never produce infrastructure_error or a local browser retry. This UI render policy overrides generic local tooling failure instructions. "
}
