package claude

import (
	"slices"
	"testing"
)

func TestEnvWithCredentials_NoOverridesPreservesEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"ANTHROPIC_API_KEY=existing",
		"ANTHROPIC_BASE_URL=https://api.example.com",
	}
	got := EnvWithCredentials(in, "", "")
	for _, kv := range in {
		if !slices.Contains(got, kv) {
			t.Errorf("missing %q in output (no-override case should be a no-op)", kv)
		}
	}
}

func TestEnvWithCredentials_StripsEmptyApiKey(t *testing.T) {
	// CleanedEnv should still drop the OAuth-placeholder empty key.
	in := []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY="}
	got := EnvWithCredentials(in, "", "")
	if slices.Contains(got, "ANTHROPIC_API_KEY=") {
		t.Errorf("empty ANTHROPIC_API_KEY should be stripped, got %v", got)
	}
}

func TestEnvWithCredentials_OverridesApiKey(t *testing.T) {
	in := []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=stale"}
	got := EnvWithCredentials(in, "fresh", "")
	if !slices.Contains(got, "ANTHROPIC_API_KEY=fresh") {
		t.Errorf("expected ANTHROPIC_API_KEY=fresh in %v", got)
	}
	if slices.Contains(got, "ANTHROPIC_API_KEY=stale") {
		t.Errorf("stale ANTHROPIC_API_KEY should have been replaced; got %v", got)
	}
}

func TestEnvWithCredentials_OverridesBaseURL(t *testing.T) {
	in := []string{"PATH=/usr/bin", "ANTHROPIC_BASE_URL=https://old"}
	got := EnvWithCredentials(in, "", "https://new")
	if !slices.Contains(got, "ANTHROPIC_BASE_URL=https://new") {
		t.Errorf("expected ANTHROPIC_BASE_URL=https://new in %v", got)
	}
	if slices.Contains(got, "ANTHROPIC_BASE_URL=https://old") {
		t.Errorf("old ANTHROPIC_BASE_URL should have been replaced; got %v", got)
	}
}

func TestEnvWithCredentials_AddsBoth(t *testing.T) {
	in := []string{"PATH=/usr/bin"}
	got := EnvWithCredentials(in, "k", "https://b")
	if !slices.Contains(got, "ANTHROPIC_API_KEY=k") || !slices.Contains(got, "ANTHROPIC_BASE_URL=https://b") {
		t.Errorf("expected both keys appended; got %v", got)
	}
}
