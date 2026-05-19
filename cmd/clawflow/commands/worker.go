package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	workerchat "github.com/zhoushoujianwork/clawflow/internal/worker/chat"
)

// NewWorkerCmd manages the long-running SaaS worker process.
func NewWorkerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Register and run this machine as a ClawFlow SaaS worker",
	}
	cmd.AddCommand(newWorkerRegisterCmd())
	cmd.AddCommand(newWorkerStartCmd())
	cmd.AddCommand(newWorkerStatusCmd())
	return cmd
}

func newWorkerRegisterCmd() *cobra.Command {
	var displayName string
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register this machine with ClawFlow SaaS",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cloud.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.AccessToken == "" {
				return fmt.Errorf("cloud access token is required; run 'clawflow cloud login' first")
			}
			resp, err := registerWorker(cmd.Context(), cfg, displayName)
			if err != nil {
				return err
			}
			fmt.Printf("machine_id: %s\nworker_id: %s\n", resp.MachineID, resp.WorkerID)
			return nil
		},
	}
	cmd.Flags().StringVar(&displayName, "name", "", "Human-readable machine display name")
	return cmd
}

func newWorkerStartCmd() *cobra.Command {
	var (
		interval time.Duration
		timeout  time.Duration
		once     bool
		capacity int
		chat     bool
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the worker heartbeat and job lease loop",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return runWorkerLoop(ctx, workerLoopOptions{
				Interval: interval,
				Timeout:  timeout,
				Once:     once,
				Capacity: capacity,
				Chat:     chat,
			})
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "Heartbeat and lease interval")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Minute, "Per-job operator timeout")
	cmd.Flags().BoolVar(&once, "once", false, "Run one heartbeat/lease cycle and exit")
	cmd.Flags().IntVar(&capacity, "capacity", 1, "Maximum jobs this worker should request per cycle")
	cmd.Flags().BoolVar(&chat, "chat", true, "Long-poll cloud for browser chat sessions and run claude locally")
	return cmd
}

func newWorkerStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local worker registration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cloud.LoadConfig()
			if err != nil {
				return err
			}
			fmt.Printf("cloud_url: %s\n", cfg.BaseURL)
			fmt.Printf("machine_id: %s\n", cfg.MachineID)
			fmt.Printf("worker_id: %s\n", cfg.WorkerID)
			fmt.Printf("worker_token_configured: %t\n", cfg.WorkerToken != "")
			fmt.Printf("capabilities: %s\n", strings.Join(capabilityStrings(detectCapabilities()), ","))
			return nil
		},
	}
}

type workerLoopOptions struct {
	Interval time.Duration
	Timeout  time.Duration
	Once     bool
	Capacity int
	// Chat enables the chat long-poll goroutine. The flag is on by
	// default; --chat=false leaves a worker as job-lease-only (useful
	// for legacy operator-only deployments).
	Chat bool
}

func runWorkerLoop(ctx context.Context, opts workerLoopOptions) error {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Minute
	}
	if opts.Capacity <= 0 {
		opts.Capacity = 1
	}
	cfg, err := cloud.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.MachineID == "" || cfg.WorkerID == "" || cfg.WorkerToken == "" {
		resp, err := registerWorker(ctx, cfg, "")
		if err != nil {
			return err
		}
		cfg.MachineID = resp.MachineID
		cfg.WorkerID = resp.WorkerID
		cfg.WorkerToken = resp.WorkerToken
	}
	client, err := cloud.NewClient(cfg)
	if err != nil {
		return err
	}

	// In --once mode we keep the simple synchronous flow: no chat
	// goroutine, just one heartbeat/lease cycle and exit. The chat
	// loop is a steady-state background concern and would never get
	// a meaningful slice of work in a single tick.
	if opts.Once {
		if err := workerCycle(ctx, client, cfg, opts); err != nil {
			fmt.Fprintf(os.Stderr, "worker cycle: %v\n", err)
			return err
		}
		return nil
	}

	// Steady-state: two goroutines share ctx — the existing job-lease
	// cycle (heartbeat + Lease + ExecuteJob) and the new chat loop
	// (long-poll + clone + spawn claude). The signal handler installed
	// by newWorkerStartCmd cancels ctx; both goroutines exit on
	// ctx.Done(); runWorkerLoop returns after both have drained.
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		runJobsLoop(ctx, client, cfg, opts)
	}()

	if opts.Chat {
		creds, credsErr := config.LoadCredentials()
		if credsErr != nil {
			fmt.Fprintf(os.Stderr, "worker chat: load credentials: %v (chat disabled)\n", credsErr)
		} else {
			loop := workerchat.NewLoop(workerchat.Config{
				Client: client,
				Creds:  creds,
			})
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := loop.Run(ctx, cfg.MachineID, cfg.WorkerID); err != nil {
					fmt.Fprintf(os.Stderr, "worker chat: %v\n", err)
				}
			}()
		}
	}

	wg.Wait()
	return nil
}

// runJobsLoop is the existing heartbeat+lease loop, factored out so
// it can run as a goroutine alongside the chat loop.
func runJobsLoop(ctx context.Context, client *cloud.Client, cfg cloud.Config, opts workerLoopOptions) {
	for {
		if err := workerCycle(ctx, client, cfg, opts); err != nil {
			fmt.Fprintf(os.Stderr, "worker cycle: %v\n", err)
		}
		timer := time.NewTimer(opts.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func workerCycle(ctx context.Context, client *cloud.Client, cfg cloud.Config, opts workerLoopOptions) error {
	caps := detectCapabilities()
	if _, err := client.Heartbeat(ctx, cloud.HeartbeatRequest{
		MachineID: cfg.MachineID,
		WorkerID:  cfg.WorkerID,
		Status:    "online",
		Capacity:  opts.Capacity,
	}); err != nil {
		return err
	}
	lease, err := client.Lease(ctx, cloud.LeaseRequest{
		MachineID:    cfg.MachineID,
		WorkerID:     cfg.WorkerID,
		Capabilities: caps,
		Capacity:     opts.Capacity,
	})
	if err != nil {
		return err
	}
	if lease.Job == nil {
		return nil
	}
	job := lease.Job
	if job.RunID == "" {
		return fmt.Errorf("leased job %s has no run_id", job.JobID)
	}
	fmt.Fprintf(os.Stderr, "leased job %s run %s: %s#%d %s\n", job.JobID, job.RunID, job.Repo, job.Number, job.Operator)
	didFire, runDir, runErr := ExecuteJob(ctx, *job, opts.Timeout)
	status := "succeeded"
	if runErr != nil || !didFire {
		status = "failed"
	}
	finish := cloud.FinishRunRequest{Status: status}
	if runErr != nil {
		finish.Error = runErr.Error()
	}
	// Attach usage from this run's events.jsonl (nil when claude
	// produced no terminal result event — finishing without usage is
	// still valid; the cloud row simply won't carry cost numbers).
	finish.Usage = ExtractCloudUsage(runDir)
	if err := client.FinishRun(ctx, job.RunID, finish); err != nil {
		return err
	}
	return runErr
}

func registerWorker(ctx context.Context, cfg cloud.Config, displayName string) (*cloud.RegisterWorkerResponse, error) {
	if cfg.AccessToken == "" {
		return nil, fmt.Errorf("cloud access token is required; run 'clawflow cloud login' first")
	}
	registerCfg := cfg
	registerCfg.WorkerToken = ""
	client, err := cloud.NewClient(registerCfg)
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	resp, err := client.RegisterWorker(ctx, cloud.RegisterWorkerRequest{
		Hostname:     hostname,
		DisplayName:  displayName,
		Version:      Version,
		Capabilities: detectCapabilities(),
	})
	if err != nil {
		return nil, err
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		return nil, err
	}
	cfg.MachineID = resp.MachineID
	cfg.WorkerID = resp.WorkerID
	cfg.WorkerToken = resp.WorkerToken
	if err := config.SaveCredentials(cfg.ApplyToCredentials(creds)); err != nil {
		return nil, err
	}
	return resp, nil
}

func detectCapabilities() []cloud.Capability {
	caps := []cloud.Capability{
		cloud.Capability(runtime.GOOS),
		cloud.Capability(runtime.GOARCH),
		"go",
		"codex",
		"github",
	}
	creds, err := config.LoadCredentials()
	if err == nil && creds != nil && creds.GitLabToken != "" {
		caps = append(caps, "gitlab")
	}
	return caps
}

func capabilityStrings(caps []cloud.Capability) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, string(c))
	}
	return out
}
