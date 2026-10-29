package vcs

import (
	"strings"
	"testing"
)

func TestFailureExcerptKeepsAllGoFailuresOutsideTail(t *testing.T) {
	noise := strings.Repeat("successful test output\n", 2000)
	blocks := []string{
		"    publish_test.go:42: directory not empty\n--- FAIL: TestPublish (0.01s)\n",
		"panic: runtime error: invalid memory address\n\ngoroutine 12 [running]:\nexample.TestDatabase(0x123)\n\t/database_test.go:88 +0x12\n",
		"    other_test.go:9: expected schema\n--- FAIL: TestOther (0.02s)\n",
	}
	input := noise + strings.Join(blocks, noise) + noise
	got := FailureExcerpt(input, 4096)
	for _, block := range blocks {
		for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
			if !strings.Contains(got, line) {
				t.Errorf("missing diagnostic %q", line)
			}
		}
	}
	if len(got) > 4096+len("[truncated]\n") || !strings.HasPrefix(got, "[truncated]\n") {
		t.Fatalf("unbounded excerpt: %d bytes", len(got))
	}
}

func TestFailureExcerptWithoutMarkersKeepsTail(t *testing.T) {
	if got := FailureExcerpt("old output\nlast", 4); got != "[truncated]\nlast" {
		t.Fatal(got)
	}
}
