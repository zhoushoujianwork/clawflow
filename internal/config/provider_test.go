package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"gopkg.in/yaml.v3"
)

// TestSeedDefaultProvider verifies that the built-in OAuth fallback entry
// is seeded exactly once and is disabled by default.
func TestSeedDefaultProvider(t *testing.T) {
	t.Run("seeds default provider on first load", func(t *testing.T) {
		c := &config.Credentials{}
		seeded := config.SeedDefaultProvider(c)
		if !seeded {
			t.Fatal("expected seeding to occur on fresh credentials")
		}
		if !c.DefaultProviderSeeded {
			t.Error("expected DefaultProviderSeeded flag to be set")
		}
		if len(c.ClaudeProviders) != 1 {
			t.Fatalf("expected 1 provider, got %d", len(c.ClaudeProviders))
		}
		p := c.ClaudeProviders[0]
		if p.Name != config.DefaultProviderName {
			t.Errorf("Name: got %q, want %q", p.Name, config.DefaultProviderName)
		}
		if p.APIKey != "" || p.BaseURL != "" {
			t.Errorf("seeded provider should have empty key/base_url, got key=%q base=%q", p.APIKey, p.BaseURL)
		}
		if p.Enabled {
			t.Error("seeded provider should be disabled by default")
		}
	})

	t.Run("does not re-seed after user deletes entry", func(t *testing.T) {
		c := &config.Credentials{DefaultProviderSeeded: true}
		seeded := config.SeedDefaultProvider(c)
		if seeded {
			t.Fatal("expected no seeding when DefaultProviderSeeded is true")
		}
		if len(c.ClaudeProviders) != 0 {
			t.Errorf("expected providers to remain empty, got %d", len(c.ClaudeProviders))
		}
	})

	t.Run("marks seeded without duplicating existing entry", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeProviders: []config.ClaudeProvider{
				{Name: config.DefaultProviderName, Enabled: true},
			},
		}
		seeded := config.SeedDefaultProvider(c)
		if !seeded {
			t.Fatal("expected SeedDefaultProvider to return true (flag flipped)")
		}
		if !c.DefaultProviderSeeded {
			t.Error("expected DefaultProviderSeeded to be set")
		}
		if len(c.ClaudeProviders) != 1 {
			t.Errorf("expected no duplicate entry, got %d providers", len(c.ClaudeProviders))
		}
	})

	t.Run("coexists with migrated legacy provider", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeAPIKey:  "sk-legacy",
			ClaudeBaseURL: "https://legacy.example.com",
		}
		// Simulate LoadCredentials: migrate first, then seed.
		if !config.MigrateLegacyProvider(c) {
			t.Fatal("legacy migration should have occurred")
		}
		if !config.SeedDefaultProvider(c) {
			t.Fatal("seeding should have occurred")
		}
		if len(c.ClaudeProviders) != 2 {
			t.Fatalf("expected 2 providers (legacy + seeded), got %d", len(c.ClaudeProviders))
		}
		// Legacy keeps index 0 so it stays the primary.
		if c.ClaudeProviders[0].APIKey != "sk-legacy" {
			t.Errorf("index 0 should be migrated legacy, got %q", c.ClaudeProviders[0].APIKey)
		}
		if !c.ClaudeProviders[0].Enabled {
			t.Error("legacy provider should remain enabled after seeding")
		}
		if c.ClaudeProviders[1].Name != config.DefaultProviderName {
			t.Errorf("index 1 should be seeded default, got %q", c.ClaudeProviders[1].Name)
		}
		if c.ClaudeProviders[1].Enabled {
			t.Error("seeded default should be disabled")
		}
	})
}

// TestMigrateLegacyProvider verifies that legacy single-provider fields are
// migrated into ClaudeProviders[0] and that the migration is idempotent.
func TestMigrateLegacyProvider(t *testing.T) {
	t.Run("migrates legacy fields", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeAPIKey:  "sk-ant-test",
			ClaudeBaseURL: "https://proxy.example.com",
		}
		migrated := config.MigrateLegacyProvider(c)
		if !migrated {
			t.Fatal("expected migration to occur")
		}
		if len(c.ClaudeProviders) != 1 {
			t.Fatalf("expected 1 provider, got %d", len(c.ClaudeProviders))
		}
		p := c.ClaudeProviders[0]
		if p.APIKey != "sk-ant-test" {
			t.Errorf("APIKey: got %q, want %q", p.APIKey, "sk-ant-test")
		}
		if p.BaseURL != "https://proxy.example.com" {
			t.Errorf("BaseURL: got %q, want %q", p.BaseURL, "https://proxy.example.com")
		}
		if !p.Enabled {
			t.Error("migrated provider should be enabled")
		}
	})

	t.Run("idempotent when providers already set", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeAPIKey: "sk-ant-old",
			ClaudeProviders: []config.ClaudeProvider{
				{Name: "existing", APIKey: "sk-ant-new", Enabled: true},
			},
		}
		migrated := config.MigrateLegacyProvider(c)
		if migrated {
			t.Fatal("expected no migration when providers already set")
		}
		if len(c.ClaudeProviders) != 1 {
			t.Fatalf("expected 1 provider, got %d", len(c.ClaudeProviders))
		}
		if c.ClaudeProviders[0].APIKey != "sk-ant-new" {
			t.Error("existing provider should not be overwritten")
		}
	})

	t.Run("no-op when no legacy fields", func(t *testing.T) {
		c := &config.Credentials{}
		migrated := config.MigrateLegacyProvider(c)
		if migrated {
			t.Fatal("expected no migration when no legacy fields")
		}
		if len(c.ClaudeProviders) != 0 {
			t.Fatalf("expected 0 providers, got %d", len(c.ClaudeProviders))
		}
	})
}

// TestEnabledProviders verifies that only enabled providers are returned.
func TestEnabledProviders(t *testing.T) {
	c := &config.Credentials{
		ClaudeProviders: []config.ClaudeProvider{
			{Name: "A", Enabled: true},
			{Name: "B", Enabled: false},
			{Name: "C", Enabled: true},
		},
	}
	enabled := c.EnabledProviders()
	if len(enabled) != 2 {
		t.Fatalf("expected 2 enabled providers, got %d", len(enabled))
	}
	if enabled[0].Name != "A" || enabled[1].Name != "C" {
		t.Errorf("unexpected enabled providers: %v", enabled)
	}
}

// TestEffectiveFailoverPatterns verifies that user patterns are merged with defaults.
func TestEffectiveFailoverPatterns(t *testing.T) {
	c := &config.Credentials{
		FailoverPatterns: []string{"custom-error-code"},
	}
	patterns := c.EffectiveFailoverPatterns()
	// Should contain defaults
	found429 := false
	foundCustom := false
	for _, p := range patterns {
		if p == "429" {
			found429 = true
		}
		if p == "custom-error-code" {
			foundCustom = true
		}
	}
	if !found429 {
		t.Error("expected default pattern '429' in effective patterns")
	}
	if !foundCustom {
		t.Error("expected user pattern 'custom-error-code' in effective patterns")
	}
}

// TestProviderRoundTrip verifies that providers survive a YAML marshal/unmarshal cycle.
func TestProviderRoundTrip(t *testing.T) {
	original := &config.Credentials{
		ClaudeProviders: []config.ClaudeProvider{
			{Name: "Primary", BaseURL: "https://api.anthropic.com", APIKey: "sk-ant-1", Enabled: true},
			{Name: "Mirror", BaseURL: "https://mirror.example.com", APIKey: "sk-2", Model: "sonnet", Enabled: true},
			{Name: "Disabled", APIKey: "sk-3", Enabled: false},
		},
	}

	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var loaded config.Credentials
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(loaded.ClaudeProviders) != 3 {
		t.Fatalf("expected 3 providers after round-trip, got %d", len(loaded.ClaudeProviders))
	}
	for i, want := range original.ClaudeProviders {
		got := loaded.ClaudeProviders[i]
		if got.Name != want.Name {
			t.Errorf("[%d] Name: got %q, want %q", i, got.Name, want.Name)
		}
		if got.APIKey != want.APIKey {
			t.Errorf("[%d] APIKey: got %q, want %q", i, got.APIKey, want.APIKey)
		}
		if got.Enabled != want.Enabled {
			t.Errorf("[%d] Enabled: got %v, want %v", i, got.Enabled, want.Enabled)
		}
	}
}

// TestSaveLoadProviders verifies that providers persist correctly to disk.
func TestSaveLoadProviders(t *testing.T) {
	// Use a temp dir to avoid touching the real credentials file.
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials.yaml")

	// Write credentials with providers.
	creds := &config.Credentials{
		ClaudeProviders: []config.ClaudeProvider{
			{Name: "First", APIKey: "key1", Enabled: true},
			{Name: "Second", APIKey: "key2", Enabled: false},
		},
	}
	data, err := yaml.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(credPath, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read back.
	var loaded config.Credentials
	raw, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := yaml.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(loaded.ClaudeProviders) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(loaded.ClaudeProviders))
	}
	if loaded.ClaudeProviders[0].Name != "First" {
		t.Errorf("order not preserved: first provider is %q", loaded.ClaudeProviders[0].Name)
	}
	if loaded.ClaudeProviders[1].Name != "Second" {
		t.Errorf("order not preserved: second provider is %q", loaded.ClaudeProviders[1].Name)
	}
}
