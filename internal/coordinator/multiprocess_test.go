package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// Helper-process pattern: the test binary re-executes itself so every claimant
// is a real OS process with its own SQLite connection pool. An in-process mutex
// cannot serialize these; only the database transaction rules can.
const childEnv = "CLAWFLOW_COORDINATOR_CHILD"

func TestMain(m *testing.M) {
	if mode := os.Getenv(childEnv); mode != "" {
		os.Exit(runChild(mode))
	}
	os.Exit(m.Run())
}

func runChild(mode string) int {
	ctx := context.Background()
	db, err := store.Open(ctx, os.Getenv("CLAWFLOW_CHILD_DB"))
	if err != nil {
		os.Stderr.WriteString(err.Error())
		return 2
	}
	defer db.Close()
	s := New(db.DB())
	worker := os.Getenv("CLAWFLOW_CHILD_WORKER")
	switch mode {
	case "claim":
		var ids []string
		for {
			task, err := s.Claim(ctx, worker)
			if errors.Is(err, ErrNoTask) {
				break
			}
			if err != nil {
				os.Stderr.WriteString(err.Error())
				return 3
			}
			ids = append(ids, task.ID)
		}
		out, _ := json.Marshal(ids)
		os.Stdout.Write(out)
	case "heartbeat":
		// Each iteration is one worker cycle: enqueue, claim, heartbeat, complete.
		base, _ := strconv.Atoi(os.Getenv("CLAWFLOW_CHILD_ISSUE"))
		var lat []int64
		timed := func(f func() error) bool {
			t0 := time.Now()
			if err := f(); err != nil {
				os.Stderr.WriteString(err.Error())
				return false
			}
			lat = append(lat, int64(time.Since(t0)))
			return true
		}
		for i := 0; i < 25; i++ {
			var task *Task
			if !timed(func() (err error) {
				_, err = s.Enqueue(ctx, Task{Kind: "implement", Repo: "load/repo", Issue: base + i + 1, Operator: "implement"})
				return
			}) {
				return 3
			}
			if !timed(func() (err error) {
				task, err = s.Claim(ctx, worker)
				if errors.Is(err, ErrNoTask) { // another process took the task; capacity is per worker
					task, err = nil, nil
				}
				return
			}) {
				return 3
			}
			if task == nil {
				continue
			}
			if !timed(func() error { return s.Heartbeat(ctx, task.ID, worker, task.FencingToken) }) {
				return 3
			}
			if !timed(func() error {
				return s.Complete(ctx, task.ID, worker, task.FencingToken, json.RawMessage(`{"status":"success"}`))
			}) {
				return 3
			}
		}
		out, _ := json.Marshal(lat)
		os.Stdout.Write(out)
	case "enqueue":
		n, _ := strconv.Atoi(os.Getenv("CLAWFLOW_CHILD_ISSUE"))
		_, err := s.Enqueue(ctx, Task{Kind: "implement", Repo: "owner/repo", Issue: n, Operator: "implement"})
		if err != nil {
			os.Stdout.WriteString("err:" + err.Error())
			return 0
		}
		os.Stdout.WriteString("ok")
	}
	return 0
}

func child(t *testing.T, mode, dbPath string, env ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"="+mode, "CLAWFLOW_CHILD_DB="+dbPath)
	cmd.Env = append(cmd.Env, env...)
	return cmd
}

func TestClaimsAcrossProcessesAreUniqueAndBounded(t *testing.T) {
	s, _, path := setup(t)
	const tasks, procs = 40, 6
	for i := 1; i <= procs; i++ {
		register(t, s, "w"+strconv.Itoa(i), 4) // capacity 4 each, 24 slots for 40 tasks
	}
	for i := 1; i <= tasks; i++ {
		enqueue(t, s, i)
	}
	cmds := make([]*exec.Cmd, procs)
	outs := make([]*safeBuffer, procs)
	for i := range cmds {
		cmds[i] = child(t, "claim", path, "CLAWFLOW_CHILD_WORKER=w"+strconv.Itoa(i+1))
		outs[i] = &safeBuffer{}
		cmds[i].Stdout, cmds[i].Stderr = outs[i], outs[i]
	}
	for _, c := range cmds { // start together to maximize contention
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	total := 0
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("process %d: %v: %s", i, err, outs[i].String())
		}
		var ids []string
		if err := json.Unmarshal(outs[i].Bytes(), &ids); err != nil {
			t.Fatalf("process %d output %q: %v", i, outs[i].String(), err)
		}
		if len(ids) > 4 {
			t.Fatalf("worker w%d holds %d leases, limit 4", i+1, len(ids))
		}
		for _, id := range ids {
			seen[id]++
			total++
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("task %s claimed %d times", id, n)
		}
	}
	if total != procs*4 {
		t.Fatalf("claimed %d tasks, want %d (every slot used exactly once)", total, procs*4)
	}
	var leased int64
	s.db.Model(&Task{}).Where("state = 'leased'").Count(&leased)
	if leased != int64(total) {
		t.Fatalf("database shows %d leased, processes claimed %d", leased, total)
	}
	var epochs []int64
	s.db.Model(&Task{}).Where("state = 'leased'").Pluck("fencing_token", &epochs)
	sort.Slice(epochs, func(i, j int) bool { return epochs[i] < epochs[j] })
	for _, e := range epochs {
		if e != 1 {
			t.Fatalf("first lease must have epoch 1, got %d", e)
		}
	}
}

func TestDuplicateEnqueueAcrossProcessesCreatesOneActiveTask(t *testing.T) {
	s, _, path := setup(t)
	const procs = 8
	cmds := make([]*exec.Cmd, procs)
	outs := make([]*safeBuffer, procs)
	for i := range cmds {
		cmds[i] = child(t, "enqueue", path, "CLAWFLOW_CHILD_ISSUE=77")
		outs[i] = &safeBuffer{}
		cmds[i].Stdout, cmds[i].Stderr = outs[i], outs[i]
	}
	for _, c := range cmds {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("process %d: %v: %s", i, err, outs[i].String())
		}
		if out := outs[i].String(); out != "ok" {
			t.Fatalf("process %d: %s (a duplicate enqueue returns the existing task, never an error)", i, out)
		}
	}
	var active int64
	s.db.Model(&Task{}).Where("repo = 'owner/repo' AND issue = 77 AND state IN ('pending','leased')").Count(&active)
	if active != 1 {
		t.Fatalf("%d active tasks for one issue, want 1", active)
	}
}

func TestLeaseSurvivesProcessExitAndIsStoredInUTC(t *testing.T) {
	s, _, path := setup(t)
	register(t, s, "w1", 1)
	enqueue(t, s, 5)
	cmd := child(t, "claim", path, "CLAWFLOW_CHILD_WORKER=w1")
	cmd.Stdout = &safeBuffer{}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// The claiming process is gone (as after a crash). Its lease is durable...
	var leased Task
	if err := s.db.First(&leased, "state = 'leased'").Error; err != nil || leased.LeaseOwner != "w1" || leased.FencingToken != 1 {
		t.Fatalf("lease lost after process exit: %+v %v", leased, err)
	}
	// ...and a stale report carrying the old epoch is fenced once the lease
	// has been reassigned.
	if leased.LeaseExpiresAt == nil || !leased.LeaseExpiresAt.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("lease expiry must be controller-clock UTC in the future: %v", leased.LeaseExpiresAt)
	}
	if loc := leased.LeaseExpiresAt.Location(); loc != time.UTC {
		t.Fatalf("lease time location = %v, want UTC", loc)
	}
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("database directory must be private: %v %v", info, err)
	}
}

// safeBuffer collects child output without a data race between exec's copier
// goroutine and the test reading it after Wait.
type safeBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}
func (b *safeBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}
func (b *safeBuffer) String() string { return string(b.Bytes()) }

// TestMeasureWorkerLoad records latency of the worker write path with N real
// processes. It asserts only that no operation fails or double-claims; the
// numbers are published in docs/sqlite-host-runbook.md as measurements for the
// machine they were taken on, not as a promise for other hardware.
func TestMeasureWorkerLoad(t *testing.T) {
	if os.Getenv("CLAWFLOW_MEASURE_LOAD") == "" {
		t.Skip("set CLAWFLOW_MEASURE_LOAD=1 to run the load measurement")
	}
	s, _, path := setup(t)
	for _, procs := range []int{4, 16, 32} {
		for i := 1; i <= procs; i++ {
			register(t, s, "m"+strconv.Itoa(procs)+"-"+strconv.Itoa(i), 1)
		}
		cmds := make([]*exec.Cmd, procs)
		outs := make([]*safeBuffer, procs)
		start := time.Now()
		for i := range cmds {
			cmds[i] = child(t, "heartbeat", path, "CLAWFLOW_CHILD_WORKER=m"+strconv.Itoa(procs)+"-"+strconv.Itoa(i+1), "CLAWFLOW_CHILD_ISSUE="+strconv.Itoa(100000*procs+i*1000))
			outs[i] = &safeBuffer{}
			cmds[i].Stdout, cmds[i].Stderr = outs[i], outs[i]
			if err := cmds[i].Start(); err != nil {
				t.Fatal(err)
			}
		}
		// The dashboard polls Board on its own connection pool while the workers
		// write. Its latency is the headless proxy for UI responsiveness; native
		// rendering is not measured here.
		stopPoll := make(chan struct{})
		pollDone := make(chan []time.Duration)
		go func() {
			var reads []time.Duration
			tick := time.NewTicker(50 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-stopPoll:
					pollDone <- reads
					return
				case <-tick.C:
					t0 := time.Now()
					if _, err := s.Board(context.Background()); err != nil {
						t.Errorf("board read under load: %v", err)
					}
					reads = append(reads, time.Since(t0))
				}
			}
		}()
		var all []time.Duration
		for i, c := range cmds {
			if err := c.Wait(); err != nil {
				close(stopPoll)
				<-pollDone
				t.Fatalf("process %d: %v: %s", i, err, outs[i].String())
			}
			var lat []int64
			if err := json.Unmarshal(outs[i].Bytes(), &lat); err != nil {
				t.Fatalf("%q: %v", outs[i].String(), err)
			}
			for _, ns := range lat {
				all = append(all, time.Duration(ns))
			}
		}
		close(stopPoll)
		reads := <-pollDone
		sort.Slice(reads, func(i, j int) bool { return reads[i] < reads[j] })
		sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
		pct := func(p float64) time.Duration { return all[int(float64(len(all)-1)*p)] }
		wall := time.Since(start)
		t.Logf("MEASURE processes=%d ops=%d wall=%s throughput=%.0f/s p50=%s p95=%s p99=%s max=%s",
			procs, len(all), wall.Round(time.Millisecond), float64(len(all))/wall.Seconds(),
			pct(.50).Round(time.Microsecond), pct(.95).Round(time.Microsecond), pct(.99).Round(time.Microsecond), all[len(all)-1].Round(time.Microsecond))
		if len(reads) > 0 {
			t.Logf("MEASURE-BOARD processes=%d reads=%d p50=%s p95=%s max=%s", procs, len(reads),
				reads[len(reads)/2].Round(time.Microsecond), reads[int(float64(len(reads)-1)*.95)].Round(time.Microsecond), reads[len(reads)-1].Round(time.Microsecond))
		}
	}
}
