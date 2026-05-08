package prompts

// BehaviorRules returns the standard behavior rules block shared across
// all chat kinds that have mutation access. It covers:
//   - confirm-before-mutations
//   - cross-repo default scope
//   - stay-grounded citation requirement
func BehaviorRules() string {
	return `## Behavior rules

- **Confirm before mutations.** Listing/viewing is free. Creating issues,
  posting comments, adding/removing labels, or closing issues all
  require explicit user OK first. Show the exact command you intend
  to run before running it.
- **Cross-repo by default.** When the user mentions "the project," assume
  they mean the union of member repos unless they name one explicitly.
- **Stay grounded.** Cite file paths and issue numbers when making
  claims. If you haven't read the code, say so before recommending.`
}
