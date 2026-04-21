package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// logStreamer pushes each stdout line from `claude -p` to SaaS over WS as it
// arrives, so users see subagent progress in the dashboard without waiting
// for the end-of-run batch upload. A nil streamer (dial failed) is safe to
// call — Send/Close are no-ops. The end-of-run HTTP batch upload is still
// the source of truth and acts as the fallback.
type logStreamer struct {
	conn *websocket.Conn
}

// dialLogStream builds ws:// or wss:// from the configured SaaS URL and opens
// a connection to /api/ws/worker/tasks/:taskID/stream. Auth is the same
// Bearer worker_token the HTTP endpoints use — WS clients can set custom
// headers (unlike browsers).
func dialLogStream(wc *config.WorkerConfig, taskID string) *logStreamer {
	if wc == nil || wc.SaasURL == "" || wc.WorkerToken == "" {
		return nil
	}
	u, err := url.Parse(wc.SaasURL)
	if err != nil {
		return nil
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/ws/worker/tasks/" + taskID + "/stream"

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+wc.WorkerToken)
	if id := agentIDHeader(); id != "" {
		headers.Set("X-Agent-Id", id)
	}

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	conn, resp, err := dialer.Dial(u.String(), headers)
	if err != nil {
		if resp != nil {
			fmt.Printf("[warn] log stream dial failed: %v (status=%d)\n", err, resp.StatusCode)
		} else {
			fmt.Printf("[warn] log stream dial failed: %v\n", err)
		}
		return nil
	}
	return &logStreamer{conn: conn}
}

// Send forwards one log entry. rawLine is the original stdout bytes — if
// they parse as JSON, they become raw_event; otherwise only the summary
// message is sent.
func (s *logStreamer) Send(level, message string, rawLine []byte) {
	if s == nil || s.conn == nil {
		return
	}
	env := map[string]interface{}{
		"level":   level,
		"message": message,
	}
	if len(rawLine) > 0 {
		var parsed interface{}
		if err := json.Unmarshal(rawLine, &parsed); err == nil {
			env["raw_event"] = parsed
		}
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := s.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		// Drop the connection so subsequent Sends become no-ops; the batch
		// HTTP upload at the end still catches everything.
		_ = s.conn.Close()
		s.conn = nil
	}
}

func (s *logStreamer) Close() {
	if s == nil || s.conn == nil {
		return
	}
	_ = s.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(1*time.Second))
	_ = s.conn.Close()
	s.conn = nil
}
