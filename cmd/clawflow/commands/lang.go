package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// LangResult is the JSON output of clawflow lang detect.
type LangResult struct {
	Language        string   `json:"language"`
	BuildCmd        string   `json:"build_cmd"`
	TestCmd         string   `json:"test_cmd"`
	ChangedPackages []string `json:"changed_packages,omitempty"`
}

func NewLangCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lang",
		Short: "Language detection and test utilities",
	}
	cmd.AddCommand(newLangDetectCmd())
	return cmd
}

func newLangDetectCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:     "detect",
		Short:   "Detect language and output build/test commands for changed files",
		Example: "  clawflow lang detect --repo owner/repo --issue 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			repoCfg, ok := cfg.Repos[repo]
			if !ok {
				return fmt.Errorf("repo %q not found in config", repo)
			}

			localPath, err := resolveLocalPath(repo, repoCfg.LocalPath)
			if err != nil {
				return err
			}

			worktreePath := config.WorktreePath(repo, issue)

			// Use worktree if it exists, otherwise fall back to local repo
			workDir := worktreePath
			if _, err := os.Stat(worktreePath); err != nil {
				workDir = localPath
			}

			// Get changed files relative to base branch
			base := repoCfg.BaseBranch
			if base == "" {
				base = "main"
			}
			changedFiles := getChangedFiles(workDir, base)

			result := detectLanguage(workDir, changedFiles, repoCfg.TestCommand)
			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func getChangedFiles(dir, base string) []string {
	c := exec.Command("git", "diff", "--name-only", "origin/"+base+"...HEAD")
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

func detectLanguage(dir string, changedFiles []string, overrideTestCmd string) LangResult {
	type langDef struct {
		marker   string
		lang     string
		buildCmd string
		testFn   func(dir string, files []string) string
	}

	defs := []langDef{
		{
			marker:   "go.mod",
			lang:     "go",
			buildCmd: "go build ./...",
			testFn:   goTestCmd,
		},
		{
			marker:   "package.json",
			lang:     "node",
			buildCmd: "npm run build --if-present && npm run lint --if-present",
			testFn:   nodeTestCmd,
		},
		{
			marker:   "pyproject.toml",
			lang:     "python",
			buildCmd: "python -m py_compile $(git diff --name-only origin/main...HEAD | grep '\\.py$' | tr '\\n' ' ')",
			testFn:   pythonTestCmd,
		},
		{
			marker:   "requirements.txt",
			lang:     "python",
			buildCmd: "python -m py_compile $(git diff --name-only origin/main...HEAD | grep '\\.py$' | tr '\\n' ' ')",
			testFn:   pythonTestCmd,
		},
		{
			marker:   "Cargo.toml",
			lang:     "rust",
			buildCmd: "cargo build",
			testFn:   rustTestCmd,
		},
		{
			marker:   "pom.xml",
			lang:     "java",
			buildCmd: "mvn compile -q",
			testFn:   javaTestCmd,
		},
	}

	for _, d := range defs {
		if _, err := os.Stat(filepath.Join(dir, d.marker)); err == nil {
			testCmd := d.testFn(dir, changedFiles)
			if overrideTestCmd != "" {
				testCmd = overrideTestCmd
			}
			return LangResult{
				Language:        d.lang,
				BuildCmd:        d.buildCmd,
				TestCmd:         testCmd,
				ChangedPackages: changedPackages(d.lang, changedFiles),
			}
		}
	}

	return LangResult{Language: "unknown", BuildCmd: "", TestCmd: ""}
}

func goTestCmd(dir string, files []string) string {
	pkgs := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, ".go") {
			pkgs["./"+filepath.Dir(f)+"/..."] = true
		}
	}
	if len(pkgs) == 0 {
		return "go test ./..."
	}
	var parts []string
	for p := range pkgs {
		parts = append(parts, p)
	}
	return "go test " + strings.Join(parts, " ")
}

func nodeTestCmd(_ string, files []string) string {
	var patterns []string
	for _, f := range files {
		if strings.HasSuffix(f, ".ts") || strings.HasSuffix(f, ".js") || strings.HasSuffix(f, ".tsx") {
			base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(f, ".ts"), ".js"), ".tsx")
			patterns = append(patterns, base)
		}
	}
	if len(patterns) == 0 {
		return "npm test -- --passWithNoTests"
	}
	return "npm test -- --testPathPattern='" + strings.Join(patterns, "|") + "' --passWithNoTests"
}

func pythonTestCmd(_ string, files []string) string {
	var modules []string
	for _, f := range files {
		if strings.HasSuffix(f, ".py") && strings.HasPrefix(f, "tests/") {
			modules = append(modules, f)
		}
	}
	if len(modules) == 0 {
		return "pytest tests/ -x -q"
	}
	return "pytest " + strings.Join(modules, " ") + " -x -q"
}

func rustTestCmd(_ string, _ []string) string {
	return "cargo test"
}

func javaTestCmd(_ string, files []string) string {
	var classes []string
	for _, f := range files {
		if strings.HasSuffix(f, "Test.java") {
			base := filepath.Base(f)
			classes = append(classes, strings.TrimSuffix(base, ".java"))
		}
	}
	if len(classes) == 0 {
		return "mvn test -q"
	}
	return "mvn test -Dtest=" + strings.Join(classes, ",") + " -q"
}

func changedPackages(lang string, files []string) []string {
	seen := map[string]bool{}
	for _, f := range files {
		switch lang {
		case "go":
			if strings.HasSuffix(f, ".go") {
				seen[filepath.Dir(f)] = true
			}
		case "node":
			if strings.HasSuffix(f, ".ts") || strings.HasSuffix(f, ".js") || strings.HasSuffix(f, ".tsx") {
				seen[filepath.Dir(f)] = true
			}
		case "python":
			if strings.HasSuffix(f, ".py") {
				seen[filepath.Dir(f)] = true
			}
		}
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	return out
}
