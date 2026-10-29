package commands

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

func TestLegacyProjectAutomationNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{}, {"enable", "missing", "--cooldown", "0"}, {"disable", "missing"}, {"bind", "missing", "--machine", "host"}, {"show", "missing"}} {
		cmd := newProjectAutomationCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.Count(strings.TrimSpace(out.String()), "\n") != 0 || !strings.Contains(out.String(), "clawflow pilot ask") {
			t.Fatalf("%q", out.String())
		}
	}
}

// Repository passes cannot invoke Pilot. Web ticks may only dispatch explicit
// user-defined checks; direct Ask/Wake calls would restore built-in patrols.
func TestAutomaticEntryPointsOnlyInvokeUserChecks(t *testing.T) {
	for _, name := range []string{"run.go", "web.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasSuffix(path, "/pilot") && name == "run.go" {
				t.Fatal("repository passes must not invoke Pilot")
			}
		}
		if name == "web.go" {
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if ok && pkg.Name == "pilot" && sel.Sel.Name != "OpenCheckStore" && sel.Sel.Name != "TickChecks" {
					t.Fatalf("web scheduler cannot directly invoke pilot.%s", sel.Sel.Name)
				}
				return true
			})
		}
	}
}

func TestStewardConversationCommands(t *testing.T) {
	oldAsk, oldStore := askSteward, requireStore
	defer func() { askSteward = oldAsk; requireStore = oldStore }()
	requireStore = func(context.Context) (*store.Store, error) { return nil, nil }
	var prompts []string
	askSteward = func(_ context.Context, message string, ops int, timeout time.Duration) (pilot.StewardResult, error) {
		prompts = append(prompts, message)
		if ops != 2 || timeout != time.Minute {
			t.Fatal(ops, timeout)
		}
		return pilot.StewardResult{Answer: "answer"}, nil
	}
	for _, mode := range []string{"ask", "wake", "chat"} {
		prompts = nil
		cmd := NewPilotCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		args := []string{mode, "--ops", "2", "--timeout", "1"}
		if mode == "ask" {
			args = append(args, "question")
		}
		if mode == "wake" {
			args = append(args, "old-project")
		}
		cmd.SetArgs(args)
		cmd.SetIn(strings.NewReader("first\nsecond\n/exit\n"))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "answer") {
			t.Fatal(out.String())
		}
		if mode == "wake" && prompts[0] != pilot.DefaultPrompt {
			t.Fatal(prompts)
		}
		if mode == "chat" && (len(prompts) != 2 || !strings.Contains(prompts[1], "first") || !strings.Contains(prompts[1], "answer")) {
			t.Fatal(prompts)
		}
	}
}
