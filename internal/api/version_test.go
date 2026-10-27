package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestVersionChannelStatus(t *testing.T) {
	old := VersionInfo
	t.Cleanup(func() { VersionInfo = old })
	VersionInfo.Status = func() VersionStatus {
		return VersionStatus{Current: "v0.75.0-54-gabcdef0", Channel: "source", Latest: "abcdef1", Behind: 3, Branch: "main", UpdateAvailable: true, CanUpgrade: true}
	}
	w := httptest.NewRecorder()
	HandleVersion(w, httptest.NewRequest("GET", "/api/version", nil))
	var status map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"current", "channel", "latest", "behind", "update_available", "can_upgrade"} {
		if _, ok := status[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
	if status["behind"] != float64(3) || status["channel"] != "source" {
		t.Fatal(status)
	}
}
func TestUpdateRejectsUnsafeStatus(t *testing.T) {
	old := VersionInfo
	t.Cleanup(func() { VersionInfo = old })
	VersionInfo.Status = func() VersionStatus {
		return VersionStatus{Channel: "source", Warning: "diverged", UpdateAvailable: true, CanUpgrade: false}
	}
	VersionInfo.Update = func(context.Context) ([]byte, error) { t.Fatal("unsafe update executed"); return nil, nil }
	w := httptest.NewRecorder()
	HandleUpdate(w, httptest.NewRequest("POST", "/api/update", nil))
	if w.Code != 409 {
		t.Fatalf("status %d", w.Code)
	}
}
func TestUpdateUsesChannelHandler(t *testing.T) {
	old := VersionInfo
	t.Cleanup(func() { VersionInfo = old })
	VersionInfo.Status = func() VersionStatus { return VersionStatus{Channel: "source", UpdateAvailable: true, CanUpgrade: true} }
	called := false
	VersionInfo.Respawn = nil
	VersionInfo.Update = func(context.Context) ([]byte, error) { called = true; return []byte("upgraded"), nil }
	w := httptest.NewRecorder()
	HandleUpdate(w, httptest.NewRequest("POST", "/api/update", nil))
	if !called || w.Code != 200 {
		t.Fatalf("called=%v status=%d", called, w.Code)
	}
}
