package gitexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestAgentGitEnvironmentContainsOnlyHelper(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	key := filepath.Join(t.TempDir(), "private.pem")
	if err := os.WriteFile(key, []byte("private-key-content"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Settings: config.Settings{GitHubApp: config.GitHubAppSettings{AppID: 1, PrivateKeyPath: key}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	original := []string{"PATH=/bin", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.bare", "GIT_CONFIG_VALUE_0=false"}
	env, err := AppEnvironment(original)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, secret := range []string{key, "private-key-content"} {
		if strings.Contains(joined, secret) {
			t.Fatal("key disclosed to agent environment")
		}
	}
	if !strings.Contains(joined, "git-credential") || !strings.Contains(joined, "GIT_CONFIG_COUNT=5") || !strings.Contains(joined, "GIT_CONFIG_VALUE_0=false") {
		t.Fatal("helper not installed or existing config lost")
	}
	if original[1] != "GIT_CONFIG_COUNT=1" {
		t.Fatal("input environment mutated")
	}
}
