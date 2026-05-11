package pilot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// TestDeploymentMDWriteBack verifies that ExtractFencedBlock correctly
// extracts a deployment.md block from Pilot output, and that the
// extracted content differs from the original (triggering a write).
// This mirrors the logic in wake() without invoking claude.
func TestDeploymentMDWriteBack(t *testing.T) {
	// Simulate Pilot output containing an updated deployment.md block.
	pilotOutput := `
I detected SOP drift — the existing log commands returned no output but
~/.clawflow/data/runs.json has recent entries. I verified the corrected
commands return real data.

` + "```deployment.md" + `
# Deployment

## Method 1: Structured logs (preferred)

` + "```bash" + `
tail -n 200 ~/.clawflow/logs/run.log
` + "```" + `
` + "```" + `

PATROL: SOP drift — deployment.md commands returned no meaningful logs but project is active → updated deployment.md
PILOT-RESULT: 1 action — refreshed deployment.md, pointed log patrol at ~/.clawflow/logs/run.log
`

	oldDeployment := "## Logs\n\n```bash\ncat /tmp/old.log\n```"

	extracted := chat.ExtractFencedBlock(pilotOutput, "deployment.md")
	if extracted == "" {
		t.Fatal("ExtractFencedBlock returned empty for deployment.md block in Pilot output")
	}
	if strings.TrimSpace(extracted) == strings.TrimSpace(oldDeployment) {
		t.Error("extracted deployment.md should differ from old content — write-back would be skipped")
	}
	if !strings.Contains(extracted, "run.log") {
		t.Error("extracted deployment.md should reference run.log")
	}
}

// TestDeploymentMDWriteBack_NoChange verifies that when the Pilot emits
// a deployment.md block identical to the current file, the write-back
// is skipped (strings.TrimSpace comparison).
func TestDeploymentMDWriteBack_NoChange(t *testing.T) {
	existing := "# Deployment\n\nSSH then journalctl."
	pilotOutput := "```deployment.md\n# Deployment\n\nSSH then journalctl.\n```"

	extracted := chat.ExtractFencedBlock(pilotOutput, "deployment.md")
	if strings.TrimSpace(extracted) != strings.TrimSpace(existing) {
		t.Errorf("expected identical content to skip write-back, got %q vs %q", extracted, existing)
	}
}

// TestDeploymentMDWriteBack_NoBlock verifies that when the Pilot output
// contains no deployment.md block, ExtractFencedBlock returns "" and
// no write-back occurs.
func TestDeploymentMDWriteBack_NoBlock(t *testing.T) {
	pilotOutput := "PATROL: clean — last 200 lines normal\nPILOT-RESULT: no-action — backlog coherent"

	extracted := chat.ExtractFencedBlock(pilotOutput, "deployment.md")
	if extracted != "" {
		t.Errorf("expected empty extraction when no deployment.md block present, got %q", extracted)
	}
}

// TestDeploymentMDWriteBack_RoundTrip verifies the full write/read cycle
// using the real project.WriteDeployment / project.ReadDeployment functions.
func TestDeploymentMDWriteBack_RoundTrip(t *testing.T) {
	// Use a temp dir as HOME so we don't pollute ~/.clawflow.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	projName := "writeback-test"
	// Ensure the project directory exists.
	projDir := filepath.Join(tmpHome, ".clawflow", "projects", projName)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	newContent := "# Deployment\n\n## Logs\n\ntail -n 200 ~/.clawflow/logs/run.log"

	if err := project.WriteDeployment(projName, newContent); err != nil {
		t.Fatalf("WriteDeployment: %v", err)
	}
	got, err := project.ReadDeployment(projName)
	if err != nil {
		t.Fatalf("ReadDeployment: %v", err)
	}
	if got != newContent {
		t.Errorf("ReadDeployment = %q, want %q", got, newContent)
	}
}
