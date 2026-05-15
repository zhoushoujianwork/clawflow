package cloud

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestFromCredentialsDefaultsBaseURL(t *testing.T) {
	cfg := FromCredentials(&config.Credentials{})
	if cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", cfg.BaseURL, DefaultBaseURL)
	}
}

func TestFromCredentialsCopiesCloudFields(t *testing.T) {
	cfg := FromCredentials(&config.Credentials{
		CloudURL:         "https://cloud.example.com/",
		CloudAccessToken: "user-token",
		CloudMachineID:   "machine-1",
		CloudWorkerID:    "worker-1",
		CloudWorkerToken: "worker-token",
	})
	if cfg.BaseURL != "https://cloud.example.com" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.AccessToken != "user-token" || cfg.MachineID != "machine-1" || cfg.WorkerID != "worker-1" || cfg.WorkerToken != "worker-token" {
		t.Fatalf("cloud fields not copied: %#v", cfg)
	}
}

func TestRegisterWorkerRequestShape(t *testing.T) {
	var got RegisterWorkerRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/worker/register" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(RegisterWorkerResponse{
			MachineID:   "machine-1",
			WorkerID:    "worker-1",
			WorkerToken: "worker-token",
		})
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL, AccessToken: "user-token"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.RegisterWorker(t.Context(), RegisterWorkerRequest{
		Hostname:     "host",
		Version:      "dev",
		Capabilities: []Capability{"go", "github"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.MachineID != "machine-1" || resp.WorkerToken != "worker-token" {
		t.Fatalf("response = %#v", resp)
	}
	if got.Hostname != "host" || got.Version != "dev" || len(got.Capabilities) != 2 {
		t.Fatalf("request = %#v", got)
	}
}
