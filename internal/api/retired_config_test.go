package api

import (
	"encoding/json"
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
	if r.Enabled || r.AutoMerge || !r.CIRequired || strings.Join(r.WorkerTags, ",") != "host-worker-1" || r.BaseBranch != "develop" {
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

func TestProvidersPilotProfile(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback", true: "override"}[override], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "")
			os.Unsetenv("CLAWFLOW_OFFLOAD_PROFILE")
			cfg := &config.Config{Settings: config.Settings{Execution: config.ExecutionSettings{Profile: "base"}}}
			want := "base"
			if override {
				cfg.Settings.Execution.RoleProfiles = map[string]string{config.RolePilot: "supervisor"}
				want = "supervisor"
			}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			HandleListProviders(w, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
			var response struct {
				PilotProfile string            `json:"pilot_profile"`
				RoleProfiles map[string]string `json:"role_profiles"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.PilotProfile != want || (override && response.RoleProfiles[config.RolePilot] != want) {
				t.Fatalf("%s", w.Body)
			}
		})
	}
}

func TestLegacyRepoBindUpdatesHostTags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {WorkerTags: []string{"codex", "host-old"}}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ body, want string }{
		{`{"repo":"o/r","bind":true,"machine":"Work-Mac.local"}`, "codex,host-work-mac"},
		{`{"repo":"o/r","bind":false}`, "codex"},
	} {
		w := httptest.NewRecorder()
		HandleRepoBind(w, httptest.NewRequest(http.MethodPost, "/api/repo/bind", strings.NewReader(tc.body)))
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		loaded, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(loaded.Repos["o/r"].WorkerTags, ","); got != tc.want {
			t.Fatalf("tags=%q want %q", got, tc.want)
		}
		data, err := os.ReadFile(snapshot.DataDir() + "/repos.json")
		if err != nil {
			t.Fatal(err)
		}
		var views []struct {
			BoundMachine string   `json:"bound_machine"`
			WorkerTags   []string `json:"worker_tags"`
		}
		if err := json.Unmarshal(data, &views); err != nil {
			t.Fatal(err)
		}
		wantHost := ""
		if strings.Contains(tc.want, "host-work-mac") {
			wantHost = "work-mac"
		}
		if len(views) != 1 || views[0].BoundMachine != wantHost || strings.Join(views[0].WorkerTags, ",") != tc.want {
			t.Fatalf("binding lost on refresh: %s", data)
		}
		var response struct {
			BoundMachine string `json:"bound_machine"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.BoundMachine != wantHost {
			t.Fatalf("response differs from snapshot: %s", w.Body.String())
		}

	}
}
