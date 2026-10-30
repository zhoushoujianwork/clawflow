package operator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/resourcelease"
)

// Both sides of the merge must hold: resource exclusion and pinned SOP input.
func TestRunPinsTestingSnapshotWhileHoldingResources(t *testing.T) {
	for _, name := range []string{"implement", "repair-ci"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
			if _, err := project.Create("fixture"); err != nil {
				t.Fatal(err)
			}
			if err := project.AddRepo("fixture", "o/r"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(project.ProjectDir("fixture"), "project.yaml")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString("shared_resource_keys: [fixture-runtime]\n")
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			if err := project.WriteTesting("fixture", "changed SOP"); err != nil {
				t.Fatal(err)
			}
			ctx := project.WithTestingSnapshot(context.Background(), "approved SOP")
			called := false
			_, _, err = Run(ctx, &Operator{Name: name}, &Subject{Number: 1}, newFakeVCS(), RunOptions{
				Repo: "o/r", Workdir: t.TempDir(), Timeout: time.Second,
				RunFunc: func(_ context.Context, _, _ string, _ time.Duration, _ io.Writer, _ string, system ...string) (string, error) {
					called = true
					if len(system) != 1 || !strings.Contains(system[0], "approved SOP") || strings.Contains(system[0], "changed SOP") {
						t.Fatalf("execution lost pinned SOP: %v", system)
					}
					release, acquireErr := project.AcquireResources("o/r", resourcelease.Holder{Task: "contender"})
					if release != nil {
						release()
					}
					var blocked *resourcelease.Blocked
					if !errors.As(acquireErr, &blocked) {
						t.Fatalf("execution did not hold resource lease: %v", acquireErr)
					}
					return "done\n<!-- clawflow:outcome=agent-implemented -->", nil
				},
			})
			if err != nil || !called {
				t.Fatalf("execution: called=%v err=%v", called, err)
			}
			release, err := project.AcquireResources("o/r", resourcelease.Holder{Task: "next"})
			if err != nil {
				t.Fatalf("resource not released: %v", err)
			}
			release()
		})
	}
}
