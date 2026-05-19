package chat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// sessionTimeout is the hard cap on a single chat session: clone +
// claude exit must complete within this window.
const sessionTimeout = 30 * time.Minute

// flushInterval / flushBytes control how aggressively stdout chunks
// are flushed to the cloud. A user typing /quit should see ack within
// flushInterval; a verbose claude shouldn't generate one POST per
// line.
const (
	flushInterval = 250 * time.Millisecond
	flushBytes    = 32 * 1024
)

// stderrTailBytes is the size of the rolling stderr buffer reported on
// failure via ChatEventError.
const stderrTailBytes = 4096

// runSession handles one ChatAssignment end-to-end. It is goroutine-
// safe with respect to other concurrent sessions: each session has
// its own workdir, subprocess, and event batcher.
//
// Errors returned by runSession are advisory — every failure path
// also pushes a ChatEventError to the cloud so the browser-side UI
// gets feedback regardless of whether the caller logs the return.
func runSession(parent context.Context, l *Loop, a *cloud.ChatAssignment) error {
	ctx, cancel := context.WithTimeout(parent, sessionTimeout)
	defer cancel()

	// Path traversal guard. Cloud already validates, but the worker
	// MUST defend against a hostile or buggy upstream — a bad repo
	// slug here would otherwise lead to clones outside ClonesDir.
	if err := validateRepo(a.Repo); err != nil {
		l.emitError(a.SessionID, fmt.Sprintf("invalid repo %q: %v", a.Repo, err))
		return err
	}

	apiKey, baseURL, err := pickProvider(l.cfg.Creds)
	if err != nil {
		l.emitError(a.SessionID, err.Error())
		return err
	}

	workdir, err := ensureWorkdir(ctx, l, a)
	if err != nil {
		l.emitError(a.SessionID, fmt.Sprintf("workdir: %v", err))
		return err
	}

	return l.runClaude(ctx, a, workdir, apiKey, baseURL)
}

// runClaude spawns the claude subprocess and shuttles stdout/stderr
// back to cloud as ChatEvents. stdout is parsed as `--output-format
// stream-json --verbose` so the worker can both unwrap assistant text
// deltas for the browser AND extract the terminal "result" event's
// token/cost breakdown for the cloud usage POST.
func (l *Loop) runClaude(ctx context.Context, a *cloud.ChatAssignment, workdir, apiKey, baseURL string) error {
	cmd := exec.CommandContext(ctx, l.cfg.ClaudeBin,
		"-p", a.Message,
		"--output-format", "stream-json",
		"--verbose",
	)
	cmd.Dir = workdir
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)
	// Provide the message on stdin too so claude clients that prefer
	// stdin over positional arg still see it.
	cmd.Stdin = strings.NewReader(a.Message)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		l.emitError(a.SessionID, fmt.Sprintf("stdout pipe: %v", err))
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		l.emitError(a.SessionID, fmt.Sprintf("stderr pipe: %v", err))
		return err
	}

	if err := cmd.Start(); err != nil {
		l.emitError(a.SessionID, fmt.Sprintf("spawn claude: %v", err))
		return err
	}

	b := newBatcher(l, a.SessionID)
	defer b.stop()

	stderrTail := &boundedBuffer{cap: stderrTailBytes}

	var (
		wg    sync.WaitGroup
		usage *cloud.Usage
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		// parseStreamJSON unwraps assistant text deltas (→ browser via
		// the batcher) and captures the terminal result event's usage.
		usage = parseStreamJSON(stdout, func(text string) {
			b.add(cloud.ChatEvent{
				Type: cloud.ChatEventOutput,
				Text: text,
				Time: time.Now().UTC(),
			})
		})
	}()
	go func() {
		defer wg.Done()
		streamReader(stderrPipe, func(chunk []byte) {
			stderrTail.Write(chunk)
			b.add(cloud.ChatEvent{
				Type: cloud.ChatEventStderr,
				Text: string(chunk),
				Time: time.Now().UTC(),
			})
		})
	}()

	wg.Wait()
	waitErr := cmd.Wait()
	b.flushNow()

	// Best-effort usage upload: a transport error here is logged but
	// does NOT fail the session — the browser already saw the answer.
	if usage != nil {
		bg, cancel := context.WithTimeout(context.Background(), eventHTTPTimeout)
		if err := l.postChatUsage(bg, a.SessionID, usage); err != nil {
			fmt.Fprintf(stderr(), "clawflow chat: post usage: %v\n", err)
		}
		cancel()
	}

	if waitErr == nil {
		l.emitEnd(a.SessionID)
		return nil
	}

	msg := waitErr.Error()
	if tail := strings.TrimSpace(stderrTail.String()); tail != "" {
		msg = fmt.Sprintf("%s: %s", msg, tail)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		msg = "session timed out: " + msg
	}
	l.emitError(a.SessionID, msg)
	return waitErr
}

// streamReader reads from r in 4 KiB blocks and calls onChunk for each
// non-empty read. It returns when r is closed / errors.
func streamReader(r io.Reader, onChunk func([]byte)) {
	br := bufio.NewReaderSize(r, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			onChunk(chunk)
		}
		if err != nil {
			return
		}
	}
}

// ---- terminal-event helpers --------------------------------------

// emitEnd / emitError POST a single terminal event using a detached
// background context so they still go through even when the session
// ctx has been cancelled.
func (l *Loop) emitEnd(sessionID string) {
	bg, cancel := context.WithTimeout(context.Background(), eventHTTPTimeout)
	defer cancel()
	if err := l.postEvents(bg, sessionID, []cloud.ChatEvent{
		{Type: cloud.ChatEventEnd, Time: time.Now().UTC()},
	}); err != nil {
		fmt.Fprintf(stderr(), "clawflow chat: post end event: %v\n", err)
	}
}

func (l *Loop) emitError(sessionID, message string) {
	bg, cancel := context.WithTimeout(context.Background(), eventHTTPTimeout)
	defer cancel()
	if err := l.postEvents(bg, sessionID, []cloud.ChatEvent{
		{Type: cloud.ChatEventError, Text: message, Time: time.Now().UTC()},
	}); err != nil {
		fmt.Fprintf(stderr(), "clawflow chat: post error event: %v\n", err)
	}
}

// ---- workdir resolution + clone -----------------------------------

// ensureWorkdir picks the local clone path for a.Repo, cloning or
// updating it as needed. Resolution order:
//
//  1. cfg.Config.Repos[<repo>].LocalPath — exists → fetch+reset;
//     missing → clone there.
//  2. cfg.ClonesDir/<owner>/<name> — clone if absent, fetch+reset if
//     present.
//
// The repo's BaseBranch (or "main") is used as the reset target.
func ensureWorkdir(ctx context.Context, l *Loop, a *cloud.ChatAssignment) (string, error) {
	dir, branch := resolveWorkdir(l.cfg, a)
	if branch == "" {
		branch = "main"
	}
	if dir == "" {
		return "", errors.New("could not derive workdir for repo")
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if err := gitFetchReset(ctx, dir, branch); err != nil {
			return "", fmt.Errorf("git fetch/reset: %w", err)
		}
		return dir, nil
	}

	cloneURL, err := buildCloneURL(a, l.cfg.Creds)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if err := gitClone(ctx, cloneURL, dir); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}
	return dir, nil
}

// resolveWorkdir returns (path, branch) for a chat assignment.
// Prefers the LocalPath configured for the repo in config.Config;
// falls back to ClonesDir/<owner>/<name>.
func resolveWorkdir(cfg Config, a *cloud.ChatAssignment) (string, string) {
	if c, err := config.Load(); err == nil && c != nil {
		if r, ok := c.Repos[a.Repo]; ok {
			if r.LocalPath != "" {
				return expandHome(r.LocalPath), r.BaseBranch
			}
		}
	}
	parts := strings.SplitN(a.Repo, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return filepath.Join(cfg.ClonesDir, parts[0], parts[1]), ""
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func gitFetchReset(ctx context.Context, dir, branch string) error {
	if err := runGit(ctx, dir, "fetch", "--quiet", "origin"); err != nil {
		return err
	}
	return runGit(ctx, dir, "reset", "--hard", "origin/"+branch)
}

func gitClone(ctx context.Context, cloneURL, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", cloneURL, dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// buildCloneURL constructs the auth-embedded HTTPS clone URL for a
// chat assignment, picking the right token by Platform.
func buildCloneURL(a *cloud.ChatAssignment, creds *config.Credentials) (string, error) {
	switch a.Platform {
	case "", "github":
		token := ""
		if creds != nil {
			token = creds.GHToken
		}
		if token == "" {
			return "https://github.com/" + a.Repo + ".git", nil
		}
		return "https://x-access-token:" + token + "@github.com/" + a.Repo + ".git", nil
	case "gitlab":
		host := "gitlab.com"
		scheme := "https"
		if a.BaseURL != "" {
			u := strings.TrimSuffix(a.BaseURL, "/")
			switch {
			case strings.HasPrefix(u, "https://"):
				host = strings.TrimPrefix(u, "https://")
			case strings.HasPrefix(u, "http://"):
				host = strings.TrimPrefix(u, "http://")
				scheme = "http"
			default:
				host = u
			}
		}
		token := ""
		if creds != nil {
			token = creds.GitLabToken
		}
		if token == "" {
			return scheme + "://" + host + "/" + a.Repo + ".git", nil
		}
		return scheme + "://oauth2:" + token + "@" + host + "/" + a.Repo + ".git", nil
	default:
		return "", fmt.Errorf("unsupported platform %q", a.Platform)
	}
}

// validateRepo rejects assignment.Repo values that could escape the
// clones directory via path traversal.
func validateRepo(repo string) error {
	if repo == "" {
		return errors.New("empty")
	}
	if strings.Contains(repo, "..") {
		return errors.New("contains '..'")
	}
	if strings.Contains(repo, "/.") || strings.HasPrefix(repo, ".") {
		return errors.New("contains '/.' or starts with '.'")
	}
	if strings.ContainsAny(repo, "\\\x00\n\r") {
		return errors.New("contains invalid characters")
	}
	cleaned := filepath.ToSlash(filepath.Clean(repo))
	if cleaned != repo {
		return errors.New("not in canonical form")
	}
	if filepath.IsAbs(repo) {
		return errors.New("absolute path")
	}
	return nil
}

// pickProvider returns the (api_key, base_url) of the first enabled
// claude provider. Errors when none is configured at all.
//
// An enabled provider with both APIKey and BaseURL empty is the
// OAuth/keychain fallback — we return empty strings and let claude
// resolve auth via its own login state. That's a valid configuration
// for chat: ANTHROPIC_* env vars are simply left untouched.
func pickProvider(creds *config.Credentials) (string, string, error) {
	if creds == nil {
		return "", "", errors.New("no credentials loaded")
	}
	for _, p := range creds.ClaudeProviders {
		if !p.Enabled {
			continue
		}
		return p.APIKey, p.BaseURL, nil
	}
	// Legacy fallback to deprecated top-level fields.
	if creds.ClaudeAPIKey != "" || creds.ClaudeBaseURL != "" {
		return creds.ClaudeAPIKey, creds.ClaudeBaseURL, nil
	}
	return "", "", errors.New("no enabled Claude provider configured")
}

// stderr is a tiny indirection so tests can capture the package's log
// output without racing on os.Stderr.
var stderrSink io.Writer = os.Stderr

func stderr() io.Writer { return stderrSink }
