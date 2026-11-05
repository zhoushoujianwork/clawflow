//go:build !windows

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordination"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type processE2EIssue struct {
	Number int
	Title  string
	Body   string
	Labels []string
}

type processE2EVCS struct {
	mu       sync.Mutex
	bare     string
	issues   map[int]*processE2EIssue
	prs      map[int]map[string]any
	comments map[int][]string
	creates  map[int]int
}

func newProcessE2EVCS() *processE2EVCS {
	return &processE2EVCS{issues: map[int]*processE2EIssue{}, prs: map[int]map[string]any{}, comments: map[int][]string{}, creates: map[int]int{}}
}

func (f *processE2EVCS) addIssue(n int) {
	f.addIssueWithBody(n, fmt.Sprintf("E2E issue %d", n), "Make the requested change")
}

func (f *processE2EVCS) addIssueWithBody(n int, title, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues[n] = &processE2EIssue{Number: n, Title: title, Body: body}
}

func (f *processE2EVCS) issueJSON(issue *processE2EIssue) map[string]any {
	now := "2026-11-05T00:00:00Z"
	return map[string]any{"id": issue.Number, "iid": issue.Number, "title": issue.Title, "description": issue.Body, "state": "opened", "labels": issue.Labels, "created_at": now, "updated_at": now, "author": map[string]any{"username": "tester"}, "web_url": fmt.Sprintf("https://example.invalid/group/repo/-/issues/%d", issue.Number)}
}

func (f *processE2EVCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncUnmanagedPR()
	w.Header().Set("Content-Type", "application/json")
	write := func(status int, value any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(value) }
	path := r.URL.Path
	switch {
	case path == "/api/v4/projects/group/repo":
		write(http.StatusOK, map[string]any{"id": 1})
	case strings.HasSuffix(path, "/repository/branches/main"):
		write(http.StatusOK, map[string]any{"commit": map[string]any{"id": strings.Repeat("a", 40)}})
	case strings.HasSuffix(path, "/labels"):
		if r.Method == http.MethodGet {
			write(http.StatusOK, []any{})
		} else {
			write(http.StatusCreated, map[string]any{"name": "label"})
		}
	case strings.Contains(path, "/merge_requests"):
		f.serveMR(r, write)
	case strings.Contains(path, "/issues"):
		f.serveIssue(r, write)
	case strings.Contains(path, "/members/"):
		write(http.StatusOK, map[string]any{"access_level": 40})
	case r.Method == http.MethodGet:
		write(http.StatusOK, []any{})
	default:
		write(http.StatusOK, map[string]any{})
	}
}

func (f *processE2EVCS) syncUnmanagedPR() {
	if f.bare == "" || f.prs[2] != nil {
		return
	}
	branch := "unmanaged/issue-2"
	out, err := exec.Command("git", "--git-dir", f.bare, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return
	}
	head := strings.TrimSpace(string(out))
	f.prs[2] = map[string]any{"iid": 2, "title": "unmanaged e2e 2", "description": "Fixes #2", "state": "opened", "web_url": "https://example.invalid/group/repo/-/merge_requests/2", "source_branch": branch, "target_branch": "main", "source_project_id": 1, "target_project_id": 1, "sha": head, "diff_refs": map[string]any{"base_sha": strings.Repeat("a", 40)}}
	f.creates[2] = 1
}

func pathPart(path, name string) (parts []string, index int) {
	parts = strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == name {
			return parts, i
		}
	}
	return parts, -1
}

func (f *processE2EVCS) serveIssue(r *http.Request, write func(int, any)) {
	parts, at := pathPart(r.URL.Path, "issues")
	if at < 0 || at+1 == len(parts) {
		numbers := make([]int, 0, len(f.issues))
		for n := range f.issues {
			numbers = append(numbers, n)
		}
		sort.Ints(numbers)
		out := make([]any, 0, len(numbers))
		for _, n := range numbers {
			out = append(out, f.issueJSON(f.issues[n]))
		}
		write(http.StatusOK, out)
		return
	}
	n, _ := strconv.Atoi(parts[at+1])
	issue := f.issues[n]
	if issue == nil {
		write(http.StatusNotFound, map[string]any{"message": "missing"})
		return
	}
	if at+2 < len(parts) && parts[at+2] == "notes" {
		if r.Method == http.MethodGet {
			out := make([]any, 0, len(f.comments[n]))
			for i, body := range f.comments[n] {
				out = append(out, map[string]any{"id": i + 1, "body": body, "author": map[string]any{"username": "clawflow"}})
			}
			write(http.StatusOK, out)
			return
		}
		_ = r.ParseForm()
		f.comments[n] = append(f.comments[n], r.Form.Get("body"))
		write(http.StatusCreated, map[string]any{"id": len(f.comments[n]), "body": r.Form.Get("body")})
		return
	}
	if at+2 < len(parts) {
		write(http.StatusOK, []any{})
		return
	}
	if r.Method == http.MethodPut {
		_ = r.ParseForm()
		if labels, ok := r.Form["labels"]; ok {
			issue.Labels = nil
			if len(labels) > 0 && labels[0] != "" {
				issue.Labels = strings.Split(labels[0], ",")
			}
		}
	}
	write(http.StatusOK, f.issueJSON(issue))
}

func (f *processE2EVCS) serveMR(r *http.Request, write func(int, any)) {
	parts, at := pathPart(r.URL.Path, "merge_requests")
	if at < 0 || at+1 == len(parts) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			n := len(f.prs) + 1
			branch := r.Form.Get("source_branch")
			head := strings.Repeat(strconv.Itoa(n), 40)[:40]
			if out, err := exec.Command("git", "--git-dir", f.bare, "rev-parse", "refs/heads/"+branch).Output(); err == nil {
				head = strings.TrimSpace(string(out))
			}
			pr := map[string]any{"iid": n, "title": r.Form.Get("title"), "description": r.Form.Get("description"), "state": "opened", "web_url": fmt.Sprintf("https://example.invalid/group/repo/-/merge_requests/%d", n), "source_branch": branch, "target_branch": r.Form.Get("target_branch"), "source_project_id": 1, "target_project_id": 1, "sha": head, "diff_refs": map[string]any{"base_sha": strings.Repeat("a", 40)}}
			f.prs[n], f.creates[n] = pr, f.creates[n]+1
			write(http.StatusCreated, pr)
			return
		}
		out := make([]any, 0, len(f.prs))
		for n := 1; n <= len(f.prs); n++ {
			out = append(out, f.prs[n])
		}
		write(http.StatusOK, out)
		return
	}
	n, _ := strconv.Atoi(parts[at+1])
	if at+2 < len(parts) && parts[at+2] == "notes" {
		write(http.StatusOK, []any{})
		return
	}
	if pr := f.prs[n]; pr != nil {
		write(http.StatusOK, pr)
	} else {
		write(http.StatusNotFound, map[string]any{"message": "missing"})
	}
}

type processE2EProc struct {
	cmd  *exec.Cmd
	log  string
	done chan error
}

func startProcessE2E(t *testing.T, root, binary string, env []string, args ...string) *processE2EProc {
	t.Helper()
	logPath := filepath.Join(root, args[0]+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	p := &processE2EProc{cmd: cmd, log: logPath, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait(); _ = log.Close() }()
	return p
}

func (p *processE2EProc) kill(sig syscall.Signal) {
	if p != nil && p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, sig)
	}
}

func (p *processE2EProc) stop() {
	if p == nil {
		return
	}
	p.kill(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		p.kill(syscall.SIGKILL)
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func stopProcessE2EChildren(root string) {
	out, _ := exec.Command("ps", "-axo", "pid=,command=").Output()
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(strings.Join(fields[1:], " "), root) {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil && pid != os.Getpid() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func processE2EWait(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func processE2ERun(t *testing.T, binary string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", binary, args, err, out)
	}
	return string(out)
}

func processE2EDiagnostics(root string) string {
	var b strings.Builder
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if name != "events.ndjson" && name != "result.json" && name != "journal.json" && !strings.HasSuffix(name, ".stderr") {
			return nil
		}
		data, _ := os.ReadFile(path)
		if len(data) > 8<<10 {
			data = data[len(data)-(8<<10):]
		}
		fmt.Fprintf(&b, "\n%s:\n%s", strings.TrimPrefix(path, root), data)
		return nil
	})
	return b.String()
}

func processE2EGit(t *testing.T, root string) (string, string) {
	t.Helper()
	bare, local := filepath.Join(root, "remote.git"), filepath.Join(root, "repo")
	for _, spec := range [][]string{{"git", "init", "--bare", bare}, {"git", "clone", bare, local}} {
		if out, err := exec.Command(spec[0], spec[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", spec, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"-c", "user.name=E2E", "-c", "user.email=e2e@example.invalid", "commit", "-m", "chore: seed"}, {"push", "-u", "origin", "HEAD:main"}} {
		if out, err := exec.Command("git", append([]string{"-C", local}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "-C", bare, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("symbolic-ref: %v: %s", err, out)
	}
	return local, bare
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func copyProcessE2EProfile(t *testing.T, destination, name string) {
	t.Helper()
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("locate host Offload config: %v", err)
		}
		configDir = filepath.Join(home, ".config")
	}
	source := filepath.Join(configDir, "offload", "config.json")
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read host Offload config %s: %v", source, err)
	}
	var host struct {
		Profiles map[string]json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &host); err != nil {
		t.Fatalf("parse host Offload config: %v", err)
	}
	profile, ok := host.Profiles[name]
	if !ok {
		t.Fatalf("Offload profile %q is not defined in %s", name, source)
	}
	var requirements struct {
		Backend  string `json:"backend"`
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
		Token    string `json:"token"`
		TokenCmd string `json:"token_cmd"`
	}
	if err := json.Unmarshal(profile, &requirements); err != nil {
		t.Fatalf("parse selected Offload profile: %v", err)
	}
	if requirements.Backend != "" && requirements.Backend != "claude" {
		t.Fatalf("Offload profile %q must use the Claude backend", name)
	}
	if requirements.BaseURL == "" || requirements.Model == "" || (requirements.Token == "" && requirements.TokenCmd == "") {
		t.Fatalf("Offload profile %q is not self-contained for an isolated Claude run", name)
	}
	isolated, err := json.Marshal(struct {
		Default  string                     `json:"default"`
		Profiles map[string]json.RawMessage `json:"profiles"`
	}{Default: name, Profiles: map[string]json.RawMessage{name: profile}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(destination, "offload")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), isolated, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeProcessE2EBudgetedClaude(t *testing.T, root string, budgetUSD float64) {
	t.Helper()
	realClaude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("locate real Claude CLI: %v", err)
	}
	script := fmt.Sprintf("#!/bin/sh\nexec %s --max-budget-usd %.2f \"$@\"\n", shellQuote(realClaude), budgetUSD)
	if err := os.WriteFile(filepath.Join(root, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func seedProcessE2ECoordination(t *testing.T, state string, issue vcs.Issue) {
	t.Helper()
	snapshot := coordination.Normalize(coordination.Snapshot{Operation: "implement", Repository: "group/repo", Issue: issue})
	record := coordination.Record{
		Snapshot:    &snapshot,
		Version:     coordination.Version,
		SnapshotID:  coordination.Digest(snapshot),
		IssueDigest: coordination.Digest([]string{issue.Title, issue.Body}),
		ObservedAt:  time.Now().UTC(),
		Decision: coordination.Decision{
			Outcome:    "proceed",
			Reason:     "The isolated issue has no competing work or prerequisites",
			NextAction: "Dispatch the requested implementation",
			NextOwner:  "implementation",
		},
	}
	if err := coordination.Validate(snapshot, record.Decision); err != nil {
		t.Fatalf("seed coordination decision: %v", err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "data", "coordination", coordination.Digest("group/repo"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeProcessE2EClaude(t *testing.T, root, clawflow string) string {
	t.Helper()
	path := filepath.Join(root, "claude")
	script := `#!/bin/sh
exec 2>>"$CLAWFLOW_E2E_BACKEND_LOG"
prompt=$(mktemp)
trap 'rm -f "$prompt"' EXIT
cat >"$prompt"
instructions=/dev/null
previous=
task=
for arg in "$@"; do
  [ "$previous" = "--append-system-prompt-file" ] && instructions="$arg"
  previous="$arg"
  task="$arg"
done
printf '%s\n' "$task" >>"$prompt"
issue=$(sed -n 's/.*Issue: #\([0-9][0-9]*\).*/\1/p' "$prompt" | head -1)
[ -n "$issue" ] || issue=$(sed -n 's/.*#\([0-9][0-9]*\).*/\1/p' "$prompt" | head -1)
[ -n "$issue" ] || issue=1
if grep -q 'proceed|waiting|needs-confirmation' "$prompt" "$instructions" 2>/dev/null || [ "$task" = "return proceed|waiting|needs-confirmation" ]; then
  report='{"outcome":"proceed","reason":"No competing work","next_action":"Dispatch implementation","next_owner":"implementation"}'
else
  echo "$issue" >>"$CLAWFLOW_E2E_INVOCATIONS"
  if [ "$issue" = 3 ]; then
    : >"$CLAWFLOW_E2E_STARTED"
    n=0
    while [ ! -f "$CLAWFLOW_E2E_RELEASE" ] && [ "$n" -lt 600 ]; do sleep 0.1; n=$((n+1)); done
  fi
  branch="fix/issue-$issue"
  [ "$issue" = 2 ] && branch="unmanaged/issue-2"
  git checkout -b "$branch" >/dev/null
  printf 'issue %s\n' "$issue" >> e2e.txt
  git add e2e.txt
  git -c user.name=E2E -c user.email=e2e@example.invalid commit -m "fix: e2e issue $issue" >/dev/null
  git push -u origin "HEAD:refs/heads/$branch" >/dev/null
  ` + shellQuote(clawflow) + ` pr create --repo group/repo --head "$branch" --base main --title "fix: e2e $issue" --body "Fixes #$issue" >/dev/null
  if [ "$issue" = 2 ]; then
    ` + shellQuote(clawflow) + ` issue comment --repo group/repo --issue 2 --body "failed after recording effects" >/dev/null
    ` + shellQuote(clawflow) + ` label add --repo group/repo --issue 2 --label agent-failed >/dev/null
    report='failed after recording effects'
  else
    report=$(printf 'implemented https://example.invalid/group/repo/-/merge_requests/%s\n<!-- clawflow:outcome=agent-implemented -->' "$issue")
  fi
fi
escaped=$(printf '%s' "$report" | awk 'BEGIN{ORS=""} {gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); if(NR>1)printf "\\n"; printf "%s",$0}')
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-e2e","model":"fixture"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}'
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"native-e2e","model":"fixture","result":"%s","total_cost_usd":0}\n' "$escaped"
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestE2ESQLiteControllerWorkerProcesses(t *testing.T) {
	if testing.Short() || os.Getenv("CLAWFLOW_E2E_SQLITE") != "1" {
		t.Skip("set CLAWFLOW_E2E_SQLITE=1 and omit -short")
	}
	for _, engine := range []string{"cli", "library"} {
		t.Run(engine, func(t *testing.T) { runProcessE2E(t, engine) })
	}
	t.Run("real-model", func(t *testing.T) {
		profile := strings.TrimSpace(os.Getenv("CLAWFLOW_E2E_REAL_MODEL"))
		if profile == "" {
			t.Skip("set CLAWFLOW_E2E_REAL_MODEL to an Offload profile to enable paid evidence")
		}
		for _, engine := range []string{"library", "cli"} {
			t.Run(engine, func(t *testing.T) { runProcessE2ERealModel(t, engine, profile) })
		}
	})
}

func runProcessE2ERealModel(t *testing.T, engine, profile string) {
	const budgetUSD = 1.75
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	root := t.TempDir()
	t.Cleanup(func() { stopProcessE2EChildren(root) })
	repoRootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := strings.TrimSpace(string(repoRootBytes))
	clawflow, offload := filepath.Join(root, "clawflow"), filepath.Join(root, "offload")
	for _, build := range []struct{ output, pkg string }{{clawflow, "./cmd/clawflow"}, {offload, "github.com/zhoushoujianwork/offload"}} {
		cmd := exec.CommandContext(ctx, "go", "build", "-o", build.output, build.pkg)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", build.pkg, err, out)
		}
	}

	const issueTitle = "Add hello.txt"
	const issueBody = "add a file hello.txt containing ok"
	fake := newProcessE2EVCS()
	fake.addIssueWithBody(1, issueTitle, issueBody)
	server := httptest.NewServer(fake)
	defer server.Close()
	local, bare := processE2EGit(t, root)
	fake.bare = bare
	home, state, xdg := filepath.Join(root, "home"), filepath.Join(root, "state"), filepath.Join(root, "xdg")
	configDir := filepath.Join(home, ".clawflow", "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	copyProcessE2EProfile(t, xdg, profile)
	writeProcessE2EBudgetedClaude(t, root, budgetUSD)
	configYAML := fmt.Sprintf("settings:\n  poll_interval: 1\n  agent_timeout: 480\n  max_concurrent_agents: 1\n  coordinator:\n    enabled: true\n    listen: 127.0.0.1:%d\n  execution:\n    engine: %s\n    command: %q\n    profile: %q\nrepos:\n  group/repo:\n    enabled: true\n    platform: gitlab\n    base_url: %q\n    local_path: %q\n    base_branch: main\n    allowed_issue_authors: [tester]\n", port, engine, offload, profile, server.URL, local)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "credentials.yaml"), []byte("gitlab_token: e2e-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedProcessE2ECoordination(t, state, vcs.Issue{ID: 1, Number: 1, Title: issueTitle, Body: issueBody, Author: "tester", State: "open", URL: "https://example.invalid/group/repo/-/issues/1", CreatedAt: "2026-11-05T00:00:00Z", UpdatedAt: "2026-11-05T00:00:00Z"})
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+xdg, "XDG_STATE_HOME="+filepath.Join(root, "xdg-state"), "CLAWFLOW_STATE_DIR="+state, "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "GIT_TERMINAL_PROMPT=0")

	controllerProc := startProcessE2E(t, root, clawflow, env, "controller")
	defer controllerProc.stop()
	processE2EWait(t, 15*time.Second, "real-model controller", func() bool {
		b, _ := os.ReadFile(controllerProc.log)
		return bytes.Contains(b, []byte("Controller ready"))
	})
	controllerLog, _ := os.ReadFile(controllerProc.log)
	marker := "SHA-256 fingerprint: "
	at := strings.Index(string(controllerLog), marker)
	if at < 0 {
		t.Fatalf("missing controller fingerprint: %s", controllerLog)
	}
	fingerprint := strings.TrimSpace(strings.SplitN(string(controllerLog[at+len(marker):]), "\n", 2)[0])
	token := strings.TrimSpace(strings.SplitN(processE2ERun(t, clawflow, env, "worker", "token", "create", "--name", "real-model-e2e"), "\n", 2)[0])
	tokenFile := filepath.Join(root, "worker.token")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	workerProc := startProcessE2E(t, root, clawflow, env, "worker", "--coordinator", fmt.Sprintf("https://127.0.0.1:%d", port), "--token-file", tokenFile, "--fingerprint", fingerprint, "--state-dir", filepath.Join(root, "worker-state"), "--max-parallel", "1")
	defer workerProc.stop()
	databasePath := filepath.Join(state, "data", store.FileName)
	processE2EWait(t, 15*time.Second, "real-model worker", func() bool {
		s, err := store.Open(context.Background(), databasePath)
		if err != nil {
			return false
		}
		defer s.Close()
		var n int64
		return s.DB().Model(&coordinator.Worker{}).Count(&n).Error == nil && n == 1
	})

	t.Log(processE2ERun(t, clawflow, env, "run", "--repo", "group/repo", "--issue", "1"))
	var task coordinator.Task
	processE2EWait(t, 8*time.Minute, "real-model terminal task", func() bool {
		s, err := store.Open(context.Background(), databasePath)
		if err != nil {
			return false
		}
		defer s.Close()
		err = s.DB().Where("repo = ? AND issue = ?", "group/repo", 1).Order("created_at DESC").First(&task).Error
		return err == nil && (task.State == "done" || task.State == "failed" || task.State == "cancelled")
	})

	s, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.IntegrityCheck(context.Background()); err != nil {
		_ = s.Close()
		t.Fatalf("integrity_check: %v", err)
	}
	var payload coordinatorPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		_ = s.Close()
		t.Fatalf("decode task admission: %v", err)
	}
	if payload.Admission == nil || payload.Admission.Author != "tester" || payload.Admission.Basis == "" || payload.Admission.CheckedAt.IsZero() {
		_ = s.Close()
		t.Fatalf("missing issue admission record: %#v", payload.Admission)
	}
	var runs []store.Run
	if err := s.DB().Where("repo = ? AND issue_number = ?", "group/repo", 1).Order("started_at").Find(&runs).Error; err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	if len(runs) != 1 {
		_ = s.Close()
		t.Fatalf("real-model invocation count = %d, want exactly 1 for %s", len(runs), engine)
	}
	run := runs[0]
	if !run.UsageReported || run.CostUSD == nil || run.InputTokens == nil || run.OutputTokens == nil || run.CacheReadInputTokens == nil || run.CacheCreationInputTokens == nil {
		_ = s.Close()
		t.Fatalf("real usage/cost missing from run row: usage=%t cost=%v", run.UsageReported, run.CostUSD)
	}
	if *run.CostUSD < 0 || *run.CostUSD > 2.0 {
		_ = s.Close()
		t.Fatalf("run cost %.4f exceeded the bounded evidence budget", *run.CostUSD)
	}
	var eventCount int64
	if err := s.DB().Model(&coordinator.LogEvent{}).Where("task_id = ?", task.ID).Count(&eventCount).Error; err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	if eventCount == 0 {
		_ = s.Close()
		t.Fatal("real-model execution uploaded no job log events")
	}
	_ = s.Close()

	fake.mu.Lock()
	prCount, createCount, commentCount := len(fake.prs), fake.creates[1], len(fake.comments[1])
	branch := ""
	if fake.prs[1] != nil {
		branch, _ = fake.prs[1]["source_branch"].(string)
	}
	fake.mu.Unlock()
	if prCount > 1 || createCount > 1 || commentCount > 1 {
		t.Fatalf("duplicate real-model effects: prs=%d creates=%d comments=%d", prCount, createCount, commentCount)
	}
	quality := []string{}
	if task.State != "done" {
		quality = append(quality, "task ended "+task.State)
	}
	if prCount != 1 || createCount != 1 {
		quality = append(quality, fmt.Sprintf("PR count=%d creates=%d", prCount, createCount))
	}
	if commentCount != 1 {
		quality = append(quality, fmt.Sprintf("result comments=%d", commentCount))
	}
	hello := ""
	if branch != "" {
		out, err := exec.Command("git", "--git-dir", bare, "show", "refs/heads/"+branch+":hello.txt").CombinedOutput()
		if err != nil {
			quality = append(quality, "hello.txt missing from "+branch)
		} else {
			hello = strings.TrimSpace(string(out))
			if hello != "ok" {
				quality = append(quality, fmt.Sprintf("hello.txt=%q", hello))
			}
		}
	} else {
		quality = append(quality, "no PR branch recorded")
	}
	for _, path := range []string{controllerProc.log, workerProc.log, filepath.Join(home, ".clawflow", "logs", "controller.log"), filepath.Join(home, ".clawflow", "logs", "run.log")} {
		data, _ := os.ReadFile(path)
		lower := strings.ToLower(string(data))
		if strings.Contains(lower, "sqlite_busy") || strings.Contains(lower, "database is locked") {
			t.Fatalf("SQLite contention in %s", path)
		}
	}
	totalTokens := *run.InputTokens + *run.OutputTokens + *run.CacheReadInputTokens + *run.CacheCreationInputTokens
	summary := strings.Join(strings.Fields(run.Summary), " ")
	if len(summary) > 500 {
		summary = summary[:500] + "…"
	}
	t.Logf("REAL MODEL EVIDENCE engine=%s state=%s duration=%s cost_usd=%.4f tokens=%d pr_branch=%q comments=%d log_events=%d hello=%q model_summary=%q grey=%q", engine, task.State, time.Duration(run.DurationMs)*time.Millisecond, *run.CostUSD, totalTokens, branch, commentCount, eventCount, hello, summary, quality)

	workerProc.stop()
	controllerProc.stop()
	processE2EWait(t, 5*time.Second, "real-model child cleanup", func() bool {
		out, _ := exec.Command("ps", "-axo", "command=").Output()
		return !strings.Contains(string(out), root)
	})
}

func runProcessE2E(t *testing.T, engine string) {
	ctx, cancel := context.WithTimeout(context.Background(), 175*time.Second)
	defer cancel()
	root := t.TempDir()
	t.Cleanup(func() { stopProcessE2EChildren(root) })
	repoRootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := strings.TrimSpace(string(repoRootBytes))
	clawflow, offload := filepath.Join(root, "clawflow"), filepath.Join(root, "offload")
	for _, build := range []struct{ output, pkg string }{{clawflow, "./cmd/clawflow"}, {offload, "github.com/zhoushoujianwork/offload"}} {
		cmd := exec.CommandContext(ctx, "go", "build", "-o", build.output, build.pkg)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", build.pkg, err, out)
		}
	}

	fake := newProcessE2EVCS()
	fake.addIssue(1)
	server := httptest.NewServer(fake)
	defer server.Close()
	local, bare := processE2EGit(t, root)
	fake.bare = bare
	home, state, xdg := filepath.Join(root, "home"), filepath.Join(root, "state"), filepath.Join(root, "xdg")
	configDir := filepath.Join(home, ".clawflow", "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	writeProcessE2EClaude(t, root, clawflow)
	if err := os.MkdirAll(filepath.Join(xdg, "offload"), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := fmt.Sprintf(`{"default":"e2e","profiles":{"e2e":{"backend":"claude","base_url":%q,"model":"fixture","token":"test-token"}}}`, server.URL)
	if err := os.WriteFile(filepath.Join(xdg, "offload", "config.json"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	configYAML := fmt.Sprintf("settings:\n  poll_interval: 1\n  agent_timeout: 60\n  max_concurrent_agents: 1\n  coordinator:\n    enabled: true\n    listen: 127.0.0.1:%d\n  execution:\n    engine: %s\n    command: %s\n    profile: e2e\nrepos:\n  group/repo:\n    enabled: true\n    platform: gitlab\n    base_url: %s\n    local_path: %s\n    base_branch: main\n    allowed_issue_authors: [tester]\n", port, engine, offload, server.URL, local)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "credentials.yaml"), []byte("gitlab_token: e2e-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invocations := filepath.Join(root, "invocations")
	backendLog := filepath.Join(root, "backend.log")
	started, release := filepath.Join(root, "started"), filepath.Join(root, "release")
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+xdg, "XDG_STATE_HOME="+filepath.Join(root, "xdg-state"), "CLAWFLOW_STATE_DIR="+state, "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "CLAWFLOW_E2E_INVOCATIONS="+invocations, "CLAWFLOW_E2E_STARTED="+started, "CLAWFLOW_E2E_RELEASE="+release, "CLAWFLOW_E2E_BACKEND_LOG="+backendLog, "GIT_TERMINAL_PROMPT=0")
	probe := exec.Command("sh", "-c", "command -v claude")
	probe.Env = env
	if out, err := probe.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != filepath.Join(root, "claude") {
		t.Fatalf("fake claude lookup: %v %q", err, out)
	}

	controllerProc := startProcessE2E(t, root, clawflow, env, "controller")
	defer func() { controllerProc.stop() }()
	processE2EWait(t, 15*time.Second, "controller", func() bool {
		b, _ := os.ReadFile(controllerProc.log)
		return bytes.Contains(b, []byte("Controller ready"))
	})
	controllerLog, _ := os.ReadFile(controllerProc.log)
	marker := "SHA-256 fingerprint: "
	at := strings.Index(string(controllerLog), marker)
	if at < 0 {
		t.Fatalf("missing controller fingerprint: %s", controllerLog)
	}
	fingerprint := strings.TrimSpace(strings.SplitN(string(controllerLog[at+len(marker):]), "\n", 2)[0])
	token := strings.TrimSpace(strings.SplitN(processE2ERun(t, clawflow, env, "worker", "token", "create", "--name", "e2e"), "\n", 2)[0])
	tokenFile := filepath.Join(root, "worker.token")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	workerProc := startProcessE2E(t, root, clawflow, env, "worker", "--coordinator", fmt.Sprintf("https://127.0.0.1:%d", port), "--token-file", tokenFile, "--fingerprint", fingerprint, "--state-dir", filepath.Join(root, "worker-state"), "--max-parallel", "1")
	defer workerProc.stop()
	processE2EWait(t, 15*time.Second, "worker", func() bool {
		s, err := store.Open(context.Background(), filepath.Join(state, "data", store.FileName))
		if err != nil {
			return false
		}
		defer s.Close()
		var n int64
		return s.DB().Model(&coordinator.Worker{}).Count(&n).Error == nil && n == 1
	})
	runScan := func(issue int) {
		t.Log(processE2ERun(t, clawflow, env, "run", "--repo", "group/repo", "--issue", strconv.Itoa(issue)))
	}
	waitState := func(issue int, want string) {
		deadline := time.Now().Add(55 * time.Second)
		for time.Now().Before(deadline) {
			s, err := store.Open(context.Background(), filepath.Join(state, "data", store.FileName))
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			var task coordinator.Task
			err = s.DB().Where("repo = ? AND issue = ?", "group/repo", issue).Order("created_at DESC").First(&task).Error
			_ = s.Close()
			if err == nil && task.State == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		var tasks []struct {
			Issue                   int
			State, ApplicationError string
			ApplicationAttempts     int
		}
		if diagnostic, err := store.Open(context.Background(), filepath.Join(state, "data", store.FileName)); err == nil {
			_ = diagnostic.DB().Model(&coordinator.Task{}).Find(&tasks).Error
			_ = diagnostic.Close()
		}
		controllerData, _ := os.ReadFile(controllerProc.log)
		workerData, _ := os.ReadFile(workerProc.log)
		runData, _ := os.ReadFile(filepath.Join(home, ".clawflow", "logs", "run.log"))
		backendData, _ := os.ReadFile(backendLog)
		t.Fatalf("timeout waiting for issue %d state %s; tasks=%+v\ncontroller:\n%s\nworker:\n%s\nrun:\n%s\nbackend:\n%s\nartifacts:%s", issue, want, tasks, controllerData, workerData, runData, backendData, processE2EDiagnostics(root))
	}

	runScan(1)
	waitState(1, "done")
	runScan(1)
	fake.addIssue(2)
	runScan(2)
	waitState(2, "failed")
	runScan(2)
	fake.addIssue(3)
	runScan(3)
	processE2EWait(t, 40*time.Second, "blocked third executor", func() bool { _, err := os.Stat(started); return err == nil })
	controllerProc.kill(syscall.SIGKILL)
	select {
	case <-controllerProc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("controller did not die")
	}
	controllerProc = startProcessE2E(t, root, clawflow, env, "controller")
	processE2EWait(t, 15*time.Second, "restarted controller", func() bool {
		b, _ := os.ReadFile(controllerProc.log)
		return bytes.Contains(b, []byte("Controller ready"))
	})
	if err := os.WriteFile(release, []byte("continue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitState(3, "done")

	s, err := store.Open(context.Background(), filepath.Join(state, "data", store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.IntegrityCheck(context.Background()); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	var runs, streams, events int64
	for table, out := range map[string]*int64{"runs": &runs, "task_log_streams": &streams, "task_log_events": &events} {
		if err := s.DB().Table(table).Count(out).Error; err != nil {
			t.Fatal(err)
		}
	}
	if runs < 3 || streams < 3 || events == 0 {
		t.Fatalf("durable records missing: runs=%d streams=%d events=%d", runs, streams, events)
	}
	fake.mu.Lock()
	for n := 1; n <= 3; n++ {
		if fake.creates[n] != 1 || len(fake.comments[n]) != 1 {
			createCount, commentCount := fake.creates[n], len(fake.comments[n])
			creates, comments, prs := fmt.Sprint(fake.creates), fmt.Sprint(fake.comments), fmt.Sprint(fake.prs)
			fake.mu.Unlock()
			t.Fatalf("issue %d: PR creates=%d comments=%d; creates=%s comments=%s prs=%s artifacts=%s", n, createCount, commentCount, creates, comments, prs, processE2EDiagnostics(root))
		}
	}
	fake.mu.Unlock()
	invocationBytes, _ := os.ReadFile(invocations)
	counts := map[string]int{}
	for _, n := range strings.Fields(string(invocationBytes)) {
		counts[n]++
	}
	if counts["1"] != 1 || counts["2"] != 1 || counts["3"] != 1 {
		t.Fatalf("executor invocations=%v", counts)
	}
	for _, path := range []string{controllerProc.log, workerProc.log, filepath.Join(home, ".clawflow", "logs", "controller.log"), filepath.Join(home, ".clawflow", "logs", "run.log")} {
		data, _ := os.ReadFile(path)
		lower := strings.ToLower(string(data))
		if strings.Contains(lower, "sqlite_busy") || strings.Contains(lower, "database is locked") {
			t.Fatalf("SQLite contention in %s: %s", path, data)
		}
	}
	workerProc.stop()
	controllerProc.stop()
	processE2EWait(t, 5*time.Second, "child cleanup", func() bool {
		out, _ := exec.Command("ps", "-axo", "command=").Output()
		return !strings.Contains(string(out), root)
	})
}
