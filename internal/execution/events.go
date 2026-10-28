package execution

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"time"
)

// eventTail validates complete metadata records as they arrive. It keeps only
// an unfinished bounded line and correlation state; terminal checks never emit.
type eventTail struct {
	path                           string
	req                            Request
	evidence                       io.Writer
	env                            []string
	observe                        func(Observation)
	file                           *os.File
	offset                         int64
	pending                        []byte
	seq                            int
	runID, session, backend, model string
	final                          *Event
}

func newEventTail(path string, req Request, evidence io.Writer, env []string, observe func(Observation)) *eventTail {
	return &eventTail{path: path, req: req, evidence: evidence, env: env, observe: observe}
}
func (t *eventTail) close() {
	if t.file != nil {
		_ = t.file.Close()
	}
}

func (t *eventTail) poll() error {
	if t.file == nil {
		info, err := os.Lstat(t.path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return &Error{Kind: "events_io"}
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return &Error{Kind: "protocol_invalid_events"}
		}
		f, err := os.Open(t.path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return &Error{Kind: "events_io"}
		}
		t.file = f
	}
	st, err := t.file.Stat()
	if err != nil {
		return &Error{Kind: "events_io"}
	}
	current, err := os.Stat(t.path)
	if err != nil || !os.SameFile(st, current) {
		return &Error{Kind: "events_io"}
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return &Error{Kind: "protocol_invalid_events"}
	}
	if st.Size() < t.offset {
		return &Error{Kind: "protocol_truncated_events"}
	}
	var chunk [32 * 1024]byte
	for {
		remaining := int64(MaxOutputBytes) + 1 - t.offset
		if remaining <= 0 {
			return &Error{Kind: "protocol_output_limit"}
		}
		size := int64(len(chunk))
		if remaining < size {
			size = remaining
		}
		n, readErr := t.file.Read(chunk[:size])
		if n > 0 {
			t.offset += int64(n)
			if t.offset > MaxOutputBytes {
				return &Error{Kind: "protocol_output_limit"}
			}
			t.pending = append(t.pending, chunk[:n]...)
			for {
				end := bytes.IndexByte(t.pending, '\n')
				if end < 0 {
					break
				}
				if end >= MaxEventBytes {
					return &Error{Kind: "protocol_output_limit"}
				}
				if err := t.accept(t.pending[:end]); err != nil {
					return err
				}
				t.pending = t.pending[end+1:]
			}
			if len(t.pending) >= MaxEventBytes {
				return &Error{Kind: "protocol_output_limit"}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return &Error{Kind: "events_io"}
		}
		if n == 0 {
			return &Error{Kind: "events_io"}
		}
	}
}

func (t *eventTail) accept(raw []byte) error {
	var e Event
	if !validJSON(raw) || json.Unmarshal(raw, &e) != nil || !hasFields(raw, "schema_version", "type", "run_id", "caller_run_id", "seq", "timestamp") || e.Version != 1 || e.Seq != t.seq+1 || e.CallerRunID != t.req.RequestID || !idPattern.MatchString(e.RunID) || t.final != nil {
		return &Error{Kind: "protocol_invalid_event"}
	}
	if _, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil {
		return &Error{Kind: "protocol_invalid_event"}
	}
	if t.seq == 0 && e.Type != "start" {
		return &Error{Kind: "protocol_missing_start"}
	}
	if t.runID != "" && e.RunID != t.runID {
		return &Error{Kind: "protocol_invalid_event"}
	}
	if e.Session != "" {
		if !idPattern.MatchString(e.Session) || (t.session != "" && e.Session != t.session) || (t.req.Resume && e.Session != t.req.SessionKey) {
			return &Error{Kind: "protocol_session_mismatch"}
		}
	}
	if (t.backend != "" && e.Backend != "" && e.Backend != t.backend) || (t.model != "" && e.Model != "" && e.Model != t.model) {
		return &Error{Kind: "protocol_identity_mismatch"}
	}
	switch e.Type {
	case "start":
		if t.seq != 0 {
			return &Error{Kind: "protocol_duplicate_start"}
		}
	case "progress":
	case "final":
		if (t.session != "" && e.Session != t.session) || (t.backend != "" && e.Backend != t.backend) || (t.model != "" && e.Model != "" && e.Model != t.model) {
			return &Error{Kind: "protocol_identity_mismatch"}
		}
		if !hasFields(raw, "status", "exit_code") || (e.Status != "ok" && e.Status != "error" && e.Status != "timeout") || (e.Status == "ok" && (e.ExitCode != 0 || e.ErrorKind != "")) || (e.Status != "ok" && e.ExitCode == 0) {
			return &Error{Kind: "protocol_invalid_event"}
		}
	default:
		return &Error{Kind: "protocol_unknown_event"}
	}
	// Unknown text, tool arguments and native envelopes are discarded, never relayed.
	clean, err := json.Marshal(e)
	if err != nil {
		return &Error{Kind: "events_io"}
	}
	clean = []byte(redactEvidence(clean, t.env) + "\n")
	if t.evidence != nil {
		n, err := t.evidence.Write(clean)
		if err != nil || n != len(clean) {
			return &Error{Kind: "events_io"}
		}
	}
	t.seq++
	t.runID = e.RunID
	if e.Session != "" {
		t.session = e.Session
	}
	if e.Backend != "" {
		t.backend = e.Backend
	}
	if e.Model != "" {
		t.model = e.Model
	}
	if e.Type == "final" {
		copy := e
		t.final = &copy
	}
	if e.Type == "progress" && t.observe != nil {
		t.observe(Observation{Type: redact(e.NativeType, t.env), Model: redact(e.Model, t.env), NativeSessionID: redact(e.Session, t.env)})
	}
	return nil
}

func (t *eventTail) finish(result Result) error {
	if t.file == nil {
		return &Error{Kind: "protocol_missing_events"}
	}
	if len(t.pending) > 0 {
		return &Error{Kind: "protocol_truncated_events"}
	}
	if t.final == nil {
		return &Error{Kind: "protocol_missing_final"}
	}
	e := t.final
	// The result record carries the resolved model and takes precedence over
	// event models. Validate run identity and outcome independently of it.
	if t.runID != result.RunID || e.CallerRunID != result.CallerRunID || e.Session != result.NativeSessionID || e.Backend != result.Backend || e.Status != result.Status || e.ExitCode != result.ExitCode || e.ErrorKind != result.ErrorKind {
		return &Error{Kind: "protocol_final_mismatch"}
	}
	if (t.session != "" && t.session != result.NativeSessionID) || (t.backend != "" && t.backend != result.Backend) {
		return &Error{Kind: "protocol_identity_mismatch"}
	}
	return nil
}
