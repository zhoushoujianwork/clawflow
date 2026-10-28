package store

import (
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// DSNEnv is the environment variable that overrides settings.database.dsn.
const DSNEnv = "CLAWFLOW_DATABASE_DSN"

// maskedSecret replaces any password found in a DSN.
const maskedSecret = "****"

// ErrNoDSN is returned when neither the environment nor the config carries a
// database DSN. PostgreSQL is required, so callers must treat it as fatal.
var ErrNoDSN = errors.New("a PostgreSQL DSN is required: set " + DSNEnv +
	" or settings.database.dsn in ~/.clawflow/config/config.yaml (see docs/storage-design.md)")

var (
	// reURLUserinfo matches the password in postgres://user:pw@host/db.
	// Greedy so an unescaped '@' inside the password is still covered.
	reURLUserinfo = regexp.MustCompile(`(://[^/?#@:\s]*:)([^/?#\s]*)@`)
	// reURLQuery matches ?password=... / &password=... in URL-form DSNs.
	reURLQuery = regexp.MustCompile(`(?i)([?&]password=)([^&\s#]*)`)
	// reKV matches password=... in key=value DSNs (quoted or bare).
	reKV = regexp.MustCompile(`(?i)(\bpassword\s*=\s*)('(?:\\.|[^'\\])*'|\S+)`)
)

// ResolveDSN returns the PostgreSQL DSN. The CLAWFLOW_DATABASE_DSN
// environment variable overrides settings.database.dsn.
func ResolveDSN(cfg *config.Config) (string, error) {
	if v := strings.TrimSpace(os.Getenv(DSNEnv)); v != "" {
		return v, nil
	}
	if cfg != nil {
		if v := strings.TrimSpace(cfg.Settings.Database.DSN); v != "" {
			return v, nil
		}
	}
	return "", ErrNoDSN
}

// MaskDSN hides the password of a URL-form or key=value DSN so the result is
// safe to print or log.
func MaskDSN(dsn string) string {
	if strings.Contains(dsn, "://") {
		dsn = reURLUserinfo.ReplaceAllString(dsn, "${1}"+maskedSecret+"@")
		return reURLQuery.ReplaceAllString(dsn, "${1}"+maskedSecret)
	}
	return reKV.ReplaceAllString(dsn, "${1}"+maskedSecret)
}

// dsnPasswords extracts every password literal (raw and URL-decoded) present
// in dsn, for scrubbing third-party error text.
func dsnPasswords(dsn string) []string {
	var out []string
	add := func(p string) {
		if p != "" {
			out = append(out, p)
		}
	}
	if strings.Contains(dsn, "://") {
		for _, re := range []*regexp.Regexp{reURLUserinfo, reURLQuery} {
			for _, m := range re.FindAllStringSubmatch(dsn, -1) {
				add(m[2])
				if dec, err := url.PathUnescape(m[2]); err == nil {
					add(dec)
				}
			}
		}
		return out
	}
	for _, m := range reKV.FindAllStringSubmatch(dsn, -1) {
		p := m[2]
		add(p)
		if len(p) >= 2 && p[0] == '\'' && p[len(p)-1] == '\'' {
			inner := p[1 : len(p)-1]
			add(inner)
			add(strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(inner))
		}
	}
	return out
}

// redact removes the raw DSN and its password literals from s.
func redact(s, dsn string) string {
	if dsn == "" {
		return s
	}
	s = strings.ReplaceAll(s, dsn, MaskDSN(dsn))
	for _, p := range dsnPasswords(dsn) {
		s = strings.ReplaceAll(s, p, maskedSecret)
	}
	return s
}

// redactedError carries a sanitized message while keeping the cause available
// to errors.Is / errors.As.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }
