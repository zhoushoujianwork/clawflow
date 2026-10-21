package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pilot/budget"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

func TestCIVariableLifecycle(t *testing.T) {
	var current *gitlab.Variable
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		base := "/api/v4/projects/group%2Fsub%2Frepo/variables"
		if r.URL.EscapedPath() != base && r.URL.EscapedPath() != base+"/DEMO" {
			t.Errorf("incorrect project path: %s", r.URL.EscapedPath())
		}
		if r.Method == "POST" || r.Method == "PUT" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if r.Method == "POST" {
				current = &gitlab.Variable{Key: "DEMO", EnvironmentScope: "*", VariableType: "env_var", Masked: true}
				w.WriteHeader(201)
			} else if _, exists := body["masked"]; exists {
				t.Error("update reset an omitted setting")
			}
			current.Value = body["value"].(string)
			if p, exists := body["protected"]; exists {
				current.Protected = p.(bool)
			}
			json.NewEncoder(w).Encode(current)
			return
		}
		if r.Method == "DELETE" {
			current = nil
			w.WriteHeader(204)
			return
		}
		if r.URL.EscapedPath() == base {
			json.NewEncoder(w).Encode([]*gitlab.Variable{current})
			return
		}
		if current == nil {
			http.Error(w, "not found", 404)
			return
		}
		json.NewEncoder(w).Encode(current)
	}))
	defer srv.Close()
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{"group/sub/repo": {Platform: "gitlab", BaseURL: srv.URL}}})
	budgetPath := filepath.Join(t.TempDir(), "budget.json")
	if err := budget.Init(budgetPath, 3); err != nil {
		t.Fatal(err)
	}
	t.Setenv(budget.EnvPath, budgetPath)
	run := func(action, stdin string, flags ...string) string {
		t.Helper()
		cmd := NewCICmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetIn(strings.NewReader(stdin))
		args := []string{"variables", action, "--repo", srv.URL + "/group/sub/repo", "--json"}
		cmd.SetArgs(append(args, flags...))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%s: %v (%s)", action, err, out.String())
		}
		return out.String()
	}
	secret := "synthetic-secret-value"
	valuePath := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(valuePath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	out := run("create", "", "--key", "DEMO", "--value-file", valuePath, "--protected", "--masked")
	if strings.Contains(out, secret) || !strings.Contains(out, "[REDACTED]") || current.Value != secret || !current.Protected {
		t.Fatalf("incorrect create or redaction: %s", out)
	}
	for _, action := range []string{"list", "view"} {
		var flags []string
		if action == "view" {
			flags = []string{"--key", "DEMO"}
		}
		if out := run(action, "", flags...); strings.Contains(out, secret) || !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("%s leaked secret: %s", action, out)
		}
	}
	out = run("view", "", "--key", "DEMO", "--show-values")
	if !strings.Contains(out, secret) {
		t.Fatal("explicit value display failed")
	}
	multiline := "first line\nsecond line\n"
	run("update", multiline, "--key", "DEMO", "--value-file", "-", "--protected=false")
	if current.Value != multiline || current.Protected || !current.Masked {
		t.Fatalf("update failed to preserve newlines/omitted settings: %+v", current)
	}
	out = run("delete", "", "--key", "DEMO")
	if current != nil || !strings.Contains(out, `"deleted":true`) {
		t.Fatalf("delete failed: %s", out)
	}
	state, err := budget.Read(budgetPath)
	if err != nil || state.Used != 3 {
		t.Fatalf("incorrect write budget: %+v %v", state, err)
	}
	if requests != 8 {
		t.Fatalf("expected 8 API requests including write preflights, got %d", requests)
	}
}

func TestCIVariableWriteBudget(t *testing.T) {
	for _, action := range []string{"create", "update", "delete"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
			defer srv.Close()
			writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{"group/repo": {Platform: "gitlab", BaseURL: srv.URL}}})
			path := filepath.Join(t.TempDir(), "budget.json")
			if err := budget.Init(path, 1); err != nil {
				t.Fatal(err)
			}
			t.Setenv(budget.EnvPath, path)
			if err := budget.Reserve("previous write"); err != nil {
				t.Fatal(err)
			}
			cmd := NewCICmd()
			args := []string{"variable", action, "--repo", "group/repo", "--key", "DEMO"}
			if action != "delete" {
				args = append(args, "--value", "demo")
			}
			cmd.SetArgs(args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "budget exhausted") || calls != 0 {
				t.Fatalf("budget did not block request: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestCIVariableInvalidArguments(t *testing.T) {
	for i, flags := range [][]string{
		{}, {"--repo", "group/repo"},
		{"--repo", "group/repo", "--key", "DEMO"},
		{"--repo", "group/repo", "--key", "DEMO", "--value", "a", "--value-file", "-"},
		{"--repo", "group/repo", "--key", "DEMO", "--value", "a", "--scope", ""},
		{"--repo", "group/repo", "--key", "DEMO", "--value", "a", "--type", "invalid"},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			cmd := NewCICmd()
			cmd.SetArgs(append([]string{"variable", "create"}, flags...))
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected argument error")
			}
		})
	}
}
