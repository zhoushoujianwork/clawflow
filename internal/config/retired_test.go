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

func TestRetiredQueueSelectorsIgnoredOnLoadSaveAndSync(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	retiredQueueNotice = sync.Once{}
	defer func() { retiredQueueNotice = sync.Once{} }()
	log, err := os.CreateTemp(t.TempDir(), "notice")
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = log
	defer func() { os.Stderr = stderr; log.Close() }()
	// No obsolete selectors: no notice and no consumption of the once guard.
	var empty Config
	if err := yaml.Unmarshal([]byte("repos: {}"), &empty); err != nil {
		t.Fatal(err)
	}
	if info, _ := log.Stat(); info.Size() != 0 {
		t.Fatal("unexpected notice")
	}
	input := `repos:
  secret/project:
    enabled: true
    queue_issue: {ignored: malformed}
    queue_scope: ranked
    max_in_flight: 3
    ci_required: true
settings:
  queue:
    scope: all
    park_after: 10m
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
	if r := cfg.Repos["secret/project"]; !r.Enabled || !r.CIRequired || r.MaxInFlight != 3 || cfg.Settings.Queue.ParkAfter != "10m" {
		t.Fatalf("lost supported settings: %+v", cfg)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	synced, err := MergeConfigs(&Config{Repos: map[string]Repo{}}, []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := MarshalForGist(synced)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{saved, payload} {
		for _, key := range []string{"queue_issue:", "queue_scope:", "scope:"} {
			if strings.Contains(string(data), key) {
				t.Fatalf("retired key %s persisted: %s", key, data)
			}
		}
	}
	if !synced.Repos["secret/project"].Enabled || synced.Repos["secret/project"].MaxInFlight != 3 {
		t.Fatal("sync lost supported fields")
	}
	message, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(message), "retired queue selectors") != 1 || strings.Contains(string(message), "secret") {
		t.Fatalf("notice=%q", message)
	}
}

func TestRetiredQueueSelectorNoticeForEachInputShape(t *testing.T) {
	for _, input := range []string{
		"repos:\n  r:\n    queue_issue: 0\n",
		"repos:\n  r:\n    queue_scope: all\n",
		"settings:\n  queue:\n    scope: ranked\n",
		"old: &old\n  queue_scope: ranked\nrepos:\n  r:\n    <<: *old\n",
	} {
		t.Run(input, func(t *testing.T) {
			retiredQueueNotice = sync.Once{}
			defer func() { retiredQueueNotice = sync.Once{} }()
			log, err := os.CreateTemp(t.TempDir(), "notice")
			if err != nil {
				t.Fatal(err)
			}
			stderr := os.Stderr
			os.Stderr = log
			defer func() { os.Stderr = stderr; log.Close() }()
			var cfg Config
			if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
				t.Fatal(err)
			}
			if info, _ := log.Stat(); info.Size() == 0 {
				t.Fatal("missing notice")
			}
		})
	}
}

func TestNoRetiredQueuePathsOutsideLegacyLoader(t *testing.T) {
	root := filepath.Join("..", "..")
	forbidden := []string{"Queue" + "Issue", "Queue" + "Scope", "queue" + " import", "queue" + "_issue", "queue" + "_scope", "run/" + "queue_", "--queue" + "-issue", "--queue" + "-scope"}
	for _, area := range []string{"cmd", "internal", "web/src", "docs", "skills", "readme.md", "CLAUDE.md"} {
		err := filepath.WalkDir(filepath.Join(root, area), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if rel == "internal/config/retired.go" || rel == "internal/config/retired_test.go" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, token := range forbidden {
				if strings.Contains(string(data), token) {
					t.Errorf("%s still references %q", rel, token)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
