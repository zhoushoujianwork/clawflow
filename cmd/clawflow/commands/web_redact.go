package commands

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// sensitiveValues lists the configured credential values that must never reach
// a read-only LAN viewer, plus secrets exported to this process. Values are
// read from disk each refresh, so a token added while the server runs is
// covered shortly after.
func sensitiveValues() []string {
	var out []string
	if c, err := config.LoadCredentials(); err == nil && c != nil {
		out = append(out, c.GHToken, c.GitLabToken, c.ClaudeAPIKey)
		for _, p := range c.ClaudeProviders {
			out = append(out, p.APIKey)
		}
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.ToUpper(k)
		if !ok || v == "" {
			continue
		}
		for _, suffix := range []string{"_TOKEN", "_KEY", "_SECRET", "_PASSWORD", "_DSN"} {
			if strings.HasSuffix(k, suffix) {
				out = append(out, v)
				break
			}
		}
	}
	return out
}

// localPathRoots lists directories whose absolute paths identify this machine:
// the state and data directories, the home directory, and every configured
// repository checkout.
func localPathRoots() []string {
	out := []string{config.StateDir(), snapshot.DataDir()}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, home)
	}
	if tmp := os.TempDir(); tmp != "" {
		out = append(out, filepath.Clean(tmp))
	}
	if cfg, err := config.Load(); err == nil {
		for _, r := range cfg.Repos {
			if r.LocalPath != "" {
				out = append(out, r.LocalPath)
			}
		}
	}
	return out
}
