package webguard

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testRedactor() *Redactor {
	return NewRedactor(
		func() []string { return []string{"configured-secret-value", "tiny"} },
		func() []string {
			return []string{"/Users/alice/Library/Application Support/ClawFlow", "/srv/checkouts/repo"}
		},
	)
}

func TestRedactTextRemovesSecretsAndPaths(t *testing.T) {
	r := testRedactor()
	for _, tc := range []struct{ name, in string }{
		{"registered secret", "token is configured-secret-value ok"},
		{"github token", "ghp_abcdefghijklmnopqrstuvwxyz0123456789 leaked"},
		{"fine-grained token", "github_pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz"},
		{"gitlab token", "glpat-abcdefghijklmnopqrst"},
		{"api key", "ANTHROPIC key sk-ant-api03-abcdefghijklmnopqrstuvwxyz"},
		{"aws", "AKIAABCDEFGHIJKLMNOP"},
		{"bearer", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123"},
		{"basic auth", `"authorization":"Basic dXNlcjpwYXNzd29yZA=="`},
		{"url password", "git clone https://user:hunter2hunter2@github.com/o/r.git"},
		{"url token only", "https://x-access-token@github.com/o/r"},
		{"env assignment", "GITHUB_TOKEN=abcd1234efgh5678"},
		{"kv colon", "password: correct-horse-battery"},
		{"json kv", `{"api_key":"abcd-1234-efgh"}`},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEabc\n-----END RSA PRIVATE KEY-----"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := r.Text(tc.in)
			for _, leak := range []string{"configured-secret-value", "ghp_abcdef", "github_pat_11", "glpat-abc", "sk-ant", "AKIAABC", "abcdefghijklmnopqrstuvwxyz0123", "dXNlcjpw", "hunter2", "x-access-token@", "abcd1234efgh", "correct-horse", "abcd-1234", "eyJhbGci", "MIIEabc"} {
				if strings.Contains(out, leak) {
					t.Fatalf("%q leaked in %q", leak, out)
				}
			}
			if out == tc.in {
				t.Fatalf("unchanged: %q", out)
			}
		})
	}
	for _, tc := range []struct{ name, in, gone string }{
		{"home path", "cd /Users/bob/work/proj && make", "/Users/bob"},
		{"linux home", "open /home/ci/runner/_work/x", "/home/ci"},
		{"tmp", "wrote /tmp/clawflow-123/out.txt", "/tmp/clawflow"},
		{"var folders", "/var/folders/ab/cd/T/x", "/var/folders"},
		{"tilde", "see ~/.clawflow/config/credentials.yaml", ".clawflow"},
		{"state dir with space", "db at /Users/alice/Library/Application Support/ClawFlow/data/clawflow.sqlite3 done", "Application Support"},
		{"registered root", "cwd=/srv/checkouts/repo/sub/file.go:12", "/srv/checkouts"},
		{"quoted", `"path":"/Users/bob/x"`, "/Users/bob"},
		{"file url", "file:///Users/bob/x.txt", "/Users/bob"},
		{"windows", `C:\Users\bob\proj\main.go`, `C:\Users`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := r.Text(tc.in)
			if strings.Contains(out, tc.gone) {
				t.Fatalf("%q leaked in %q", tc.gone, out)
			}
			if !strings.Contains(out, RedactedPath) {
				t.Fatalf("no placeholder in %q", out)
			}
		})
	}
}

func TestRedactTextKeepsOrdinaryContent(t *testing.T) {
	r := testRedactor()
	for _, in := range []string{
		"GET /api/jobs/abc123/logs?after=5",
		"see /data/runs/x and /assets/app.js",
		"input_tokens: 1234 output_tokens=99",
		"max_tokens=4096",
		`"has_token":true`,
		"https://github.com/owner/repo/pull/12",
		"owner/repo#800 failed after 3m4s",
		"2026-11-10T09:25:58Z",
		"src/components/TraceView.tsx and internal/api/jobs.go",
		"a/b/c and ./relative/path and ../up",
		"ratio 3/4 and 10/12",
		"tiny words stay: tiny",
	} {
		if out := r.Text(in); out != in {
			t.Fatalf("changed %q -> %q", in, out)
		}
	}
}

func TestRedactJSONByKeyAndValue(t *testing.T) {
	r := testRedactor()
	in := `{"local_path":"/Users/alice/proj","token":"abc","token_file":"/x/y","input_tokens":42,"has_token":true,"nested":{"api_key":"k-1234567","note":"see /Users/alice/proj/a.go"},"list":["/home/u/x","fine"],"n":1.5}`
	out, ok := r.JSON([]byte(in))
	if !ok {
		t.Fatal("not json")
	}
	s := string(out)
	for _, leak := range []string{"alice", "abc", "/x/y", "k-1234567", "/home/u"} {
		if strings.Contains(s, leak) {
			t.Fatalf("%q leaked in %s", leak, s)
		}
	}
	for _, keep := range []string{`"input_tokens":42`, `"has_token":true`, `"n":1.5`, `"fine"`} {
		if !strings.Contains(s, keep) {
			t.Fatalf("%q missing in %s", keep, s)
		}
	}
	if _, ok := r.JSON([]byte(`{"a":1} {"b":2}`)); ok {
		t.Fatal("two documents accepted as one")
	}
	if _, ok := r.JSON([]byte(`not json`)); ok {
		t.Fatal("garbage accepted")
	}
}

func TestRedactNilAndShortSecrets(t *testing.T) {
	var r *Redactor
	if got := r.Text("plain /api/x"); got != "plain /api/x" {
		t.Fatal(got)
	}
	// A short registered value is ignored rather than corrupting ordinary words.
	if got := testRedactor().Text("tiny words"); got != "tiny words" {
		t.Fatal(got)
	}
}

// lanServe runs one request through the policy and returns the response.
func lanServe(t *testing.T, p *RemotePolicy, peer, method, path string, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = peer
	w := httptest.NewRecorder()
	p.Middleware(h).ServeHTTP(w, req)
	return w
}

func TestRemoteRedactsLANResponsesOnly(t *testing.T) {
	p, _ := NewRemotePolicy("readonly", nil)
	p.WithRedactor(testRedactor())
	body := `{"path":"/Users/bob/x","token":"configured-secret-value","ok":true}`
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
	lan := lanServe(t, p, "192.168.1.2:1", "GET", "/api/pipeline", h)
	for _, leak := range []string{"bob", "configured-secret-value"} {
		if strings.Contains(lan.Body.String(), leak) {
			t.Fatalf("LAN saw %q: %s", leak, lan.Body.String())
		}
	}
	if !strings.Contains(lan.Body.String(), `"ok":true`) {
		t.Fatal(lan.Body.String())
	}
	local := lanServe(t, p, "127.0.0.1:1", "GET", "/api/pipeline", h)
	if local.Body.String() != body {
		t.Fatalf("local session was altered: %s", local.Body.String())
	}
}

func TestRemoteRedactsStreamedLogs(t *testing.T) {
	p, _ := NewRemotePolicy("readonly", nil)
	p.WithRedactor(testRedactor())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a peer by rewriting the address the policy sees.
		r.RemoteAddr = "192.168.1.9:5555"
		p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			w.WriteHeader(200)
			f.Flush()
			io.WriteString(w, "id: 1\ndata: {\"event\":{\"output\":\"cd /Users/bob/p && echo ghp_abcdefghijklmnopqrstuvwxyz0123456789\"}}\n\n")
			f.Flush()
			io.WriteString(w, ": ping\n\n")
			f.Flush()
			io.WriteString(w, "data: line two /home/ci/x")
			// no trailing newline: must still be delivered and redacted at the end
		})).ServeHTTP(w, r)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/jobs/abc/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	// The first frame must arrive while the handler is still running.
	var first strings.Builder
	for {
		line, err := br.ReadString('\n')
		first.WriteString(line)
		if err != nil || line == "\n" {
			break
		}
	}
	rest, _ := io.ReadAll(br)
	all := first.String() + string(rest)
	for _, leak := range []string{"bob", "ghp_abcdef", "/home/ci"} {
		if strings.Contains(all, leak) {
			t.Fatalf("%q leaked: %s", leak, all)
		}
	}
	for _, keep := range []string{"id: 1", "data: ", ": ping", "line two"} {
		if !strings.Contains(all, keep) {
			t.Fatalf("%q missing: %s", keep, all)
		}
	}
}

func TestRemoteLeavesAssetsUntouched(t *testing.T) {
	p, _ := NewRemotePolicy("readonly", nil)
	p.WithRedactor(testRedactor())
	js := "const root='/Users/x'; const t='token: abcdefgh';"
	w := lanServe(t, p, "192.168.1.2:1", "GET", "/assets/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Content-Length", "47")
		io.WriteString(w, js)
	})
	if w.Body.String() != js || w.Header().Get("Content-Length") != "47" {
		t.Fatalf("asset changed: %q len=%s", w.Body.String(), w.Header().Get("Content-Length"))
	}
}

func TestRemoteJobPathsAreReadOnly(t *testing.T) {
	p, _ := NewRemotePolicy("readonly", nil)
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/jobs", 200},
		{"GET", "/api/runners", 200},
		{"GET", "/api/jobs/0123abcd", 200},
		{"GET", "/api/jobs/0123abcd/logs", 200},
		{"GET", "/jobs", 200},
		{"GET", "/runners", 200},
		{"GET", "/jobs/0123abcd", 200},
		{"POST", "/api/jobs", 403},
		{"POST", "/api/jobs/0123abcd/logs", 403},
		{"DELETE", "/api/jobs/0123abcd", 403},
		{"PUT", "/api/runners", 403},
		{"GET", "/api/jobs/0123abcd/cancel", 403},
		{"GET", "/api/jobs/0123abcd/logs/extra", 403},
		{"GET", "/api/jobs/../settings", 403},
		{"GET", "/api/jobs/a%2Fb", 403},
		{"GET", "/api/jobs/", 403},
		{"GET", "/jobs/0123abcd/extra", 403},
	} {
		w := lanServe(t, p, "192.168.1.2:1", tc.method, tc.path, ok)
		if w.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
	// The local owner keeps write access.
	if w := lanServe(t, p, "127.0.0.1:1", "POST", "/api/run", ok); w.Code != 200 {
		t.Fatalf("loopback write = %d", w.Code)
	}
}
