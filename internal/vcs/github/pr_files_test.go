package github_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestListPRFilesPaginationAndRename(t *testing.T) {
	calls := 0
	c := newTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/9/files": func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Query().Get("per_page") != "100" {
				t.Error("missing page size")
			}
			if r.URL.Query().Get("page") == "1" {
				rows := make([]map[string]string, 100)
				for i := range rows {
					rows[i] = map[string]string{"filename": fmt.Sprintf("f%d.go", i)}
				}
				jsonResp(w, 200, rows)
			} else {
				jsonResp(w, 200, []map[string]string{{"filename": "new.go", "previous_filename": "old.go"}})
			}
		},
	})
	files, err := c.ListPRFiles("o/r", 9)
	if err != nil || calls != 2 || len(files) != 102 || files[101] != "old.go" {
		t.Fatalf("files=%v calls=%d err=%v", files, calls, err)
	}
}

func TestListPRFilesError(t *testing.T) {
	c := newTestClient(t, map[string]http.HandlerFunc{"GET /repos/o/r/pulls/9/files": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }})
	if files, err := c.ListPRFiles("o/r", 9); err == nil || files != nil {
		t.Fatal("failed response must not look complete")
	}
}
