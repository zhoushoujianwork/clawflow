package pty

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
	"github.com/gorilla/websocket"
	"github.com/zhoushoujianwork/clawflow/internal/chat"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WebSocket close codes the browser uses to signal user intent.
// Browsers reserve the 4000–4999 range for app use; keep both ends in
// sync with web/src/components/ChatDrawer.tsx.
const (
	wsCloseDestroy  = 4001 // X button: end the chat for good (delete transcript)
	wsCloseCollapse = 4000 // outside-click / Escape: detach but keep subprocess running
)

const (
	// bufferCap is how much PTY output we retain for replay on
	// reattach. xterm needs this to reconstruct the visible state
	// when the user reopens a collapsed drawer mid-conversation.
	bufferCap = 256 * 1024

	// idleKill is how long a session can stay detached (no
	// WebSocket attached) before the reaper terminates it. The
	// transcript on disk survives so the next reopen --resumes.
	idleKill = 30 * time.Minute
)

// session represents one `clawflow chat` subprocess. A session can
// outlive any individual WebSocket: when the user collapses the
// drawer (close code 4000), the WS closes but the subprocess keeps
// running so any in-flight claude turn finishes uninterrupted. A
// later reopen reattaches to the same subprocess and replays the
// output buffer so the user sees a continuous transcript.
type session struct {
	key      string // "<repo>#<issue>" — uniqueness scope
	repo     string
	issueNum int

	cmd  *exec.Cmd
	ptmx *os.File

	bufMu sync.Mutex
	buf   ringBuffer

	attachMu   sync.Mutex
	conn       *websocket.Conn // nil when detached
	detachedAt time.Time       // zero until first detach

	closed    chan struct{}
	closeOnce sync.Once
}

// ringBuffer holds at most `cap` bytes; writes past the cap drop the
// oldest data. Cheap and good enough for replay-on-reattach.
type ringBuffer struct {
	data []byte
	cap  int
}

func (r *ringBuffer) Write(p []byte) {
	r.data = append(r.data, p...)
	if len(r.data) > r.cap {
		r.data = append([]byte(nil), r.data[len(r.data)-r.cap:]...)
	}
}

func (r *ringBuffer) Snapshot() []byte {
	return append([]byte(nil), r.data...)
}

var (
	sessionsMu sync.Mutex
	sessions   = map[string]*session{}
)

func init() {
	go reaper()
}

func sessionKey(repo string, issue int) string {
	return fmt.Sprintf("%s#%d", repo, issue)
}

// HandlePTY upgrades an HTTP request to a WebSocket and either
// reattaches to a running chat session for (repo, issue) or spawns a
// fresh one. Query params:
//
//	repo  — required, e.g. "owner/repo"
//	issue — optional issue number
//	model — optional model name (default: haiku)
//
// On WS close the browser tags its intent via a 4xxx close code:
//   - wsCloseDestroy: kill the subprocess and delete the on-disk
//     session transcript so the next open starts fresh.
//   - wsCloseCollapse (or any other code): detach this WS, leave the
//     subprocess running so claude can finish its work; a later
//     reopen reattaches to the same process and replays buffered
//     output.
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

	key := sessionKey(repo, issueNum)

	sessionsMu.Lock()
	s, ok := sessions[key]
	if ok {
		// Stale entry: subprocess already exited. Drop it so we
		// spawn fresh below.
		select {
		case <-s.closed:
			delete(sessions, key)
			ok = false
		default:
		}
	}
	if !ok {
		s, err = newSession(key, repo, issueNum, r)
		if err != nil {
			sessionsMu.Unlock()
			_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("pty start: %v", err)))
			return
		}
		sessions[key] = s
	}
	sessionsMu.Unlock()

	// attach takes over the conn; returns when the WS closes or
	// the subprocess exits. Returns the user's close intent.
	intent := s.attach(conn)

	switch intent {
	case "destroy":
		s.destroy()
	case "subprocess-exited":
		// Subprocess died on its own (claude /exit, crash, etc.).
		// Map already cleaned up by markClosed. Nothing to do.
	default:
		// "collapse" or unknown — leave subprocess running for the
		// next reopen. Reaper will sweep it after idleKill.
	}
}

// newSession spawns a `clawflow chat` subprocess attached to a fresh
// PTY and starts the always-on PTY reader goroutine. Caller must
// register the returned session in `sessions` while holding
// sessionsMu before any other goroutine can find it.
func newSession(key, repo string, issueNum int, r *http.Request) (*session, error) {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}

	args := []string{"chat", "--repo", repo}
	if issueStr := r.URL.Query().Get("issue"); issueStr != "" {
		args = append(args, "--issue", issueStr)
	}
	if model := r.URL.Query().Get("model"); model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.Command(self, args...)
	cmd.Env = os.Environ()

	ptmx, err := creackpty.Start(cmd)
	if err != nil {
		return nil, err
	}

	s := &session{
		key:      key,
		repo:     repo,
		issueNum: issueNum,
		cmd:      cmd,
		ptmx:     ptmx,
		buf:      ringBuffer{cap: bufferCap},
		closed:   make(chan struct{}),
	}

	go s.readPTY()
	return s, nil
}

// readPTY drains the PTY into the buffer, forwarding live to any
// attached conn. Runs for the lifetime of the subprocess; on EOF or
// error it triggers session cleanup via markClosed.
func (s *session) readPTY() {
	defer s.markClosed()
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)

			s.bufMu.Lock()
			s.buf.Write(chunk)
			s.bufMu.Unlock()

			s.attachMu.Lock()
			conn := s.conn
			s.attachMu.Unlock()
			if conn != nil {
				if werr := conn.WriteMessage(websocket.BinaryMessage, chunk); werr != nil {
					// Detach on write error so the next reattach
					// can succeed; subprocess keeps running.
					s.attachMu.Lock()
					if s.conn == conn {
						s.conn = nil
						s.detachedAt = time.Now()
					}
					s.attachMu.Unlock()
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// attach takes ownership of `conn` for the duration of one
// connection. Replays the buffer first so the user sees state up to
// "now", then pumps WS reads into the PTY. Returns:
//   - "destroy" when the WS closes with code wsCloseDestroy
//   - "collapse" when the WS closes any other way
//   - "subprocess-exited" when the subprocess died first
func (s *session) attach(conn *websocket.Conn) string {
	// Single-attach: force-detach any prior conn so a second tab
	// opening the same chat takes over cleanly.
	s.attachMu.Lock()
	if old := s.conn; old != nil {
		_ = old.Close()
	}
	s.conn = conn
	s.detachedAt = time.Time{}
	s.attachMu.Unlock()

	// Replay everything we've buffered. xterm reconstructs visible
	// state by parsing the ANSI stream front-to-back.
	s.bufMu.Lock()
	snapshot := s.buf.Snapshot()
	s.bufMu.Unlock()
	if len(snapshot) > 0 {
		_ = conn.WriteMessage(websocket.BinaryMessage, snapshot)
	}

	intent := "collapse"
	for {
		select {
		case <-s.closed:
			return "subprocess-exited"
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ce, ok := err.(*websocket.CloseError); ok && ce.Code == wsCloseDestroy {
				intent = "destroy"
			}
			// Detach without killing the subprocess.
			s.attachMu.Lock()
			if s.conn == conn {
				s.conn = nil
				s.detachedAt = time.Now()
			}
			s.attachMu.Unlock()
			return intent
		}
		if _, err := s.ptmx.Write(msg); err != nil {
			return intent
		}
	}
}

// markClosed runs exactly once when the subprocess exits. Drops the
// session from the registry so future opens spawn fresh, closes the
// PTY master, and reaps the child to release its resources.
func (s *session) markClosed() {
	s.closeOnce.Do(func() {
		close(s.closed)
		sessionsMu.Lock()
		if sessions[s.key] == s {
			delete(sessions, s.key)
		}
		sessionsMu.Unlock()
		_ = s.ptmx.Close()
		_ = s.cmd.Wait()
	})
}

// destroy terminates the subprocess (SIGTERM with a 2s SIGKILL
// fallback) and deletes its on-disk transcript so the next reopen
// starts a fresh conversation.
func (s *session) destroy() {
	s.killGroup(syscall.SIGTERM)
	select {
	case <-s.closed:
	case <-time.After(2 * time.Second):
		s.killGroup(syscall.SIGKILL)
		<-s.closed
	}
	sid := chat.SessionID(s.repo, s.issueNum)
	_, _ = chat.DeleteSessions(sid)
}

// killGroup signals the subprocess's entire process group so the
// `claude` grandchild dies along with the `clawflow chat` wrapper.
// Signaling only cmd.Process orphans claude, which keeps writing
// to the session jsonl we're about to delete and produces a stale
// transcript on disk. creackpty.Start uses Setsid, which puts the
// child in its own session+group, so the negative PID is safe (it
// won't reach the clawflow-web process).
func (s *session) killGroup(sig syscall.Signal) {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	pid := s.cmd.Process.Pid
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, sig); err != nil {
		// Group kill not allowed (no Setsid?) — fall back to the
		// single-process signal so we still try our best.
		_ = s.cmd.Process.Signal(sig)
	}
}

// reaper periodically kills sessions that have been detached for too
// long. Keeps SIGTERM-exit semantics — transcript on disk survives so
// users can still --resume after the reaper sweeps a forgotten chat.
func reaper() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		var stale []*session
		sessionsMu.Lock()
		for _, s := range sessions {
			s.attachMu.Lock()
			if s.conn == nil && !s.detachedAt.IsZero() && now.Sub(s.detachedAt) > idleKill {
				stale = append(stale, s)
			}
			s.attachMu.Unlock()
		}
		sessionsMu.Unlock()

		for _, s := range stale {
			s.killGroup(syscall.SIGTERM)
			select {
			case <-s.closed:
			case <-time.After(2 * time.Second):
				s.killGroup(syscall.SIGKILL)
			}
		}
	}
}

