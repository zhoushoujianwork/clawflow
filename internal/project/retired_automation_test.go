package project

import (
	"os"
	"strings"
	"testing"
)

func TestLegacyAutomationIgnoredAndDropped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Create("legacy"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(yamlPath("legacy"))
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("automation:\n  enabled: true\n  cooldown_minutes: 99\n  bound_machine: another-host\n")...)
	if err := os.WriteFile(yamlPath("legacy"), data, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if p.Automation.Enabled || p.Automation.BoundMachine != "" {
		t.Fatalf("%+v", p.Automation)
	}
	if err := AddRepo("legacy", "o/r"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(yamlPath("legacy"))
	if err != nil || strings.Contains(string(data), "automation:") {
		t.Fatalf("%s %v", data, err)
	}
}
