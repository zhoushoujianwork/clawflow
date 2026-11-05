package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultProfileWritesThroughOffload(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "offload")
	script := "#!/bin/sh\nif [ \"$1\" = default ]; then printf '%s' \"$2\" > '" + dir + "/selected'; else printf '%s' '{\"configured_default\":\"chosen\"}'; fi\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := DefaultProfile(context.Background(), Config{Command: command}, "chosen")
	raw, _ := os.ReadFile(filepath.Join(dir, "selected"))
	if err != nil || got != "chosen" || string(raw) != "chosen" {
		t.Fatalf("%q %q %v", got, raw, err)
	}
}
