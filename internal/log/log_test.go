package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeHome points os.UserHomeDir to a tempdir so tests don't write
// into the real ~/.clawflow/logs.
func withFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestOpenWriteFormat(t *testing.T) {
	home := withFakeHome(t)
	lg, err := Open("test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer lg.Close()

	lg.Info("run/scan", "repo", "owner/r", "matched", 1)
	lg.Warn("run/api_retry", "attempt", 3, "status", 502)

	data, err := os.ReadFile(filepath.Join(home, ".clawflow", "logs", "test.log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), string(data))
	}
	if !strings.Contains(lines[0], "INFO") || !strings.Contains(lines[0], "run/scan") || !strings.Contains(lines[0], "repo=owner/r") || !strings.Contains(lines[0], "matched=1") {
		t.Errorf("line 0 missing fields: %q", lines[0])
	}
	if !strings.Contains(lines[1], "WARN") || !strings.Contains(lines[1], "attempt=3") {
		t.Errorf("line 1 missing fields: %q", lines[1])
	}
}

func TestQuotingForSpaces(t *testing.T) {
	withFakeHome(t)
	lg, _ := Open("test")
	defer lg.Close()
	lg.Error("run/end", "err", "boom with space")
	data, _ := os.ReadFile(filepath.Join(LogsDir(), "test.log"))
	if !strings.Contains(string(data), `err="boom with space"`) {
		t.Errorf("expected quoted value, got: %q", string(data))
	}
}

func TestNilLoggerIsNoop(t *testing.T) {
	var lg *Logger
	// Should not panic.
	lg.Info("any", "k", "v")
	lg.Warn("any")
	lg.Error("any")
	if err := lg.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
}

func TestAppendAcrossOpens(t *testing.T) {
	withFakeHome(t)
	lg, _ := Open("test")
	lg.Info("first")
	lg.Close()

	lg2, err := Open("test")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	lg2.Info("second")
	lg2.Close()

	data, _ := os.ReadFile(filepath.Join(LogsDir(), "test.log"))
	if !strings.Contains(string(data), "first") || !strings.Contains(string(data), "second") {
		t.Errorf("append broken: %q", string(data))
	}
}

func TestRotation(t *testing.T) {
	withFakeHome(t)
	lg, _ := Open("test")
	defer lg.Close()

	// Write enough bytes to trip rotation. Each line is ~50 bytes; we need
	// >10MB to cross the threshold. Use a fat field to speed it up.
	big := strings.Repeat("x", 10*1024) // 10 KB per line
	for i := 0; i < 1100; i++ {         // ~11 MB, just past the cap
		lg.Info("bench", "n", i, "blob", big)
	}

	live := filepath.Join(LogsDir(), "test.log")
	rotated := live + ".1"
	if _, err := os.Stat(rotated); err != nil {
		t.Errorf("expected %s to exist after rotation: %v", rotated, err)
	}
	st, err := os.Stat(live)
	if err != nil {
		t.Fatalf("live log gone: %v", err)
	}
	if st.Size() > maxSizeBytes {
		t.Errorf("post-rotation live file is %d bytes, expected <= %d", st.Size(), maxSizeBytes)
	}
}
