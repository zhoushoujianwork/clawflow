package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"gopkg.in/yaml.v3"
)

func init() { pilot.OverrideMerged = confirmedMerges.note }

var pilotUpgrade = func(ctx context.Context, cfg *config.Config) error {
	return failedRunsError(runSelfUpgradePhase(ctx, cfg, newSelfUpgrader()))
}

func upgradePilotMerges(ctx context.Context, cfg *config.Config, results []pilot.ActionRecord) error {
	selected := &config.Config{Settings: cfg.Settings, Repos: map[string]config.Repo{}}
	for _, r := range results {
		if r.Action.Type == "override_merge" && r.Outcome == "applied" && cfg.Repos[r.Action.Repo].SelfUpgrade {
			selected.Repos[r.Action.Repo] = cfg.Repos[r.Action.Repo]
		}
	}
	if len(selected.Repos) == 0 {
		return nil
	}
	return pilotUpgrade(ctx, selected)
}

// NewPilotCmd returns the `clawflow pilot` parent command. Subcommands
// expose Pilot operations that don't naturally belong on `project`
// (which manages the project itself, not the AI driving it). It supports
// manual wakes and validated action application for external supervisors.
func NewPilotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pilot",
		Short: "Manage Pilot wakes",
		Long: `Pilot is the per-project triage agent. Normally it wakes
automatically at the end of every 'clawflow run' pass, throttled by
the project's cooldown. This command lets you trigger a wake on demand.`,
	}
	cmd.AddCommand(newPilotWakeCmd(), newPilotApplyCmd())
	return cmd
}

func newPilotWakeCmd() *cobra.Command {
	var timeoutMin int
	cmd := &cobra.Command{
		Use:   "wake <project>",
		Short: "Manually trigger a Pilot wake for a project (ignores cooldown)",
		Long: `Trigger a single Pilot wake for the named project, bypassing the
cooldown. The wake still respects bound_machine and require_binding —
if the project is bound to a different machine, this errors out
rather than running claude with the wrong credentials.

The wake updates LastWokenAt, so the cooldown clock restarts from
this wake's start time (mirroring a scheduled wake's behaviour).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			release, err := gateStore(cmd.Context())
			if err != nil {
				return err
			}
			defer release()
			timeout := time.Duration(timeoutMin) * time.Minute
			if err := pilot.WakeOne(cmd.Context(), args[0], timeout); err != nil {
				return fmt.Errorf("wake %s: %w", args[0], err)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&timeoutMin, "timeout", 60, "wake timeout in minutes")
	return cmd
}

func newPilotApplyCmd() *cobra.Command {
	var repo, file string
	var dry bool
	cmd := &cobra.Command{Use: "apply", Short: "校验并执行结构化 Pilot 动作", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			set, err := pilot.DecodeActions(data)
			if err != nil {
				return err
			}
			var cfg *config.Config
			if dry {
				// Load does legacy migration; dry-run must not write config.
				raw, readErr := os.ReadFile(config.ConfigPath())
				if readErr != nil {
					return readErr
				}
				cfg = &config.Config{}
				if err := yaml.Unmarshal(raw, cfg); err != nil {
					return err
				}
			} else {
				cfg, err = config.Load()
				if err != nil {
					return err
				}
			}
			if _, ok := cfg.Repos[repo]; !ok {
				return fmt.Errorf("repo %q not configured", repo)
			}
			var creds *config.Credentials
			if !dry {
				creds, err = config.LoadCredentials()
				if err != nil {
					return err
				}
			}
			u, err := user.Current()
			if err != nil {
				return err
			}
			executor := pilot.NewActionExecutor(cfg, creds, []string{repo}, "cli:"+u.Username)
			executor.DefaultRepo = repo
			results, err := executor.Apply(set, dry)
			if err != nil {
				return err
			}
			if err = json.NewEncoder(cmd.OutOrStdout()).Encode(results); err != nil {
				return err
			}
			if !dry {
				if err := upgradePilotMerges(cmd.Context(), cfg, results); err != nil {
					return err
				}
			}
			for _, r := range results {
				if r.Outcome != "applied" && r.Outcome != "already_applied" && r.Outcome != "valid" {
					return fmt.Errorf("pilot action %s: %s", r.Action.ID, r.Outcome)
				}
			}
			return nil
		}}
	cmd.Flags().StringVar(&repo, "repo", "", "目标仓库")
	cmd.Flags().StringVar(&file, "file", "", "动作 JSON 文件")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "仅打印校验结果，不写入或执行动作")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
