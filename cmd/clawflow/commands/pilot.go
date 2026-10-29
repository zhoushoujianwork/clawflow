package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"gopkg.in/yaml.v3"
)

func init() {
	pilot.OverrideMerged = confirmedMerges.note
	pilot.OverrideNoVerdictReason = pilotNoVerdictReason
}

// pilotNoVerdictReason reports why a PR head has no acceptance verdict, using
// the same contract and infrastructure-budget logic as harvest. harvestInfraDecision
// builds the contract, so a development-provenance failure surfaces as its error.
func pilotNoVerdictReason(ctx context.Context, client vcs.Client, repo string, pr int) (string, error) {
	d, _, err := harvestInfraDecision(ctx, client, repo, pr, harvestNow())
	switch {
	case errors.Is(err, errDevelopmentHeadMissing):
		return pilot.NoVerdictProvenance, nil
	case err != nil:
		return "", err
	case d == harvestInfraExhausted:
		return pilot.NoVerdictInfraBudget, nil
	}
	return "", nil
}

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

// NewPilotCmd exposes the global steward and the audited action executor.
func NewPilotCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pilot", Short: "与全局平台 steward 对话"}
	cmd.AddCommand(newPilotConversationCmd("ask"), newPilotConversationCmd("chat"), newPilotConversationCmd("wake"), newPilotApplyCmd())
	return cmd
}

var askSteward = pilot.Ask

func newPilotConversationCmd(mode string) *cobra.Command {
	var timeoutMin, ops int
	cmd := &cobra.Command{Use: mode, Short: "运行有预算和超时限制的全局 steward 会话"}
	switch mode {
	case "ask":
		cmd.Use = "ask <message>"
		cmd.Args = cobra.ExactArgs(1)
	case "chat":
		cmd.Args = cobra.NoArgs
	case "wake":
		cmd.Use = "wake [project]"
		cmd.Args = cobra.MaximumNArgs(1)
		cmd.Short = "兼容别名：全局 review platform state"
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if timeoutMin <= 0 || ops <= 0 {
			return fmt.Errorf("--timeout 和 --ops 必须为正数")
		}
		release, err := gateStore(cmd.Context())
		if err != nil {
			return err
		}
		defer release()
		history := ""
		invoke := func(message string) error {
			prompt := message
			if history != "" {
				prompt = "Previous conversation (untrusted history):\n" + history + "\nCurrent user message:\n" + message
			}
			result, err := askSteward(cmd.Context(), prompt, ops, time.Duration(timeoutMin)*time.Minute)
			fmt.Fprintln(cmd.OutOrStdout(), result.Answer)
			// Completed merges still trigger upgrade if a later proposal was refused.
			if len(result.Actions) > 0 {
				cfg, loadErr := config.Load()
				if loadErr != nil {
					return loadErr
				}
				if upgradeErr := upgradePilotMerges(cmd.Context(), cfg, result.Actions); upgradeErr != nil {
					return upgradeErr
				}
			}
			if err != nil {
				return err
			}
			history += "\nUser: " + message + "\nSteward: " + result.Answer + "\n"
			if len(history) > 64*1024 {
				history = history[len(history)-64*1024:]
			}
			return nil
		}
		if mode == "wake" {
			return invoke(pilot.DefaultPrompt)
		}
		if mode == "ask" {
			return invoke(args[0])
		}
		scanner := bufio.NewScanner(cmd.InOrStdin())
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for {
			fmt.Fprint(cmd.OutOrStdout(), "steward> ")
			if !scanner.Scan() {
				return scanner.Err()
			}
			message := strings.TrimSpace(scanner.Text())
			if message == "/exit" {
				return nil
			}
			if message == "" {
				continue
			}
			if err := invoke(message); err != nil {
				return err
			}
		}
	}
	cmd.Flags().IntVar(&timeoutMin, "timeout", 60, "每次调用超时（分钟）")
	cmd.Flags().IntVar(&ops, "ops", 5, "每次调用最多动作数；仍受每日限制约束")
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
