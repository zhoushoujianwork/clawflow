package config

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWorkflowRoundTrip(t *testing.T) {
	for _, mode := range []string{"", WorkflowAutonomous, WorkflowLegacy} {
		t.Run("mode="+mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			cfg := &Config{Repos: map[string]Repo{"o/r": {Workflow: mode, LocalPath: "/local/checkout"}}}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if got := loaded.Repos["o/r"].Workflow; got != mode {
				t.Fatalf("got %q, want %q", got, mode)
			}
			want := mode
			if want == "" {
				want = WorkflowAutonomous
			}
			if got := loaded.Repos["o/r"].EffectiveWorkflow(); got != want {
				t.Fatalf("effective = %q", got)
			}
			data, err := MarshalForGist(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var payload gistPayload
			if err := yaml.Unmarshal(data, &payload); err != nil {
				t.Fatal(err)
			}
			restored := fromSyncable(payload.Repos["o/r"], "/another/checkout")
			if restored.Workflow != mode || restored.LocalPath != "/another/checkout" {
				t.Fatalf("sync lost workflow/local path: %+v", restored)
			}
			merged, err := MergeConfigs(&Config{Repos: map[string]Repo{"o/r": {LocalPath: "/another/checkout"}}}, data)
			if err != nil {
				t.Fatal(err)
			}
			if merged.Repos["o/r"].Workflow != mode {
				t.Fatal("merge lost workflow")
			}
		})
	}
}

func TestWorkflowValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := ValidateWorkflow("typo"); err == nil {
		t.Fatal("invalid workflow accepted")
	}
	cfg := &Config{Repos: map[string]Repo{"o/r": {Workflow: "typo"}}}
	if err := cfg.Save(); err == nil {
		t.Fatal("saved invalid workflow")
	}
	if err := (&Config{}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), []byte("repos:\n  o/r:\n    workflow: typo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("loaded invalid workflow")
	}
	if _, err := MergeConfigs(&Config{}, []byte("repos:\n  o/r:\n    workflow: typo\n")); err == nil {
		t.Fatal("merged invalid workflow")
	}
}
