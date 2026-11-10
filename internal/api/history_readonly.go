package api

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// readOnlyErrorLimit bounds the failure text shown to read-only (LAN) viewers.
const readOnlyErrorLimit = 600

// The order matters: scheme-qualified credentials go first, so a later key/value
// rule cannot stop at the scheme word and leave the token behind.
var readOnlyErrorSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9+/=._~-]+`),
	regexp.MustCompile(`(?i)(?:https?|postgres(?:ql)?)://[^\s/@]+:[^\s/@]+@`),
	regexp.MustCompile(`(?i)(?:gh[pousr]_[a-z0-9_.-]+|github_pat_[a-z0-9_]+|glpat-[a-z0-9_-]+|AKIA[A-Z0-9]{16}|sk-[a-z0-9_-]{16,})`),
	regexp.MustCompile(`(?i)(?:authorization|password|passwd|secret|token|api[_-]?key|credential|dsn)[a-z0-9_-]*["']?\s*[:=]\s*[^\s",]+`),
	regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`\b[0-9a-fA-F]{40,}\b`),
	// Machine-local absolute paths reveal user names and directory layout.
	// macOS state lives under "Application Support", so a path may contain a
	// space; the rest of such a line is dropped rather than leaving a fragment.
	regexp.MustCompile(`(?:/Users|/home|/private|/var/folders|/tmp|/opt|/root)/[^\n"'<>;,)]*`),
}

// redactRunError makes a recorded failure message safe for a read-only viewer:
// credentials, long hashes and machine-local paths are replaced before the text
// is truncated, so a limit can never cut a secret in half.
func redactRunError(s string) string {
	for _, re := range readOnlyErrorSecrets {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if len(s) > readOnlyErrorLimit {
		cut := readOnlyErrorLimit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// readOnlyRunDetails projects one attempt for a read-only session: the same
// whitelisted metadata as a list row plus the redacted failure reason. It never
// includes the issue body, operator output, working directories, prompts or the
// raw event stream.
func readOnlyRunDetails(rows []store.Run, root, kind string) ([]map[string]any, error) {
	payload, err := store.RunListPayloads(rows, root, kind)
	if err != nil {
		return nil, err
	}
	for i, r := range rows {
		if msg := redactRunError(r.Error); msg != "" {
			payload[i]["error"] = msg
		}
	}
	return payload, nil
}
