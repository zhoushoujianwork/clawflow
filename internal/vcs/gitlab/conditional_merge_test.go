package gitlab_test

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
		{"success", 200, `{"state":"merged","merge_commit_sha":"` + merged + `"}`, merged},
		{"sha256", 200, `{"state":"merged","merge_commit_sha":"` + strings.Repeat("c", 64) + `"}`, strings.Repeat("c", 64)},
		{"not_mergeable_405", 405, `{"message":"not mergeable"}`, ""},
		{"not_mergeable_406", 406, `{"message":"not acceptable"}`, ""},
		{"not_mergeable_422", 422, `{"message":"cannot merge"}`, ""},
		{"scheduled", 200, `{"state":"opened","merge_when_pipeline_succeeds":true}`, ""},
		{"merged_but_scheduled", 200, `{"state":"merged","merge_when_pipeline_succeeds":true,"merge_commit_sha":"` + merged + `"}`, ""},
		{"squash", 200, `{"state":"merged","squash_commit_sha":"` + merged + `"}`, merged},
		{"changed_head", 409, `{"message":"Head branch was modified"}`, ""},
		{"false_merged", 200, `{"state":"opened","merge_commit_sha":"` + merged + `"}`, ""},
		{"missing_merged", 200, `{"merge_commit_sha":"` + merged + `"}`, ""},
		{"null_merged", 200, `{"state":null,"merge_commit_sha":"` + merged + `"}`, ""},
		{"empty_sha", 200, `{"state":"merged","merge_commit_sha":""}`, ""},
		{"missing_sha", 200, `{"state":"merged"}`, ""},
		{"malformed_sha", 200, `{"state":"merged","merge_commit_sha":"symbolic-sha"}`, ""},
		{"nonhex_sha", 200, `{"state":"merged","merge_commit_sha":"` + strings.Repeat("g", 40) + `"}`, ""},
		{"wrong_sha_length", 200, `{"state":"merged","merge_commit_sha":"` + strings.Repeat("b", 41) + `"}`, ""},
		{"invalid_json", 200, `{`, ""},
		{"wrong_status", 201, `{"state":"merged","merge_commit_sha":"` + merged + `"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := newTestClient(t, map[string]http.HandlerFunc{
				"PUT /api/v4/projects/ns%2Fgroup%2Frepo/merge_requests/11/merge": func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if len(body) != 2 || body["sha"] != head || body["merge_when_pipeline_succeeds"] != false {
						t.Fatalf("unexpected conditional merge body: %v", body)
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.response)
				},
			})
			got, err := client.MergePRAtHead("ns/group/repo", 11, head)
			if got != tc.wantSHA || (err == nil) != (tc.wantSHA != "") || calls != 1 {
				t.Fatalf("got %q, err %v, calls %d; want %q", got, err, calls, tc.wantSHA)
			}
		})
	}
}

func TestMergePRAtHeadValidatesExpectedSHABeforeRequest(t *testing.T) {
	for _, head := range []string{"", "symbolic-sha", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 63), strings.Repeat("g", 40), strings.Repeat("A", 40), " " + strings.Repeat("a", 40)} {
		client := newTestClient(t, nil)
		if got, err := client.MergePRAtHead("ns/group/repo", 11, head); err == nil || got != "" {
			t.Fatalf("invalid head %q: got %q, err %v", head, got, err)
		}
	}
}

func TestMergePRAtHeadAcceptsSHA256Head(t *testing.T) {
	head := strings.Repeat("a", 64)
	client := newTestClient(t, map[string]http.HandlerFunc{
		"PUT /api/v4/projects/ns%2Fgroup%2Frepo/merge_requests/11/merge": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["sha"] != head {
				t.Fatalf("expected original head, got %v", body)
			}
			fmt.Fprintf(w, `{"state":"merged","merge_commit_sha":%q}`, head)
		},
	})
	if got, err := client.MergePRAtHead("ns/group/repo", 11, head); err != nil || got != head {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestGetPRAcceptanceMetadata(t *testing.T) {
	for _, squash := range []bool{false, true} {
		client := newTestClient(t, map[string]http.HandlerFunc{
			"GET " + projectPath + "/merge_requests/11": func(w http.ResponseWriter, r *http.Request) {
				result := map[string]any{
					"iid": 11, "state": "opened", "sha": strings.Repeat("a", 40),
					"target_branch": "main", "source_branch": "feature",
					"source_project_id": 1, "target_project_id": 1,
					"diff_refs": map[string]string{"base_sha": strings.Repeat("b", 40)},
					"draft":     true,
				}
				field := "merge_commit_sha"
				if squash {
					field = "squash_commit_sha"
				}
				result[field] = strings.Repeat("c", 40)
				jsonResp(w, 200, result)
			},
		})
		pr, err := client.GetPR("ns/group/repo", 11)
		if err != nil || pr.HeadSHA != strings.Repeat("a", 40) || pr.BaseSHA != strings.Repeat("b", 40) || pr.MergeCommitSHA != strings.Repeat("c", 40) || pr.BaseBranch != "main" || pr.HeadRepo != "ns/group/repo" || !pr.Draft {
			t.Fatalf("PR=%+v, err=%v", pr, err)
		}
	}
}
