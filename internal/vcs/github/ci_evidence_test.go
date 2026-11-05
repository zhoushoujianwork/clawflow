package github

import (
	"encoding/json"
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
		{"failed job in cancelled workflow", strings.ReplaceAll(run, "failure", "cancelled"), job, 200, "FAIL TestRace", true},
		{"cancelled job", strings.ReplaceAll(run, "failure", "cancelled"), strings.ReplaceAll(job, "failure", "cancelled"), 200, "FAIL", false},
		{"successful workflow", strings.ReplaceAll(run, "failure", "success"), job, 200, "FAIL", false},
		{"pending workflow", strings.ReplaceAll(strings.ReplaceAll(run, "failure", "cancelled"), "completed", "in_progress"), job, 200, "FAIL", false},
		{"wrong run SHA", strings.ReplaceAll(run, sha, strings.Repeat("b", 40)), job, 200, "FAIL", false},
		{"fork", strings.ReplaceAll(run, "o/r", "fork/r"), job, 200, "FAIL", false},
		{"wrong job SHA", run, strings.ReplaceAll(job, sha, strings.Repeat("b", 40)), 200, "FAIL", false},
		{"wrong run ID", run, strings.ReplaceAll(job, `"run_id":1`, `"run_id":2`), 200, "FAIL", false},
		{"successful job", run, strings.ReplaceAll(job, "failure", "success"), 200, "FAIL", false},
		{"expired", run, job, 410, "secret", false},
		{"missing", run, job, 404, "secret", false},
		{"empty", run, job, 200, "", false},
		{"oversized log keeps its tail", run, job, 200, strings.Repeat("x", evidenceDownloadBytes+1), true},
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
			if err != nil || (!IsNoEvidence(got)) != tc.want {
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
		if want == 0 && !IsNoEvidence(got) {
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

// Shape of a real Actions job log (timestamps, groups, ##[error] annotations).
const realShapeLog = `2026-11-05T10:00:00.1234567Z ##[group]Run sudo apt-get install -y libfoo
2026-11-05T10:00:00.1234567Z sudo apt-get install -y libfoo
2026-11-05T10:00:00.1234567Z shell: /usr/bin/bash -e {0}
2026-11-05T10:00:00.1234567Z ##[endgroup]
2026-11-05T10:00:01.0000000Z E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 1234
2026-11-05T10:00:01.0000001Z ##[error]Process completed with exit code 100.
`

func TestLogErrorLinesRealShape(t *testing.T) {
	step, errs := logErrorLines(realShapeLog, 5)
	if !strings.Contains(step, "Run sudo apt-get install") || len(errs) != 1 || !strings.Contains(errs[0], "exit code 100") {
		t.Fatalf("step=%q errs=%q", step, errs)
	}
}

func evidenceFixture(t *testing.T, sha string, logHandler http.HandlerFunc) *Client {
	run := fmt.Sprintf(`{"id":1,"workflow_id":7,"run_attempt":1,"head_sha":%q,"head_repository":{"full_name":"o/r"},"status":"completed","conclusion":"failure"}`, sha)
	job := func(id int, name, concl string) string {
		return fmt.Sprintf(`{"id":%d,"run_id":1,"head_sha":%q,"name":%q,"status":"completed","conclusion":%q,"steps":[{"number":2,"name":"Install deps","status":"completed","conclusion":%q}]}`, id, sha, name, concl, concl)
	}
	return ciTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/actions/runs": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[%s]}`, run)
		},
		"GET /repos/o/r/actions/runs/1/attempts/1/jobs": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"total_count":3,"jobs":[%s,%s,%s]}`, job(5, "lint", "success"), job(6, "build", "failure"), job(7, "docs", "success"))
		},
		"GET /repos/o/r/actions/jobs/6/logs": logHandler,
		"GET /dl/6": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				t.Error("credential forwarded")
			}
			fmt.Fprint(w, strings.Repeat("noise line\n", 2000)+realShapeLog+"password=hunter2\n")
		},
	})
}

func TestEvidenceMultiJobRedirectCapRedaction(t *testing.T) {
	sha := strings.Repeat("c", 40)
	c := evidenceFixture(t, sha, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://storage.invalid/dl/6")
		w.WriteHeader(302)
	})
	got, err := c.GetFailedJobEvidence("o/r", sha, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "\n") != 0 {
		t.Fatalf("expected exactly one failed job excerpt: %q", got)
	}
	for _, want := range []string{`"job":"build"`, "Install deps", "exit code 100", "dpkg/lock-frontend", `"log_step"`, `"error_lines"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, `"job":"lint"`) {
		t.Fatalf("leak or wrong job: %s", got)
	}
	var item struct{ Tail string }
	if json.Unmarshal([]byte(got), &item) != nil || len(item.Tail) > evidenceTailBytes+12 {
		t.Fatalf("tail unbounded (%d)", len(item.Tail))
	}
}

func TestEvidenceReportsReason(t *testing.T) {
	sha := strings.Repeat("d", 40)
	c := evidenceFixture(t, sha, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	got, _ := c.GetFailedJobEvidence("o/r", sha, 0)
	if !IsNoEvidence(got) || !strings.Contains(got, "job 6 logs: HTTP 403") {
		t.Fatal(got)
	}
	got, _ = c.GetFailedJobEvidence("o/r", sha, 99)
	if !strings.Contains(got, "no failed job with id 99") {
		t.Fatal(got)
	}
}
