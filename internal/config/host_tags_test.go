package config

import (
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHostTagMigrationRoundTrip(t *testing.T) {
	input := `repos:
  o/r:
    bound_machine: Work-Mac.LOCAL
    worker_tags: [codex, host-work-mac]
    worker_tags_by_kind: {repair: [heavy]}
    enabled: true
    base_branch: develop
    local_path: /checkout
    auto_merge: true
    acceptance_required: true
    acceptance_overlap_ci: false
    max_in_flight: 3
    ci_required: true
    labels: {trigger: ready}
    priority: 2
settings:
  require_binding: true
  run_paused: true
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatal(err)
	}
	r := cfg.Repos["o/r"]
	if !reflect.DeepEqual(r.WorkerTags, []string{"codex", "host-work-mac"}) || r.BoundMachine != "" || cfg.Settings.RequireBinding {
		t.Fatalf("migration: %+v", cfg)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "bound_machine") || strings.Contains(string(data), "require_binding") {
		t.Fatalf("retired fields persisted: %s", data)
	}
	var reloaded Config
	if err := yaml.Unmarshal(data, &reloaded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Repos, reloaded.Repos) || !reloaded.Settings.RunPaused {
		t.Fatalf("rewrite changed settings: %+v", reloaded)
	}
	for _, coordinator := range []bool{false, true} {
		settings := Settings{Coordinator: CoordinatorSettings{Enabled: coordinator}, RequireBinding: true}
		if !settings.RunsOnHost(r, "other") {
			t.Fatal("scheduler still enforces binding")
		}
	}
}

func TestHostTagNormalization(t *testing.T) {
	for input, want := range map[string]string{"": "", "Work-Mac.local": "host-work-mac", "Work-Mac.LOCAL": "host-work-mac", "Work.Example": "host-work.example"} {
		if got := HostTag(input); got != want {
			t.Fatalf("HostTag(%q)=%q want %q", input, got, want)
		}
	}
}

func TestRetiredHostNoticesOnce(t *testing.T) {
	retiredHostNotice, retiredBindingNotice = sync.Once{}, sync.Once{}
	defer func() { retiredHostNotice, retiredBindingNotice = sync.Once{}, sync.Once{} }()
	log, err := os.CreateTemp(t.TempDir(), "notice")
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = log
	defer func() { os.Stderr = stderr; log.Close() }()
	for i := 0; i < 2; i++ {
		var cfg Config
		if err := yaml.Unmarshal([]byte("repos:\n  secret-repo:\n    bound_machine: secret-host\nsettings:\n  require_binding: false\n"), &cfg); err != nil {
			t.Fatal(err)
		}
	}
	message, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(message), "bound_machine migrated") != 1 || strings.Count(string(message), "require_binding is retired") != 1 || strings.Contains(string(message), "secret") {
		t.Fatalf("unexpected notices: %s", message)
	}
}
