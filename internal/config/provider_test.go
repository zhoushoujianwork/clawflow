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
			{Name: "Mirror", BaseURL: "https://mirror.example.com", APIKey: "sk-2", ChatModel: "haiku", EvalModel: "opus", OperatorModel: "sonnet", Enabled: true},
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

// TestMigrateProviderModels_LegacyPerProviderModel verifies the old
// single-slot ClaudeProvider.Model field is fanned out into the three
// role-specific slots (when those are empty) and then cleared.
func TestMigrateProviderModels_LegacyPerProviderModel(t *testing.T) {
	c := &config.Credentials{
		ClaudeProviders: []config.ClaudeProvider{
			// has legacy Model, no role fields → all three fields get it
			{Name: "A", Model: "haiku", Enabled: true},
			// has legacy Model AND one role field → role field wins, others get Model
			{Name: "B", Model: "sonnet", EvalModel: "opus", Enabled: true},
			// already-migrated provider with no legacy Model → no-op
			{Name: "C", ChatModel: "haiku", EvalModel: "opus", OperatorModel: "sonnet", Enabled: true},
		},
	}

	mutated := config.MigrateProviderModels(c)
	if !mutated {
		t.Fatal("expected mutation, got none")
	}

	a := c.ClaudeProviders[0]
	if a.Model != "" {
		t.Errorf("A: legacy Model not cleared: %q", a.Model)
	}
	if a.ChatModel != "haiku" || a.EvalModel != "haiku" || a.OperatorModel != "haiku" {
		t.Errorf("A: expected all slots = 'haiku', got chat=%q eval=%q op=%q", a.ChatModel, a.EvalModel, a.OperatorModel)
	}

	b := c.ClaudeProviders[1]
	if b.Model != "" {
		t.Errorf("B: legacy Model not cleared: %q", b.Model)
	}
	if b.ChatModel != "sonnet" || b.EvalModel != "opus" || b.OperatorModel != "sonnet" {
		t.Errorf("B: expected chat=sonnet eval=opus op=sonnet, got chat=%q eval=%q op=%q", b.ChatModel, b.EvalModel, b.OperatorModel)
	}

	// Second call must be idempotent.
	if config.MigrateProviderModels(c) {
		t.Error("expected second MigrateProviderModels call to be a no-op")
	}
}

// TestMigrateProviderModels_GlobalToPerProvider verifies that the old
// credentials-level ClaudeChatModel/EvalModel/OperatorModel are pushed
// into each provider's empty role slot and then cleared.
func TestMigrateProviderModels_GlobalToPerProvider(t *testing.T) {
	c := &config.Credentials{
		ClaudeChatModel:     "haiku",
		ClaudeEvalModel:     "opus",
		ClaudeOperatorModel: "sonnet",
		ClaudeProviders: []config.ClaudeProvider{
			{Name: "A", Enabled: true},                               // no per-provider models — inherit all three
			{Name: "B", EvalModel: "claude-opus-4-6", Enabled: true}, // keeps its own eval, inherits others
		},
	}

	mutated := config.MigrateProviderModels(c)
	if !mutated {
		t.Fatal("expected mutation")
	}

	if c.ClaudeChatModel != "" || c.ClaudeEvalModel != "" || c.ClaudeOperatorModel != "" {
		t.Errorf("global slots not cleared: chat=%q eval=%q op=%q", c.ClaudeChatModel, c.ClaudeEvalModel, c.ClaudeOperatorModel)
	}

	a := c.ClaudeProviders[0]
	if a.ChatModel != "haiku" || a.EvalModel != "opus" || a.OperatorModel != "sonnet" {
		t.Errorf("A: got chat=%q eval=%q op=%q", a.ChatModel, a.EvalModel, a.OperatorModel)
	}

	b := c.ClaudeProviders[1]
	if b.ChatModel != "haiku" || b.EvalModel != "claude-opus-4-6" || b.OperatorModel != "sonnet" {
		t.Errorf("B: got chat=%q eval=%q op=%q", b.ChatModel, b.EvalModel, b.OperatorModel)
	}
}

// TestResolveModelForRole verifies the three-tier resolution order:
// env override > first enabled provider's role slot > built-in default.
func TestResolveModelForRole(t *testing.T) {
	t.Run("falls back to default when no providers", func(t *testing.T) {
		c := &config.Credentials{}
		if got := config.ResolveModelForRole(c, config.RoleChat); got != config.DefaultChatModel {
			t.Errorf("chat: got %q, want %q", got, config.DefaultChatModel)
		}
		if got := config.ResolveModelForRole(c, config.RoleEval); got != config.DefaultEvalModel {
			t.Errorf("eval: got %q, want %q", got, config.DefaultEvalModel)
		}
		if got := config.ResolveModelForRole(c, config.RoleOperator); got != config.DefaultOperatorModel {
			t.Errorf("operator: got %q, want %q", got, config.DefaultOperatorModel)
		}
	})

	t.Run("picks first enabled provider", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeProviders: []config.ClaudeProvider{
				{Name: "Disabled", ChatModel: "ignored", Enabled: false},
				{Name: "A", ChatModel: "claude-haiku-4-5", EvalModel: "claude-opus-4-7", Enabled: true},
				{Name: "B", ChatModel: "later", Enabled: true},
			},
		}
		if got := config.ResolveModelForRole(c, config.RoleChat); got != "claude-haiku-4-5" {
			t.Errorf("chat: got %q, want claude-haiku-4-5", got)
		}
		if got := config.ResolveModelForRole(c, config.RoleEval); got != "claude-opus-4-7" {
			t.Errorf("eval: got %q, want claude-opus-4-7", got)
		}
		// A's OperatorModel is empty → falls back to the built-in default,
		// NOT to B (provider precedence is name-index based, not round-robin).
		if got := config.ResolveModelForRole(c, config.RoleOperator); got != config.DefaultOperatorModel {
			t.Errorf("operator: got %q, want default %q", got, config.DefaultOperatorModel)
		}
	})

	t.Run("nil credentials returns default", func(t *testing.T) {
		if got := config.ResolveModelForRole(nil, config.RoleChat); got != config.DefaultChatModel {
			t.Errorf("nil: got %q, want %q", got, config.DefaultChatModel)
		}
	})
}

func TestResolveClaudeCredentials(t *testing.T) {
	t.Run("picks first enabled provider before legacy fields", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeAPIKey:  "legacy-key",
			ClaudeBaseURL: "https://legacy.example.com",
			ClaudeProviders: []config.ClaudeProvider{
				{Name: "Disabled", APIKey: "disabled-key", BaseURL: "https://disabled.example.com", Enabled: false},
				{Name: "Primary", APIKey: "primary-key", BaseURL: "https://primary.example.com", Enabled: true},
				{Name: "Backup", APIKey: "backup-key", BaseURL: "https://backup.example.com", Enabled: true},
			},
		}

		apiKey, baseURL := config.ResolveClaudeCredentials(c)
		if apiKey != "primary-key" || baseURL != "https://primary.example.com" {
			t.Fatalf("got key=%q base=%q, want first enabled provider", apiKey, baseURL)
		}
	})

	t.Run("falls back to legacy fields when no provider is enabled", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeAPIKey:  "legacy-key",
			ClaudeBaseURL: "https://legacy.example.com",
			ClaudeProviders: []config.ClaudeProvider{
				{Name: "Disabled", APIKey: "disabled-key", BaseURL: "https://disabled.example.com", Enabled: false},
			},
		}

		apiKey, baseURL := config.ResolveClaudeCredentials(c)
		if apiKey != "legacy-key" || baseURL != "https://legacy.example.com" {
			t.Fatalf("got key=%q base=%q, want legacy credentials", apiKey, baseURL)
		}
	})

	t.Run("empty enabled provider means inherit claude auth", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeAPIKey:  "legacy-key",
			ClaudeBaseURL: "https://legacy.example.com",
			ClaudeProviders: []config.ClaudeProvider{
				{Name: config.DefaultProviderName, Enabled: true},
			},
		}

		apiKey, baseURL := config.ResolveClaudeCredentials(c)
		if apiKey != "" || baseURL != "" {
			t.Fatalf("got key=%q base=%q, want empty provider credentials", apiKey, baseURL)
		}
	})

	t.Run("env overrides loaded credentials", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAWFLOW_CLAUDE_API_KEY", "env-key")
		t.Setenv("CLAWFLOW_CLAUDE_BASE_URL", "https://env.example.com")

		creds := &config.Credentials{
			ClaudeProviders: []config.ClaudeProvider{
				{Name: "Primary", APIKey: "primary-key", BaseURL: "https://primary.example.com", Enabled: true},
			},
			DefaultProviderSeeded: true,
		}
		data, err := yaml.Marshal(creds)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		path := config.CredentialsPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		loaded, err := config.LoadCredentials()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		apiKey, baseURL := config.ResolveClaudeCredentials(loaded)
		if apiKey != "env-key" || baseURL != "https://env.example.com" {
			t.Fatalf("got key=%q base=%q, want env credentials", apiKey, baseURL)
		}
	})
}

// TestEffectiveMaxOutputTokens verifies the per-provider output-token ceiling
// falls back to the built-in default when unset and honors explicit overrides
// (issue #286).
func TestEffectiveMaxOutputTokens(t *testing.T) {
	var nilP *config.ClaudeProvider
	if got := nilP.EffectiveMaxOutputTokens(); got != config.DefaultMaxOutputTokens {
		t.Errorf("nil provider: got %d, want default %d", got, config.DefaultMaxOutputTokens)
	}

	unset := &config.ClaudeProvider{Name: "A"}
	if got := unset.EffectiveMaxOutputTokens(); got != config.DefaultMaxOutputTokens {
		t.Errorf("unset: got %d, want default %d", got, config.DefaultMaxOutputTokens)
	}

	custom := &config.ClaudeProvider{Name: "B", MaxOutputTokens: 96000}
	if got := custom.EffectiveMaxOutputTokens(); got != 96000 {
		t.Errorf("custom: got %d, want 96000", got)
	}

	zeroOrNeg := &config.ClaudeProvider{Name: "C", MaxOutputTokens: -1}
	if got := zeroOrNeg.EffectiveMaxOutputTokens(); got != config.DefaultMaxOutputTokens {
		t.Errorf("negative: got %d, want default %d", got, config.DefaultMaxOutputTokens)
	}
}
