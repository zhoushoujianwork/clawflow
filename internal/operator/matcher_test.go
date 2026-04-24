package operator

import "testing"

func TestMatches_TargetMismatch(t *testing.T) {
	issueOp := &Operator{Trigger: Trigger{Target: "issue"}}
	prOp := &Operator{Trigger: Trigger{Target: "pr"}}
	iss := &Subject{IsPR: false}
	pr := &Subject{IsPR: true}

	if !Matches(iss, issueOp) {
		t.Error("issue op should match an issue subject")
	}
	if Matches(iss, prOp) {
		t.Error("pr op should NOT match an issue subject")
	}
	if Matches(pr, issueOp) {
		t.Error("issue op should NOT match a PR subject")
	}
	if !Matches(pr, prOp) {
		t.Error("pr op should match a PR subject")
	}
}

func TestMatches_LabelsRequired(t *testing.T) {
	op := &Operator{Trigger: Trigger{
		Target:         "issue",
		LabelsRequired: []string{"bug", "important"},
	}}
	cases := map[string]struct {
		labels []string
		want   bool
	}{
		"both present":       {[]string{"bug", "important"}, true},
		"both plus extras":   {[]string{"bug", "important", "extra"}, true},
		"only one present":   {[]string{"bug"}, false},
		"only other present": {[]string{"important"}, false},
		"neither present":    {[]string{"unrelated"}, false},
		"empty":              {[]string{}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := &Subject{Labels: c.labels}
			if got := Matches(s, op); got != c.want {
				t.Errorf("labels=%v: got %v want %v", c.labels, got, c.want)
			}
		})
	}
}

func TestMatches_LabelsExcluded(t *testing.T) {
	op := &Operator{Trigger: Trigger{
		Target:         "issue",
		LabelsExcluded: []string{"skip", "wip"},
	}}
	cases := map[string]struct {
		labels []string
		want   bool
	}{
		"neither excluded": {[]string{"bug"}, true},
		"empty":            {[]string{}, true},
		"first excluded":   {[]string{"skip"}, false},
		"second excluded":  {[]string{"wip"}, false},
		"both excluded":    {[]string{"skip", "wip"}, false},
		"mixed":            {[]string{"bug", "skip"}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := &Subject{Labels: c.labels}
			if got := Matches(s, op); got != c.want {
				t.Errorf("labels=%v: got %v want %v", c.labels, got, c.want)
			}
		})
	}
}

func TestMatches_RequiredAndExcludedCombined(t *testing.T) {
	op := &Operator{Trigger: Trigger{
		Target:         "issue",
		LabelsRequired: []string{"bug"},
		LabelsExcluded: []string{"agent-running"},
	}}
	// Required present, excluded absent → match
	if !Matches(&Subject{Labels: []string{"bug"}}, op) {
		t.Error("bug without agent-running should match")
	}
	// Both required and excluded present → no match (excluded wins)
	if Matches(&Subject{Labels: []string{"bug", "agent-running"}}, op) {
		t.Error("excluded label should block even when required is present")
	}
}

func TestMatches_NoRulesAlwaysMatches(t *testing.T) {
	op := &Operator{Trigger: Trigger{Target: "issue"}} // no required, no excluded
	if !Matches(&Subject{Labels: []string{}}, op) {
		t.Error("op with no label rules should match empty-labeled subject")
	}
	if !Matches(&Subject{Labels: []string{"anything"}}, op) {
		t.Error("op with no label rules should match any label set")
	}
}

func TestSubject_HasLabel(t *testing.T) {
	s := &Subject{Labels: []string{"bug", "p1"}}
	if !s.HasLabel("bug") {
		t.Error("HasLabel(bug) should be true")
	}
	if !s.HasLabel("p1") {
		t.Error("HasLabel(p1) should be true")
	}
	if s.HasLabel("missing") {
		t.Error("HasLabel(missing) should be false")
	}

	empty := &Subject{Labels: nil}
	if empty.HasLabel("anything") {
		t.Error("HasLabel on nil Labels should be false")
	}
}
