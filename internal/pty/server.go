package pty

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// HandlePTY upgrades an HTTP request to a WebSocket and bridges it to a
// `clawflow chat` subprocess running in a PTY. Query params:
//
//	repo  — required, e.g. "owner/repo"
//	issue — optional issue number
//	model — optional model name (default: haiku)
func HandlePTY(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		http.Error(w, "repo query param required", http.StatusBadRequest)
		return
	}

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
}
