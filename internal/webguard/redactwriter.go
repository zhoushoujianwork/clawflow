package webguard

import (
	"bytes"
	"net/http"
	"strings"
)

// redactWriter filters a response before it leaves the host. The body is
// treated by content type:
//
//   - application/json is buffered and redacted as one document, so credential
//     keys are recognized by name.
//   - event streams, newline-delimited JSON and plain text are redacted line by
//     line as they are written, so live logs keep streaming.
//   - everything else (the bundled UI assets) passes through untouched.
type redactWriter struct {
	http.ResponseWriter
	r       *Redactor
	mode    redactMode
	decided bool
	status  int
	wrote   bool
	buf     bytes.Buffer // whole document (json) or the unfinished line (lines)
}

type redactMode int

const (
	modePass redactMode = iota
	modeJSON
	modeLines
)

func newRedactWriter(w http.ResponseWriter, r *Redactor) *redactWriter {
	return &redactWriter{ResponseWriter: w, r: r}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *redactWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *redactWriter) decide(first []byte) {
	if w.decided {
		return
	}
	w.decided = true
	ct := strings.ToLower(w.Header().Get("Content-Type"))
	if ct == "" && len(first) > 0 {
		ct = strings.ToLower(http.DetectContentType(first))
	}
	switch {
	case strings.HasPrefix(ct, "application/json"):
		w.mode = modeJSON
	case strings.HasPrefix(ct, "text/event-stream"), strings.HasPrefix(ct, "application/x-ndjson"),
		strings.HasPrefix(ct, "application/jsonl"), strings.HasPrefix(ct, "text/plain"):
		w.mode = modeLines
	default:
		w.mode = modePass
	}
	if w.mode != modePass {
		// The length changes when text is replaced.
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Range")
		w.Header().Del("Accept-Ranges")
	}
}

func (w *redactWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.status = code
	if code == http.StatusNoContent || code == http.StatusNotModified || (code >= 100 && code < 200) {
		w.decided, w.mode, w.wrote = true, modePass, true
		w.ResponseWriter.WriteHeader(code)
		return
	}
	// Defer sending the header until the first body bytes are known, because
	// the content type may be sniffed from them.
}

func (w *redactWriter) sendHeader() {
	if w.wrote {
		return
	}
	w.wrote = true
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.status)
}

func (w *redactWriter) Write(b []byte) (int, error) {
	w.decide(b)
	if w.mode == modePass {
		w.sendHeader()
		return w.ResponseWriter.Write(b)
	}
	w.buf.Write(b)
	if w.mode == modeLines {
		if err := w.flushLines(false); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// flushLines emits every complete line. At the end of the response the
// unfinished last line is emitted too.
func (w *redactWriter) flushLines(final bool) error {
	data := w.buf.Bytes()
	cut := bytes.LastIndexByte(data, '\n') + 1
	if final {
		cut = len(data)
	}
	if cut == 0 {
		return nil
	}
	chunk := append([]byte(nil), data[:cut]...)
	w.buf.Next(cut)
	w.sendHeader()
	var out bytes.Buffer
	for len(chunk) > 0 {
		line := chunk
		rest := []byte(nil)
		if i := bytes.IndexByte(chunk, '\n'); i >= 0 {
			line, rest = chunk[:i+1], chunk[i+1:]
		}
		out.WriteString(w.line(string(line)))
		chunk = rest
	}
	_, err := w.ResponseWriter.Write(out.Bytes())
	return err
}

// line redacts one line, keeping its terminator. Event-stream "data:" lines and
// JSON lines are redacted structurally when they parse as JSON.
func (w *redactWriter) line(s string) string {
	body, nl := s, ""
	if strings.HasSuffix(body, "\n") {
		body, nl = body[:len(body)-1], "\n"
		if strings.HasSuffix(body, "\r") {
			body, nl = body[:len(body)-1], "\r\n"
		}
	}
	prefix := ""
	if rest, ok := strings.CutPrefix(body, "data:"); ok {
		prefix = "data:"
		trimmed := strings.TrimLeft(rest, " ")
		prefix += rest[:len(rest)-len(trimmed)]
		body = trimmed
	}
	if t := strings.TrimSpace(body); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		if out, ok := w.r.JSON([]byte(t)); ok {
			return prefix + string(out) + nl
		}
	}
	return prefix + w.r.Text(body) + nl
}

// Flush sends the response header, which is final once a handler flushes, and
// forwards complete lines of a live stream. A buffered JSON document is held
// back until the handler returns.
func (w *redactWriter) Flush() {
	if !w.decided {
		w.decide(nil)
	}
	w.sendHeader()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// finish writes whatever is still buffered. It must run after the handler.
func (w *redactWriter) finish() {
	if !w.decided {
		w.decide(nil)
	}
	switch w.mode {
	case modeJSON:
		out := w.r.Bytes(w.buf.Bytes())
		w.sendHeader()
		_, _ = w.ResponseWriter.Write(out)
	case modeLines:
		_ = w.flushLines(true)
		w.sendHeader()
	default:
		w.sendHeader()
	}
}
