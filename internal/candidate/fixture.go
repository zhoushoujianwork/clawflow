package candidate

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FixtureRepo is the only repository the isolated task run uses. Its forge is a
// loopback fake, so nothing leaves the host and no real credential is involved.
const (
	FixtureRepo  = "group/repo"
	FixtureIssue = 1
)

// ForgeCounts is what the fake forge observed. A task whose write-back never
// reached the forge shows zero here even when the coordinator says "done".
type ForgeCounts struct {
	MergeRequests int `json:"merge_requests"`
	Comments      int `json:"comments"`
	Labels        int `json:"labels"`
}

// Forge is a minimal GitLab-compatible server: one project, issues with notes
// and labels, and merge requests whose head is read from the local bare repo.
type Forge struct {
	mu       sync.Mutex
	bare     string
	issues   map[int]map[string]any
	labels   map[int][]string
	comments map[int][]string
	prs      map[int]map[string]any
	creates  int
	ln       net.Listener
	srv      *http.Server
}

// NewForge returns a forge that resolves branch heads in the bare repository.
func NewForge(bare string) *Forge {
	return &Forge{bare: bare, issues: map[int]map[string]any{}, labels: map[int][]string{}, comments: map[int][]string{}, prs: map[int]map[string]any{}}
}

// AddIssue registers an open issue authored by a repository member.
func (f *Forge) AddIssue(n int, title, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues[n] = map[string]any{"title": title, "description": body}
}

// Counts reports the writes the forge has received.
func (f *Forge) Counts() ForgeCounts {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := ForgeCounts{MergeRequests: f.creates}
	for _, v := range f.comments {
		c.Comments += len(v)
	}
	for _, v := range f.labels {
		c.Labels += len(v)
	}
	return c
}

// Serve listens on addr (a loopback host:port) until Close.
func (f *Forge) Serve(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	f.ln = ln
	f.srv = &http.Server{Handler: f, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = f.srv.Serve(ln) }()
	return nil
}

// Close stops the listener.
func (f *Forge) Close() {
	if f.srv != nil {
		_ = f.srv.Close()
	}
}

func (f *Forge) issueJSON(n int) map[string]any {
	i := f.issues[n]
	const now = "2026-11-05T00:00:00Z"
	return map[string]any{"id": n, "iid": n, "title": i["title"], "description": i["description"], "state": "opened", "labels": f.labels[n], "created_at": now, "updated_at": now,
		"author": map[string]any{"username": "tester"}, "web_url": fmt.Sprintf("https://example.invalid/%s/-/issues/%d", FixtureRepo, n)}
}

func (f *Forge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	path := r.URL.Path
	switch {
	case path == "/api/v4/projects/group/repo" || path == "/api/v4/projects/group%2Frepo":
		write(200, map[string]any{"id": 1})
	case strings.HasSuffix(path, "/repository/branches/main"):
		write(200, map[string]any{"commit": map[string]any{"id": strings.Repeat("a", 40)}})
	case strings.HasSuffix(path, "/labels"):
		if r.Method == http.MethodGet {
			write(200, []any{})
		} else {
			write(201, map[string]any{"name": "label"})
		}
	case strings.Contains(path, "/merge_requests"):
		f.serveMR(r, write)
	case strings.Contains(path, "/issues"):
		f.serveIssue(r, write)
	case strings.Contains(path, "/members/"):
		write(200, map[string]any{"access_level": 40})
	case r.Method == http.MethodGet:
		write(200, []any{})
	default:
		write(200, map[string]any{})
	}
}

func after(path, name string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == name {
			return parts[i+1:]
		}
	}
	return nil
}

func (f *Forge) serveIssue(r *http.Request, write func(int, any)) {
	rest := after(r.URL.Path, "issues")
	if len(rest) == 0 {
		var nums []int
		for n := range f.issues {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		out := []any{}
		for _, n := range nums {
			out = append(out, f.issueJSON(n))
		}
		write(200, out)
		return
	}
	n, _ := strconv.Atoi(rest[0])
	if f.issues[n] == nil {
		write(404, map[string]any{"message": "missing"})
		return
	}
	if len(rest) > 1 && rest[1] == "notes" {
		if r.Method == http.MethodGet {
			out := []any{}
			for i, b := range f.comments[n] {
				out = append(out, map[string]any{"id": i + 1, "body": b, "author": map[string]any{"username": "clawflow"}})
			}
			write(200, out)
			return
		}
		_ = r.ParseForm()
		f.comments[n] = append(f.comments[n], r.Form.Get("body"))
		write(201, map[string]any{"id": len(f.comments[n]), "body": r.Form.Get("body")})
		return
	}
	if len(rest) > 1 {
		write(200, []any{})
		return
	}
	if r.Method == http.MethodPut {
		_ = r.ParseForm()
		if labels, ok := r.Form["labels"]; ok {
			f.labels[n] = nil
			if len(labels) > 0 && labels[0] != "" {
				f.labels[n] = strings.Split(labels[0], ",")
			}
		}
	}
	write(200, f.issueJSON(n))
}

func (f *Forge) serveMR(r *http.Request, write func(int, any)) {
	rest := after(r.URL.Path, "merge_requests")
	if len(rest) == 0 {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			n := len(f.prs) + 1
			branch := r.Form.Get("source_branch")
			head := strings.Repeat(strconv.Itoa(n), 40)[:40]
			if out, err := exec.Command("git", "--git-dir", f.bare, "rev-parse", "refs/heads/"+branch).Output(); err == nil {
				head = strings.TrimSpace(string(out))
			}
			pr := map[string]any{"iid": n, "title": r.Form.Get("title"), "description": r.Form.Get("description"), "state": "opened",
				"web_url": fmt.Sprintf("https://example.invalid/%s/-/merge_requests/%d", FixtureRepo, n), "source_branch": branch, "target_branch": r.Form.Get("target_branch"),
				"source_project_id": 1, "target_project_id": 1, "sha": head, "diff_refs": map[string]any{"base_sha": strings.Repeat("a", 40)}}
			f.prs[n] = pr
			f.creates++
			write(201, pr)
			return
		}
		out := []any{}
		for n := 1; n <= len(f.prs); n++ {
			out = append(out, f.prs[n])
		}
		write(200, out)
		return
	}
	n, _ := strconv.Atoi(rest[0])
	if len(rest) > 1 && rest[1] == "notes" {
		write(200, []any{})
		return
	}
	if pr := f.prs[n]; pr != nil {
		write(200, pr)
		return
	}
	write(404, map[string]any{"message": "missing"})
}

// SeedGit creates a bare repository with one commit on main and a clone of it at
// local, the shape a worker needs to branch, commit and push for the task.
func SeedGit(bare, local string) error {
	steps := [][]string{{"git", "init", "--bare", bare}, {"git", "clone", bare, local}}
	for _, s := range steps {
		if out, err := exec.Command(s[0], s[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w: %s", strings.Join(s, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "README.md"}, {"-c", "user.name=candidate", "-c", "user.email=candidate@example.invalid", "commit", "-m", "chore: seed"}, {"push", "-u", "origin", "HEAD:main"}} {
		if out, err := exec.Command("git", append([]string{"-C", local}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}
	if out, err := exec.Command("git", "-C", bare, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
		return fmt.Errorf("symbolic-ref: %w: %s", err, out)
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// AgentScript is a stand-in for the Claude CLI. It answers the dispatch
// coordination prompt with "proceed"; otherwise it creates the fix branch,
// pushes it, opens the merge request through the candidate binary, and reports
// the outcome marker. It exercises the real claim, execution and write-back
// path of the candidate without any model.
func AgentScript(clawflow string) string {
	return `#!/bin/sh
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
  branch="fix/issue-$issue"
  printf '%s\n' "$issue" >>"$CLAWFLOW_CANDIDATE_ROOT/logs/agent.log"
  git checkout -b "$branch" >/dev/null
  printf 'issue %s\n' "$issue" >> candidate-run.txt
  git add candidate-run.txt
  git -c user.name=candidate -c user.email=candidate@example.invalid commit -m "fix: candidate run $issue" >/dev/null
  git push -u origin "HEAD:refs/heads/$branch" >/dev/null
  ` + shellQuote(clawflow) + ` pr create --repo ` + FixtureRepo + ` --head "$branch" --base main --title "fix: candidate run $issue" --body "Fixes #$issue" >/dev/null
  report=$(printf 'implemented https://example.invalid/` + FixtureRepo + `/-/merge_requests/1\n<!-- clawflow:outcome=agent-implemented -->')
fi
escaped=$(printf '%s' "$report" | awk 'BEGIN{ORS=""} {gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); if(NR>1)printf "\\n"; printf "%s",$0}')
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-candidate","model":"fixture"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}'
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"native-candidate","model":"fixture","result":"%s","total_cost_usd":0}\n' "$escaped"
`
}

// HasLabel reports whether the forge issue carries label.
func (f *Forge) HasLabel(n int, label string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.labels[n] {
		if l == label {
			return true
		}
	}
	return false
}

// AgentLeavingBackground wraps the fixture agent so it also starts a background
// process that outlives the task, in its own session. It models a runtime
// defect (the program under test leaves a writer behind) that no unit test of
// the surrounding code can see.
func AgentLeavingBackground(clawflow string) string {
	script := AgentScript(clawflow)
	marker := "prompt=$(mktemp)\n"
	return strings.Replace(script, marker, marker+"(cd \"$CLAWFLOW_CANDIDATE_ROOT\" && exec perl -MPOSIX -e 'POSIX::setsid(); exec q(sleep), q(300)') >/dev/null 2>&1 &\n", 1)
}

// AgentSkippingWriteBack runs the work but never opens the merge request or
// reports success, as if the write-back path were broken.
func AgentSkippingWriteBack(clawflow string) string {
	script := AgentScript(clawflow)
	i := strings.Index(script, "  git push -u origin")
	j := strings.Index(script, "  report=$(printf 'implemented")
	if i < 0 || j < 0 {
		return script
	}
	return script[:i] + "  report='nothing to write back'\n" + script[strings.Index(script[j:], "\nfi\n")+j+1:]
}
