package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func TestNativeBridgeLANLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{"owner/repo": {Enabled: true, CIRequired: true, AcceptanceRequired: true, WorkerTags: []string{"project-a"}}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	s := coordinator.New(db.DB())
	adminSecret, err := s.CreateToken(ctx, "viewer", coordinator.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	adminPath := filepath.Join(t.TempDir(), "admin.token")
	if err := os.WriteFile(adminPath, []byte(adminSecret), 0600); err != nil {
		t.Fatal(err)
	}
	admin := &platformAdmin{board: s.Board, workers: s}
	h := coordinator.NewPlatformHandler(s, admin)
	join := coordinator.JoinHandler(s)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker/v1/join" {
			join.ServeHTTP(w, r)
		} else {
			h.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	sum := sha256.Sum256(server.Certificate().Raw)
	pin := hex.EncodeToString(sum[:])
	run := func(args ...string) []byte {
		t.Helper()
		cmd := NewDashboardCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append(args, "--url", server.URL, "--token-file", adminPath, "--fingerprint", pin))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	var initial coordinator.Dashboard
	if err := json.Unmarshal(run("snapshot"), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.SchemaVersion != 1 || len(initial.Projects) != 1 || !initial.Projects[0].Repos[0].CIRequired {
		t.Fatal(initial)
	}
	var offer coordinator.JoinOffer
	if err := json.Unmarshal(run("invite", "lan-1"), &offer); err != nil {
		t.Fatal(err)
	}
	workerPath := filepath.Join(t.TempDir(), "worker.token")
	cmd := NewWorkerCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(offer.Secret + "\n"))
	cmd.SetArgs([]string{"join", "--coordinator", server.URL, "--fingerprint", pin, "--token-file", workerPath})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(workerPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private credential file", err)
	}
	api, err := newWorkerAPI(workerOptions{Coordinator: server.URL, TokenFile: workerPath, Fingerprint: pin})
	if err != nil {
		t.Fatal(err)
	}
	defer api.client.CloseIdleConnections()
	var out any
	if err := api.postOnce(ctx, "register", map[string]any{"tags": []string{"project-a"}, "roles": []string{"operator"}, "max_parallel": 2}, &out); err != nil {
		t.Fatal(err)
	}
	task, err := s.Enqueue(ctx, coordinator.Task{Repo: "owner/repo", Issue: 42, Kind: "implement", Operator: "implement", Tags: []string{"project-a"}})
	if err != nil {
		t.Fatal(err)
	}
	var claim struct {
		Task *coordinator.Task `json:"task"`
	}
	if err := api.postOnce(ctx, "claim", struct{}{}, &claim); err != nil || claim.Task != nil {
		t.Fatal("invited worker not paused", err)
	}
	run("resume", "lan-1")
	if err := api.postOnce(ctx, "claim", struct{}{}, &claim); err != nil || claim.Task == nil {
		t.Fatal("resumed worker cannot claim", err)
	}
	run("drain", "lan-1")
	if err := api.postOnce(ctx, "tasks/"+task.ID+"/heartbeat", map[string]any{"fencing_token": claim.Task.FencingToken}, &out); err != nil {
		t.Fatal(err)
	}
	data := run("snapshot")
	if bytes.Contains(data, []byte(adminSecret)) || bytes.Contains(data, []byte(offer.Secret)) || bytes.Contains(data, []byte("fencing_token")) {
		t.Fatal("private data in snapshot")
	}
	var live coordinator.Dashboard
	if err := json.Unmarshal(data, &live); err != nil {
		t.Fatal(err)
	}
	if len(live.Board.Workers) != 1 || !live.Board.Workers[0].Paused || len(live.Board.Workers[0].RunningTasks) != 1 {
		t.Fatal(live.Board)
	}
	run("events", task.ID)
	if err := api.postOnce(ctx, "tasks/"+task.ID+"/complete", map[string]any{"fencing_token": claim.Task.FencingToken, "result": map[string]any{}}, &out); err != nil {
		t.Fatal(err)
	}
	run("remove", "lan-1")
	if err := api.postOnce(ctx, "claim", struct{}{}, &out); err == nil {
		t.Fatal("removed worker authenticated")
	}
	// Native pause leaves CI/acceptance/merge policy intact.
	run("disable", "owner/repo")
	after, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := after.Repos["owner/repo"]
	if r.Enabled || !r.CIRequired || !r.AcceptanceRequired || r.AutoMerge {
		t.Fatal("pause changed delivery policy", r)
	}
}

func TestControllerAndLegacyWebShareOwnership(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	lock, err := controllerLock()
	if err != nil {
		t.Fatal(err)
	}
	defer filelock.Release(lock)
	if second, err := controllerLock(); err == nil {
		filelock.Release(second)
		t.Fatal("second scheduler accepted")
	}
}

func TestControllerLaunchAgentHasNoWebArguments(t *testing.T) {
	s := webService{controller: true, home: "/tmp/test user", binary: "/tmp/clawflow", path: "/usr/bin", domain: "gui/1"}
	data, err := s.plist("127.0.0.1", 8090)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("<string>controller</string>")) || bytes.Contains(data, []byte("<string>web</string>")) {
		t.Fatal(string(data))
	}
	if !strings.HasSuffix(s.target(), ".controller") || !strings.HasSuffix(s.plistPath(), ".controller.plist") {
		t.Fatal("controller service aliases legacy web")
	}
}
