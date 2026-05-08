package prompts

// Scope controls which CLI commands are included in the cheatsheet.
// Each chat kind uses a scope appropriate to its permissions.
type Scope int

const (
	// ScopeFull — all commands including issue create, sub-issue management,
	// and PR operations. Used by project chat and Pilot.
	ScopeFull Scope = iota

	// ScopeRepo — repo-level triage: create issues, label, comment, close,
	// list PRs. No sub-issue commands (those are issue-specific). Used by
	// repo chat.
	ScopeRepo

	// ScopeSingleIssue — operations on one specific issue only: label,
	// comment, close. No create, no sub-issue management. Used by issue chat.
	ScopeSingleIssue
)
