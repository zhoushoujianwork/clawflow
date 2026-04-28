package pty

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
	"github.com/gorilla/websocket"
	"github.com/zhoushoujianwork/clawflow/internal/chat"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WebSocket close codes the browser sends to express user intent. The
// browser-reserved range starts at 4000; pick anything in 4000–4999 and
// keep both ends in sync.
const (
	wsCloseDestroy  = 4001 // X button: end the chat for good (delete transcript)
	wsCloseCollapse = 4000 // outside-click / Escape: hide drawer, keep transcript
)

// HandlePTY upgrades an HTTP request to a WebSocket and bridges it to a
// `clawflow chat` subprocess running in a PTY. Query params:
//
//	repo  — required, e.g. "owner/repo"
//	issue — optional issue number
//	model — optional model name (default: haiku)
//
// On WS close the browser tags its intent via a 4xxx close code:
//   - wsCloseDestroy: delete the session transcript so the next open
//     starts fresh
//   - wsCloseCollapse (or any other code): keep the transcript so the
//     next open --resumes mid-conversation
func HandlePTY(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		http.Error(w, "repo query param required", http.StatusBadRequest)
		return
	}
	issueNum, _ := strconv.Atoi(r.URL.Query().Get("issue"))

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	args := []string{"chat", "--repo", repo}
	if issue := r.URL.Query().Get("issue"); issue != "" {
		args = append(args, "--issue", issue)
	}
	if model := r.URL.Query().Get("model"); model != "" {
		args = append(args, "--model", model)
	}

	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}

	cmd := exec.Command(self, args...)
	cmd.Env = os.Environ()

	ptmx, err := creackpty.Start(cmd)
	if err != nil {
		msg := fmt.Sprintf("pty start: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte(msg))
		return
	}
	defer ptmx.Close()

	// `done` fires the moment either io goroutine returns — i.e. the
	// browser closed the WebSocket. Without an explicit teardown the
	// `claude` subprocess keeps running and holds its `--session-id`
	// lock; the next reconnect for the same repo+issue then fails with
	// "Session ID … is already in use".
	done := make(chan struct{})
	var doneOnce sync.Once
	signalDone := func() { doneOnce.Do(func() { close(done) }) }

	// destroy is set when the WS→PTY goroutine sees a close frame with
	// code wsCloseDestroy. After the subprocess exits we use this to
	// decide whether to wipe the session transcript on disk.
	var destroy atomic.Bool

	// PTY → WebSocket
	go func() {
		defer signalDone()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket → PTY
	go func() {
		defer signalDone()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				// Inspect the close frame for the user's intent. Gorilla
				// surfaces it as a *websocket.CloseError whose Code we
				// can read directly.
				if ce, ok := err.(*websocket.CloseError); ok && ce.Code == wsCloseDestroy {
					destroy.Store(true)
				}
				return
			}
			if _, err := ptmx.Write(msg); err != nil {
				return
			}
		}
	}()

	<-done

	// SIGTERM lets claude release its session-id lock cleanly. If it
	// doesn't exit within the grace window (rare; usually it's
	// effectively instant on a stdin-blocking REPL), SIGKILL guarantees
	// the next connection isn't blocked behind a stale subprocess.
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-exited
	}

	if destroy.Load() {
		// Subprocess is fully gone; safe to delete the on-disk transcript.
		// Glob across all project dirs because session files for the same
		// (repo, issue) can have accumulated across cwd evolutions.
		sid := chat.SessionID(repo, issueNum)
		_, _ = chat.DeleteSessions(sid)
	}
}
