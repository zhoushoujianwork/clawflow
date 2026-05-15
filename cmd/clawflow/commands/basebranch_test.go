package commands

import (
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestParseBaseBranchOverride(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		comments    []vcs.IssueComment
		wantBranch  string
		wantSource  string
		wantOK      bool
		wantErrFrag string // non-empty → expect an error containing this string
	}{
		{
			name:       "no marker anywhere",
			body:       "This is a plain issue body with no marker.",
			comments:   nil,
			wantBranch: "",
			wantOK:     false,
		},
		{
			name:       "body-only marker",
			body:       "Please implement on the feature branch.\n\nClawFlow-Base-Branch: feat/new-api",
			comments:   nil,
			wantBranch: "feat/new-api",
			wantSource: "issue body",
			wantOK:     true,
		},
		{
			name: "comment marker wins over body marker",
			body: "ClawFlow-Base-Branch: main",
			comments: []vcs.IssueComment{
				{Author: "alice", Body: "Let's use this branch.\nClawFlow-Base-Branch: feat/override", CreatedAt: "2024-01-02T10:00:00Z"},
			},
			wantBranch: "feat/override",
			wantSource: "comment by alice",
			wantOK:     true,
		},
		{
			name: "latest comment marker wins (multiple comments with markers)",
			body: "ClawFlow-Base-Branch: old-branch",
			comments: []vcs.IssueComment{
				{Author: "bob", Body: "ClawFlow-Base-Branch: second-branch", CreatedAt: "2024-01-02T09:00:00Z"},
				{Author: "carol", Body: "ClawFlow-Base-Branch: latest-branch", CreatedAt: "2024-01-03T12:00:00Z"},
				{Author: "dave", Body: "no marker here", CreatedAt: "2024-01-04T15:00:00Z"},
			},
			wantBranch: "latest-branch",
			wantSource: "comment by carol",
			wantOK:     true,
		},
		{
			name: "only non-marker comments — falls back to body",
			body: "ClawFlow-Base-Branch: codex/saas-worker-foundation",
			comments: []vcs.IssueComment{
				{Author: "alice", Body: "looks good", CreatedAt: "2024-01-02T10:00:00Z"},
			},
			wantBranch: "codex/saas-worker-foundation",
			wantSource: "issue body",
			wantOK:     true,
		},
		{
			name:        "invalid marker in body — empty value",
			body:        "ClawFlow-Base-Branch: ",
			comments:    nil,
			wantOK:      false, // regex won't match (requires \S+)
			wantErrFrag: "",    // no error either — regex filters it
		},
		{
			name:        "invalid marker — leading dash",
			body:        "ClawFlow-Base-Branch: -foo",
			comments:    nil,
			wantOK:      true,
			wantErrFrag: "must not start with '-'",
		},
		{
			name:    "invalid marker — path traversal",
			body:    "ClawFlow-Base-Branch: ../etc/passwd",
			comments: nil,
			// "../etc/passwd" starts with "." so the leading-dot check fires
			// before the ".." check. Both are valid rejections.
			wantOK:      true,
			wantErrFrag: "must not start with '.'",
		},
		{
			name:    "invalid marker — shell injection semicolon (no spaces)",
			body:    "ClawFlow-Base-Branch: foo;bar",
			comments: nil,
			// "foo;bar" is captured as a single \S+ token; semicolon is not
			// in the allowed character set so validation rejects it.
			wantOK:      true,
			wantErrFrag: "invalid characters",
		},
		{
			name:    "shell injection with spaces — no marker match",
			body:    "ClawFlow-Base-Branch: foo;rm -rf /",
			comments: nil,
			// The regex requires \S+\s*$ so trailing " -rf /" prevents a match.
			// No marker is found — ok == false, no error.
			wantOK:      false,
			wantErrFrag: "",
		},
		{
			name:        "invalid marker — leading slash",
			body:        "ClawFlow-Base-Branch: /absolute/path",
			comments:    nil,
			wantOK:      true,
			wantErrFrag: "must not start with '/'",
		},
		{
			name:        "invalid marker — leading dot",
			body:        "ClawFlow-Base-Branch: .hidden",
			comments:    nil,
			wantOK:      true,
			wantErrFrag: "must not start with '.'",
		},
		{
			name: "invalid marker in comment overrides valid body",
			body: "ClawFlow-Base-Branch: valid-branch",
			comments: []vcs.IssueComment{
				{Author: "alice", Body: "ClawFlow-Base-Branch: -bad-branch", CreatedAt: "2024-01-02T10:00:00Z"},
			},
			wantOK:      true,
			wantErrFrag: "must not start with '-'",
		},
		{
			name:       "valid branch with dots and underscores via slash",
			body:       "ClawFlow-Base-Branch: release/v1.2.3",
			comments:   nil,
			wantBranch: "release/v1.2.3",
			wantSource: "issue body",
			wantOK:     true,
		},
		{
			name:       "valid branch — simple name no slash",
			body:       "ClawFlow-Base-Branch: main",
			comments:   nil,
			wantBranch: "main",
			wantSource: "issue body",
			wantOK:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			branch, source, ok, err := ParseBaseBranchOverride(tc.body, tc.comments)

			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}

			if tc.wantErrFrag != "" {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tc.wantErrFrag)
				} else if !strings.Contains(err.Error(), tc.wantErrFrag) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrFrag)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tc.wantErrFrag == "" && ok {
				if branch != tc.wantBranch {
					t.Errorf("branch = %q, want %q", branch, tc.wantBranch)
				}
				if source != tc.wantSource {
					t.Errorf("source = %q, want %q", source, tc.wantSource)
				}
			}
		})
	}
}

func TestValidateBranchName(t *testing.T) {
	valid := []string{
		"main",
		"feat/new-api",
		"fix/issue-123",
		"codex/saas-worker-foundation",
		"release/v1.2.3",
		"v0.1.0",
		"my_branch",
	}
	for _, b := range valid {
		if err := validateBranchName(b); err != nil {
			t.Errorf("validateBranchName(%q) unexpected error: %v", b, err)
		}
	}

	invalid := []struct {
		name string
		frag string
	}{
		{"", "empty"},
		{"-foo", "'-'"},
		{"/absolute", "'/'"},
		{".hidden", "'.'"},
		{"foo/../bar", "'..'"},
		{"foo;bar", "invalid characters"},
		{"foo bar", "invalid characters"},
		{"foo\x00bar", "invalid characters"},
	}
	for _, tc := range invalid {
		t.Run("invalid:"+tc.name, func(t *testing.T) {
			err := validateBranchName(tc.name)
			if err == nil {
				t.Errorf("validateBranchName(%q) expected error, got nil", tc.name)
				return
			}
			if !strings.Contains(err.Error(), tc.frag) {
				t.Errorf("validateBranchName(%q) error %q does not contain %q", tc.name, err.Error(), tc.frag)
			}
		})
	}
}
