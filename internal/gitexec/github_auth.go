package gitexec

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/githubauth"
)

// The helper reads secrets from the child environment, never its arguments or
// Git config. It answers only for the exact HTTPS repository being operated on.
const githubHelper = `!f() {
 [ "$1" = get ] || exit 0
 while IFS='=' read -r key value; do
 case "$key" in protocol) protocol="$value";; host) host="$value";; path) path="$value";; esac
 done
 [ "$protocol" = https ] && [ "$host" = "$CLAWFLOW_GIT_HOST" ] && [ "$path" = "$CLAWFLOW_GIT_PATH" ] || exit 0
 printf 'username=x-access-token\npassword=%s\n' "$CLAWFLOW_GIT_TOKEN"
}; f`

func githubCredentials(ctx context.Context, original *exec.Cmd, args, env []string) ([]string, []string, error) {
	cfg, err := config.Load()
	if err != nil {
		if _, e := os.Stat(config.ConfigPath()); os.IsNotExist(e) {
			return args, env, nil
		}
		return nil, nil, fmt.Errorf("github_app: cannot load machine-local authentication configuration")
	}
	// Existing personal-token behavior stays unchanged when no app is configured.
	if !cfg.Settings.GitHubApp.Enabled() {
		return args, env, nil
	}
	op, dir := operation(original.Args[1:])
	if dir == "" {
		dir = original.Dir
	}
	target := "origin"
	seen := false
	skip := false
	for _, arg := range original.Args[1:] {
		if !seen {
			if arg == op {
				seen = true
			}
			continue
		}
		if skip {
			skip = false
			continue
		}
		if arg == "--depth" || arg == "--filter" || arg == "--branch" || arg == "-b" {
			skip = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		target = arg
		break
	}
	remote := target
	if !strings.Contains(remote, "://") && !strings.HasPrefix(remote, "git@") {
		probe := exec.CommandContext(ctx, "git", "remote", "get-url", target)
		if op == "push" {
			probe = exec.CommandContext(ctx, "git", "remote", "get-url", "--push", target)
		}
		probe.Dir = dir
		out, e := probe.Output()
		if e != nil {
			return args, env, nil
		}
		remote = strings.TrimSpace(string(out))
	}
	sshPrefix := "git@github.com:"
	if strings.HasPrefix(remote, sshPrefix) {
		remote = "https://github.com/" + strings.TrimPrefix(remote, sshPrefix)
	}
	u, e := url.Parse(remote)
	if e != nil || u.Scheme != "https" {
		return args, env, nil
	}
	repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	base := ""
	if u.Host != "github.com" {
		rc, ok := cfg.Repos[repo]
		if !ok || rc.Platform == "gitlab" || rc.BaseURL == "" {
			return args, env, nil
		}
		api, e := url.Parse(rc.BaseURL)
		if e != nil || api.Host != u.Host {
			return args, env, nil
		}
		base = rc.BaseURL
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		return nil, nil, fmt.Errorf("github_app: cannot load fallback credentials")
	}
	id, err := githubauth.Resolve(ctx, repo, creds.GHToken, base)
	if err != nil {
		return nil, nil, err
	}
	if id.Token == "" {
		return args, env, nil
	}
	// Reject old credential-bearing remotes instead of persisting or logging them.
	if u.User != nil {
		return nil, nil, fmt.Errorf("github_app: remove credentials from the Git remote URL")
	}
	prefix := []string{"-c", "credential.helper=", "-c", "credential.helper=" + githubHelper, "-c", "credential.useHttpPath=true"}
	if strings.HasPrefix(strings.TrimSpace(remote), "https://github.com/") {
		prefix = append(prefix, "-c", "url.https://github.com/.insteadOf=git@github.com:")
	}
	clean := env[:0]
	for _, v := range env {
		if !strings.HasPrefix(v, "CLAWFLOW_GIT_") {
			clean = append(clean, v)
		}
	}
	env = append(clean, "CLAWFLOW_GIT_HOST="+u.Host, "CLAWFLOW_GIT_PATH="+strings.TrimPrefix(u.Path, "/"), "CLAWFLOW_GIT_TOKEN="+id.Token)
	return append(prefix, args...), env, nil
}
