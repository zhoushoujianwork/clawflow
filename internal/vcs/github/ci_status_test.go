package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestGetCIStatusTransportError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New("test-token", "https://example.invalid")
	c.SetRequestContext(ctx)
	c.http = &http.Client{Transport: repairTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/repos/o/r/pulls/11" {
			w := httptest.NewRecorder()
			fmt.Fprint(w, `{"head":{"sha":"captured-sha"}}`)
			return w.Result(), nil
		}
		cancel() // Bound the normal retry policy while simulating a transport error.
		return nil, context.Canceled
	})}
	if got, err := c.GetCIStatus("o/r", 11); got == vcs.CIStatusSuccess || err == nil {
		t.Fatalf("transport error returned %q, %v", got, err)
	}
}

// Exercise HTTP fixtures without binding a port in restricted test environments.
func ciTestClient(t *testing.T, routes map[string]http.HandlerFunc) *Client {
	t.Helper()
	c := New("test-token", "https://example.invalid")
	c.http = &http.Client{Transport: repairTransport(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			recorder.WriteHeader(http.StatusNotFound)
		} else {
			handler(recorder, r)
		}
		return recorder.Result(), nil
	})}
	return c
}

func ciRun(id int, status, conclusion string) string {
	return fmt.Sprintf(`{"id":%d,"head_sha":"captured-sha","status":%q,"conclusion":%s}`, id, status, conclusion)
}

func ciPage(total int, runs ...string) string {
	return fmt.Sprintf(`{"total_count":%d,"check_runs":[%s]}`, total, strings.Join(runs, ","))
}

func TestGetCIStatusExactRevision(t *testing.T) {
	success := ciRun(1, "completed", `"success"`)
	failure := ciRun(2, "completed", `"failure"`)
	pending := ciRun(3, "in_progress", "null")
	for _, tc := range []struct {
		name, body string
		want       vcs.CIStatus
		wantErr    bool
	}{
		{"empty", ciPage(0), vcs.CIStatusNone, false},
		{"success", ciPage(1, success), vcs.CIStatusSuccess, false},
		{"all success", ciPage(2, success, ciRun(2, "completed", `"success"`)), vcs.CIStatusSuccess, false},
		{"skipped", ciPage(1, ciRun(1, "completed", `"skipped"`)), vcs.CIStatusFailure, false},
		{"neutral", ciPage(1, ciRun(1, "completed", `"neutral"`)), vcs.CIStatusFailure, false},
		{"cancelled", ciPage(1, ciRun(1, "completed", `"cancelled"`)), vcs.CIStatusFailure, false},
		{"failure", ciPage(1, failure), vcs.CIStatusFailure, false},
		{"pending", ciPage(1, pending), vcs.CIStatusPending, false},
		{"queued", ciPage(1, ciRun(1, "queued", "null")), vcs.CIStatusPending, false},
		{"waiting", ciPage(1, ciRun(1, "waiting", "null")), vcs.CIStatusPending, false},
		{"requested", ciPage(1, ciRun(1, "requested", "null")), vcs.CIStatusPending, false},
		{"pending status", ciPage(1, ciRun(1, "pending", "null")), vcs.CIStatusPending, false},
		{"failure then pending", ciPage(2, failure, pending), vcs.CIStatusFailure, false},
		{"pending then failure", ciPage(2, pending, failure), vcs.CIStatusFailure, false},
		{"wrong SHA", ciPage(1, strings.ReplaceAll(success, "captured-sha", "other")), vcs.CIStatusNone, true},
		{"missing SHA", ciPage(1, strings.ReplaceAll(success, `"head_sha":"captured-sha",`, "")), vcs.CIStatusNone, true},
		{"missing count", `{"check_runs":[]}`, vcs.CIStatusNone, true},
		{"missing runs", `{"total_count":0}`, vcs.CIStatusNone, true},
		{"null runs", `{"total_count":0,"check_runs":null}`, vcs.CIStatusNone, true},
		{"null response", `null`, vcs.CIStatusNone, true},
		{"malformed JSON", `{`, vcs.CIStatusNone, true},
		{"wrong schema", `{"total_count":"1","check_runs":[]}`, vcs.CIStatusNone, true},
		{"empty incomplete", ciPage(1), vcs.CIStatusNone, true},
		{"short incomplete", ciPage(2, success), vcs.CIStatusNone, true},
		{"count mismatch", ciPage(0, success), vcs.CIStatusNone, true},
		{"negative count", ciPage(-1), vcs.CIStatusNone, true},
		{"bounded count", ciPage(10001), vcs.CIStatusNone, true},
		{"duplicate ID", ciPage(2, success, success), vcs.CIStatusNone, true},
		{"missing ID", ciPage(1, strings.ReplaceAll(success, `"id":1,`, "")), vcs.CIStatusNone, true},
		{"unknown status", ciPage(1, ciRun(1, "unknown", "null")), vcs.CIStatusNone, true},
		{"missing status", ciPage(1, ciRun(1, "", "null")), vcs.CIStatusNone, true},
		{"missing conclusion", ciPage(1, ciRun(1, "completed", "null")), vcs.CIStatusNone, true},
		{"empty conclusion", ciPage(1, ciRun(1, "completed", `""`)), vcs.CIStatusNone, true},
		{"unfinished conclusion", ciPage(1, ciRun(1, "queued", `"success"`)), vcs.CIStatusNone, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			client := ciTestClient(t, map[string]http.HandlerFunc{
				"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) {
					reads++
					fmt.Fprint(w, `{"head":{"ref":"mutable/branch","sha":"captured-sha"}}`)
				},
				"GET /repos/o/r/commits/captured-sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
					if q := r.URL.Query(); q.Get("filter") != "latest" || q.Get("per_page") != "100" || q.Get("page") != "1" {
						t.Errorf("unexpected query: %s", r.URL.RawQuery)
					}
					fmt.Fprint(w, tc.body)
				},
			})
			got, err := client.GetCIStatus("o/r", 11)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("got %q, %v; want %q, error=%v", got, err, tc.want, tc.wantErr)
			}
			if tc.want == vcs.CIStatusSuccess && reads != 2 {
				t.Fatalf("success requires PR re-read; reads=%d", reads)
			}
		})
	}
}

func TestGetCIStatusPagination(t *testing.T) {
	first := make([]string, 100)
	for i := range first {
		first[i] = ciRun(i+1, "completed", `"success"`)
	}
	for _, tc := range []struct {
		name, second string
		httpError    bool
		want         vcs.CIStatus
		wantErr      bool
	}{
		{"success", ciPage(101, ciRun(101, "completed", `"success"`)), false, vcs.CIStatusSuccess, false},
		{"failure", ciPage(101, ciRun(101, "completed", `"failure"`)), false, vcs.CIStatusFailure, false},
		{"page error", "", true, vcs.CIStatusNone, true},
		{"duplicate page", ciPage(101, first...), false, vcs.CIStatusNone, true},
		{"duplicate ID across pages", ciPage(101, first[0]), false, vcs.CIStatusNone, true},
		{"changed total", ciPage(102, ciRun(101, "completed", `"success"`)), false, vcs.CIStatusNone, true},
		{"missing second page", ciPage(101), false, vcs.CIStatusNone, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pages := 0
			client := ciTestClient(t, map[string]http.HandlerFunc{
				"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, `{"head":{"sha":"captured-sha"}}`)
				},
				"GET /repos/o/r/commits/captured-sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
					pages++
					switch r.URL.Query().Get("page") {
					case "1":
						fmt.Fprint(w, ciPage(101, first...))
					case "2":
						if tc.httpError {
							w.WriteHeader(http.StatusForbidden)
						} else {
							fmt.Fprint(w, tc.second)
						}
					default:
						t.Errorf("unexpected page: %s", r.URL.RawQuery)
					}
				},
			})
			got, err := client.GetCIStatus("o/r", 11)
			if got != tc.want || (err != nil) != tc.wantErr || pages != 2 {
				t.Fatalf("got %q, %v, pages=%d; want %q, error=%v", got, err, pages, tc.want, tc.wantErr)
			}
		})
	}
}

func TestGetCIStatusPRIdentity(t *testing.T) {
	for _, mode := range []string{"missing initial SHA", "changed head", "missing final SHA", "initial read error", "final read error"} {
		t.Run(mode, func(t *testing.T) {
			reads := 0
			client := ciTestClient(t, map[string]http.HandlerFunc{
				"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) {
					reads++
					if mode == "initial read error" || (mode == "final read error" && reads == 2) {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					sha := "captured-sha"
					if mode == "missing initial SHA" || (mode == "missing final SHA" && reads == 2) {
						sha = ""
					} else if mode == "changed head" && reads == 2 {
						sha = "new-sha"
					}
					fmt.Fprintf(w, `{"head":{"ref":"branch","sha":%q}}`, sha)
				},
				"GET /repos/o/r/commits/captured-sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, ciPage(1, ciRun(1, "completed", `"success"`)))
				},
			})
			got, err := client.GetCIStatus("o/r", 11)
			wantErr := mode != "changed head" && mode != "missing final SHA"
			if got == vcs.CIStatusSuccess || (err != nil) != wantErr {
				t.Fatalf("got %q, %v; want non-success, error=%v", got, err, wantErr)
			}
		})
	}
}

func TestCIEvidenceIdentity(t *testing.T) {
	for _, head := range []string{"captured-sha", "changed-sha"} {
		t.Run(head, func(t *testing.T) {
			reads := 0
			c := ciTestClient(t, map[string]http.HandlerFunc{
				"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) {
					reads++
					sha := "captured-sha"
					if reads > 1 {
						sha = head
					}
					fmt.Fprintf(w, `{"head":{"sha":%q}}`, sha)
				},
				"GET /repos/o/r/commits/captured-sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, ciPage(2, ciRun(21, "completed", `"success"`), ciRun(22, "completed", `"success"`)))
				},
			})
			e, err := c.GetCIEvidence("o/r", 11)
			if err != nil || e.HeadSHA != "captured-sha" || len(e.CheckIDs) != 2 || e.CheckIDs[0] != 21 || e.CheckIDs[1] != 22 {
				t.Fatalf("%+v %v", e, err)
			}
			if (e.Status == vcs.CIStatusSuccess) != (head == "captured-sha") {
				t.Fatalf("wrong status: %+v", e)
			}
		})
	}
}
