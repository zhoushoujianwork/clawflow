package config

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubAppLocalOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	key := filepath.Join(t.TempDir(), "private-key.pem")
	if err := os.WriteFile(key, []byte("key-content-must-not-escape"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Settings: Settings{GitHubApp: GitHubAppSettings{AppID: 1, PrivateKeyPath: key}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0640, 0644, 0666} {
		if err := os.Chmod(key, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil || strings.Contains(err.Error(), key) {
			t.Fatal("unsafe mode accepted or path disclosed")
		}
	}
	data, err := MarshalForGist(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), key) || strings.Contains(string(data), "github_app") {
		t.Fatal("app configuration synced")
	}
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), key) {
		t.Fatal("key path exported as JSON")
	}
	remote := &Config{Settings: Settings{GitHubApp: GitHubAppSettings{AppID: 99, PrivateKeyPath: "remote"}}}
	// Merge must retain machine-local authentication even when cloud settings win.
	remoteYAML, _ := yaml.Marshal(remote)
	merged, _ := MergeConfigs(cfg, remoteYAML)
	if merged.Settings.GitHubApp != cfg.Settings.GitHubApp {
		t.Fatal("remote replaced local app settings")
	}
}
