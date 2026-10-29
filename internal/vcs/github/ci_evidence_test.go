package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFailedJobEvidence(t *testing.T) {
	sha := strings.Repeat("a", 40)
	run := fmt.Sprintf(`{"id":1,"workflow_id":7,"run_attempt":2,"head_sha":%q,"head_repository":{"full_name":"o/r"},"status":"completed","conclusion":"failure"}`, sha)
	job := fmt.Sprintf(`{"id":9,"run_id":1,"head_sha":%q,"name":"test","status":"completed","conclusion":"failure","steps":[{"number":3,"name":"go test","status":"completed","conclusion":"failure"}]}`, sha)
	for _, tc := range []struct {
		name, run, job string
		status         int
		log            string
		want           bool
	}{
		{"matching", run, job, 200, "FAIL TestRace", true},
		{"wrong run SHA", strings.ReplaceAll(run, sha, strings.Repeat("b", 40)), job, 200, "FAIL", false},
		{"fork", strings.ReplaceAll(run, "o/r", "fork/r"), job, 200, "FAIL", false},
		{"wrong job SHA", run, strings.ReplaceAll(job, sha, strings.Repeat("b", 40)), 200, "FAIL", false},
		{"wrong run ID", run, strings.ReplaceAll(job, `"run_id":1`, `"run_id":2`), 200, "FAIL", false},
		{"successful job", run, strings.ReplaceAll(job, "failure", "success"), 200, "FAIL", false},
		{"expired", run, job, 410, "secret", false},
		{"missing", run, job, 404, "secret", false},
		{"empty", run, job, 200, "", false},
		{"oversized", run, job, 200, strings.Repeat("x", evidenceDownloadBytes+1), false},
		{"newer success", run + "," + strings.ReplaceAll(strings.ReplaceAll(run, `"id":1`, `"id":2`), "failure", "success"), job, 200, "FAIL", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 1
			if tc.name == "newer success" {
				count = 2
			}
			logs := 0
			c := ciTestClient(t, map[string]http.HandlerFunc{
				"GET /repos/o/r/actions/runs": func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("head_sha") != sha {
						t.Fatal("missing SHA filter")
					}
					fmt.Fprintf(w, `{"total_count":%d,"workflow_runs":[%s]}`, count, tc.run)
				},
				"GET /repos/o/r/actions/runs/1/attempts/2/jobs": func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"total_count":1,"jobs":[%s]}`, tc.job) },
				"GET /repos/o/r/actions/jobs/9/logs":            func(w http.ResponseWriter, r *http.Request) { logs++; w.WriteHeader(tc.status); fmt.Fprint(w, tc.log) },
			})
			got, err := c.GetFailedJobEvidence("o/r", sha, 0)
			if err != nil || (got != NoJobEvidence) != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
			if tc.want && (!strings.Contains(got, "go test") || !strings.Contains(got, `"attempt":2`) || logs != 1) {
				t.Fatal(got)
			}
		})
	}
}

func TestEvidenceRedactionAndTail(t *testing.T) {
	c := New("configured-secret", "")
	input := strings.Repeat("old line\n", 1000) + "\x1b[31mFAIL TestRace\x1b[0m\nconfigured-secret\nghp_abcdef123\nTOKEN=hidden\nAuthorization: Bearer hidden2\nhttps://user:pass@host/\n-----BEGIN PRIVATE KEY-----\nprivate\n-----END PRIVATE KEY-----\n"
	got := c.evidenceText(input, evidenceTailBytes)
	for _, secret := range []string{"configured-secret", "ghp_abcdef123", "hidden", "user:pass", "private", "\x1b"} {
		if strings.Contains(got, secret) {
			t.Fatalf("leaked %q", secret)
		}
	}
	if !strings.Contains(got, "FAIL TestRace") || !strings.HasPrefix(got, "[truncated]") || len(got) > evidenceTailBytes+12 {
		t.Fatalf("bad tail (%d)", len(got))
	}
}

func TestEvidenceRedirectDropsCredentials(t *testing.T) {
	c := ciTestClient(t, map[string]http.HandlerFunc{
		"GET /logs": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://storage.invalid/download")
			w.WriteHeader(302)
		},
		"GET /download": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				t.Fatal("credential forwarded")
			}
			fmt.Fprint(w, "tail")
		},
	})
	got, err := c.evidenceGET("/logs", true)
	if err != nil || string(got) != "tail" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestEvidenceJobFilterAndExcerptLimit(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, filter := range []int64{0, 4, 99} {
		downloads := 0
		c := ciTestClient(t, map[string]http.HandlerFunc{
			"GET /repos/o/r/actions/runs": func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[{"id":1,"workflow_id":2,"run_attempt":1,"head_sha":%q,"head_repository":{"full_name":"o/r"},"status":"completed","conclusion":"failure"}]}`, sha)
			},
			"GET /repos/o/r/actions/runs/1/attempts/1/jobs": func(w http.ResponseWriter, r *http.Request) {
				var jobs []string
				for i := 1; i <= 4; i++ {
					jobs = append(jobs, fmt.Sprintf(`{"id":%d,"run_id":1,"head_sha":%q,"status":"completed","conclusion":"failure"}`, i, sha))
				}
				fmt.Fprintf(w, `{"total_count":4,"jobs":[%s]}`, strings.Join(jobs, ","))
			},
		})
		original := c.http.Transport
		c.http.Transport = repairTransport(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/logs") {
				downloads++
				if filter != 0 && r.URL.Path != "/repos/o/r/actions/jobs/4/logs" {
					t.Fatal("job filter bypassed")
				}
				w := httptest.NewRecorder()
				fmt.Fprint(w, "FAIL")
				return w.Result(), nil
			}
			return original.RoundTrip(r)
		})
		got, err := c.GetFailedJobEvidence("o/r", sha, filter)
		want := 3
		if filter == 4 {
			want = 1
		}
		if filter == 99 {
			want = 0
		}
		if err != nil || downloads != want {
			t.Fatalf("downloads=%d want=%d err=%v", downloads, want, err)
		}
		if want == 0 && got != NoJobEvidence {
			t.Fatal(got)
		}
	}
}

func TestEvidenceKeepsEarlyFailureAfterRedaction(t *testing.T) {
	c := New("configured-secret", "")
	input := "--- FAIL: TestStore (0.01s)\n    store_test.go:42: configured-secret failed\npanic: nil pointer\nexample.TestStore()\n /store_test.go:43\n" + strings.Repeat("passing output\n", 2000)
	got := c.evidenceText(input, evidenceTailBytes)
	for _, want := range []string{"--- FAIL: TestStore", "store_test.go:42: [redacted] failed", "panic: nil pointer", "example.TestStore()", "/store_test.go:43"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got, "configured-secret") || len(got) > evidenceTailBytes+12 {
		t.Fatal("unbounded or unredacted excerpt")
	}
}
