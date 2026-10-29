package gitlab_test

import (
	"net/http"
	"testing"
)

func TestListPRFilesRenameAndOverflow(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		c := newTestClient(t, map[string]http.HandlerFunc{
			"GET " + projectPath + "/merge_requests/9/changes": func(w http.ResponseWriter, r *http.Request) {
				jsonResp(w, 200, map[string]any{"overflow": overflow, "changes": []map[string]string{{"old_path": "old.go", "new_path": "new.go"}}})
			},
		})
		files, err := c.ListPRFiles("ns/group/repo", 9)
		if overflow {
			if err == nil || files != nil {
				t.Fatal("overflow must not return partial evidence")
			}
			continue
		}
		if err != nil || len(files) != 2 || files[0] != "new.go" || files[1] != "old.go" {
			t.Fatalf("files=%v err=%v", files, err)
		}
	}
}
