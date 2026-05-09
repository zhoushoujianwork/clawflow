package operator

import (
	"errors"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// TestIsFailoverError verifies that each default failover pattern triggers
// failover and that unrelated errors do not.
func TestIsFailoverError(t *testing.T) {
	patterns := config.DefaultFailoverPatterns

	failoverCases := []struct {
		name   string
		errMsg string
		output string
	}{
		{"rate limit text", "exit status 1", "You've hit your limit"},
		{"429 in output", "exit status 1", "HTTP 429 Too Many Requests"},
		{"rate_limit_error", "rate_limit_error: too many requests", ""},
		{"quota exceeded", "exit status 1", "quota exceeded for this billing period"},
		{"credit balance", "exit status 1", "credit balance is too low"},
		{"overloaded", "exit status 1", "overloaded_error"},
		{"401 auth", "exit status 1", "401 Unauthorized"},
		{"invalid api key", "exit status 1", "invalid api key provided"},
		{"connection refused", "dial tcp: connection refused", ""},
		{"dial tcp", "dial tcp 1.2.3.4:443: i/o timeout", ""},
		{"tls handshake", "exit status 1", "TLS handshake timeout"},
		{"http 5xx", "exit status 1", "HTTP 500 Internal Server Error"},
		{"status 5xx", "exit status 1", "status 503 Service Unavailable"},
	}

	for _, tc := range failoverCases {
		t.Run("failover/"+tc.name, func(t *testing.T) {
			err := errors.New(tc.errMsg)
			if !isFailoverError(err, tc.output, patterns) {
				t.Errorf("expected failover for err=%q output=%q", tc.errMsg, tc.output)
			}
		})
	}

	nonFailoverCases := []struct {
		name   string
		errMsg string
		output string
	}{
		{"nil error", "", ""},
		{"generic exit", "exit status 1", "some unrelated error"},
		{"context canceled", "context canceled", ""},
		{"parse error", "parse stream: unexpected EOF", ""},
		{"operator logic error", "exit status 1", "cannot find file foo.go"},
	}

	for _, tc := range nonFailoverCases {
		t.Run("no-failover/"+tc.name, func(t *testing.T) {
			var err error
			if tc.errMsg != "" {
				err = errors.New(tc.errMsg)
			}
			if isFailoverError(err, tc.output, patterns) {
				t.Errorf("unexpected failover for err=%q output=%q", tc.errMsg, tc.output)
			}
		})
	}
}

// TestBuildProviderList verifies provider list construction from credentials.
func TestBuildProviderList(t *testing.T) {
	t.Run("uses enabled providers in order", func(t *testing.T) {
		creds := &config.Credentials{
			ClaudeProviders: []config.ClaudeProvider{
				{Name: "A", APIKey: "key-a", Enabled: true},
				{Name: "B", APIKey: "key-b", Enabled: false},
				{Name: "C", APIKey: "key-c", Enabled: true},
			},
		}
		list := buildProviderList(creds)
		if len(list) != 2 {
			t.Fatalf("expected 2 providers, got %d", len(list))
		}
		if list[0].name != "A" || list[1].name != "C" {
			t.Errorf("unexpected order: %v", list)
		}
	})

	t.Run("falls back to legacy fields when no providers", func(t *testing.T) {
		creds := &config.Credentials{
			ClaudeAPIKey:  "sk-legacy",
			ClaudeBaseURL: "https://legacy.example.com",
		}
		list := buildProviderList(creds)
		if len(list) != 1 {
			t.Fatalf("expected 1 provider, got %d", len(list))
		}
		if list[0].apiKey != "sk-legacy" {
			t.Errorf("expected legacy apiKey, got %q", list[0].apiKey)
		}
		if list[0].baseURL != "https://legacy.example.com" {
			t.Errorf("expected legacy baseURL, got %q", list[0].baseURL)
		}
	})

	t.Run("returns default entry when nil creds", func(t *testing.T) {
		list := buildProviderList(nil)
		if len(list) != 1 {
			t.Fatalf("expected 1 provider, got %d", len(list))
		}
	})
}

// TestScrubAPIKey verifies that API keys are redacted from error strings.
func TestScrubAPIKey(t *testing.T) {
	key := "sk-ant-secret123"
	msg := "error: invalid key sk-ant-secret123 for request"
	scrubbed := scrubAPIKey(msg, key)
	if scrubbed == msg {
		t.Error("expected key to be scrubbed from message")
	}
	if contains(scrubbed, key) {
		t.Errorf("key still present in scrubbed message: %q", scrubbed)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
