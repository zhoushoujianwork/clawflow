package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildDocUpdatePrompt_IncludesCurrentAndInstructions(t *testing.T) {
	prompt := buildDocUpdatePrompt("my-proj", "context.md", "# Existing\n\nFoo bar.", "Add a section on testing")

	for _, want := range []string{
		"my-proj",
		"context.md",
		"# Existing",
		"Foo bar.",
		"Add a section on testing",
		"```context.md", // opening fence info string
		"COMPLETE",      // protocol emphasis
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n--- prompt ---\n%s", want, prompt)
		}
	}
}

func TestBuildDocUpdatePrompt_EmptyCurrentFlagged(t *testing.T) {
	prompt := buildDocUpdatePrompt("p", "deployment.md", "", "Add SSH log retrieval")

	if !strings.Contains(prompt, "fresh authoring task") {
		t.Error("empty-current case should signal 'fresh authoring task' to the model")
	}
	if !strings.Contains(prompt, "Add SSH log retrieval") {
		t.Error("instructions missing from prompt")
	}
	if !strings.Contains(prompt, "```deployment.md") {
		t.Error("output fence tag should match the target file")
	}
}

func TestHandleProjectUpdateDoc_RejectsBadMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/project/update-doc", nil)
	HandleProjectUpdateDoc(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be 405, got %d", rec.Code)
	}
}

func TestHandleProjectUpdateDoc_RejectsBadJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/project/update-doc",
		strings.NewReader("not json"))
	HandleProjectUpdateDoc(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON should be 400, got %d", rec.Code)
	}
}

func TestHandleProjectUpdateDoc_RejectsMissingProject(t *testing.T) {
	rec := postJSON(t, projectUpdateDocRequest{File: "context.md", Instructions: "x"})
	HandleProjectUpdateDoc(rec.recorder, rec.request)
	expectBadRequestContaining(t, rec.recorder, "project is required")
}

func TestHandleProjectUpdateDoc_RejectsDisallowedFile(t *testing.T) {
	for _, bad := range []string{"CLAUDE.md", "project.yaml", "../etc/passwd", "goals.md", ""} {
		rec := postJSON(t, projectUpdateDocRequest{
			Project:      "p",
			File:         bad,
			Instructions: "x",
		})
		HandleProjectUpdateDoc(rec.recorder, rec.request)
		if rec.recorder.Code != http.StatusBadRequest {
			t.Errorf("file=%q should be 400, got %d", bad, rec.recorder.Code)
		}
		if !strings.Contains(rec.recorder.Body.String(), "not updatable") {
			t.Errorf("file=%q error message should explain whitelist, got: %s", bad, rec.recorder.Body.String())
		}
	}
}

func TestHandleProjectUpdateDoc_RejectsEmptyInstructions(t *testing.T) {
	for _, bad := range []string{"", "   ", "\n\t"} {
		rec := postJSON(t, projectUpdateDocRequest{
			Project:      "p",
			File:         "context.md",
			Instructions: bad,
		})
		HandleProjectUpdateDoc(rec.recorder, rec.request)
		expectBadRequestContaining(t, rec.recorder, "instructions is required")
	}
}

// --- helpers ---

type jsonPostRig struct {
	recorder *httptest.ResponseRecorder
	request  *http.Request
}

func postJSON(t *testing.T, body any) jsonPostRig {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return jsonPostRig{
		recorder: httptest.NewRecorder(),
		request:  httptest.NewRequest(http.MethodPost, "/api/project/update-doc", bytes.NewReader(buf)),
	}
}

func expectBadRequestContaining(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (body: %s)", rec.Code, rec.Body.String())
		return
	}
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("body should contain %q, got: %s", want, rec.Body.String())
	}
}
