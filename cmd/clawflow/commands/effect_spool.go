package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

const effectSpoolEnv = "CLAWFLOW_EFFECT_SPOOL"

// queueEffect is checked before constructing a VCS client or uploading anything.
func queueEffect(out io.Writer, e jobEffect) (bool, error) {
	path := os.Getenv(effectSpoolEnv)
	if path == "" {
		return false, nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return true, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return true, err
	}
	defer f.Close()
	if _, err = f.Write(append(data, '\n')); err != nil {
		return true, err
	}
	if err = f.Sync(); err != nil {
		return true, err
	}
	_, err = fmt.Fprintln(out, "queued for coordinator: "+e.Kind)
	return true, err
}
func readEffectSpool(path string) ([]jobEffect, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("effect spool exceeds limit")
	}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 1<<20)
	var effects []jobEffect
	for scan.Scan() {
		var e jobEffect
		if err = json.Unmarshal(scan.Bytes(), &e); err != nil {
			return nil, err
		}
		effects = append(effects, e)
		if len(effects) > 100 {
			return nil, fmt.Errorf("too many effects")
		}
	}
	return effects, scan.Err()
}
func workerEffectContext(ctx context.Context, path string) context.Context {
	return execution.WithEffectSpool(ctx, path)
}

// installSpoolGuards blocks VCS mutations outside the spool vocabulary. Read
// commands keep their original handlers, including standalone command tests.
func installSpoolGuards(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		installSpoolGuards(child)
	}
	parent := cmd.Parent()
	if parent == nil {
		return
	}
	forbidden := false
	switch parent.Name() {
	case "issue":
		switch cmd.Name() {
		case "list", "view", "search", "history", "comment", "comment-list", "list-sub":
		default:
			forbidden = true
		}
	case "pr":
		switch cmd.Name() {
		case "list", "view", "ci-wait", "create", "comment":
		default:
			forbidden = true
		}
	case "label":
		switch cmd.Name() {
		case "add", "remove":
		default:
			forbidden = true
		}
	case "ci":
		switch cmd.Name() {
		case "rerun":
			forbidden = true
		}
	}
	if parent.Name() == "job" && parent.Parent() != nil && parent.Parent().Name() == "ci" && cmd.Name() == "play" {
		forbidden = true
	}
	if !forbidden {
		return
	}
	runE, run := cmd.RunE, cmd.Run
	if runE != nil {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			if os.Getenv(effectSpoolEnv) != "" {
				return fmt.Errorf("worker VCS write is not supported: %s", c.CommandPath())
			}
			return runE(c, args)
		}
	} else if run != nil {
		cmd.Run = nil
		cmd.RunE = func(c *cobra.Command, args []string) error {
			if os.Getenv(effectSpoolEnv) != "" {
				return fmt.Errorf("worker VCS write is not supported: %s", c.CommandPath())
			}
			run(c, args)
			return nil
		}
	}
}
