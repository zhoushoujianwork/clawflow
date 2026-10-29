package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCommandsSpoolWithoutVCS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effects.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(effectSpoolEnv, path)
	for _, args := range [][]string{
		{"pr", "create", "--repo", "o/r", "--head", "fix", "--base", "main", "--title", "fix", "--body", "Fixes #1"},
		{"pr", "comment", "--repo", "o/r", "--pr", "2", "--body", "review"},
		{"issue", "comment", "--repo", "o/r", "--issue", "1", "--body", "hello"},
		{"label", "add", "--repo", "o/r", "--issue", "1", "--label", "done"},
		{"label", "remove", "--repo", "o/r", "--issue", "1", "--label", "pending"},
	} {
		cmd := NewRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "queued for coordinator") {
			t.Fatal(out.String())
		}
	}
	effects, err := readEffectSpool(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 5 || effects[0].Head != "fix" || effects[0].Kind != "pr-create" || effects[2].Body != "hello" {
		t.Fatalf("%+v", effects)
	}
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"issue", "close", "--repo", "o/r", "--issue", "1"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "worker VCS write") {
		t.Fatalf("unsupported write not blocked: %v", err)
	}
}
func TestSpoolUnsetAndInvalidFile(t *testing.T) {
	t.Setenv(effectSpoolEnv, "")
	var out bytes.Buffer
	if queued, err := queueEffect(&out, jobEffect{}); queued || err != nil || out.Len() != 0 {
		t.Fatal("local command intercepted")
	}
	t.Setenv(effectSpoolEnv, filepath.Join(t.TempDir(), "missing"))
	if queued, err := queueEffect(&out, jobEffect{}); !queued || err == nil {
		t.Fatal("spool error fell through to VCS")
	}
}
