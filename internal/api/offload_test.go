package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOffloadReadinessAndRetiredProviderRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", "/bin/sh")
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "engineering")
	path := filepath.Join(os.Getenv("HOME"), ".clawflow", "config", "credentials.yaml")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte("malformed: [legacy-secret"), 0600)
	response := httptest.NewRecorder()
	HandleListProviders(response, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"profile":"engineering"`) || strings.Contains(response.Body.String(), "legacy-secret") {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, handler := range []http.HandlerFunc{HandleAddProvider, HandleUpdateProvider, HandleDeleteProvider, HandleReorderProviders, HandleRevealProviderKey, HandleTestProvider} {
		response = httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodPost, "/api/providers/0", strings.NewReader(`{}`)))
		if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), "offload config") {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "malformed: [legacy-secret" {
		t.Fatal("credentials mutated")
	}
}
