package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	clawsync "github.com/zhoushoujianwork/clawflow/internal/sync"
	"gopkg.in/yaml.v3"
)

type projectCheck struct {
	ID         string `json:"id"`
	Scope      string `json:"scope"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	NextAction string `json:"next_action,omitempty"`
}

type projectCheckResult struct {
	SchemaVersion int            `json:"schema_version"`
	Project       string         `json:"project"`
	Status        string         `json:"status"`
	Checks        []projectCheck `json:"checks"`
	Unverified    []string       `json:"unverified"`
}

func newProjectCheckCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{Use: "check <name>", Short: "Inspect local project readiness without agents, network or writes", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			r := inspectProject(args[0])
			if jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(r); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Project %s: %s (local prerequisites only)\n", r.Project, r.Status)
				for _, check := range r.Checks {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s [%s]: %s\n", check.Status, check.ID, check.Scope, check.Message)
					if check.NextAction != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "  Next: %s\n", check.NextAction)
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Unverified: %s\n", strings.Join(r.Unverified, ", "))
			}
			if r.Status == "blocked" {
				return fmt.Errorf("project readiness blocked")
			}
			return nil
		}}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Return one versioned JSON readiness result")
	return cmd
}

func (r *projectCheckResult) add(id, scope, status, message, next string) {
	r.Checks = append(r.Checks, projectCheck{id, scope, status, message, next})
	if status == "blocked" {
		r.Status = "blocked"
	}
}

// Read canonical files directly: config.Load may migrate legacy configuration.
// Errors are deliberately generic so malformed YAML cannot expose secret values.
func readCheckYAML(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return fmt.Errorf("empty YAML")
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("expected YAML mapping")
	}
	return yaml.Unmarshal(data, value)
}

func validCheckProjectName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\") && !filepath.IsAbs(name)
}

func inspectProject(name string) projectCheckResult {
	r := projectCheckResult{SchemaVersion: 1, Project: name, Status: "ready", Checks: []projectCheck{}, Unverified: []string{"remote_auth", "remote_freshness", "provider_execution", "project_tests", "issue_workflow"}}
	if !validCheckProjectName(name) {
		r.add("project_name", "project", "blocked", "Invalid project name", "Use a single project directory name")
		return r
	}
	var p project.Project
	pOK := true
	if err := readCheckYAML(filepath.Join(project.ProjectDir(name), "project.yaml"), &p); err != nil {
		pOK = false
		r.add("project_registration", name, "blocked", "Project registration is missing, unreadable or malformed", "Inspect project registration or create the project")
	} else if p.Name != name {
		pOK = false
		r.add("project_registration", name, "blocked", "Embedded project name differs from requested name", "Correct the project registration")
	} else {
		r.add("project_registration", name, "passed", "Project registration loaded", "")
	}
	var cfg config.Config
	cfgOK := true
	if err := readCheckYAML(config.ConfigPath(), &cfg); err != nil {
		cfgOK = false
		id := "config_registration"
		message := "Canonical config is missing, unreadable or malformed"
		if os.IsNotExist(err) {
			if _, legacyErr := os.Stat(filepath.Join(filepath.Dir(config.ConfigPath()), "repos.yaml")); legacyErr == nil {
				id, message = "migration_required", "Legacy config requires explicit migration before checking"
			}
		}
		r.add(id, name, "blocked", message, "Inspect or migrate configuration separately")
	} else {
		r.add("config_registration", name, "passed", "Canonical config loaded without migration", "")
	}
	for _, tool := range []struct{ name, binary string }{{"git", "git"}, {"claude", claude.Resolve()}} {
		if _, err := exec.LookPath(tool.binary); err != nil {
			r.add("executable_"+tool.name, name, "blocked", tool.name+" executable unavailable", "Install the required executable")
		} else {
			r.add("executable_"+tool.name, name, "passed", tool.name+" executable available; execution unverified", "")
		}
	}
	if paused, err := clawsync.IsPaused(); err != nil {
		r.add("sync", name, "blocked", "Cannot inspect local sync pause state", "Inspect sync pause marker")
	} else {
		r.add("sync", name, "passed", fmt.Sprintf("Local sync paused=%t; remote sync configuration unverified", paused), "")
	}
	if cfgOK {
		r.add("scheduler", name, "passed", fmt.Sprintf("Scheduler paused=%t, interval_minutes=%d; external schedulers unverified", cfg.Settings.RunPaused, cfg.Settings.RunIntervalMinutes), "")
	}
	reg, err := loadRegistry()
	if err != nil {
		r.add("operators", name, "blocked", "Operator definitions could not be loaded", "Run clawflow operators validate and correct definitions")
	} else if len(reg.All()) == 0 {
		r.add("operators", name, "blocked", "No operators registered", "Restore operator definitions")
	} else {
		r.add("operators", name, "passed", fmt.Sprintf("%d operators loaded", len(reg.All())), "")
		for _, d := range reg.Diagnose() {
			status := "warning"
			if d.Severity == operator.SeverityError {
				status = "blocked"
			}
			r.add("operator_diagnostic", d.Operator, status, d.Message, "Inspect clawflow operators doctor")
		}
	}
	if !pOK {
		return r
	}
	host, hostErr := os.Hostname()
	r.add("pilot", name, stateWarning(p.Automation.Enabled), fmt.Sprintf("Pilot enabled=%t, cooldown_minutes=%d, cooldown_remaining_seconds=%d", p.Automation.Enabled, p.Automation.CooldownMinutes, int(p.CooldownRemaining(time.Now()).Seconds())), "")
	checkMachine(&r, "pilot_binding", name, p.Automation.BoundMachine, host, hostErr, cfgOK && cfg.Settings.RequireBinding)
	for _, doc := range []string{"context", "testing"} {
		data, err := os.ReadFile(filepath.Join(project.ProjectDir(name), doc+".md"))
		if err != nil || strings.TrimSpace(string(data)) == "" {
			r.add("document_"+doc, name, "warning", doc+" instructions missing, unreadable or empty", "Add project "+doc+" instructions before execution")
		} else {
			r.add("document_"+doc, name, "passed", doc+" instructions present; contents not evaluated", "")
		}
	}
	if !cfgOK {
		return r
	}
	repos := append([]string(nil), p.Repos...)
	sort.Strings(repos)
	if len(repos) == 0 {
		r.add("project_repos", name, "blocked", "Project has no repositories", "Register a repository and add project membership")
	}
	seen := map[string]bool{}
	for _, repo := range repos {
		if seen[repo] {
			r.add("repo_registration", repo, "blocked", "Duplicate project repository reference", "Correct project membership")
			continue
		}
		seen[repo] = true
		rc, ok := cfg.Repos[repo]
		if !ok {
			r.add("repo_registration", repo, "blocked", "Project repository is not registered", "Register the repository separately")
			continue
		}
		r.add("repo_registration", repo, "passed", "Repository registered", "")
		r.add("repo_automation", repo, stateWarning(rc.Enabled), fmt.Sprintf("Monitoring enabled=%t, auto_approve=%t, auto_merge=%t, ci_required=%t", rc.Enabled, rc.AutoApprove, rc.AutoMerge, rc.CIRequired), "")
		if rc.PrimaryProject == "" {
			r.add("primary_project", repo, "warning", "Primary project unset; runtime uses membership fallback", "Set an explicit primary project when authoritative context matters")
		} else if !validCheckProjectName(rc.PrimaryProject) {
			r.add("primary_project", repo, "blocked", "Primary project name is invalid", "Correct primary project binding")
		} else {
			var primary project.Project
			err := readCheckYAML(filepath.Join(project.ProjectDir(rc.PrimaryProject), "project.yaml"), &primary)
			member := false
			for _, v := range primary.Repos {
				member = member || v == repo
			}
			if err != nil || primary.Name != rc.PrimaryProject || !member {
				r.add("primary_project", repo, "blocked", "Primary project registration or membership is inconsistent", "Correct primary project binding")
			} else {
				status := "passed"
				if rc.PrimaryProject != name {
					status = "warning"
				}
				r.add("primary_project", repo, status, "Authoritative project: "+rc.PrimaryProject, "")
			}
		}
		checkMachine(&r, "repo_binding", repo, rc.BoundMachine, host, hostErr, cfg.Settings.RequireBinding)
		platform := rc.Platform
		if platform == "" {
			platform = "github"
		}
		if rc.LocalPath == "" {
			r.add("checkout_origin", repo, "blocked", "Local checkout path is missing", "Configure the intended local checkout")
			continue
		}
		path, err := validateRepoCheckout(rc.LocalPath, config.RepoInfo{OwnerRepo: repo, Platform: platform, BaseURL: rc.BaseURL})
		if err != nil {
			r.add("checkout_origin", repo, "blocked", "Local checkout is missing or its origin does not match the registered repository", "Inspect local path and origin provider, host and namespace")
			continue
		}
		r.add("checkout_origin", repo, "passed", "Checkout origin matches registered repository", "")
		base := rc.BaseBranch
		if base == "" {
			base = "main"
		}
		_, syntaxErr := projectCheckGit(path, "check-ref-format", "--branch", base)
		if validateBranchName(base) != nil || syntaxErr != nil {
			r.add("checkout_base", repo, "blocked", "Configured base branch syntax is invalid", "Correct configured base branch")
		} else {
			_, remoteErr := projectCheckGit(path, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+base+"^{commit}")
			_, localErr := projectCheckGit(path, "rev-parse", "--verify", "--quiet", "refs/heads/"+base+"^{commit}")
			if remoteErr != nil && localErr != nil {
				r.add("checkout_base", repo, "blocked", "Base commit unavailable locally; remote freshness unknown", "Verify and fetch the intended base separately")
			} else {
				r.add("checkout_base", repo, "passed", "Base commit available locally; remote freshness unknown", "")
			}
		}
		out, err := projectCheckGit(path, "status", "--porcelain", "--untracked-files=normal")
		if err != nil {
			r.add("checkout_cleanliness", repo, "blocked", "Cannot inspect checkout changes", "Inspect checkout locally")
		} else if strings.TrimSpace(out) != "" {
			r.add("checkout_cleanliness", repo, "warning", "Checkout contains local changes", "Preserve existing work and use an isolated workspace for execution")
		} else {
			r.add("checkout_cleanliness", repo, "passed", "Checkout is clean", "")
		}
	}
	return r
}

func stateWarning(enabled bool) string {
	if enabled {
		return "passed"
	}
	return "warning"
}

func checkMachine(r *projectCheckResult, id, scope, bound, host string, hostErr error, required bool) {
	status, message := "passed", "Machine binding matches current host"
	if hostErr != nil || host == "" {
		status, message = "warning", "Current hostname unavailable; binding not verified"
	} else if bound != "" && bound != host {
		status, message = "blocked", "Bound to another machine"
	} else if bound == "" && required {
		status, message = "blocked", "Machine binding required but absent"
	} else if bound == "" {
		status, message = "warning", "No machine restriction configured"
	}
	next := ""
	if status == "blocked" {
		next = "Review machine binding before execution"
	}
	r.add(id, scope, status, message, next)
}

func projectCheckGit(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A read-only status check must not launch a configured fsmonitor hook
	// or daemon. Optional-lock suppression alone only prevents index writes.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false", "-C", path}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return string(out), err
}
