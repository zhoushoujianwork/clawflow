package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

type workerGeneration struct {
	Version    int
	ID         string
	Executable string
	Options    workerOptions
}

func runManagedWorker(ctx context.Context, api *workerAPI, o workerOptions, build workerBuilder) error {
	o = coordinationWorkerOptions(o)
	if !o.Durable {
		return runWorker(ctx, api, o, 30*time.Second, 5*time.Second, build)
	}
	// Verify authenticated protocol registration before replacing admissions.
	// A controller outage or incompatible candidate leaves the old manager active.
	tags := append([]string{}, o.Tags...)
	for _, capability := range o.Capabilities {
		tags = append(tags, "capability:"+capability)
	}
	registration := map[string]any{"durable": true, "tags": tags, "roles": o.Roles, "max_parallel": o.MaxParallel, "run_untagged": o.RunUntagged}
	if err := api.post(ctx, "register", registration, nil); err != nil {
		return err
	}
	root := workerJournalRoot(o)
	if o.Executable == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(executable)
		if err != nil {
			return err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(raw))
		o.Executable = filepath.Join(root, "generations", digest, "clawflow")
		if _, err := os.Stat(o.Executable); os.IsNotExist(err) {
			if err := writeDurableFile(o.Executable, raw, 0700); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		descriptor := workerGeneration{Version: 1, ID: digest, Executable: o.Executable, Options: o}
		data, err := json.Marshal(descriptor)
		if err != nil {
			return err
		}
		if err := writeDurableFile(filepath.Join(root, "generation.json"), data, 0600); err != nil {
			return err
		}
	}
	generation := execution.NewSessionKey()
	// An explicit new manager replaces admissions only. Task executors are independent.
	if err := writeDurableFile(filepath.Join(root, "current"), []byte(generation), 0600); err != nil {
		return err
	}
	parent := ctx
	var superseded atomic.Bool
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := os.ReadFile(filepath.Join(root, "current"))
				if err != nil || string(current) != generation {
					superseded.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	// Serialize admission generations; the database still enforces shared capacity
	// across their detached executors and any other manager on the same identity.
	lock, err := filelock.Wait(ctx, filepath.Join(root, "admissions.lock"), map[string]any{"PID": os.Getpid(), "generation": generation})
	if err != nil {
		return err
	}
	err = runWorker(ctx, api, o, 30*time.Second, 5*time.Second, build)
	filelock.Release(lock)
	// Keep a launchd-owned superseded manager inert until its service is explicitly
	// replaced; exiting would let KeepAlive resurrect the obsolete generation.
	if superseded.Load() && o.KeepAliveOwner {
		<-parent.Done()
		return nil
	}
	return err
}

func startWorkerGeneration(o workerOptions, candidate string) error {
	if candidate == "" {
		var err error
		candidate, err = os.Executable()
		if err != nil {
			return err
		}
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	// Probe protocol without starting a provider or touching controller state.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, candidate, "worker", "protocol").Output()
	if err != nil || string(output) != "1\n" {
		return fmt.Errorf("candidate does not support durable worker protocol 1")
	}
	raw, err := os.ReadFile(candidate)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	root := filepath.Join(workerJournalRoot(o), "generations", digest)
	pinned := filepath.Join(root, "clawflow")
	if _, err := os.Stat(pinned); os.IsNotExist(err) {
		if err := writeDurableFile(pinned, raw, 0700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	descriptor := filepath.Join(root, "generation.json")
	o.Executable = pinned
	generation := workerGeneration{Version: 1, ID: digest, Executable: pinned, Options: o}
	data, err := json.Marshal(generation)
	if err != nil {
		return err
	}
	if err := writeDurableFile(descriptor, data, 0600); err != nil {
		return err
	}
	// Persist the launch descriptor for service managers and future rolling upgrades.
	if err := writeDurableFile(filepath.Join(workerJournalRoot(o), "generation.json"), data, 0600); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(root, "manager.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(pinned, "worker", "serve", "--descriptor", descriptor)
	cmd.Stdout, cmd.Stderr, cmd.SysProcAttr = log, log, detachedWorkerAttributes()
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func readWorkerGeneration(path string) (workerGeneration, error) {
	var g workerGeneration
	raw, err := os.ReadFile(path)
	if err != nil {
		return g, err
	}
	if json.Unmarshal(raw, &g) != nil || g.Version != 1 || !g.Options.Durable || g.Executable == "" {
		return g, fmt.Errorf("invalid worker generation")
	}
	return g, nil
}

func newWorkerGenerationCommands() []*cobra.Command {
	protocol := &cobra.Command{Use: "protocol", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { _, err := fmt.Fprintln(cmd.OutOrStdout(), "1"); return err }}
	var descriptor, candidate string
	serve := &cobra.Command{Use: "serve", Short: "Run a durable worker generation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		g, err := readWorkerGeneration(descriptor)
		if err != nil {
			return err
		}
		if g.Options.StateDir != "" {
			if err := os.Setenv("CLAWFLOW_STATE_DIR", g.Options.StateDir); err != nil {
				return err
			}
		}
		api, err := newWorkerAPI(g.Options)
		if err != nil {
			return err
		}
		defer api.client.CloseIdleConnections()
		reg, err := loadRegistry()
		if err != nil {
			return err
		}
		g.Options.KeepAliveOwner = false
		return runManagedWorker(cmd.Context(), api, g.Options, func(ctx context.Context, t *coordinator.Task) (*runJob, time.Duration, error) {
			return buildWorkerJob(ctx, t, reg)
		})
	}}
	serve.Flags().StringVar(&descriptor, "descriptor", "", "Private generation descriptor")
	_ = serve.MarkFlagRequired("descriptor")
	var rollDescriptor string
	roll := &cobra.Command{Use: "roll", Short: "Start a new manager generation without stopping task executors", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		g, err := readWorkerGeneration(rollDescriptor)
		if err != nil {
			return err
		}
		if err := startWorkerGeneration(g.Options, candidate); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "New admissions generation launched; existing task executors retain their pinned versions.")
		return err
	}}
	roll.Flags().StringVar(&rollDescriptor, "descriptor", "", "Existing worker generation descriptor")
	roll.Flags().StringVar(&candidate, "binary", "", "Verified candidate CLI executable")
	_ = roll.MarkFlagRequired("descriptor")
	_ = roll.MarkFlagRequired("binary")
	return []*cobra.Command{protocol, serve, roll}
}
