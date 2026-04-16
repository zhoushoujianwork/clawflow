package vcs

import (
	"testing"
)

func TestParseDependencies(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		comments []string
		want     []Dependency
	}{
		{
			name: "depends-on-merge from body",
			body: "some text\n<!-- clawflow:depends-on #1 #2 -->\nmore text",
			want: []Dependency{
				{IssueNumber: 1, Type: DependsOnMerge},
				{IssueNumber: 2, Type: DependsOnMerge},
			},
		},
		{
			name: "depends-on-merge explicit keyword",
			body: "<!-- clawflow:depends-on-merge #3 -->",
			want: []Dependency{
				{IssueNumber: 3, Type: DependsOnMerge},
			},
		},
		{
			name: "depends-on-pr from comment",
			body: "no deps in body",
			comments: []string{
				"<!-- clawflow:depends-on-pr #5 -->",
			},
			want: []Dependency{
				{IssueNumber: 5, Type: DependsOnPR},
			},
		},
		{
			name: "mixed types",
			body: "<!-- clawflow:depends-on #1 -->\n<!-- clawflow:depends-on-pr #2 -->",
			want: []Dependency{
				{IssueNumber: 1, Type: DependsOnMerge},
				{IssueNumber: 2, Type: DependsOnPR},
			},
		},
		{
			name: "dedup same issue same type",
			body: "<!-- clawflow:depends-on #1 -->",
			comments: []string{
				"<!-- clawflow:depends-on #1 -->",
			},
			want: []Dependency{
				{IssueNumber: 1, Type: DependsOnMerge},
			},
		},
		{
			name: "no annotation",
			body: "just a regular issue body",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseDependencies(tt.body, tt.comments)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d deps, want %d: %v", len(got), len(tt.want), got)
			}
			for i, d := range got {
				if d != tt.want[i] {
					t.Errorf("dep[%d] = %+v, want %+v", i, d, tt.want[i])
				}
			}
		})
	}
}

func TestContainsIssue(t *testing.T) {
	deps := []Dependency{
		{IssueNumber: 1, Type: DependsOnMerge},
		{IssueNumber: 3, Type: DependsOnPR},
	}
	if !ContainsIssue(deps, 1) {
		t.Error("expected to contain #1")
	}
	if ContainsIssue(deps, 2) {
		t.Error("should not contain #2")
	}
}

func TestFilterByType(t *testing.T) {
	deps := []Dependency{
		{IssueNumber: 1, Type: DependsOnMerge},
		{IssueNumber: 2, Type: DependsOnPR},
		{IssueNumber: 3, Type: DependsOnMerge},
	}
	merge := FilterByType(deps, DependsOnMerge)
	if len(merge) != 2 {
		t.Errorf("expected 2 merge deps, got %d", len(merge))
	}
	pr := FilterByType(deps, DependsOnPR)
	if len(pr) != 1 {
		t.Errorf("expected 1 pr dep, got %d", len(pr))
	}
}
