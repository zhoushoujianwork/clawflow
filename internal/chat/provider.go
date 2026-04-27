package chat

import (
	"context"
	"io"
	"time"
)

// Message represents a single turn in a chat conversation.
type Message struct {
	Role      string    `json:"role"`    // "user" | "assistant"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Request holds everything needed for a single chat turn.
type Request struct {
	System   string    // injected context (repo/issue info)
	Messages []Message // conversation history
	Model    string    // claude model name, default "haiku"
}

// Provider abstracts the AI backend. The default implementation wraps
// `claude -p`; future implementations can target opencode or direct API.
type Provider interface {
	Chat(ctx context.Context, req Request, out io.Writer) error
}
