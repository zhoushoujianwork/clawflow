package github

import (
	"fmt"
	"os"
	"testing"
)

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
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
