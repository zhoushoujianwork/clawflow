package commands

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	storepkg "github.com/zhoushoujianwork/clawflow/internal/store"
)

// TestMeasureControllerScale is deliberately opt-in: it starts a real controller
// process and drives only its authenticated HTTPS worker API with synthetic work.
// It never starts an Offload invocation or contacts a VCS provider.
func TestMeasureControllerScale(t *testing.T) {
	if os.Getenv("CLAWFLOW_MEASURE_SCALE") != "1" {
		t.Skip("set CLAWFLOW_MEASURE_SCALE=1 to run the controller scale measurement")
	}
	if os.Getenv("CLAWFLOW_SCALE_WORKER_GROUP") == "1" {
		runScaleWorkerGroup(t)
		return
	}
	duration := scaleDuration("CLAWFLOW_SCALE_DURATION", 15*time.Second)
	tiers := scaleTiers(os.Getenv("CLAWFLOW_SCALE_TIERS"))
	t.Logf("PLAN workers=%v projects=100 duration/tier=%s heartbeat=30s task_mix=70%%/100ms,25%%/1s,5%%/5s SLO claim_p95<500ms error_rate<0.1%% recovery<60s", tiers, duration)
	t.Logf("HOST os=%s arch=%s cpu=%s memory=%s", runtime.GOOS, runtime.GOARCH, scaleSysctl("machdep.cpu.brand_string"), scaleMemory())
	t.Logf("SWAP before=%s", scaleSysctl("vm.swapusage"))

	root := t.TempDir()
	bin := filepath.Join(root, "clawflow-scale")
	build := exec.Command("go", "build", "-o", bin, "../../../cmd/clawflow")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build controller: %v: %s", err, out)
	}

	var previousFailed bool
	for _, workers := range tiers {
		if previousFailed {
			t.Logf("TIER workers=%d stopped_before_start reason=previous_tier_SLO_failure", workers)
			continue
		}
		result := measureScaleTier(t, root, bin, workers, duration)
		t.Logf("TIER workers=%d workload=%s observed=%s registered=%d completed=%d throughput=%.1f/s claim_p50=%s claim_p95=%s claim_p99=%s requests=%d errors=%d error_rate=%.3f%% busy=%d cpu_avg=%.1f%% rss_peak_mib=%.1f db_growth_mib=%.2f db_growth_mib_per_hour=%.1f audit_growth_mib_per_hour=%.1f lease_churn=%d isolation_failures=%d noisy_share=%.1f%% regular_share=%.1f%% noisy_max_leased=%d worker_loss_recovery=%s worker_recovered=%t restart_recovery=%s duplicates=%d",
			workers, duration, result.observed, result.registered, result.completed, result.throughput, result.p50, result.p95, result.p99, result.requests, result.errors,
			100*float64(result.errors)/float64(max64(1, result.requests)), result.busy, result.cpu, float64(result.rssPeak)/(1024*1024),
			float64(result.dbGrowth)/(1024*1024), result.dbBytesHour/(1024*1024), result.auditBytesHour/(1024*1024), result.leaseChurn, result.isolationFailures,
			result.noisyShare, 100-result.noisyShare, result.noisyMax, result.workerRecovery, result.workerRecovered, result.restartRecovery, result.duplicates)
		previousFailed = result.p95 >= 500*time.Millisecond || float64(result.errors)/float64(max64(1, result.requests)) >= .001
	}
	t.Logf("SWAP after=%s", scaleSysctl("vm.swapusage"))
}

type scaleResult struct {
	registered, completed, requests, errors, busy, leaseChurn, isolationFailures, duplicates int64
	p50, p95, p99, restartRecovery, observed                                                 time.Duration
	throughput, cpu, noisyShare, auditBytesHour, dbBytesHour                                 float64
	rssPeak, dbGrowth                                                                        int64
	workerRecovery                                                                           time.Duration
	workerRecovered                                                                          bool
	noisyMax                                                                                 int64
}

type scaleController struct {
	cmd         *exec.Cmd
	output      *scaleBuffer
	base        string
	home, state string
}

func measureScaleTier(t *testing.T, root, bin string, workers int, duration time.Duration) scaleResult {
	t.Helper()
	tierRoot := filepath.Join(root, fmt.Sprintf("tier-%d", workers))
	home, state := filepath.Join(tierRoot, "home"), filepath.Join(tierRoot, "state")
	if err := os.MkdirAll(filepath.Join(home, ".clawflow", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	port := scaleFreePort(t)
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "settings:\n  max_concurrent_agents: %d\n  coordinator:\n    enabled: true\n    listen: 127.0.0.1:%d\nrepos:\n", workers, port)
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&cfg, "  synthetic/project-%03d:\n    enabled: false\n    platform: gitlab\n    base_url: http://127.0.0.1:1\n    max_in_flight: 5\n", i)
	}
	configDir := filepath.Join(home, ".clawflow", "config")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(cfg.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "credentials.yaml"), []byte("gitlab_token: synthetic-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	dbPath := filepath.Join(state, "data", storepkg.FileName)
	db, err := storepkg.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, storepkg.Migrations); err != nil {
		t.Fatal(err)
	}
	cs := coordinator.New(db.DB())
	secrets := make([]string, workers)
	for i := range secrets {
		secrets[i], err = cs.CreateToken(ctx, fmt.Sprintf("scale-%05d", i))
		if err != nil {
			t.Fatalf("create token %d: %v", i, err)
		}
	}
	taskCount := workers * 2
	for i := 0; i < taskCount; i++ {
		project := 1 + (i % 99)
		if i < taskCount/2 {
			project = 0
		}
		_, err = cs.Enqueue(ctx, coordinator.Task{Kind: "implement", Repo: fmt.Sprintf("synthetic/project-%03d", project), Issue: i + 1, Operator: "implement", Payload: json.RawMessage(`{"kind":"implement","operator":"implement","timeout_sec":3600}`)})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	var auditBefore int64
	db.DB().Raw("SELECT COALESCE(SUM(length(worker)+length(task)+length(action)+length(CAST(created_at AS TEXT))+8),0) FROM coordinator_audit").Scan(&auditBefore)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := scaleDBSize(dbPath)
	tierStarted := time.Now()
	c := startScaleController(t, bin, home, state, port)
	defer c.stop()
	client := scaleHTTPClient()

	var result scaleResult
	var dispatchMu sync.Mutex
	dispatchTotal, dispatchNoisy := int64(0), int64(0)
	activeByRepo := map[string]int64{}
	stopSample := make(chan struct{})
	var sampleWG sync.WaitGroup
	sampleWG.Add(1)
	go func() {
		defer sampleWG.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		var cpuTotal float64
		var samples int64
		for {
			select {
			case <-stopSample:
				if samples > 0 {
					result.cpu = cpuTotal / float64(samples)
				}
				return
			case <-tick.C:
				cpu, rss := scaleProcessUsage(c.cmd.Process.Pid)
				cpuTotal += cpu
				samples++
				if rss > result.rssPeak {
					result.rssPeak = rss
				}
			}
		}
	}()

	var killedTasks []string
	var killedAt time.Time
	if workers == 100 {
		ready := filepath.Join(tierRoot, "worker-group-ready.json")
		group := exec.Command(os.Args[0], "-test.run=^TestMeasureControllerScale$")
		group.Env = append(os.Environ(), "CLAWFLOW_MEASURE_SCALE=1", "CLAWFLOW_SCALE_WORKER_GROUP=1", "CLAWFLOW_SCALE_BASE="+c.base, "CLAWFLOW_SCALE_SECRETS="+strings.Join(secrets[:5], ","), "CLAWFLOW_SCALE_READY="+ready)
		group.Stdout, group.Stderr = c.output, c.output
		if err := group.Start(); err != nil {
			t.Fatal(err)
		}
		waitUntil := time.Now().Add(20 * time.Second)
		for time.Now().Before(waitUntil) {
			if raw, err := os.ReadFile(ready); err == nil && json.Unmarshal(raw, &killedTasks) == nil && len(killedTasks) == 5 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if len(killedTasks) != 5 {
			_ = group.Process.Kill()
			_ = group.Wait()
			t.Fatalf("worker group did not claim five tasks: %s", c.output.String())
		}
		dispatchTotal, dispatchNoisy = 5, 5
		activeByRepo["synthetic/project-000"] = 5
		result.noisyMax = 5
		// A non-owner must not be able to mutate another worker's lease.
		if st, _, _ := scalePost(client, c.base, secrets[5], "tasks/"+killedTasks[0]+"/heartbeat", map[string]any{"fencing_token": 1}); st == 200 {
			result.isolationFailures++
		}
		killedAt = time.Now()
		_ = group.Process.Signal(syscall.SIGKILL)
		_ = group.Wait()
	}
	var latMu sync.Mutex
	var claims []time.Duration
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			secret := secrets[i]
			if _, _, err := scalePost(client, c.base, secret, "register", map[string]any{"roles": []string{"operator"}, "max_parallel": 1, "run_untagged": true}); err != nil {
				atomic.AddInt64(&result.errors, 1)
				atomic.AddInt64(&result.requests, 1)
				return
			}
			atomic.AddInt64(&result.registered, 1)
			atomic.AddInt64(&result.requests, 1)
			for time.Now().Before(deadline) {
				started := time.Now()
				status, raw, err := scalePost(client, c.base, secret, "claim", map[string]any{})
				atomic.AddInt64(&result.requests, 1)
				if err != nil || status != 200 {
					atomic.AddInt64(&result.errors, 1)
					continue
				}
				var claim struct {
					Task *coordinator.Task `json:"task"`
				}
				if json.Unmarshal(raw, &claim) != nil || claim.Task == nil {
					time.Sleep(25 * time.Millisecond)
					continue
				}
				latMu.Lock()
				claims = append(claims, time.Since(started))
				latMu.Unlock()
				dispatchMu.Lock()
				dispatchTotal++
				if claim.Task.Repo == "synthetic/project-000" {
					dispatchNoisy++
				}
				activeByRepo[claim.Task.Repo]++
				if claim.Task.Repo == "synthetic/project-000" && activeByRepo[claim.Task.Repo] > result.noisyMax {
					result.noisyMax = activeByRepo[claim.Task.Repo]
				}
				dispatchMu.Unlock()
				d := 100 * time.Millisecond
				if i%20 == 0 {
					d = 5 * time.Second
				} else if i%4 == 0 {
					d = time.Second
				}
				time.Sleep(d)
				for _, action := range []string{"heartbeat", "progress"} {
					body := map[string]any{"fencing_token": claim.Task.FencingToken}
					if action == "progress" {
						body["last_event_age_sec"] = 0
					}
					st, _, e := scalePost(client, c.base, secret, "tasks/"+claim.Task.ID+"/"+action, body)
					atomic.AddInt64(&result.requests, 1)
					if e != nil || st != 200 {
						atomic.AddInt64(&result.errors, 1)
					}
				}
				body := map[string]any{"fencing_token": claim.Task.FencingToken, "result": map[string]any{"status": "failed", "error": "synthetic control-plane result", "infra_kind": "synthetic"}}
				st, _, e := scalePost(client, c.base, secret, "tasks/"+claim.Task.ID+"/fail", body)
				atomic.AddInt64(&result.requests, 1)
				if e != nil || st != 200 {
					atomic.AddInt64(&result.errors, 1)
				} else {
					atomic.AddInt64(&result.completed, 1)
				}
				dispatchMu.Lock()
				activeByRepo[claim.Task.Repo]--
				dispatchMu.Unlock()
			}
		}()
	}
	// A reconnect burst uses the same authenticated identities and closes pooled connections first.
	time.AfterFunc(duration/2, func() { client.CloseIdleConnections() })
	wg.Wait()
	close(stopSample)
	sampleWG.Wait()
	result.p50, result.p95, result.p99 = scalePercentiles(claims)
	result.throughput = float64(result.completed) / duration.Seconds()
	if dispatchTotal > 0 {
		result.noisyShare = 100 * float64(dispatchNoisy) / float64(dispatchTotal)
	}
	if len(killedTasks) > 0 {
		faultDeadline := killedAt.Add(60 * time.Second)
		for time.Now().Before(faultDeadline) {
			probe, err := storepkg.Open(ctx, dbPath)
			if err == nil {
				var pending int64
				probe.DB().Raw("SELECT COUNT(*) FROM coordinator_tasks WHERE id IN ? AND state = 'pending'", killedTasks).Scan(&pending)
				_ = probe.Close()
				if pending == int64(len(killedTasks)) {
					result.workerRecovered = true
					break
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
		result.workerRecovery = time.Since(killedAt)
	}

	// Restart the real controller while a burst of workers reconnects.
	reconnectStop := make(chan struct{})
	var reconnectWG sync.WaitGroup
	reconnectBase := c.base
	for i := 0; i < min(10, len(secrets)); i++ {
		reconnectWG.Add(1)
		go func(secret string) {
			defer reconnectWG.Done()
			for {
				select {
				case <-reconnectStop:
					return
				default:
					_, _, _ = scalePost(client, reconnectBase, secret, "register", map[string]any{"roles": []string{"operator"}, "max_parallel": 1, "run_untagged": true})
				}
			}
		}(secrets[i])
	}
	loadOutput := c.output.String()
	restartStart := time.Now()
	c.kill()
	c = startScaleController(t, bin, home, state, port)
	defer c.stop()
	for {
		st, _, e := scalePost(client, c.base, secrets[0], "register", map[string]any{"roles": []string{"operator"}, "max_parallel": 1, "run_untagged": true})
		if e == nil && st == 200 {
			break
		}
		if time.Since(restartStart) > 60*time.Second {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	result.restartRecovery = time.Since(restartStart)
	close(reconnectStop)
	reconnectWG.Wait()

	measureDB, err := storepkg.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer measureDB.Close()
	measureDB.DB().Raw("SELECT COUNT(*) FROM coordinator_audit WHERE action IN ('claim','expire')").Scan(&result.leaseChurn)
	measureDB.DB().Raw("SELECT COUNT(*) FROM coordinator_task_attempts WHERE attempt_number > 1").Scan(&result.duplicates)
	result.dbGrowth = scaleDBSize(dbPath) - before
	var auditAfter int64
	measureDB.DB().Raw("SELECT COALESCE(SUM(length(worker)+length(task)+length(action)+length(CAST(created_at AS TEXT))+8),0) FROM coordinator_audit").Scan(&auditAfter)
	result.observed = time.Since(tierStarted)
	result.auditBytesHour = float64(max64(0, auditAfter-auditBefore)) * 3600 / result.observed.Seconds()
	result.dbBytesHour = float64(max64(0, result.dbGrowth)) * 3600 / result.observed.Seconds()
	allOutput := loadOutput + c.output.String()
	result.busy = int64(strings.Count(allOutput, "SQLITE_BUSY") + strings.Count(allOutput, "database is locked"))
	return result
}

func runScaleWorkerGroup(t *testing.T) {
	base, ready := os.Getenv("CLAWFLOW_SCALE_BASE"), os.Getenv("CLAWFLOW_SCALE_READY")
	secrets := strings.Split(os.Getenv("CLAWFLOW_SCALE_SECRETS"), ",")
	client := scaleHTTPClient()
	ids := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if st, _, err := scalePost(client, base, secret, "register", map[string]any{"roles": []string{"operator"}, "max_parallel": 1, "run_untagged": true}); err != nil || st != 200 {
			t.Fatalf("register worker group: status=%d err=%v", st, err)
		}
		st, raw, err := scalePost(client, base, secret, "claim", map[string]any{})
		var claimed struct {
			Task *coordinator.Task `json:"task"`
		}
		if err != nil || st != 200 || json.Unmarshal(raw, &claimed) != nil || claimed.Task == nil {
			t.Fatalf("claim worker group: status=%d err=%v body=%s", st, err, raw)
		}
		ids = append(ids, claimed.Task.ID)
	}
	raw, _ := json.Marshal(ids)
	if err := os.WriteFile(ready, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func startScaleController(t *testing.T, bin, home, state string, port int) *scaleController {
	t.Helper()
	out := &scaleBuffer{}
	cmd := exec.Command(bin, "controller")
	cmd.Env = append(os.Environ(), "HOME="+home, "CLAWFLOW_STATE_DIR="+state, "GITLAB_TOKEN=synthetic-only")
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &scaleController{cmd: cmd, output: out, base: fmt.Sprintf("https://127.0.0.1:%d", port), home: home, state: state}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return c
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.kill()
	t.Fatalf("controller did not start: %s", out.String())
	return nil
}

func (c *scaleController) kill() {
	if c != nil && c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Signal(syscall.SIGKILL)
		_ = c.cmd.Wait()
	}
}
func (c *scaleController) stop() {
	if c != nil && c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
		_ = c.cmd.Wait()
	}
}

func scaleHTTPClient() *http.Client {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, MaxIdleConns: 20000, MaxIdleConnsPerHost: 20000, IdleConnTimeout: 60 * time.Second}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second}
}

func scalePost(client *http.Client, base, secret, path string, body any) (int, []byte, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, base+"/worker/v1/"+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, err
}

type scaleBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *scaleBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *scaleBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func scalePercentiles(v []time.Duration) (time.Duration, time.Duration, time.Duration) {
	if len(v) == 0 {
		return 0, 0, 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	at := func(p float64) time.Duration { return v[int(float64(len(v)-1)*p)] }
	return at(.50), at(.95), at(.99)
}
func scaleFreePort(t *testing.T) int {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func scaleDuration(k string, d time.Duration) time.Duration {
	if v, e := time.ParseDuration(os.Getenv(k)); e == nil && v > 0 {
		return v
	}
	return d
}
func scaleTiers(raw string) []int {
	if raw == "" {
		return []int{100, 1000, 10000}
	}
	var out []int
	for _, s := range strings.Split(raw, ",") {
		if n, e := strconv.Atoi(strings.TrimSpace(s)); e == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}
func scaleDBSize(path string) int64 {
	var n int64
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if st, e := os.Stat(p); e == nil {
			n += st.Size()
		}
	}
	return n
}
func scaleSysctl(k string) string {
	out, e := exec.Command("sysctl", "-n", k).Output()
	if e != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(out))
}
func scaleMemory() string {
	s := scaleSysctl("hw.memsize")
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil {
		return s
	}
	return fmt.Sprintf("%.0f GiB", float64(n)/(1<<30))
}
func scaleProcessUsage(pid int) (float64, int64) {
	out, e := exec.Command("ps", "-o", "%cpu=,rss=", "-p", strconv.Itoa(pid)).Output()
	if e != nil {
		return 0, 0
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return 0, 0
	}
	cpu, _ := strconv.ParseFloat(f[0], 64)
	rss, _ := strconv.ParseInt(f[1], 10, 64)
	return cpu, rss * 1024
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
