package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMergePRAtHead(t *testing.T) {
	head := strings.Repeat("a", 40)
	merged := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name     string
		status   int
		response string
		wantSHA  string
	}{
		{"success", 200, `{"merged":true,"sha":"` + merged + `"}`, merged},
		{"sha256", 200, `{"merged":true,"sha":"` + strings.Repeat("c", 64) + `"}`, strings.Repeat("c", 64)},
		{"changed_head", 409, `{"message":"Head branch was modified"}`, ""},
		{"false_merged", 200, `{"merged":false,"sha":"` + merged + `"}`, ""},
		{"missing_merged", 200, `{"sha":"` + merged + `"}`, ""},
		{"null_merged", 200, `{"merged":null,"sha":"` + merged + `"}`, ""},
		{"empty_sha", 200, `{"merged":true,"sha":""}`, ""},
		{"missing_sha", 200, `{"merged":true}`, ""},
		{"malformed_sha", 200, `{"merged":true,"sha":"symbolic-sha"}`, ""},
		{"nonhex_sha", 200, `{"merged":true,"sha":"` + strings.Repeat("g", 40) + `"}`, ""},
		{"wrong_sha_length", 200, `{"merged":true,"sha":"` + strings.Repeat("b", 41) + `"}`, ""},
		{"invalid_json", 200, `{`, ""},
		{"wrong_status", 201, `{"merged":true,"sha":"` + merged + `"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := ciTestClient(t, map[string]http.HandlerFunc{
				"PUT /repos/o/r/pulls/11/merge": func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if len(body) != 2 || body["sha"] != head || body["merge_method"] != "merge" {
						t.Fatalf("unexpected conditional merge body: %v", body)
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.response)
				},
			})
			got, err := client.MergePRAtHead("o/r", 11, head)
			if got != tc.wantSHA || (err == nil) != (tc.wantSHA != "") || calls != 1 {
				t.Fatalf("got %q, err %v, calls %d; want %q", got, err, calls, tc.wantSHA)
			}
		})
	}
}

func TestMergePRAtHeadValidatesExpectedSHABeforeRequest(t *testing.T) {
	for _, head := range []string{"", "symbolic-sha", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 63), strings.Repeat("g", 40), strings.Repeat("A", 40), " " + strings.Repeat("a", 40)} {
		client := ciTestClient(t, nil)
		if got, err := client.MergePRAtHead("o/r", 11, head); err == nil || got != "" {
			t.Fatalf("invalid head %q: got %q, err %v", head, got, err)
		}
	}
}

func TestMergePRAtHeadAcceptsSHA256Head(t *testing.T) {
	head := strings.Repeat("a", 64)
	client := ciTestClient(t, map[string]http.HandlerFunc{
		"PUT /repos/o/r/pulls/11/merge": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["sha"] != head {
				t.Fatalf("expected original head, got %v", body)
			}
			fmt.Fprintf(w, `{"merged":true,"sha":%q}`, head)
		},
	})
	if got, err := client.MergePRAtHead("o/r", 11, head); err != nil || got != head {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestMergePRDetailedRequiresConfirmedMerge(t *testing.T) {
	for _, tc := range []struct {
		response string
		wantSHA  string
	}{
		{`{"merged":true,"sha":"legacy-symbolic-sha"}`, "legacy-symbolic-sha"},
		{`{"merged":false,"sha":"legacy-symbolic-sha"}`, ""},
		{`{"sha":"legacy-symbolic-sha"}`, ""},
		{`{"merged":true}`, ""},
		{`{"merged":true,"sha":" "}`, ""},
	} {
		client := ciTestClient(t, map[string]http.HandlerFunc{
			"PUT /repos/o/r/pulls/11/merge": func(w http.ResponseWriter, r *http.Request) {
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 1 || body["merge_method"] != "merge" {
					t.Fatalf("legacy request changed: %v", body)
				}
				fmt.Fprint(w, tc.response)
			},
		})
		got, err := client.MergePRDetailed("o/r", 11)
		if got != tc.wantSHA || (err == nil) != (tc.wantSHA != "") {
			t.Fatalf("response %s: got %q, err %v", tc.response, got, err)
		}
	}
}

func TestGetPRBaseAndMergeMetadata(t *testing.T) {
	for _, mergeSHA := range []string{`null`, `""`, `"merged-sha"`} {
		client := ciTestClient(t, map[string]http.HandlerFunc{
			"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"number":11,"state":"open","base":{"ref":"main","sha":"base-sha"},"head":{"ref":"feature","sha":"head-sha","repo":{"full_name":"o/r"}},"merge_commit_sha":%s}`, mergeSHA)
			},
		})
		pr, err := client.GetPR("o/r", 11)
		wantMergeSHA := ""
		if mergeSHA == `"merged-sha"` {
			wantMergeSHA = "merged-sha"
		}
		if err != nil || pr.BaseBranch != "main" || pr.BaseSHA != "base-sha" || pr.MergeCommitSHA != wantMergeSHA || pr.HeadSHA != "head-sha" || pr.HeadRepo != "o/r" {
			t.Fatalf("PR=%+v, err=%v", pr, err)
		}
	}
}
