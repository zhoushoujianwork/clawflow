package github

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/retry"
)

// Retry fixtures use millisecond backoff so the suite does not really wait
// 1/2/4 seconds per transient response; tests that check the production
// schedule set their own config.
//
// HTTP fixtures must not resolve machine-local GitHub App credentials. Tests
// exercising app authentication install their own config inside this test home.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "clawflow-github-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", dir); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	retry.DefaultConfig = retry.Config{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
