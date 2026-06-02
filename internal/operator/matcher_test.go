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

func TestMatches_LabelsRequiredAny(t *testing.T) {
	op := &Operator{Trigger: Trigger{
		Target:            "issue",
		LabelsRequiredAny: []string{"feat", "feature"},
	}}
	cases := map[string]struct {
		labels []string
		want   bool
	}{
		"feat present":        {[]string{"feat"}, true},
		"feature present":     {[]string{"feature"}, true},
		"both present":        {[]string{"feat", "feature"}, true},
		"neither present":     {[]string{"bug"}, false},
		"empty":               {[]string{}, false},
		"feat plus extras":    {[]string{"feat", "urgent"}, true},
		"feature plus extras": {[]string{"feature", "urgent"}, true},
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

func TestMatches_LabelsRequiredAndRequiredAnyCombined(t *testing.T) {
	op := &Operator{Trigger: Trigger{
		Target:            "issue",
		LabelsRequired:    []string{"urgent"},
		LabelsRequiredAny: []string{"feat", "feature"},
	}}
	// Both AND and OR satisfied
	if !Matches(&Subject{Labels: []string{"urgent", "feat"}}, op) {
		t.Error("urgent + feat should match")
	}
	// OR satisfied but AND missing
	if Matches(&Subject{Labels: []string{"feat"}}, op) {
		t.Error("feat without urgent should not match")
	}
	// AND satisfied but OR missing
	if Matches(&Subject{Labels: []string{"urgent", "bug"}}, op) {
		t.Error("urgent + bug without feat/feature should not match")
	}
}

func TestMatches_AppliesTo(t *testing.T) {
	cases := map[string]struct {
		appliesTo string
		subTotal  int
		want      bool
	}{
		"parent matches issue with sub-issues":  {AppliesParent, 3, true},
		"parent rejects leaf issue":             {AppliesParent, 0, false},
		"leaf matches issue without sub-issues": {AppliesLeaf, 0, true},
		"leaf rejects parent issue":             {AppliesLeaf, 2, false},
		"any matches parent":                    {AppliesAny, 5, true},
		"any matches leaf":                      {AppliesAny, 0, true},
		"empty (back-compat) matches parent":    {"", 5, true},
		"empty (back-compat) matches leaf":      {"", 0, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			op := &Operator{Trigger: Trigger{Target: "issue", AppliesTo: c.appliesTo}}
			s := &Subject{SubTotal: c.subTotal}
			if got := Matches(s, op); got != c.want {
				t.Errorf("applies_to=%q subTotal=%d: got %v want %v", c.appliesTo, c.subTotal, got, c.want)
			}
		})
	}
}

// Structural gate composes with label rules: required labels still apply, and
// a structural miss is reported even when labels match.
func TestMatches_AppliesToWithLabels(t *testing.T) {
	op := &Operator{Trigger: Trigger{
		Target:         "issue",
		LabelsRequired: []string{"progress-check"},
		AppliesTo:      AppliesParent,
	}}
	// Labels match but subject is a leaf → reject on structure.
	if ok, reason := MatchesWithReason(&Subject{Labels: []string{"progress-check"}, SubTotal: 0}, op); ok {
		t.Error("leaf should be rejected by applies_to=parent even with matching labels")
	} else if reason == "" {
		t.Error("expected a non-empty structural reason")
	}
	// Labels match and subject is a parent → match.
	if !Matches(&Subject{Labels: []string{"progress-check"}, SubTotal: 2}, op) {
		t.Error("parent with matching labels should match")
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
