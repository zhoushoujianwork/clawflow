package acceptance

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func environmentFixture() (RuntimeProbe, TargetBinding, EnvironmentLayers) {
	p := RuntimeProbe{Candidate: "candidate-head", LoadedCandidate: "candidate-head", ActualCLIPath: "fixture-cli", PathCLIPath: "fixture-cli", HostCLIPath: "fixture-cli", DaemonVersion: "daemon-build", ConnectorVersion: "connector-build", LoggedIn: true, TargetCount: 1, TargetID: "target-nonce", Generation: "connection-nonce"}
	passed := EnvironmentLayer{Status: "passed", Evidence: "observed-fixture-result"}
	binding := TargetBinding{p.TargetID, p.Generation}
	live := passed
	live.Binding = &binding
	return p, binding, EnvironmentLayers{passed, passed, passed, live, live}
}

func TestEnvironmentIdentityGate(t *testing.T) {
	cases := []struct {
		name, missing string
		change        func(*RuntimeProbe, *TargetBinding)
	}{
		{"PATH differs", "cli-path-match", func(p *RuntimeProbe, b *TargetBinding) { p.PathCLIPath = "other-cli" }},
		{"actual CLI differs", "cli-path-match", func(p *RuntimeProbe, b *TargetBinding) { p.ActualCLIPath = "other-cli" }},
		{"host CLI missing", "cli-path-match", func(p *RuntimeProbe, b *TargetBinding) { p.HostCLIPath = "" }},
		{"login missing", "login", func(p *RuntimeProbe, b *TargetBinding) { p.LoggedIn = false }},
		{"connector missing", "connector-version", func(p *RuntimeProbe, b *TargetBinding) { p.ConnectorVersion = "" }},
		{"daemon missing", "daemon-version", func(p *RuntimeProbe, b *TargetBinding) { p.DaemonVersion = "" }},
		{"no target", "unique-target", func(p *RuntimeProbe, b *TargetBinding) { p.TargetCount = 0 }},
		{"multiple targets", "unique-target", func(p *RuntimeProbe, b *TargetBinding) { p.TargetCount = 2 }},
		{"reconnected", "fresh-target-binding", func(p *RuntimeProbe, b *TargetBinding) { p.Generation = "new-connection" }},
		{"document switched", "fresh-target-binding", func(p *RuntimeProbe, b *TargetBinding) { p.TargetID = "new-target" }},
		{"candidate not loaded", "candidate-provenance", func(p *RuntimeProbe, b *TargetBinding) { p.LoadedCandidate = "released-build" }},
		{"candidate missing", "candidate-provenance", func(p *RuntimeProbe, b *TargetBinding) { p.Candidate = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, b, l := environmentFixture()
			tc.change(&p, &b)
			e := NewEnvironmentEvidence(p, b, l)
			if e.Result("candidate-head") == "live-verified" || !strings.Contains(e.missingText("candidate-head"), tc.missing) {
				t.Fatalf("incorrect gate: %s %v", e.Result("candidate-head"), e.Missing("candidate-head"))
			}
			r := structuredRecord()
			r.Contract.HeadSHA = "candidate-head"
			r.Checks[0].Evidence[0] = RequirementEvidence{Type: "live", Check: "fixture", Reference: "observation", Commit: "candidate-head", Result: "passed", Environment: &e}
			// ValidateChecks checks the fixed plan, independently of contract digests.
			if err := ValidateChecks(r.Contract, r); err == nil || !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("report bypassed identity gate: %v", err)
			}
		})
	}
}

func TestEnvironmentLayersRetainFailedAndUnrun(t *testing.T) {
	p, b, l := environmentFixture()
	l.LiveWrite = EnvironmentLayer{Status: "failed", Evidence: "write failed"}
	l.SaveReload = EnvironmentLayer{Status: "not-run", Evidence: "write prerequisite failed"}
	e := NewEnvironmentEvidence(p, b, l)
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var restored EnvironmentEvidence
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e, restored) {
		t.Fatal("transport dropped evidence")
	}
	for _, name := range []string{"environment-ready", "candidate-loaded", "offline", "live-write", "save-reload"} {
		if !strings.Contains(string(data), `"`+name+`"`) {
			t.Fatalf("missing %s", name)
		}
	}
	if restored.Layers.LiveWrite.Status != "failed" || restored.Layers.SaveReload.Status != "not-run" || restored.Result(p.Candidate) == "live-verified" {
		t.Fatal("lost failed or unrun layer")
	}
	privateJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(privateJSON), p.ActualCLIPath) {
		t.Fatal("private probe serialized CLI paths")
	}
	if strings.Contains(string(data), p.ActualCLIPath) {
		t.Fatal("private CLI path leaked")
	}
}

func TestEnvironmentReconnectRequiresNewBinding(t *testing.T) {
	p, b, l := environmentFixture()
	if NewEnvironmentEvidence(p, b, l).Result(p.Candidate) != "live-verified" {
		t.Fatal("valid fixture rejected")
	}
	p.Generation = "reconnected-nonce"
	if NewEnvironmentEvidence(p, b, l).Result(p.Candidate) == "live-verified" {
		t.Fatal("reconnect reused old binding")
	}
	b = TargetBinding{p.TargetID, p.Generation}
	if NewEnvironmentEvidence(p, b, l).Result(p.Candidate) == "live-verified" {
		t.Fatal("rebound target reused stale live evidence")
	}
	l.LiveWrite.Binding = &b
	l.SaveReload.Binding = &b
	e := NewEnvironmentEvidence(p, b, l)
	if e.Result(p.Candidate) != "live-verified" {
		t.Fatal("fresh binding rejected")
	}
	if e.Result("different-head") == "live-verified" {
		t.Fatal("accepted other candidate")
	}
	r := structuredRecord()
	e.Candidate = r.Contract.HeadSHA
	e.LoadedCandidate = e.Candidate
	r.Checks[0].Evidence[0].Type = "live"
	r.Checks[0].Evidence[0].Environment = &e
	if err := Evaluate(r.Contract, r); err != nil {
		t.Fatal(err)
	}
	r.Checks[0].Evidence[0].Environment = nil
	if err := Evaluate(r.Contract, r); err == nil {
		t.Fatal("accepted live proof without runtime evidence")
	}
}

func TestEnvironmentBlockedReportRetainsLayers(t *testing.T) {
	r := structuredRecord()
	p, b, l := environmentFixture()
	p.Candidate, p.LoadedCandidate = r.Contract.HeadSHA, r.Contract.HeadSHA
	l.LiveWrite = EnvironmentLayer{Status: "failed", Evidence: "fixture write rejected", Binding: &b}
	l.SaveReload = EnvironmentLayer{Status: "not-run", Evidence: "write did not complete"}
	environment := NewEnvironmentEvidence(p, b, l)
	r.Checks[0].Status = Blocked
	r.Checks[0].Missing = "live-write and save-reload evidence unavailable"
	r.Checks[0].Evidence[0].Type = "live"
	r.Checks[0].Evidence[0].Result = "unverified"
	r.Checks[0].Evidence[0].Environment = &environment
	data, err := json.Marshal(map[string]any{"status": "blocked", "summary": "live evidence incomplete", "evidence": r.Evidence, "checks": r.Checks, "findings": []Finding{}, "next_action": "collect missing live evidence in an authorized window", "next_owner": "environment-maintainer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyReviewReport(r.Contract, &r, string(data)); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var restored Record
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	e := restored.Checks[0].Evidence[0].Environment
	if e == nil || e.Layers.LiveWrite.Status != "failed" || e.Layers.SaveReload.Status != "not-run" || !reflect.DeepEqual(environment.Layers, e.Layers) {
		t.Fatal("acceptance report lost evidence layers")
	}
	if Evaluate(restored.Contract, restored) == nil {
		t.Fatal("blocked live report authorized acceptance")
	}
}
