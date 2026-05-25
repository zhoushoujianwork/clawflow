package github_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// decodeGistFiles pulls the "files" object out of a Gist request body.
func decodeGistFiles(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Files map[string]any `json:"files"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("cannot decode request body %q: %v", raw, err)
	}
	return body.Files
}

func TestUpdateGist_DropsEmptyContent(t *testing.T) {
	var sentFiles map[string]any
	client := newTestClient(t, map[string]http.HandlerFunc{
		"PATCH /gists/abc": func(w http.ResponseWriter, r *http.Request) {
			sentFiles = decodeGistFiles(t, r)
			jsonResp(w, 200, map[string]any{"id": "abc"})
		},
	})

	_, err := client.UpdateGist("abc", map[string]string{
		"config.yaml":   "real content",
		"projects--x.md": "", // empty → must not be sent (would delete the file)
	})
	if err != nil {
		t.Fatalf("UpdateGist: %v", err)
	}
	if _, ok := sentFiles["projects--x.md"]; ok {
		t.Error("empty-content file was sent to GitHub (would delete it)")
	}
	if _, ok := sentFiles["config.yaml"]; !ok {
		t.Error("config.yaml with real content should have been sent")
	}
}

func TestUpdateGist_RefusesAllEmpty(t *testing.T) {
	// No HTTP route registered: the test fails loudly if any request is made.
	client := newTestClient(t, map[string]http.HandlerFunc{})

	_, err := client.UpdateGist("abc", map[string]string{
		"config.yaml": "",
	})
	if err == nil {
		t.Fatal("expected error when all files are empty, got nil")
	}
}

func TestCreateGist_RefusesAllEmpty(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{})

	_, err := client.CreateGist("clawflow-config", map[string]string{
		"config.yaml": "",
	})
	if err == nil {
		t.Fatal("expected error when all files are empty, got nil")
	}
}
