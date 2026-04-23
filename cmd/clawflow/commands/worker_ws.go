package commands

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// wsChannel maintains a persistent WebSocket connection to the SaaS control
// channel. It receives real-time task pushes and sends heartbeat/discover/
// health messages, replacing the separate HTTP polling loops when connected.
type wsChannel struct {
	wc   *config.WorkerConfig
	conn *websocket.Conn
	mu   sync.Mutex

	// taskCh delivers task_available and tasks_snapshot payloads to the main
	// worker loop so it can process them immediately without polling.
	taskCh chan []workerTask

	// connected is true when the WS is up. The main loop checks this to
	// decide whether to fall back to HTTP polling.
	connected bool
	connMu    sync.RWMutex

	stopCh chan struct{}
}

type wsServerMsg struct {
	Type  string          `json:"type"`
	Task  json.RawMessage `json:"task,omitempty"`
	Tasks json.RawMessage `json:"tasks,omitempty"`
}

func newWSChannel(wc *config.WorkerConfig) *wsChannel {
	return &wsChannel{
		wc:     wc,
		taskCh: make(chan []workerTask, 16),
		stopCh: make(chan struct{}),
	}
}

func (ws *wsChannel) IsConnected() bool {
	ws.connMu.RLock()
	defer ws.connMu.RUnlock()
	return ws.connected
}

func (ws *wsChannel) setConnected(v bool) {
	ws.connMu.Lock()
	ws.connected = v
	ws.connMu.Unlock()
}

func (ws *wsChannel) TaskCh() <-chan []workerTask {
	return ws.taskCh
}

// Run connects to the WS control channel and reconnects on failure with
// exponential backoff. Blocks until stopCh is closed.
func (ws *wsChannel) Run() {
	attempt := 0
	for {
		select {
		case <-ws.stopCh:
			return
		default:
		}

		err := ws.connect()
		if err != nil {
			attempt++
			delay := wsBackoff(attempt)
			fmt.Printf("[ws] connect failed: %v — retry in %s\n", err, delay)
			select {
			case <-ws.stopCh:
				return
			case <-time.After(delay):
			}
			continue
		}

		attempt = 0
		ws.setConnected(true)
		fmt.Println("[ws] control channel connected")

		ws.readLoop()

		ws.setConnected(false)
		fmt.Println("[ws] control channel disconnected")
	}
}

func (ws *wsChannel) Stop() {
	close(ws.stopCh)
	ws.mu.Lock()
	if ws.conn != nil {
		_ = ws.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(1*time.Second))
		_ = ws.conn.Close()
		ws.conn = nil
	}
	ws.mu.Unlock()
}

func (ws *wsChannel) connect() error {
	u, err := url.Parse(ws.wc.SaasURL)
	if err != nil {
		return err
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/ws/worker/channel"

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+ws.wc.WorkerToken)
	if id := agentIDHeader(); id != "" {
		headers.Set("X-Agent-Id", id)
	}

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	conn, resp, err := dialer.Dial(u.String(), headers)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial: %v (status=%d)", err, resp.StatusCode)
		}
		return fmt.Errorf("dial: %v", err)
	}

	ws.mu.Lock()
	ws.conn = conn
	ws.mu.Unlock()
	return nil
}

func (ws *wsChannel) readLoop() {
	for {
		select {
		case <-ws.stopCh:
			return
		default:
		}

		ws.mu.Lock()
		conn := ws.conn
		ws.mu.Unlock()
		if conn == nil {
			return
		}

		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, message, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg wsServerMsg
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "task_available":
			if msg.Task != nil {
				var t workerTask
				if err := json.Unmarshal(msg.Task, &t); err == nil {
					select {
					case ws.taskCh <- []workerTask{t}:
					default:
					}
				}
			}
		case "tasks_snapshot":
			if msg.Tasks != nil {
				var tasks []workerTask
				if err := json.Unmarshal(msg.Tasks, &tasks); err == nil && len(tasks) > 0 {
					select {
					case ws.taskCh <- tasks:
					default:
					}
				}
			}
		}
	}
}

// SendHeartbeat sends a heartbeat message over the WS channel.
// Returns false if not connected.
func (ws *wsChannel) SendHeartbeat(hostname, cliVersion string) bool {
	return ws.sendJSON(map[string]any{
		"type":        "heartbeat",
		"hostname":    hostname,
		"cli_version": cliVersion,
	})
}

// SendDiscover sends a discovered issue over the WS channel.
func (ws *wsChannel) SendDiscover(platform, fullName string, issueNumber int64, title string) bool {
	return ws.sendJSON(map[string]any{
		"type":         "discover",
		"platform":     platform,
		"full_name":    fullName,
		"issue_number": issueNumber,
		"issue_title":  title,
	})
}

// SendHealthReport sends a health check result over the WS channel.
func (ws *wsChannel) SendHealthReport(platform, fullName string, ok bool, message string) bool {
	return ws.sendJSON(map[string]any{
		"type":      "health_report",
		"platform":  platform,
		"full_name": fullName,
		"ok":        ok,
		"message":   message,
	})
}

func (ws *wsChannel) sendJSON(v any) bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.conn == nil {
		return false
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return false
	}
	_ = ws.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := ws.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		_ = ws.conn.Close()
		ws.conn = nil
		return false
	}
	return true
}

func wsBackoff(attempt int) time.Duration {
	secs := math.Min(float64(int(1)<<uint(attempt)), 30)
	return time.Duration(secs) * time.Second
}
