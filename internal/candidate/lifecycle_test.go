package candidate

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func buildFake(t *testing.T, dest string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", dest, "./testdata/fakecf")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake: %v\n%s", err, out)
	}
}

func pidAlive(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	return err == nil && strings.TrimSpace(string(out)) != "" && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// A candidate controller/worker pair starts, serves and stops while a stable
// listener, process, config and data stay untouched; an unrelated process
// that shares a recorded stale pid is never signalled.
func TestCandidateLifecycleIsolatedFromStableHost(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	stable := testStable(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	stable.Ports = []int{ln.Addr().(*net.TCPAddr).Port}
	bystander := exec.Command("sleep", "120")
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bystander.Process.Kill(); _ = bystander.Wait() }()
	probe := StableProbe{Processes: func() ([]string, error) { return []string{strconv.Itoa(bystander.Process.Pid)}, nil }}
	before, err := TakeStable(stable, probe)
	if err != nil {
		t.Fatal(err)
	}

	free, _ := net.Listen("tcp", "127.0.0.1:0")
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()
	p, err := NewPlan(commitA, filepath.Join(t.TempDir(), "cands"), port, Pin{Capabilities: []string{"fixture-runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Prepare(p, stable, FreePort)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(p.Bin, "clawflow")
	buildFake(t, binary)

	// The stale-pid bystander is recorded as if it were a crashed candidate worker.
	if err := m.Record("crashed-worker", bystander.Process.Pid, binary); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := m.Up(ctx, binary, "postgres://u@127.0.0.1/db?search_path="+p.Schema)
	if err != nil {
		t.Fatal(err)
	}
	if !pidAlive(res.Controller) || len(res.Workers) != 2 {
		t.Fatalf("pair not running: %+v", res)
	}
	if res.Identities["operator"] == res.Identities["acceptance"] {
		t.Fatalf("worker identities must differ: %+v", res.Identities)
	}
	dev, review := p.Lanes()
	for _, w := range []Worker{dev, review} {
		info, err := os.Stat(filepath.Join(w.StateDir, "worker.token"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("token for %s: %v", w.Name, err)
		}
	}
	if err := CheckIndependence(dev, review); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port)), time.Second)
	if err != nil {
		t.Fatalf("candidate listener: %v", err)
	}
	conn.Close()

	// Mid-run the stable host is unchanged.
	if during, _ := TakeStable(stable, probe); len(before.Diff(during)) != 0 {
		t.Fatalf("stable host changed while running: %v", before.Diff(during))
	}

	stopped, err := m.Down()
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 3 {
		t.Fatalf("stopped %v", stopped)
	}
	for _, pid := range []int{res.Controller, res.Workers["dev-"+p.ID], res.Workers["review-"+p.ID]} {
		if WaitFor(ctx, 2*time.Second, func() bool { return !pidAlive(pid) }) == false {
			t.Fatalf("pid %d still alive", pid)
		}
	}
	if !pidAlive(bystander.Process.Pid) {
		t.Fatal("an unrelated process with a recorded stale pid was stopped")
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port)), 300*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("candidate listener survived Down")
	}
	after, _ := TakeStable(stable, probe)
	if d := before.Diff(after); len(d) != 0 {
		t.Fatalf("stable host changed: %v", d)
	}
	if err := m.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stable.Root); err != nil {
		t.Fatal("stable root disturbed")
	}
}

func TestBuildBinaryKeepsPinnedCheckoutReadOnly(t *testing.T) {
	checkout := t.TempDir()
	files := map[string]string{
		"go.mod":               "module fixture\n\ngo 1.24.0\n\nrequire fixturedep v0.0.0 // indirect\n\nreplace fixturedep => ./dep\n",
		"dep/go.mod":           "module fixturedep\n\ngo 1.24.0\n",
		"dep/dep.go":           "package fixturedep\n",
		"cmd/clawflow/main.go": "package main\nimport _ \"fixturedep\"\nfunc main(){}\n",
	}
	for name, body := range files {
		path := filepath.Join(checkout, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init"}, {"add", "go.mod", "dep/go.mod", "dep/dep.go", "cmd/clawflow/main.go"}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", checkout}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	head, err := gitOutput(context.Background(), checkout, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	p := plan(t, head, 0)
	if _, err := Prepare(p, testStable(t), func(int) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildBinary(context.Background(), p, checkout); err != nil {
		t.Fatal(err)
	}
	dirty, err := gitOutput(context.Background(), checkout, "status", "--porcelain")
	if err != nil || dirty != "" {
		t.Fatalf("pinned checkout was modified by build: %q: %v", dirty, err)
	}
}
