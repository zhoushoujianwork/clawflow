package pilot

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/offloadtest"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

func TestOffloadHelper(t *testing.T) { offloadtest.Helper(t) }
func stubOffloadFailure(t *testing.T, kind, text string) {
	offloadtest.Setup(t)
	t.Setenv("OFFLOAD_TEST_ERROR", kind)
	t.Setenv("OFFLOAD_TEST_TEXT", text)
}

func TestWakeUsesPilotRole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	_, err := project.Create("role-test")
	if err != nil {
		t.Fatal(err)
	}
	original := runWakeOffload
	t.Cleanup(func() { runWakeOffload = original })
	called := false
	runWakeOffload = func(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer, role string, instructions ...string) (string, error) {
		called = true
		if role != config.RolePilot {
			t.Errorf("role = %q", role)
		}
		marker := "Wake run_id: "
		pos := strings.LastIndex(prompt, marker)
		if pos < 0 {
			t.Fatal("wake prompt missing run ID")
		}
		runID := strings.TrimSpace(prompt[pos+len(marker):])
		if !actionID.MatchString(runID) {
			t.Fatalf("invalid wake run ID: %q", runID)
		}
		return "```pilot-actions\n{\"run_id\":\"" + runID + "\",\"actions\":[]}\n```\nPILOT-RESULT: no-action", nil
	}
	if _, err := Ask(context.Background(), "review", 5, time.Second); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("wake did not invoke executor")
	}
}
