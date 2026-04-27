package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionMeta holds metadata for a chat session.
type SessionMeta struct {
	ID          string `json:"id"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number,omitempty"`
	Model       string `json:"model"`
	CreatedAt   string `json:"created_at"`
}

// SessionDir returns the base directory for all chat sessions.
func SessionDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "chats")
}

// SessionPath returns the directory for a specific session.
func SessionPath(repoSlug, sessionID string) string {
	return filepath.Join(SessionDir(), repoSlug, sessionID)
}

// NewSession creates a new session directory and writes meta.json.
func NewSession(repo string, issueNumber int, model string) (*SessionMeta, string, error) {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	slug := repoSlug(repo)
	dir := SessionPath(slug, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}

	meta := &SessionMeta{
		ID:          id,
		Repo:        repo,
		IssueNumber: issueNumber,
		Model:       model,
		CreatedAt:   time.Now().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
		return nil, "", err
	}

	return meta, dir, nil
}

// AppendMessage appends a message to the session's messages.jsonl.
func AppendMessage(sessionDir string, msg Message) error {
	f, err := os.OpenFile(filepath.Join(sessionDir, "messages.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(msg)
}

// LoadMessages reads all messages from a session's messages.jsonl.
func LoadMessages(sessionDir string) ([]Message, error) {
	data, err := os.ReadFile(filepath.Join(sessionDir, "messages.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var msgs []Message
	dec := json.NewDecoder(strings.NewReader(string(data)))
	for dec.More() {
		var m Message
		if err := dec.Decode(&m); err != nil {
			break
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// LoadSessionMeta reads meta.json from a session directory.
func LoadSessionMeta(sessionDir string) (*SessionMeta, error) {
	data, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	if err != nil {
		return nil, err
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func repoSlug(repo string) string {
	return strings.ReplaceAll(repo, "/", "__")
}
