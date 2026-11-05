// Package joblog defines the bounded, normalized execution-output event shared
// by workers (which read Offload transcripts) and the controller (which stores
// and serves them). It carries message, tool-call and tool-result content only;
// scheduling events, metadata events and final results are never part of it.
package joblog

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Per-field byte caps. Offload already bounds its own events; these caps keep
// the controller bounded even if a worker or a newer Offload sends more.
const (
	MaxTextBytes      = 64 << 10
	MaxArgumentsBytes = 16 << 10
	MaxOutputBytes    = 32 << 10
	MaxShortBytes     = 256
	MaxPageEvents     = 200
)

// Event types a reader may render. Anything else is skipped, never guessed.
const (
	TypeMessage      = "message"
	TypeToolCall     = "tool_call"
	TypeToolResult   = "tool_result"
	TypeBackendError = "backend_error"
	TypeGap          = "gap"
	TypeAttemptStart = "attempt_start"
	TypeEnd          = "execution_end"
	TypeCaptureStart = "capture_start"
)

// Availability values mirror Offload's transcript interface so the UI can tell
// every "no content" situation apart.
const (
	Available          = "available"
	NotYetAvailable    = "not_yet_available"
	NoCapturedContent  = "no_captured_content"
	CaptureDisabled    = "capture_disabled"
	Expired            = "expired"
	Unreadable         = "unreadable"
	UnsupportedBackend = "unsupported_backend"
	UnsupportedVersion = "unsupported_version"
	NotFound           = "not_found"
	Ambiguous          = "ambiguous"
	CursorInvalid      = "cursor_invalid"
	IdentityMismatch   = "identity_mismatch"
	// UnsupportedOffload is reported by ClawFlow when the installed Offload
	// has no -capture-transcript flag; Offload itself never produces it.
	UnsupportedOffload = "unsupported_offload"
)

var availability = map[string]bool{
	Available: true, NotYetAvailable: true, NoCapturedContent: true, CaptureDisabled: true, Expired: true,
	Unreadable: true, UnsupportedBackend: true, UnsupportedVersion: true, NotFound: true, Ambiguous: true,
	CursorInvalid: true, IdentityMismatch: true, UnsupportedOffload: true,
}

// ValidAvailability reports whether v is a known availability value.
func ValidAvailability(v string) bool { return availability[v] }

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// ValidID reports whether v is a safe invocation or run identifier.
func ValidID(v string) bool { return idPattern.MatchString(v) }

// Summary is Offload's capture summary from execution_end.
type Summary struct {
	Events           int   `json:"events"`
	ContentEvents    int   `json:"content_events"`
	Gaps             int   `json:"gaps"`
	TruncatedEvents  int   `json:"truncated_events"`
	DroppedEvents    int   `json:"dropped_events"`
	DroppedBytes     int64 `json:"dropped_bytes"`
	CaptureTruncated bool  `json:"capture_truncated"`
	WriteFailed      bool  `json:"write_failed"`
	SkippedBlocks    int   `json:"skipped_blocks"`
	Unrecognized     int   `json:"unrecognized_events"`
	OpenToolCalls    int   `json:"open_tool_calls"`
}

// Event is one normalized execution-output record.
type Event struct {
	Seq          int64          `json:"seq"`
	TS           string         `json:"ts,omitempty"`
	Type         string         `json:"type"`
	Attempt      int            `json:"attempt,omitempty"`
	Role         string         `json:"role,omitempty"`
	Origin       string         `json:"origin,omitempty"`
	MessageID    string         `json:"message_id,omitempty"`
	Text         string         `json:"text,omitempty"`
	ToolCallID   string         `json:"tool_call_id,omitempty"`
	ToolName     string         `json:"tool_name,omitempty"`
	ToolCategory string         `json:"tool_category,omitempty"`
	Arguments    string         `json:"arguments,omitempty"`
	Output       string         `json:"output,omitempty"`
	IsError      *bool          `json:"is_error,omitempty"`
	ExitCode     *int           `json:"exit_code,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Bytes        int            `json:"bytes,omitempty"`
	Status       string         `json:"status,omitempty"`
	ErrorKind    string         `json:"error_kind,omitempty"`
	StopReason   string         `json:"stop_reason,omitempty"`
	Truncated    map[string]int `json:"truncated,omitempty"`
	Omitted      []string       `json:"omitted,omitempty"`
	Summary      *Summary       `json:"capture,omitempty"`
}

// offloadEvent is the subset of an Offload transcript event ClawFlow reads.
type offloadEvent struct {
	SchemaVersion int            `json:"schema_version"`
	Seq           int64          `json:"seq"`
	TS            string         `json:"ts"`
	CallerRunID   string         `json:"caller_run_id"`
	Attempt       int            `json:"attempt"`
	Type          string         `json:"type"`
	Role          string         `json:"role"`
	Origin        string         `json:"origin"`
	MessageID     string         `json:"message_id"`
	Text          string         `json:"text"`
	ToolCallID    string         `json:"tool_call_id"`
	ToolName      string         `json:"tool_name"`
	ToolCategory  string         `json:"tool_category"`
	Arguments     string         `json:"arguments"`
	Output        string         `json:"output"`
	IsError       *bool          `json:"is_error"`
	ExitCode      *int           `json:"exit_code"`
	Reason        string         `json:"reason"`
	Bytes         int            `json:"bytes"`
	Status        string         `json:"status"`
	ErrorKind     string         `json:"error_kind"`
	StopReason    string         `json:"stop_reason"`
	Truncated     map[string]int `json:"truncated"`
	Omitted       []string       `json:"omitted"`
	Capture       *Summary       `json:"capture"`
}

// ParseOffload converts one raw Offload transcript event. seq is always
// returned when it could be read so a reader can advance past skipped records.
// ok is false when the event is not renderable (unknown type, other schema);
// callerRunID, when non-empty, must match the event's caller_run_id.
func ParseOffload(raw json.RawMessage, callerRunID string, redact func(string) string) (e Event, seq int64, ok bool) {
	var o offloadEvent
	if json.Unmarshal(raw, &o) != nil || o.Seq < 1 {
		return Event{}, 0, false
	}
	if o.SchemaVersion != 1 || (callerRunID != "" && o.CallerRunID != callerRunID) {
		return Event{}, o.Seq, false
	}
	switch o.Type {
	case TypeMessage, TypeToolCall, TypeToolResult, TypeBackendError, TypeGap, TypeAttemptStart, TypeEnd, TypeCaptureStart:
	default:
		return Event{}, o.Seq, false
	}
	if redact == nil {
		redact = func(s string) string { return s }
	}
	e = Event{Seq: o.Seq, TS: o.TS, Type: o.Type, Attempt: o.Attempt, Role: o.Role, Origin: o.Origin, MessageID: o.MessageID,
		Text: redact(o.Text), ToolCallID: o.ToolCallID, ToolName: o.ToolName, ToolCategory: o.ToolCategory,
		Arguments: redact(o.Arguments), Output: redact(o.Output), IsError: o.IsError, ExitCode: o.ExitCode,
		Reason: o.Reason, Bytes: o.Bytes, Status: o.Status, ErrorKind: o.ErrorKind, StopReason: o.StopReason,
		Truncated: o.Truncated, Omitted: o.Omitted, Summary: o.Capture}
	e.Bound()
	return e, o.Seq, true
}

// Bound enforces the field caps in place and records what was cut.
func (e *Event) Bound() {
	cut := func(field string, v *string, max int) {
		if len(*v) <= max {
			return
		}
		n := max
		for n > 0 && !utf8.RuneStart((*v)[n]) {
			n--
		}
		if e.Truncated == nil {
			e.Truncated = map[string]int{}
		}
		if e.Truncated[field] < len(*v) {
			e.Truncated[field] = len(*v)
		}
		*v = (*v)[:n]
	}
	cut("text", &e.Text, MaxTextBytes)
	cut("arguments", &e.Arguments, MaxArgumentsBytes)
	cut("output", &e.Output, MaxOutputBytes)
	for _, v := range []*string{&e.TS, &e.Role, &e.Origin, &e.MessageID, &e.ToolCallID, &e.ToolName, &e.ToolCategory, &e.Reason, &e.Status, &e.ErrorKind, &e.StopReason} {
		cut("meta", v, MaxShortBytes)
	}
	if len(e.Omitted) > 8 {
		e.Omitted = e.Omitted[:8]
	}
	for i := range e.Omitted {
		e.Omitted[i] = strings.ToValidUTF8(e.Omitted[i], "")
		cut("meta", &e.Omitted[i], MaxShortBytes)
	}
	if len(e.Truncated) > 8 {
		e.Truncated = map[string]int{"meta": 1}
	}
	e.Text = strings.ToValidUTF8(e.Text, "�")
	e.Arguments = strings.ToValidUTF8(e.Arguments, "�")
	e.Output = strings.ToValidUTF8(e.Output, "�")
}

// Size is the approximate stored size of the event.
func (e Event) Size() int {
	n := len(e.Text) + len(e.Arguments) + len(e.Output) + len(e.ToolName) + len(e.ToolCallID) + len(e.Reason) + 64
	return n
}

// Valid reports whether the event type is renderable.
func (e Event) Valid() bool {
	switch e.Type {
	case TypeMessage, TypeToolCall, TypeToolResult, TypeBackendError, TypeGap, TypeAttemptStart, TypeEnd, TypeCaptureStart:
		return e.Seq > 0
	}
	return false
}
