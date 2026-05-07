// Package log writes structured single-line records to ~/.clawflow/logs/<name>.log
// with size-based rotation. It is the cross-cutting trail for outer-runner
// lifecycle events (scan, lock, reconcile) that complements the per-run
// claude stream-json in data/runs/<repo>/issue-<N>/<ts>/events.jsonl.
//
// Format (one event per line, plain text for grep/tail-friendliness):
//
//	2026-05-07T09:52:47Z INFO  run/lock         pid=93588 repo=owner/r issue=103 op=implement
//
// The format is intentionally not JSON — the audience is the developer
// running the binary on their own laptop, who will tail and grep, not ship
// to a log aggregator.
package log

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxSizeBytes is the cap for a single .log file before it gets rotated.
// Picked at 10 MB so a few minutes of verbose tracing won't fill the
// disk, yet a typical week of operations stays in the live file.
const maxSizeBytes = 10 * 1024 * 1024

// maxRotations bounds disk usage: live + N archived = N+1 files of up to
// maxSizeBytes each. 5 strikes a balance between forensic depth and footprint.
const maxRotations = 5

// Logger appends timestamped lines to a single file and rotates when the
// file passes maxSizeBytes. Safe for concurrent use.
//
// A nil *Logger is a usable no-op so call sites can write `lg.Info(...)`
// without guarding against the open-failure path.
type Logger struct {
	mu   sync.Mutex
	f    *os.File
	path string
	size int64
}

// LogsDir returns ~/.clawflow/logs/. Created on demand by Open.
func LogsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "logs")
}

// Open opens (or creates) ~/.clawflow/logs/<name>.log for append. Use
// "run", "web", "pilot" etc. Caller must Close when done. If the open
// fails (e.g. read-only home), Open returns the error and a nil Logger;
// the nil is still safe to use as a no-op so callers can ignore the error.
func Open(name string) (*Logger, error) {
	dir := LogsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	var size int64
	if info, _ := f.Stat(); info != nil {
		size = info.Size()
	}
	return &Logger{f: f, path: path, size: size}, nil
}

// Close flushes and closes the underlying file. No-op on nil receiver.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

// Info / Warn / Error emit one line at the given level. `area` is a
// stable slug like "run/lock" or "web/reconcile" that grep targets.
// `kv` is alternating key/value pairs rendered as `k=v` separated by
// spaces; values are formatted via %v. An odd-length kv silently drops
// the trailing key.
func (l *Logger) Info(area string, kv ...any)  { l.write("INFO ", area, kv...) }
func (l *Logger) Warn(area string, kv ...any)  { l.write("WARN ", area, kv...) }
func (l *Logger) Error(area string, kv ...any) { l.write("ERROR", area, kv...) }

func (l *Logger) write(level, area string, kv ...any) {
	if l == nil || l.f == nil {
		return
	}
	var sb strings.Builder
	sb.WriteString(time.Now().UTC().Format(time.RFC3339))
	sb.WriteByte(' ')
	sb.WriteString(level)
	sb.WriteByte(' ')
	sb.WriteString(area)
	for i := 0; i+1 < len(kv); i += 2 {
		sb.WriteByte(' ')
		fmt.Fprintf(&sb, "%v=%v", kv[i], formatValue(kv[i+1]))
	}
	sb.WriteByte('\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	n, _ := l.f.Write([]byte(sb.String()))
	l.size += int64(n)
	if l.size > maxSizeBytes {
		l.rotateLocked()
	}
}

// formatValue quotes a value if it contains spaces, so a key=value pair
// with a multi-word string value (e.g. an error message) doesn't break
// the kv parsing of downstream grep/awk.
func formatValue(v any) string {
	s := fmt.Sprintf("%v", v)
	if strings.ContainsAny(s, " \t") {
		return strconvQuote(s)
	}
	return s
}

// strconvQuote wraps a string in double quotes and escapes embedded
// quotes/backslashes. Inlined to avoid an import.
func strconvQuote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case '\n':
			sb.WriteString("\\n")
		case '\r':
			sb.WriteString("\\r")
		case '\t':
			sb.WriteString("\\t")
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// rotateLocked shifts <path>.4 → .5, .3 → .4, ..., .log → .log.1, then
// reopens a fresh .log. Caller must hold l.mu.
func (l *Logger) rotateLocked() {
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
	// Drop the oldest by overwrite via shift. Iterate from the end so
	// we don't clobber an existing file before it's renamed forward.
	for i := maxRotations - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", l.path, i)
		dst := fmt.Sprintf("%s.%d", l.path, i+1)
		_ = os.Rename(src, dst)
	}
	_ = os.Rename(l.path, l.path+".1")
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		// Rotation failed; the next write will be a silent drop. The
		// stderr mirror still reaches the user, so we don't escalate.
		return
	}
	l.f = f
	l.size = 0
}
