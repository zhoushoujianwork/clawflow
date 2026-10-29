package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExecutionSettingsStayMachineLocal(t *testing.T) {
	local := &Config{Repos: map[string]Repo{}, Settings: Settings{Execution: ExecutionSettings{TestDatabaseDSN: "postgres://local-secret", Command: "/local/offload", Profile: "local", RoleProfiles: map[string]string{"chat": "chat-profile"}, CodexSandbox: "workspace-write"}}}
	raw, err := MarshalForGist(local)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/local/") || strings.Contains(string(raw), "execution:") || strings.Contains(string(raw), "local-secret") {
		t.Fatalf("runtime settings leaked into sync: %s", raw)
	}
	merged, err := MergeConfigs(local, []byte("settings:\n  execution:\n    command: /remote/agh\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(merged.Settings.Execution, local.Settings.Execution) {
		t.Fatal("remote execution settings overrode local paths")
	}
}

func TestOffloadLegacyCredentialsRemainUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := CredentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("gh_token: vcs-token\nclaude_api_key: old-key\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if c.GHToken != "vcs-token" {
		t.Fatal("lost VCS credential")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != string(original) {
		t.Fatal("legacy credentials migrated")
	}
}
