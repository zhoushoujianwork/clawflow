package pilot

import (
	"context"
	"io"
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
	p, err := project.Create("role-test")
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
		return "PILOT-RESULT: no-action", nil
	}
	if err := wake(context.Background(), p, cfg, &config.Credentials{}, time.Second); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("wake did not invoke executor")
	}
}
