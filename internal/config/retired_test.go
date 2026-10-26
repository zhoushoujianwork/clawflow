package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRetiredConfigurationMigration(t *testing.T) {
	for _, mode := range []string{"", "autonomous", "legacy", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			input := `repos:
  group/project:
    workflow: "` + mode + `"
    auto_approve: true
    auto_evaluate_all_issues: true
    enabled: false
    platform: gitlab
    base_url: https://gitlab.example.test
    base_branch: develop
    local_path: /checkout
    bound_machine: worker-1
    primary_project: delivery
    auto_merge: false
    ci_required: true
settings:
  confidence_threshold: 0
  run_paused: true
`
			if err := os.MkdirAll(filepath.Dir(ConfigPath()), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ConfigPath(), []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			r := cfg.Repos["group/project"]
			if r.Enabled || r.AutoMerge || !r.CIRequired || r.Platform != "gitlab" || r.BaseURL != "https://gitlab.example.test" || r.BaseBranch != "develop" || r.LocalPath != "/checkout" || r.BoundMachine != "worker-1" || r.PrimaryProject != "delivery" || !cfg.Settings.RunPaused {
				t.Fatalf("migration changed active settings: %+v / %+v", r, cfg.Settings)
			}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(ConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			assertNoRetiredKeys(t, data)
			gist, err := MarshalForGist(cfg)
			if err != nil {
				t.Fatal(err)
			}
			assertNoRetiredKeys(t, gist)
			// Merge the old payload at the same timestamp. Retired keys must
			// neither conflict nor overwrite active settings or the local path.
			remote := string(gist) + "\n"
			remote = strings.Replace(remote, "        enabled:", "        workflow: legacy\n        auto_approve: true\n        auto_evaluate_all_issues: true\n        enabled:", 1)
			remote = strings.Replace(remote, "settings:\n", "settings:\n    confidence_threshold: 9\n", 1)
			if !strings.Contains(remote, "workflow: legacy") {
				t.Fatal("retired remote fields were not injected")
			}
			merged, err := MergeConfigs(cfg, []byte(remote))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(merged.Repos, cfg.Repos) {
				t.Fatalf("merge changed repos: %+v", merged.Repos)
			}
			before, err := yaml.Marshal(cfg.Settings)
			if err != nil {
				t.Fatal(err)
			}
			after, err := yaml.Marshal(merged.Settings)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("merge changed settings: %s", after)
			}
		})
	}
}

func assertNoRetiredKeys(t *testing.T, data []byte) {
	t.Helper()
	for _, key := range []string{"workflow:", "auto_approve:", "auto_evaluate_all_issues:", "confidence_threshold:"} {
		if strings.Contains(string(data), key) {
			t.Fatalf("serialized retired key %q", key)
		}
	}
}

func TestRetiredWorkflowNotice(t *testing.T) {
	retiredWorkflowNotice = sync.Once{}
	defer func() { retiredWorkflowNotice = sync.Once{} }()
	log, err := os.CreateTemp(t.TempDir(), "notice")
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = log
	defer func() { os.Stderr = stderr; log.Close() }()
	// The current mode must not consume the one-time migration notice.
	for _, input := range []string{
		"repos:\n  r:\n    workflow: autonomous\n",
		"mode: &current autonomous\nrepos:\n  r:\n    workflow: *current\n",
	} {
		var current Config
		if err := yaml.Unmarshal([]byte(input), &current); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := log.Stat(); err != nil || info.Size() != 0 {
		t.Fatalf("unexpected current-mode warning: %v", err)
	}
	for _, mode := range []string{"legacy", "secret-value"} {
		var cfg Config
		if err := yaml.Unmarshal([]byte("repos:\n  secret-repo:\n    workflow: "+mode+"\n"), &cfg); err != nil {
			t.Fatal(err)
		}
	}
	message, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(message), "migrated to autonomous") != 1 || strings.Contains(string(message), "secret") {
		t.Fatalf("expected one notice without input values, got %q", message)
	}
}
