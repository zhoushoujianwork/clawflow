package acceptance

// UI checks have no browser fixtures: the repository verifies the web bundle
// by building and type-checking it in CI, and reviewers judge UI criteria
// from source.
func renderReviewInstruction() string {
	return " UI evidence policy: there are no browser render fixtures. Do not launch Chromium, Playwright or another browser inside the agent sandbox. Verify UI layout, grouping and interaction criteria by inspecting the exact-head source (components, routes, styles, API wiring) as source proof; a successful exact-head web build/typecheck CI job is execution proof that the bundle compiles, not proof of layout. Do not mark UI criteria blocked only because no rendered screenshot or render job exists, and never report infrastructure_error or retry a local browser for missing render evidence. "
}
