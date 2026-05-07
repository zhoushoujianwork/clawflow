package pilot

import (
	"strings"
	"testing"
)

func TestParseHealthCheckOutput_Healthy(t *testing.T) {
	out := `## Health summary

All repos and project docs are healthy. No changes proposed.

<!-- clawflow:project-outcome=healthy -->
`
	res := parseHealthCheckOutput(out)
	if res.Outcome != "healthy" {
		t.Fatalf("outcome = %q, want healthy", res.Outcome)
	}
	if len(res.Changes) != 0 {
		t.Fatalf("changes = %d, want 0", len(res.Changes))
	}
	if !strings.Contains(res.Summary, "All repos and project docs are healthy") {
		t.Fatalf("summary missing expected text: %q", res.Summary)
	}
}

func TestParseHealthCheckOutput_ChangesProposed(t *testing.T) {
	out := `## Health summary

- ` + "`acme/api`" + ` — missing deployment, shallow testing
- ` + "`acme/web`" + ` — healthy
- Project context.md — needs update for new repo
- Project testing.md — healthy

## Proposed changes

<!-- clawflow:propose target=repo:acme/api path=CLAUDE.md action=update -->
# acme/api

Updated CLAUDE.md content here.

## Deployment
Deployed via GitHub Actions to staging.acme.com.
<!-- clawflow:propose-end -->

<!-- clawflow:propose target=project path=context.md action=update -->
# Project context

This project has 2 repos:
- acme/api: backend
- acme/web: frontend
<!-- clawflow:propose-end -->

<!-- clawflow:project-outcome=changes-proposed -->
`
	res := parseHealthCheckOutput(out)
	if res.Outcome != "changes-proposed" {
		t.Fatalf("outcome = %q, want changes-proposed", res.Outcome)
	}
	if len(res.Changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(res.Changes))
	}

	c0 := res.Changes[0]
	if c0.Target != "repo" || c0.RepoID != "acme/api" || c0.Path != "CLAUDE.md" || c0.Action != "update" {
		t.Fatalf("change[0] = %+v", c0)
	}
	if !strings.Contains(c0.ProposedContent, "Updated CLAUDE.md content here.") {
		t.Fatalf("change[0] proposed_content missing body: %q", c0.ProposedContent)
	}
	if strings.Contains(c0.ProposedContent, "clawflow:propose-end") {
		t.Fatalf("change[0] content leaked the closing marker: %q", c0.ProposedContent)
	}

	c1 := res.Changes[1]
	if c1.Target != "project" || c1.RepoID != "" || c1.Path != "context.md" || c1.Action != "update" {
		t.Fatalf("change[1] = %+v", c1)
	}
	if !strings.Contains(c1.ProposedContent, "This project has 2 repos") {
		t.Fatalf("change[1] proposed_content missing body: %q", c1.ProposedContent)
	}
}

func TestParseHealthCheckOutput_CreateAction(t *testing.T) {
	out := `## Health summary
- ` + "`acme/new`" + ` — no CLAUDE.md present, draft created

## Proposed changes

<!-- clawflow:propose target=repo:acme/new path=CLAUDE.md action=create -->
# acme/new

Brand new file.
<!-- clawflow:propose-end -->

<!-- clawflow:project-outcome=changes-proposed -->
`
	res := parseHealthCheckOutput(out)
	if len(res.Changes) != 1 || res.Changes[0].Action != "create" {
		t.Fatalf("expected one create change, got %+v", res.Changes)
	}
}

func TestParseHealthCheckOutput_LastOutcomeWins(t *testing.T) {
	// If the model accidentally emits the marker mid-output and then
	// emits it again at the end, the *last* one wins — same rule as
	// issue-operator outcome markers.
	out := `bogus prefix <!-- clawflow:project-outcome=healthy -->

## Health summary
- ` + "`x`" + ` — needs update

## Proposed changes
<!-- clawflow:propose target=repo:x path=CLAUDE.md action=update -->
new
<!-- clawflow:propose-end -->

<!-- clawflow:project-outcome=changes-proposed -->
`
	res := parseHealthCheckOutput(out)
	if res.Outcome != "changes-proposed" {
		t.Fatalf("outcome = %q, want changes-proposed (last marker wins)", res.Outcome)
	}
}

func TestStripFrontmatter(t *testing.T) {
	in := `---
name: pm-health-check
description: "test"
---

# Body
hello
`
	body, err := stripFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimLeft(body, "\n"), "# Body") {
		t.Fatalf("body = %q (expected to start with # Body after leading newlines)", body)
	}
	if strings.Contains(body, "name: pm-health-check") {
		t.Fatalf("frontmatter leaked into body: %q", body)
	}
}

func TestStripFrontmatter_NoFrontmatter(t *testing.T) {
	in := "no frontmatter, just body\n"
	body, err := stripFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	if body != in {
		t.Fatalf("body should pass through unchanged, got %q", body)
	}
}
