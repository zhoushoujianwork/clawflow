package pipeline

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// MaxEvents is how many run.log events the board returns.
const MaxEvents = 50

const maxFieldLen = 300

// eventPrefixes are the run/* areas the board shows; everything else in
// run.log (per-lock, per-scan chatter) is noise for a delivery view.
var eventPrefixes = []string{
	"run/harvest", "run/automerge", "run/queue", "queue/", "run/self_upgrade",
	"run/lock_timeout", "run/target_skip", "run/exit", "run/ci_repair", "run/end",
}

// IsBoardEvent reports whether a run.log area belongs on the board.
func IsBoardEvent(area string) bool {
	for _, p := range eventPrefixes {
		if area == p || strings.HasPrefix(area, p) {
			return true
		}
	}
	return false
}

// sensitiveKey reports field names whose value must never leave the process.
func sensitiveKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"token", "secret", "password", "passwd", "authorization", "api_key", "apikey", "prompt"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// ParseLine parses one run.log line:
//
//	2026-10-27T04:09:43Z INFO  run/automerge_sweep repo=o/r open_prs=1 err="a \"b\""
//
// ok is false for blank or malformed lines.
func ParseLine(line string) (Event, bool) {
	line = strings.TrimRight(line, "\r\n")
	rest := strings.TrimSpace(line)
	var head [3]string
	for i := range head {
		j := strings.IndexAny(rest, " \t")
		if j < 0 {
			if i == 2 && rest != "" {
				head[i], rest = rest, ""
				break
			}
			return Event{}, false
		}
		head[i], rest = rest[:j], strings.TrimLeft(rest[j:], " \t")
	}
	if ParseTime(head[0]).IsZero() || !strings.Contains(head[2], "/") {
		return Event{}, false
	}
	ev := Event{Time: head[0], Level: strings.ToLower(head[1]), Event: head[2], Fields: map[string]string{}}
	for rest != "" {
		eq := strings.IndexByte(rest, '=')
		sp := strings.IndexAny(rest, " \t")
		if eq < 0 || (sp >= 0 && sp < eq) {
			// Not a key=value token; skip it.
			if sp < 0 {
				break
			}
			rest = strings.TrimLeft(rest[sp:], " \t")
			continue
		}
		key := rest[:eq]
		rest = rest[eq+1:]
		var val string
		if strings.HasPrefix(rest, `"`) {
			val, rest = unquote(rest)
		} else if sp = strings.IndexAny(rest, " \t"); sp >= 0 {
			val, rest = rest[:sp], rest[sp:]
		} else {
			val, rest = rest, ""
		}
		rest = strings.TrimLeft(rest, " \t")
		if sensitiveKey(key) {
			val = "[redacted]"
		}
		if runes := []rune(val); len(runes) > maxFieldLen {
			val = string(runes[:maxFieldLen]) + "…"
		}
		ev.Fields[key] = val
	}
	return ev, true
}

// unquote reads one double-quoted value written by the run logger (which
// escapes only \", \\, \n, \r and \t) and returns the remainder.
func unquote(s string) (string, string) {
	var sb strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			if i+1 >= len(s) {
				return sb.String(), ""
			}
			i++
			switch s[i] {
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			default:
				sb.WriteByte(s[i])
			}
		case '"':
			return sb.String(), s[i+1:]
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String(), ""
}

// ParseEvents reads run.log text and returns every board event in file order.
// A partial first line (from tailing a large file) simply fails to parse.
func ParseEvents(r io.Reader) []Event {
	var out []Event
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		ev, ok := ParseLine(sc.Text())
		if !ok || !IsBoardEvent(ev.Event) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// LastDecision returns the most recent run/queue_{pick,wait,blocked} event of
// repo's queue, or nil when none is recorded.
func LastDecision(events []Event, repo string) *QueueDecision {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Fields["repo"] != repo {
			continue
		}
		var action string
		switch ev.Event {
		case "run/queue_pick":
			action = "pick"
		case "run/queue_wait":
			action = "wait"
		case "run/queue_blocked":
			action = "blocked"
		default:
			continue
		}
		d := &QueueDecision{Action: action, Reason: ev.Fields["reason"], At: ev.Time}
		d.Issue = atoi(ev.Fields["issue"])
		return d
	}
	return nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Relevant keeps the last MaxEvents events that concern one of the given
// repositories (events without a repo field, such as run/exit, always count).
func Relevant(events []Event, repos map[string]bool) []Event {
	var out []Event
	for i := len(events) - 1; i >= 0 && len(out) < MaxEvents; i-- {
		if r := events[i].Fields["repo"]; r != "" && !repos[r] {
			continue
		}
		out = append(out, events[i])
	}
	// Return oldest-first so the timeline reads top to bottom; the UI reverses as needed.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// ReadEventsTail parses at most the last maxBytes of the run.log at path. A
// missing file yields no events and no error: a fresh install has no log yet.
func ReadEventsTail(path string, maxBytes int64) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && maxBytes > 0 && st.Size() > maxBytes {
		if _, err := f.Seek(st.Size()-maxBytes, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return ParseEvents(f), nil
}
