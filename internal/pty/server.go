package pty

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"

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

	var wg sync.WaitGroup
	wg.Add(2)

	// PTY stdout → WebSocket
	go func() {
		defer wg.Done()
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

	// WebSocket → PTY stdin
	go func() {
		defer wg.Done()
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

	// Wait for the subprocess to exit, then close connections.
	_ = cmd.Wait()
	wg.Wait()
}
