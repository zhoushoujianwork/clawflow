package gitlab_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

func TestListProjectVariablesPagination(t *testing.T) {
	pages := 0
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/variables": func(w http.ResponseWriter, r *http.Request) {
			pages++
			if r.URL.Query().Get("page") != fmt.Sprint(pages) || r.URL.Query().Get("per_page") != "100" {
				t.Error("pagination parameters missing")
			}
			count := 100
			if pages == 2 {
				count = 1
			}
			rows := make([]map[string]any, count)
			for i := range rows {
				rows[i] = map[string]any{"key": fmt.Sprintf("KEY_%d_%d", pages, i), "value": "private"}
			}
			jsonResp(w, 200, rows)
		},
	})
	vars, err := client.ListProjectVariables("ns/group/repo")
	if err != nil || len(vars) != 101 || pages != 2 {
		t.Fatalf("pagination failed: %d variables, %d pages, %v", len(vars), pages, err)
	}
	if vars[0].EnvironmentScope != "*" || vars[0].VariableType != "env_var" {
		t.Fatalf("legacy defaults missing: %+v", vars[0])
	}
}

func TestProjectVariableCRUD(t *testing.T) {
	value := "first\nsecond\n"
	protected := false
	scope := "review/*"
	calls := map[string]int{}
	client := newTestClient(t, map[string]http.HandlerFunc{
		"POST " + projectPath + "/variables": func(w http.ResponseWriter, r *http.Request) {
			calls["create"]++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["key"] != "DEPLOY_CONFIG" || body["value"] != value || body["environment_scope"] != scope {
				t.Errorf("incorrect create body: %+v", body)
			}
			jsonResp(w, 201, map[string]any{"key": "DEPLOY_CONFIG", "value": value, "environment_scope": scope})
		},
		"GET " + projectPath + "/variables/DEPLOY_CONFIG": func(w http.ResponseWriter, r *http.Request) {
			calls["get"]++
			if r.URL.Query().Get("filter[environment_scope]") != scope {
				t.Error("scope filter missing")
			}
			jsonResp(w, 200, map[string]any{"key": "DEPLOY_CONFIG", "value": value, "environment_scope": scope})
		},
		"PUT " + projectPath + "/variables/DEPLOY_CONFIG": func(w http.ResponseWriter, r *http.Request) {
			calls["update"]++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if r.URL.Query().Get("filter[environment_scope]") != scope || body["value"] != "" || body["protected"] != false {
				t.Errorf("empty value/false/scope lost: %+v", body)
			}
			for _, field := range []string{"masked", "raw", "description", "variable_type", "environment_scope"} {
				if _, exists := body[field]; exists {
					t.Errorf("unspecified field %s was sent", field)
				}
			}
			jsonResp(w, 200, map[string]any{"key": "DEPLOY_CONFIG", "value": "", "environment_scope": scope})
		},
		"DELETE " + projectPath + "/variables/DEPLOY_CONFIG": func(w http.ResponseWriter, r *http.Request) {
			calls["delete"]++
			if r.URL.Query().Get("filter[environment_scope]") != scope {
				t.Error("delete scope missing")
			}
			w.WriteHeader(204)
		},
	})
	if _, err := client.CreateProjectVariable("ns/group/repo", "DEPLOY_CONFIG", gitlab.VariableOptions{Value: &value, EnvironmentScope: &scope}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetProjectVariable("ns/group/repo", "DEPLOY_CONFIG", scope); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if _, err := client.UpdateProjectVariable("ns/group/repo", "DEPLOY_CONFIG", scope, gitlab.VariableOptions{Value: &empty, Protected: &protected}); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteProjectVariable("ns/group/repo", "DEPLOY_CONFIG", scope); err != nil {
		t.Fatal(err)
	}
	if calls["create"] != 1 || calls["update"] != 1 || calls["delete"] != 1 || calls["get"] != 3 {
		t.Fatalf("unexpected CRUD requests: %v", calls)
	}
}

func TestProjectVariableScopeMismatchBlocksWrites(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/variables/KEY": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, map[string]any{"key": "KEY", "value": "secret"})
		},
	})
	value := "updated"
	if _, err := client.UpdateProjectVariable("ns/group/repo", "KEY", "production", gitlab.VariableOptions{Value: &value}); err == nil {
		t.Fatal("expected unsupported scope to block update before PUT")
	}
	if err := client.DeleteProjectVariable("ns/group/repo", "KEY", "production"); err == nil {
		t.Fatal("expected unsupported scope to block delete before DELETE")
	}
}

func TestProjectVariableErrorsAndValidation(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client := newTestClient(t, map[string]http.HandlerFunc{
				"GET " + projectPath + "/variables":     func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private-value", status) },
				"GET " + projectPath + "/variables/KEY": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private-value", status) },
				"POST " + projectPath + "/variables":    func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private-value", status) },
			})
			value := "private-value"
			_, listErr := client.ListProjectVariables("ns/group/repo")
			_, getErr := client.GetProjectVariable("ns/group/repo", "KEY", "")
			_, createErr := client.CreateProjectVariable("ns/group/repo", "KEY", gitlab.VariableOptions{Value: &value})
			for _, err := range []error{listErr, getErr, createErr} {
				if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), value) {
					t.Fatalf("expected sanitized HTTP error, got %v", err)
				}
			}
		})
	}
	client := newTestClient(t, nil)
	value := "value"
	for _, key := range []string{"", "BAD/KEY", "BAD KEY", strings.Repeat("A", 256)} {
		if _, err := client.CreateProjectVariable("ns/group/repo", key, gitlab.VariableOptions{Value: &value}); err == nil {
			t.Errorf("invalid key accepted: %q", key)
		}
	}
	if _, err := client.CreateProjectVariable("ns/group/repo", "KEY", gitlab.VariableOptions{}); err == nil {
		t.Fatal("missing value accepted")
	}
}
