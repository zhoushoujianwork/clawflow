package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/project"
)

func TestHandleProjectWriteGoals_OK(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if _, err := project.Create("write-goals-ok"); err != nil {
		t.Fatalf("project.Create: %v", err)
	}

	body, _ := json.Marshal(map[string]string{
		"project": "write-goals-ok",
		"content": "# Goals\n\n- thing one",
	})
	r := httptest.NewRequest(http.MethodPost, "/api/project/write-goals", bytes.NewReader(body))
	w := httptest.NewRecorder()
	HandleProjectWriteGoals(w, r)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got, err := project.ReadGoals("write-goals-ok")
	if err != nil {
		t.Fatalf("ReadGoals: %v", err)
	}
	if !strings.Contains(got, "thing one") {
		t.Errorf("written goals.md missing content; got %q", got)
	}
}

func TestHandleProjectWriteGoals_MissingProject(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"content": "x"})
	r := httptest.NewRequest(http.MethodPost, "/api/project/write-goals", bytes.NewReader(body))
	w := httptest.NewRecorder()
	HandleProjectWriteGoals(w, r)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleProjectWriteGoals_WrongMethod(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/project/write-goals", nil)
	w := httptest.NewRecorder()
	HandleProjectWriteGoals(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}
