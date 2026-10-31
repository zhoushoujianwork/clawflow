package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/upgrade"
)

func upgradeDir() string { return filepath.Join(config.StateDir(), "upgrade") }

// upgradeHost implements upgrade.Host for the installed macOS host. Process
// management goes through the existing launchd controller service and the
// controller ownership lock; scheduling state is read from the coordinator
// database through the same store the controller uses.
type upgradeHost struct {
	svc webService
	cfg *config.Config

	mu     sync.Mutex
	dbOnce *store.Store
	hashes map[string]cachedHash
}

type cachedHash struct {
	size  int64
	mtime time.Time
	sum   string
}

func newUpgradeHost(cfg *config.Config) (*upgradeHost, error) {
	svc, err := newWebService()
	if err != nil {
		return nil, err
	}
	svc.controller = true
	return &upgradeHost{svc: svc, cfg: cfg, hashes: map[string]cachedHash{}}, nil
}

func (h *upgradeHost) db(ctx context.Context) (*store.Store, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dbOnce != nil {
		return h.dbOnce, nil
	}
	dsn, err := store.ResolveDSN(h.cfg)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	h.dbOnce = s
	return s, nil
}

func (h *upgradeHost) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dbOnce != nil {
		_ = h.dbOnce.Close()
		h.dbOnce = nil
	}
}

func (h *upgradeHost) Preserve(_ context.Context, a upgrade.Artifact) (upgrade.Artifact, error) {
	return upgrade.PreserveFile(filepath.Join(upgradeDir(), "artifacts"), a)
}

// Describe asks the artifact itself what it is. A binary that predates the
// manifest command is reported as inferred; the engine then trusts it only up
// to the schema level of the database it is demonstrably running against.
func (h *upgradeHost) Describe(ctx context.Context, a upgrade.Artifact) (upgrade.Manifest, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, a.Path, "upgrade", "manifest").Output()
	if err != nil {
		proto, perr := exec.CommandContext(cctx, a.Path, "worker", "protocol").Output()
		if perr != nil {
			return upgrade.Manifest{}, fmt.Errorf("%s reports no manifest and no worker protocol", filepath.Base(a.Path))
		}
		n, _ := strconv.Atoi(strings.TrimSpace(string(proto)))
		return upgrade.Manifest{Version: "unknown", Commit: a.Commit, WorkerProtocol: n, Inferred: true}, nil
	}
	var m upgrade.Manifest
	if err := json.Unmarshal(out, &m); err != nil {
		return m, fmt.Errorf("manifest of %s is not valid: %w", filepath.Base(a.Path), err)
	}
	return m, nil
}

func (h *upgradeHost) Applied(ctx context.Context) ([]int, error) {
	s, err := h.db(ctx)
	if err != nil {
		return nil, err
	}
	return s.AppliedVersions(ctx)
}

// MigrateExpand runs the target's own migrations. Every migration is
// transactional, and the engine has already refused contract migrations.
func (h *upgradeHost) MigrateExpand(ctx context.Context, target upgrade.Artifact) ([]int, error) {
	before, err := h.Applied(ctx)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, target.Path, "db", "migrate")
	cmd.Stdout, cmd.Stderr = &buf, &buf
	runErr := cmd.Run()
	after, err := h.Applied(ctx)
	if err != nil {
		return nil, err
	}
	had := map[int]bool{}
	for _, v := range before {
		had[v] = true
	}
	var done []int
	for _, v := range after {
		if !had[v] {
			done = append(done, v)
		}
	}
	if runErr != nil {
		return done, fmt.Errorf("db migrate: %w: %s", runErr, upgrade.Redact(lastLine(buf.String())))
	}
	return done, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func (h *upgradeHost) Snapshot(ctx context.Context, watch []string) (coordinator.UpgradeSnapshot, error) {
	s, err := h.db(ctx)
	if err != nil {
		return coordinator.UpgradeSnapshot{}, err
	}
	return coordinator.New(s.DB()).UpgradeSnapshot(ctx, watch)
}

func (h *upgradeHost) SetPaused(ctx context.Context, worker string, paused bool) error {
	s, err := h.db(ctx)
	if err != nil {
		return err
	}
	return coordinator.New(s.DB()).SetWorkerPaused(ctx, worker, paused)
}

// apiGet requests a controller path over the pinned self-signed certificate.
// A 200 proves an authenticated session; 401/403 still proves the HTTPS server
// is up, which is all a health check without an admin token can establish.
func (h *upgradeHost) apiGet(ctx context.Context, path string) error {
	_, port, err := net.SplitHostPort(h.cfg.Settings.Coordinator.ListenAddress())
	if err != nil {
		return err
	}
	_, pin, err := coordinator.LoadCertificate(filepath.Join(snapshot.DataDir(), "coordinator"))
	if err != nil {
		return err
	}
	want, err := hex.DecodeString(strings.ReplaceAll(pin, ":", ""))
	if err != nil {
		return fmt.Errorf("coordinator fingerprint is unreadable")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if !bytes.Equal(sum[:], want) {
				return fmt.Errorf("certificate does not match the pinned coordinator")
			}
			return nil
		}}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://127.0.0.1:"+port+path, nil)
	if err != nil {
		return err
	}
	if p := h.cfg.Settings.Platform; p.TokenFile != "" {
		if raw, err := os.ReadFile(p.TokenFile); err == nil {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(raw)))
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("controller API unreachable")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case 200, 401, 403:
		return nil
	}
	return fmt.Errorf("controller API returned HTTP %d", resp.StatusCode)
}

func (h *upgradeHost) Probe(ctx context.Context) error { return h.apiGet(ctx, "/admin/v1/status") }

// ViewerProbe fetches the endpoint the native app reads. The app itself is not
// contacted, restarted or observed.
func (h *upgradeHost) ViewerProbe(ctx context.Context) error {
	return h.apiGet(ctx, "/admin/v1/dashboard")
}

func (h *upgradeHost) fileSHA(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	h.mu.Lock()
	c, ok := h.hashes[path]
	h.mu.Unlock()
	if ok && c.size == info.Size() && c.mtime.Equal(info.ModTime()) {
		return c.sum
	}
	sum, err := upgrade.FileSHA(path)
	if err != nil {
		return ""
	}
	h.mu.Lock()
	h.hashes[path] = cachedHash{info.Size(), info.ModTime(), sum}
	h.mu.Unlock()
	return sum
}

func (h *upgradeHost) Observe(context.Context) (upgrade.Observation, error) {
	st, err := h.svc.status()
	if err != nil {
		return upgrade.Observation{}, err
	}
	obs := upgrade.Observation{LockHeld: filelock.Held(filepath.Join(config.StateDir(), "locks", "controller.lock"))}
	if !st.Loaded || !st.Running || st.PID <= 0 {
		return obs, nil
	}
	obs.Running = true
	out, err := exec.Command("ps", "-p", strconv.Itoa(st.PID), "-o", "comm=").Output()
	if err != nil {
		return obs, nil // identity stays unknown; the engine treats that as ambiguous
	}
	path := strings.TrimSpace(string(out))
	if filepath.IsAbs(path) {
		obs.RunningPath, obs.RunningSHA = path, h.fileSHA(path)
	}
	return obs, nil
}

// StopController unloads the service so launchd cannot restart the old binary,
// then waits for the process to exit and the ownership lock to be released.
func (h *upgradeHost) StopController(ctx context.Context) error {
	st, err := h.svc.status()
	if err != nil {
		return err
	}
	if st.Loaded {
		if _, err := h.svc.call("launchctl", "bootout", h.svc.target()); err != nil {
			return err
		}
	}
	lockPath := filepath.Join(config.StateDir(), "locks", "controller.lock")
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := h.svc.status()
		if err == nil && !cur.Loaded && !filelock.Held(lockPath) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("controller did not release ownership within 90s")
}

func (h *upgradeHost) Fence(context.Context) (func(), error) {
	lock, err := controllerLock()
	if err != nil {
		return nil, err
	}
	return func() { filelock.Release(lock) }, nil
}

// Install rewrites the LaunchAgent so it runs the immutable artifact. It does
// not load the service; StartController does.
func (h *upgradeHost) Install(_ context.Context, a upgrade.Artifact) error {
	svc := h.svc
	svc.binary = a.Path
	svc.path = webServicePATH(os.Getenv("PATH"), h.cfg.Settings.Execution.Command, exec.LookPath)
	data, err := svc.plist("127.0.0.1", 8090)
	if err != nil {
		return err
	}
	dest := svc.plistPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".controller-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := svc.call("plutil", "-lint", tmp.Name()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

func (h *upgradeHost) StartController(context.Context) error {
	_, err := h.svc.call("launchctl", "bootstrap", h.svc.domain, h.svc.plistPath())
	return err
}

// RollWorker starts a new manager generation with the target. Running task
// executors keep the binary they were started from.
func (h *upgradeHost) RollWorker(ctx context.Context, descriptor string, a upgrade.Artifact) error {
	out, err := exec.CommandContext(ctx, a.Path, "worker", "roll", "--descriptor", descriptor, "--binary", a.Path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("worker roll: %w: %s", err, upgrade.Redact(lastLine(string(out))))
	}
	return nil
}

var _ upgrade.Host = (*upgradeHost)(nil)
