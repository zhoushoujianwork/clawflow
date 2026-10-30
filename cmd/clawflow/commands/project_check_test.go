package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"gopkg.in/yaml.v3"
)

func checkWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func checkYAML(t *testing.T, path string, value any) {
	t.Helper()
	data, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	checkWrite(t, path, string(data))
}

func checkFixture(t *testing.T) (string, config.Config) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git required for fixture")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "absent-gitconfig"))
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	bin := filepath.Join(home, "bin")
	checkWrite(t, filepath.Join(bin, "offload"), "#!/bin/sh\nprintf invoked > \"$HOME/agent-invoked\"\nexit 99\n")
	if err := os.Chmod(filepath.Join(bin, "offload"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Dir(git)+string(os.PathListSeparator)+os.Getenv("PATH"))
	checkout := filepath.Join(home, "checkout")
	if err := os.MkdirAll(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture"}, {"remote", "add", "origin", "https://gitlab.example.invalid/ns/repo.git"}} {
		cmd := exec.Command(git, append([]string{"-C", checkout}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	p := project.Project{Name: "demo", Repos: []string{"ns/repo"}, Automation: project.Automation{BoundMachine: host}}
	checkYAML(t, filepath.Join(project.ProjectDir("demo"), "project.yaml"), p)
	cfg := config.Config{Repos: map[string]config.Repo{"ns/repo": {Platform: "gitlab", BaseURL: "https://gitlab.example.invalid", LocalPath: checkout, BaseBranch: "main", PrimaryProject: "demo", BoundMachine: host}}}
	cfg.Settings.Execution = config.ExecutionSettings{Command: "offload", Profile: "test-profile"}
	checkYAML(t, config.ConfigPath(), cfg)
	// Credentials are fixtures only: the command must neither parse nor change them.
	checkWrite(t, filepath.Join(home, ".clawflow", "config", "credentials.yaml"), "secret: DO-NOT-EXPOSE\n")
	return home, cfg
}

type checkFileState struct {
	Data     string
	Mode     os.FileMode
	Modified time.Time
}

func checkTree(t *testing.T, root string) map[string]checkFileState {
	t.Helper()
	files := map[string]checkFileState{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		files[rel] = checkFileState{string(data), info.Mode(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func executeCheck(t *testing.T, name string) (projectCheckResult, error, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := NewProjectCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"check", name, "--json"})
	err := cmd.Execute()
	var result projectCheckResult
	decoder := json.NewDecoder(&stdout)
	if decodeErr := decoder.Decode(&result); decodeErr != nil {
		t.Fatalf("invalid JSON: %v, stderr=%s", decodeErr, stderr.String())
	}
	var extra any
	if e := decoder.Decode(&extra); e != io.EOF {
		t.Fatalf("extra stdout after JSON: %v %+v", e, extra)
	}
	if result.SchemaVersion != 1 || !reflect.DeepEqual(result.Unverified, []string{"remote_auth", "remote_freshness", "provider_execution", "project_tests", "issue_workflow"}) {
		t.Fatalf("contract: %+v", result)
	}
	return result, err, stderr.String()
}

func requireCheck(t *testing.T, r projectCheckResult, id, status string) {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id && c.Status == status {
			return
		}
	}
	t.Fatalf("missing %s=%s: %+v", id, status, r.Checks)
}

func TestProjectCheckReadOnlyJSON(t *testing.T) {
	for _, scenario := range []string{"ready", "dirty", "legacy", "missing_config", "null_config", "malformed_config", "malformed_project", "missing_project", "name_mismatch", "missing_base", "invalid_base", "origin_mismatch", "origin_secret", "missing_local", "binding", "primary", "unregistered", "invalid_operator", "missing_tools"} {
		t.Run(scenario, func(t *testing.T) {
			home, cfg := checkFixture(t)
			rc := cfg.Repos["ns/repo"]
			switch scenario {
			case "dirty":
				checkWrite(t, filepath.Join(rc.LocalPath, "unrelated.txt"), "private work\n")
			case "legacy":
				if err := os.Rename(config.ConfigPath(), filepath.Join(filepath.Dir(config.ConfigPath()), "repos.yaml")); err != nil {
					t.Fatal(err)
				}
			case "malformed_config":
				checkWrite(t, config.ConfigPath(), "repos: [DO-NOT-EXPOSE")
			case "missing_config":
				if err := os.Remove(config.ConfigPath()); err != nil {
					t.Fatal(err)
				}
			case "null_config":
				checkWrite(t, config.ConfigPath(), "null\n")
			case "malformed_project":
				checkWrite(t, filepath.Join(project.ProjectDir("demo"), "project.yaml"), "repos: [DO-NOT-EXPOSE")
			case "missing_project":
				if err := os.Remove(filepath.Join(project.ProjectDir("demo"), "project.yaml")); err != nil {
					t.Fatal(err)
				}
			case "name_mismatch":
				checkYAML(t, filepath.Join(project.ProjectDir("demo"), "project.yaml"), project.Project{Name: "other"})
			case "missing_base":
				rc.BaseBranch = "absent"
			case "invalid_base":
				rc.BaseBranch = "bad..base"
			case "origin_mismatch":
				rc.BaseURL = "https://other.example.invalid"
			case "origin_secret":
				if _, err := projectCheckGit(rc.LocalPath, "remote", "set-url", "origin", "https://DO-NOT-EXPOSE:token@example.invalid/other/repo.git"); err != nil {
					t.Fatal(err)
				}
			case "missing_local":
				rc.LocalPath = ""
			case "binding":
				rc.BoundMachine = "another-host.invalid"
			case "primary":
				rc.PrimaryProject = "missing"
			case "unregistered":
				delete(cfg.Repos, "ns/repo")
			case "invalid_operator":
				checkWrite(t, filepath.Join(home, ".clawflow", "skills", "broken", "SKILL.md"), "DO-NOT-EXPOSE")
			case "missing_tools":
				t.Setenv("PATH", t.TempDir())
			}
			if scenario == "missing_base" || scenario == "invalid_base" || scenario == "origin_mismatch" || scenario == "missing_local" || scenario == "binding" || scenario == "primary" {
				cfg.Repos["ns/repo"] = rc
				checkYAML(t, config.ConfigPath(), cfg)
			}
			if scenario == "unregistered" {
				checkYAML(t, config.ConfigPath(), cfg)
			}
			old := http.DefaultTransport
			requests := 0
			http.DefaultTransport = lifecycleTransport(func(*http.Request) (*http.Response, error) { requests++; return nil, fmt.Errorf("network prohibited") })
			t.Cleanup(func() { http.DefaultTransport = old })
			before := checkTree(t, home)
			r, err, stderr := executeCheck(t, "demo")
			ready := scenario == "ready" || scenario == "dirty" || scenario == "binding"
			if ready && (err != nil || r.Status != "ready") || !ready && (err == nil || r.Status != "blocked") {
				t.Fatalf("status=%s err=%v checks=%+v", r.Status, err, r.Checks)
			}
			if scenario == "dirty" {
				requireCheck(t, r, "checkout_cleanliness", "warning")
			}
			if scenario == "legacy" {
				requireCheck(t, r, "migration_required", "blocked")
			}
			if ready {
				requireCheck(t, r, "repo_automation", "warning")
				requireCheck(t, r, "document_testing", "warning")
			}
			wire, _ := json.Marshal(r)
			if strings.Contains(string(wire)+stderr, "DO-NOT-EXPOSE") || strings.Contains(string(wire), "https://") {
				t.Fatalf("sensitive output: %s %s", wire, stderr)
			}
			if requests != 0 {
				t.Fatalf("HTTP requests=%d", requests)
			}
			if after := checkTree(t, home); !reflect.DeepEqual(before, after) {
				t.Fatal("inspection changed fixture files, modes or modification times")
			}
		})
	}
}

func TestProjectCheckRejectsTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"../demo", "/tmp/demo", `a\b`, ".", ".."} {
		r, err, _ := executeCheck(t, name)
		if err == nil || r.Status != "blocked" {
			t.Fatalf("accepted %q", name)
		}
		requireCheck(t, r, "project_name", "blocked")
	}
}

func TestProjectCheckDoesNotInvokeFSMonitor(t *testing.T) {
	home, cfg := checkFixture(t)
	checkout := cfg.Repos["ns/repo"].LocalPath
	checkWrite(t, filepath.Join(checkout, "tracked.txt"), "fixture\n")
	for _, args := range [][]string{{"add", "tracked.txt"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "tracked fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", checkout}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
	}
	hook := filepath.Join(home, "fsmonitor-hook")
	checkWrite(t, hook, "#!/bin/sh\nprintf invoked > \"$HOME/fsmonitor-invoked\"\nexit 1\n")
	if err := os.Chmod(hook, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", checkout, "config", "core.fsmonitor", hook).CombinedOutput(); err != nil {
		t.Fatalf("fixture config: %v %s", err, out)
	}
	before := checkTree(t, home)
	r, err, _ := executeCheck(t, "demo")
	if err != nil || r.Status != "ready" {
		t.Fatalf("check: %+v, %v", r, err)
	}
	if after := checkTree(t, home); !reflect.DeepEqual(before, after) {
		t.Fatal("readiness invoked a monitor or changed files")
	}
}

func TestProjectCheckDeterministicOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	checkYAML(t, config.ConfigPath(), config.Config{})
	checkYAML(t, filepath.Join(project.ProjectDir("demo"), "project.yaml"), project.Project{Name: "demo", Repos: []string{"z/repo", "a/repo"}})
	first, _, _ := executeCheck(t, "demo")
	second, _, _ := executeCheck(t, "demo")
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated checks changed result ordering")
	}
	var scopes []string
	for _, c := range first.Checks {
		if c.ID == "repo_registration" {
			scopes = append(scopes, c.Scope)
		}
	}
	if !reflect.DeepEqual(scopes, []string{"a/repo", "z/repo"}) {
		t.Fatalf("repository order: %v", scopes)
	}
}
