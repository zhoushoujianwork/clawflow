package config_test

import (
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// shouldSkipRepo encapsulates the bound_machine skip logic from the run loop
// so it can be unit-tested without spinning up the full operator pipeline.
// This mirrors the condition in cmd/clawflow/commands/run.go exactly:
//
//	repoCfg.BoundMachine != "" && hostname != "" && repoCfg.BoundMachine != hostname
func shouldSkipRepo(repo config.Repo, hostname string) bool {
	return repo.BoundMachine != "" && hostname != "" && repo.BoundMachine != hostname
}

func TestBoundMachine_NoBoundMachine(t *testing.T) {
	// Repos with no BoundMachine are always processed, regardless of hostname.
	repo := config.Repo{Enabled: true, BoundMachine: ""}
	if shouldSkipRepo(repo, "machine-a") {
		t.Error("repo with empty BoundMachine should not be skipped")
	}
	if shouldSkipRepo(repo, "") {
		t.Error("repo with empty BoundMachine should not be skipped even when hostname is empty")
	}
}

func TestBoundMachine_MatchingHostname(t *testing.T) {
	// Repos bound to the current machine are processed normally.
	repo := config.Repo{Enabled: true, BoundMachine: "machine-a"}
	if shouldSkipRepo(repo, "machine-a") {
		t.Error("repo bound to current machine should not be skipped")
	}
}

func TestBoundMachine_DifferentHostname(t *testing.T) {
	// Repos bound to a different machine are skipped.
	repo := config.Repo{Enabled: true, BoundMachine: "machine-b"}
	if !shouldSkipRepo(repo, "machine-a") {
		t.Error("repo bound to a different machine should be skipped")
	}
}

func TestBoundMachine_EmptyHostname(t *testing.T) {
	// When hostname resolution fails (empty string), we conservatively process
	// all repos — no work is silently dropped due to a misconfigured machine.
	repo := config.Repo{Enabled: true, BoundMachine: "machine-b"}
	if shouldSkipRepo(repo, "") {
		t.Error("repo should not be skipped when hostname is empty (hostname resolution failed)")
	}
}
