// Package api — chat_spawn opens the user's native terminal pre-loaded
// with `clawflow chat --repo X --issue Y`. Trades the in-browser xterm.js
// drawer for whatever terminal emulator the user already trusts (font,
// keybindings, copy/paste, IME, scrollback). The chat CLI itself already
// writes the issue/repo context as a system prompt at session start, so
// "preloaded with context" is automatic — this endpoint is just a launcher.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// chatSpawnRequest names the issue (or whole repo) the user wants
// to chat about. Issue=0 means "chat at repo level"; the CLI handles
// both shapes via clawflow chat --repo <r> [--issue <n>].
//
// Project-scoped spawns use the Project + Action fields instead of
// Repo/Issue. Action is "chat" or "generate"; the spawned command is
// `clawflow project <action> <name>`.
type chatSpawnRequest struct {
	Repo    string `json:"repo,omitempty"`
	Issue   int    `json:"issue,omitempty"`
	Model   string `json:"model,omitempty"`
	Project string `json:"project,omitempty"`
	Action  string `json:"action,omitempty"` // "generate" or "chat" (project-scoped)
}

// HandleChatSpawn POSTs spawn a clawflow-chat session in the user's
// native terminal application. Returns 200 + {status:"ok"} once the
// terminal has been asked to open; we don't wait for the chat to
// actually start (that's owned by the OS / terminal app).
//
// Platform support:
//   - macOS: Terminal.app via osascript (most common, works everywhere).
//   - linux: $TERMINAL env, then a short list of common emulators.
//   - other: 501 Not Implemented with a hint to run the printed command
//            manually.
func HandleChatSpawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req chatSpawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}

	// Require either a repo or a project target.
	hasRepo := strings.TrimSpace(req.Repo) != ""
	hasProject := strings.TrimSpace(req.Project) != ""
	if !hasRepo && !hasProject {
		writeJSON(w, 400, map[string]string{"error": "repo or project is required"})
		return
	}

	// Use the absolute path of the currently running clawflow binary
	// so the spawned terminal hits the SAME version, regardless of
	// what's on the user's PATH. Falls back to the bare name if the
	// OS can't tell us our own path (rare; e.g. binary self-deleted).
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "clawflow"
	}

	var parts []string
	if hasProject {
		// Project-scoped spawn: `clawflow project <action> <name>`
		action := req.Action
		if action != "generate" {
			action = "chat" // default to chat
		}
		parts = []string{shellEscape(self), "project", action, shellEscape(req.Project)}
	} else {
		// Repo-scoped spawn: `clawflow chat --repo <r> [--issue <n>]`
		parts = []string{shellEscape(self), "chat", "--repo", shellEscape(req.Repo)}
		if req.Issue > 0 {
			parts = append(parts, "--issue", strconv.Itoa(req.Issue))
		}
	}
	if req.Model != "" {
		parts = append(parts, "--model", shellEscape(req.Model))
	}
	cmdLine := strings.Join(parts, " ")

	if err := openInTerminal(cmdLine); err != nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error":   err.Error(),
			"command": cmdLine,
			"hint":    "Run the command above in your own terminal.",
		})
		return
	}
	writeJSON(w, 200, map[string]string{
		"status":  "ok",
		"command": cmdLine,
	})
}

// openInTerminal asks the OS to open a terminal window and run cmdLine
// inside it. The returned error names the platform-specific failure
// (no terminal found, osascript missing, etc.) so the caller can
// surface it to the user verbatim.
func openInTerminal(cmdLine string) error {
	switch runtime.GOOS {
	case "darwin":
		return openInTerminalMac(cmdLine)
	case "linux":
		return openInTerminalLinux(cmdLine)
	default:
		return fmt.Errorf("native terminal launch not implemented for %s", runtime.GOOS)
	}
}

// openInTerminalMac uses AppleScript to ask Terminal.app to run the
// command in a new window and bring itself to the foreground. Picked
// over `open -a Terminal <script.command>` because we don't have to
// write a temp file — and over iTerm-specific scripting because
// Terminal.app is universally present on macOS.
func openInTerminalMac(cmdLine string) error {
	if _, err := exec.LookPath("osascript"); err != nil {
		return fmt.Errorf("osascript not found on PATH: %w", err)
	}
	script := fmt.Sprintf(
		`tell application "Terminal" to do script %s
tell application "Terminal" to activate`,
		appleScriptQuote(cmdLine),
	)
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// openInTerminalLinux tries a short ladder of emulators. $TERMINAL
// wins if set (matches xdg conventions); otherwise we look for the
// common defaults on Ubuntu/Debian/Fedora/Arch desktops. Each
// emulator has a slightly different "run this command" flag — we
// hand-pick the one that's known to work for each.
func openInTerminalLinux(cmdLine string) error {
	type term struct {
		bin  string
		args []string // {flags before cmdLine, ...}; the cmdLine is appended last
	}
	candidates := []term{}
	if t := os.Getenv("TERMINAL"); t != "" {
		// User-specified — trust their pick. Most emulators accept -e
		// for "execute"; if theirs doesn't they can override the
		// behavior by writing a tiny wrapper script.
		candidates = append(candidates, term{bin: t, args: []string{"-e"}})
	}
	candidates = append(candidates,
		term{bin: "x-terminal-emulator", args: []string{"-e"}},
		term{bin: "gnome-terminal", args: []string{"--", "bash", "-c"}},
		term{bin: "konsole", args: []string{"-e", "bash", "-c"}},
		term{bin: "xfce4-terminal", args: []string{"-e"}},
		term{bin: "alacritty", args: []string{"-e", "bash", "-c"}},
		term{bin: "kitty", args: []string{"bash", "-c"}},
		term{bin: "xterm", args: []string{"-e", "bash", "-c"}},
	)
	for _, t := range candidates {
		path, err := exec.LookPath(t.bin)
		if err != nil {
			continue
		}
		args := append([]string(nil), t.args...)
		args = append(args, cmdLine)
		cmd := exec.Command(path, args...)
		// Detach: we don't want our process to be the parent of a
		// long-lived terminal window; if clawflow web exits the user's
		// chat shouldn't die with it.
		cmd.SysProcAttr = detachAttr()
		if err := cmd.Start(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no supported terminal emulator found (tried $TERMINAL, x-terminal-emulator, gnome-terminal, konsole, xfce4-terminal, alacritty, kitty, xterm)")
}

// shellEscape wraps a value in single quotes for safe inclusion in a
// POSIX shell command. Embedded single quotes are escaped via the
// standard '"'"' dance.
func shellEscape(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// appleScriptQuote returns the AppleScript-string literal form of s,
// suitable as the argument to `do script`. AppleScript strings use
// double quotes and escape backslash + quote with backslashes.
func appleScriptQuote(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
	)
	return `"` + r.Replace(s) + `"`
}
