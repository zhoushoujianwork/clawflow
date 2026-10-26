package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func TestRetiredRepoApprovalDoesNotEnableAutomation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "")
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {
		BaseBranch: "develop", CIRequired: true, BoundMachine: "worker-1",
	}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	HandleRepoConfig(w, httptest.NewRequest(http.MethodPost, "/api/repo/config", strings.NewReader(`{"repo":"o/r","auto_approve":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "auto_approve") {
		t.Fatalf("retired response field: %s", w.Body)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := loaded.Repos["o/r"]
	if r.Enabled || r.AutoMerge || !r.CIRequired || r.BoundMachine != "worker-1" || r.BaseBranch != "develop" {
		t.Fatalf("retired request changed operational settings: %+v", r)
	}
	data, err := os.ReadFile(snapshot.DataDir() + "/repos.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "auto_approve") {
		t.Fatalf("retired snapshot field: %s", data)
	}
}
