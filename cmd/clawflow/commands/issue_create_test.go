package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestIssueCreatePrintsCanonicalURL(t *testing.T) {
	tests := []struct {
		name, webURL, want string
	}{
		{"modern", "/ns/proj/-/issues/5", "created issue #5: bug\nSERVER/ns/proj/-/issues/5\n"},
		{"legacy", "/ns/proj/issues/5", "created issue #5: bug\nSERVER/ns/proj/issues/5\n"},
		{"empty URL keeps one line", "", "created issue #5: bug\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var srvURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.EscapedPath() != "/api/v4/projects/ns%2Fproj/issues" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
				}
				resp := map[string]any{"id": 9, "iid": 5, "title": "bug"}
				if tt.webURL != "" {
					resp["web_url"] = srvURL + tt.webURL
				}
				w.WriteHeader(201)
				json.NewEncoder(w).Encode(resp)
			}))
			defer srv.Close()
			srvURL = srv.URL
			writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{
				"ns/proj": {Platform: "gitlab", BaseURL: srv.URL},
			}})
			cmd := newIssueCreateCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"--repo", "ns/proj", "--title", "bug"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			want := bytes.ReplaceAll([]byte(tt.want), []byte("SERVER"), []byte(srv.URL))
			if out.String() != string(want) || calls != 1 {
				t.Errorf("output=%q calls=%d, want %q with one request", out.String(), calls, want)
			}
		})
	}
}
