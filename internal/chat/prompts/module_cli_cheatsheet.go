package prompts

import (
	"fmt"
	"strings"
)

// CLICheatsheet returns the clawflow CLI command reference block,
// filtered to the commands appropriate for the given scope.
//
// This is the single source of truth for all CLI command descriptions.
// All chat builders must call this instead of inlining their own lists.
//
// Key facts about flags (verified against cmd/clawflow/commands/issue.go):
//   - `issue create` has NO --labels flag. To add labels after creation,
//     parse the new issue number from stdout and use `clawflow label add`.
//   - `issue add-sub` requires --parent and --sub (GitHub only).
//   - `issue list-sub` requires --issue (GitHub only).
func CLICheatsheet(scope Scope) string {
	var b strings.Builder

	fmt.Fprintln(&b, "## clawflow CLI cheatsheet")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Use Bash to invoke these. Never use `gh` — clawflow wraps both")
	fmt.Fprintln(&b, "GitHub and GitLab uniformly.")
	fmt.Fprintln(&b)

	// Issues section — always present
	fmt.Fprintln(&b, "### Issues")
	fmt.Fprintln(&b, "- `clawflow issue list --repo <owner/name> [--label foo] [--state open]`")
	fmt.Fprintln(&b, "- `clawflow issue view --repo <owner/name> <number>`")
	fmt.Fprintln(&b, "- `clawflow issue search \"<keywords>\" --repo <owner/name> [--state all] [--json] [--limit N]`")

	if scope == ScopeFull || scope == ScopeRepo {
		fmt.Fprintln(&b, "- `clawflow issue create --repo <owner/name> --title \"...\" [--body \"...\"]`")
		fmt.Fprintln(&b, "  `issue create` has no `--labels` flag. To add labels, parse the")
		fmt.Fprintln(&b, "  new issue number from stdout and follow up with `clawflow label add`.")
	}

	if scope == ScopeFull || scope == ScopeSingleIssue {
		fmt.Fprintln(&b, "- `clawflow issue comment --repo <owner/name> <number> --body \"...\"`")
		fmt.Fprintln(&b, "- `clawflow issue close --repo <owner/name> <number>`")
	} else if scope == ScopeRepo {
		fmt.Fprintln(&b, "- `clawflow issue comment --repo <owner/name> <number> --body \"...\"`")
		fmt.Fprintln(&b, "- `clawflow issue close --repo <owner/name> <number>`")
	}

	// Sub-issue commands — only for scopes that allow cross-issue work
	if scope == ScopeFull {
		fmt.Fprintln(&b, "- `clawflow issue add-sub --repo <owner/name> --parent <n> --sub <n>`")
		fmt.Fprintln(&b, "  (GitHub only — link an existing issue as a sub-issue of another)")
		fmt.Fprintln(&b, "- `clawflow issue list-sub --repo <owner/name> --issue <n> [--json]`")
		fmt.Fprintln(&b, "  (GitHub only — list sub-issues of a parent issue)")
	}

	fmt.Fprintln(&b)

	// PRs section
	fmt.Fprintln(&b, "### PRs")
	fmt.Fprintln(&b, "- `clawflow pr list --repo <owner/name> [--state open]`")
	fmt.Fprintln(&b, "- `clawflow pr view --repo <owner/name> <number>`")
	fmt.Fprintln(&b, "- `clawflow pr comment --repo <owner/name> <number> --body \"...\"`")
	fmt.Fprintln(&b)

	// Labels section
	fmt.Fprintln(&b, "### Labels")
	fmt.Fprintln(&b, "- `clawflow label add --repo <owner/name> --issue <n> --label <name>`")
	fmt.Fprintln(&b, "- `clawflow label remove --repo <owner/name> --issue <n> --label <name>`")

	return strings.TrimRight(b.String(), "\n")
}
