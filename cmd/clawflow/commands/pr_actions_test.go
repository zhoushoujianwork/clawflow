package commands

import (
	"bytes"
	"strings"
	"testing"
)

func TestActionsObservationRefusesWorkerPolicyInput(t *testing.T) {
	t.Setenv("CLAWFLOW_EFFECT_SPOOL", "worker.jsonl")
	cmd := newPRActionsObserveCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--request", "does-not-exist.json"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "control plane") {
		t.Fatalf("worker must fail before reading policy/starting observation: %v", err)
	}
}
