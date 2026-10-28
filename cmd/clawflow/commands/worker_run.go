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
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/clone"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type workerOptions struct {
	Coordinator, TokenFile, Fingerprint, StateDir string
	Tags, Roles                                   []string
	RunUntagged                                   bool
	MaxParallel                                   int
}

func configureWorker(cmd *cobra.Command) {
	o := workerOptions{}
	f := cmd.Flags()
	f.StringVar(&o.Coordinator, "coordinator", "", "Coordinator HTTPS 地址")
	f.StringVar(&o.TokenFile, "token-file", "", "Worker token 文件")
	f.StringVar(&o.Fingerprint, "fingerprint", "", "Coordinator SHA-256 指纹")
	f.StringVar(&o.StateDir, "state-dir", os.Getenv("CLAWFLOW_STATE_DIR"), "Worker 状态目录")
	f.StringSliceVar(&o.Tags, "tags", nil, "Worker 标签")
	f.StringSliceVar(&o.Roles, "roles", []string{"operator"}, "Worker 角色")
	f.BoolVar(&o.RunUntagged, "run-untagged", true, "接受无标签任务")
	f.IntVar(&o.MaxParallel, "max-parallel", 1, "最大并行任务数")
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if o.MaxParallel < 1 || len(o.Roles) != 1 || o.Roles[0] != "operator" {
			return fmt.Errorf("max-parallel 必须为正数；当前仅支持 operator 角色")
		}
		api, err := newWorkerAPI(o)
		if err != nil {
			return err
		}
		defer api.client.CloseIdleConnections()
		if o.StateDir != "" {
			dir, err := filepath.Abs(expandHomeStr(o.StateDir))
			if err != nil {
				return err
			}
			if err = os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			previous, exists := os.LookupEnv("CLAWFLOW_STATE_DIR")
			if err = os.Setenv("CLAWFLOW_STATE_DIR", dir); err != nil {
				return err
			}
			defer func() {
				if exists {
					_ = os.Setenv("CLAWFLOW_STATE_DIR", previous)
				} else {
					_ = os.Unsetenv("CLAWFLOW_STATE_DIR")
				}
			}()
		}
		reg, err := loadRegistry()
		if err != nil {
			return err
		}
		hostname, err := os.Hostname()
		if err != nil {
			return err
		}
		o.Tags = append(o.Tags, "host-"+strings.TrimSuffix(strings.ToLower(hostname), ".local"))
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runWorker(ctx, api, o, 30*time.Second, 5*time.Second, func(ctx context.Context, t *coordinator.Task) (*runJob, time.Duration, error) {
			return buildWorkerJob(ctx, t, reg)
		})
	}
}

type workerAPI struct {
	base, token string
	client      *http.Client
}

func newWorkerAPI(o workerOptions) (*workerAPI, error) {
	u, err := url.Parse(o.Coordinator)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("coordinator 必须为 HTTPS 源地址")
	}
	pin, err := hex.DecodeString(strings.ReplaceAll(o.Fingerprint, ":", ""))
	if err != nil || len(pin) != sha256.Size {
		return nil, fmt.Errorf("fingerprint 必须为 SHA-256 指纹")
	}
	token, err := os.ReadFile(o.TokenFile)
	if err != nil {
		return nil, err
	}
	secret := strings.TrimSpace(string(token))
	if secret == "" {
		return nil, fmt.Errorf("token 文件为空")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, // Trust the explicitly supplied certificate pin instead of system CAs.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("missing certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if !bytes.Equal(sum[:], pin) {
				return fmt.Errorf("coordinator fingerprint mismatch")
			}
			return nil
		}}}
	return &workerAPI{strings.TrimSuffix(o.Coordinator, "/"), secret, &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (a *workerAPI) post(ctx context.Context, path string, in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if len(data) > coordinator.MaxRequestBytes {
		return fmt.Errorf("worker report exceeds coordinator limit")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.base+"/worker/v1/"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("coordinator request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("coordinator HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, coordinator.MaxRequestBytes)).Decode(out)
	}
	return nil
}

type workerBuilder func(context.Context, *coordinator.Task) (*runJob, time.Duration, error)

func runWorker(ctx context.Context, api *workerAPI, o workerOptions, heartbeat, poll time.Duration, build workerBuilder) error {
	registration := map[string]any{"tags": o.Tags, "roles": o.Roles, "max_parallel": o.MaxParallel, "run_untagged": o.RunUntagged}
	if err := api.post(ctx, "register", registration, nil); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errors := make(chan error, o.MaxParallel)
	var wg sync.WaitGroup
	for i := 0; i < o.MaxParallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				var claim struct {
					Task *coordinator.Task `json:"task"`
				}
				if err := api.post(ctx, "claim", struct{}{}, &claim); err != nil {
					errors <- err
					cancel()
					return
				}
				if claim.Task == nil {
					select {
					case <-ctx.Done():
						return
					case <-time.After(poll):
					}
					continue
				}
				if err := runWorkerTask(ctx, api, o, claim.Task, heartbeat, build); err != nil {
					errors <- err
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errors:
		return err
	default:
		return nil
	}
}
func runWorkerTask(parent context.Context, api *workerAPI, o workerOptions, t *coordinator.Task, interval time.Duration, build workerBuilder) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := make(chan struct{})
	done := make(chan bool, 1)
	path := "tasks/" + url.PathEscape(t.ID) + "/"
	fence := map[string]any{"fencing_token": t.FencingToken}
	go func() {
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				done <- false
				return
			case <-ctx.Done():
				done <- true
				return
			case <-tick.C:
				if err := api.post(ctx, path+"heartbeat", fence, nil); err != nil {
					cancel()
					done <- true
					return
				}
			}
		}
	}()
	result := &jobResult{Status: "failed"}
	eligible := len(t.Tags) > 0 || o.RunUntagged
	for _, tag := range t.Tags {
		if !slices.Contains(o.Tags, tag) {
			eligible = false
		}
	}
	if !eligible {
		result.Error = "任务标签与 worker 不匹配"
	} else {
		j, timeout, err := build(ctx, t)
		if err != nil {
			result.Error = err.Error()
		} else {
			j.hooks = &jobHooks{SkipIndex: true, AfterRun: func(r *jobResult) { result = r }}
			_, _, failed := runOneOperatorFn(ctx, j, timeout)
			if j.result != nil {
				result = j.result
			}
			if failed && result.Status == "" {
				result.Status = "failed"
			}
		}
	}
	close(stop)
	lost := <-done
	if lost || parent.Err() != nil {
		result = &jobResult{Status: "lease-lost", Error: "worker lease lost or execution cancelled"}
	}
	action := "fail"
	if result.Status == "success" || result.Status == "skipped" {
		action = "complete"
	}
	reportCtx, reportCancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer reportCancel()
	err := api.post(reportCtx, path+action, map[string]any{"fencing_token": t.FencingToken, "result": result}, nil)
	if lost || parent.Err() != nil {
		return nil
	}
	return err
}

// Clone preparation is serialized because the local repo mapping can share a clone.
var workerCloneMu sync.Mutex

func buildWorkerJob(ctx context.Context, t *coordinator.Task, reg *operator.Registry) (*runJob, time.Duration, error) {
	var p coordinatorPayload
	if err := json.Unmarshal(t.Payload, &p); err != nil {
		return nil, 0, err
	}
	if p.TaskID != t.ID || p.Repo != t.Repo || p.Issue != t.Issue || p.Operator != t.Operator || p.Kind != t.Kind || p.TimeoutSec <= 0 || p.TimeoutSec > int64((1<<63-1)/time.Second) {
		return nil, 0, fmt.Errorf("invalid worker task payload")
	}
	op, ok := reg.Get(p.Operator)
	if !ok || skillDigest(op) != p.SkillDigest {
		return nil, 0, fmt.Errorf("skill_digest mismatch for operator %s", p.Operator)
	}
	if p.Kind != "implement" && p.Kind != "repair" && p.Kind != "reply" {
		return nil, 0, fmt.Errorf("unsupported worker task kind")
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, 0, err
	}
	rc, ok := cfg.Repos[p.Repo]
	if !ok {
		return nil, 0, fmt.Errorf("worker repo mapping missing: %s", p.Repo)
	}
	platform := rc.Platform
	if platform == "" {
		platform = "github"
	}
	if platform != p.Platform || rc.BaseURL != p.BaseURL {
		return nil, 0, fmt.Errorf("worker repo provider does not match payload")
	}
	client, err := newVCSClient(rc)
	if err != nil {
		return nil, 0, err
	}
	if c, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
		c.SetRequestContext(ctx)
	}
	issue, err := client.GetIssue(p.Repo, p.Issue)
	if err != nil {
		return nil, 0, err
	}
	if issue == nil {
		return nil, 0, fmt.Errorf("worker issue no longer exists")
	}
	if rc.LocalPath == "" {
		sum := sha256.Sum256([]byte(platform + "/" + rc.BaseURL + "/" + p.Repo))
		rc.LocalPath = filepath.Join(snapshot.StateDir(), "clones", hex.EncodeToString(sum[:12]))
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		return nil, 0, err
	}
	workerCloneMu.Lock()
	local, err := clone.EnsureLocalCloneContext(ctx, cfg, p.Repo, rc, io.Discard, &clone.Token{GHToken: creds.GHToken, GitLabToken: creds.GitLabToken})
	workerCloneMu.Unlock()
	if err != nil {
		return nil, 0, err
	}
	rc.LocalPath = local
	rc.BaseBranch = p.RepoPolicy.BaseBranch
	rc.TestCommand = p.RepoPolicy.TestCommand
	rc.CIRequired = p.RepoPolicy.CIRequired
	rc.AutoMerge = false
	j := &runJob{op: op, repo: p.Repo, repoCfg: rc, client: client, language: p.Language, sub: &operator.Subject{Number: issue.Number, Title: issue.Title, Body: issue.Body, Labels: issue.Labels, State: issue.State, SubTotal: issue.SubIssuesTotal, URL: issue.URL, IsPR: op.Trigger.Target == "pr"}}
	if p.ReopenEventID != "" {
		j.reopen = &reopenContext{Event: vcs.IssueEvent{ID: p.ReopenEventID}}
	}
	if p.Repair != nil {
		r := p.Repair
		j.repair = &ciRepairContext{PR: vcs.PR{Number: r.PR, HeadSHA: r.HeadSHA, HeadBranch: r.HeadBranch, HeadRepo: r.HeadRepo, BaseSHA: r.BaseSHA, BaseBranch: r.BaseBranch}, Attempt: r.Attempt, Acceptance: r.Acceptance, MergeConflict: r.Reason == "merge_conflict"}
	}
	if p.Kind == "repair" && j.repair == nil {
		return nil, 0, fmt.Errorf("repair task missing repair payload")
	}
	return j, time.Duration(p.TimeoutSec) * time.Second, nil
}
