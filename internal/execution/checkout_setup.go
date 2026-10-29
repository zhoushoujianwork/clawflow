package execution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SetupExecutor uses the caller's native execution boundary, including its
// profile, sandbox, network policy and credential filtering.
type SetupExecutor func(context.Context, string, Options) (Result, error)

// PrepareCheckout runs a configured prerequisite once per checkout/command.
// Only a fixed status survives; shell output and native answer text are discarded.
func PrepareCheckout(ctx context.Context, command string, opts Options, execute SetupExecutor) string {
	if strings.TrimSpace(command) == "" {
		return ""
	}
	const dir = ".clawflow-checkout-setup"
	root := filepath.Join(opts.Workspace, dir)
	if st, err := os.Lstat(root); err == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
		return "failed"
	}
	// Keep the script and fallback out of ordinary git add/commit operations,
	// including repositories which do not already ignore generated assets.
	git := exec.CommandContext(ctx, "git", "rev-parse", "--git-path", "info/exclude")
	git.Dir = opts.Workspace
	b, err := git.Output()
	if err != nil {
		return "failed"
	}
	exclude := strings.TrimSpace(string(b))
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(opts.Workspace, exclude)
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0700); err != nil {
		return "failed"
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return "failed"
	}
	_, err = f.WriteString("\n/" + dir + "/\n/web/dist/index.html\n")
	closeErr := f.Close()
	if err != nil || closeErr != nil || os.MkdirAll(root, 0700) != nil {
		return "failed"
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(command)))
	statusPath := filepath.Join(root, key+".status")
	readStatus := func() string {
		b, _ := os.ReadFile(statusPath)
		switch string(b) {
		case "ok", "fallback", "failed":
			return string(b)
		}
		return "failed"
	}
	if st, err := os.Lstat(statusPath); err == nil {
		if !st.Mode().IsRegular() {
			return "failed"
		}
		return readStatus()
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	status := quote(filepath.ToSlash(filepath.Join(dir, key+".status")))
	// Both the command and fallback run inside the native sandbox. Never replace
	// tracked content or follow asset symlinks when creating the placeholder.
	script := "#!/bin/sh\n" +
		"if sh -c " + quote(command) + " >/dev/null 2>&1; then\n printf ok > " + status + "\n" +
		"elif [ ! -L web ] && [ ! -L web/dist ] && [ ! -L web/dist/index.html ] && " +
		"! git ls-files --error-unmatch web/dist/index.html >/dev/null 2>&1 && " +
		"mkdir -p web/dist && { [ -f web/dist/index.html ] || : > web/dist/index.html; }; then\n" +
		" printf fallback > " + status + "\nelse\n printf failed > " + status + "\nfi\n"
	scriptFile, err := os.CreateTemp(root, "run-*.sh")
	if err != nil {
		return "failed"
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	_, err = scriptFile.WriteString(script)
	closeErr = scriptFile.Close()
	if err != nil || closeErr != nil {
		return "failed"
	}
	statusFile, err := os.OpenFile(statusPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "failed"
	}
	_, err = statusFile.WriteString("failed")
	closeErr = statusFile.Close()
	if err != nil || closeErr != nil {
		return "failed"
	}
	if execute == nil {
		execute = Execute
	}
	setupCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	opts.Resume, opts.SessionKey, opts.Events, opts.OnResult, opts.OnSession = false, "", nil, nil, nil
	opts.PermissionMode, opts.ToolsDisabled = "bypassPermissions", false
	opts.Timeout = 10 * time.Minute
	_, err = execute(setupCtx, "Run only this prerequisite script once: sh "+quote(filepath.ToSlash(filepath.Join(dir, filepath.Base(scriptPath))))+" >/dev/null 2>&1. Do not inspect or report command output, read repository history, edit source, commit, or publish anything. Reply only done.", opts)
	if err != nil {
		return "failed"
	}
	return readStatus()
}
