package commands

import (
	"encoding/json"
	"fmt"
	"os/user"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

func newPilotScheduleCmd() *cobra.Command {
	root := &cobra.Command{Use: "schedule", Short: "管理平台定时检查"}
	var j store.ScheduledCheck
	add := &cobra.Command{Use: "add", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := pilot.OpenCheckStore(cmd.Context())
		if err != nil {
			return err
		}
		defer s.Close()
		u, err := user.Current()
		if err != nil {
			return err
		}
		j.Owner = u.Username
		j.Enabled = true
		if j.ID == "" {
			j.ID = fmt.Sprintf("check-%x", time.Now().UnixNano())
		}
		if err := s.SaveScheduledCheck(cmd.Context(), &j, true); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(j)
	}}
	add.Flags().StringVar(&j.Cron, "cron", "", "5 字段 cron（数字）")
	add.Flags().StringVar(&j.Timezone, "tz", "", "IANA 时区")
	add.Flags().StringVar(&j.Prompt, "prompt", "", "检查指令")
	add.Flags().IntVar(&j.Ops, "ops", 5, "动作预算")
	add.Flags().IntVar(&j.TimeoutSeconds, "timeout", 3600, "超时（秒，最多 86400）")
	for _, name := range []string{"cron", "tz", "prompt"} {
		_ = add.MarkFlagRequired(name)
	}
	root.AddCommand(add)
	for _, mode := range []string{"list", "show", "enable", "disable", "remove"} {
		c := &cobra.Command{Use: mode, Args: cobra.NoArgs}
		if mode != "list" {
			c.Use += " <id>"
			c.Args = cobra.ExactArgs(1)
		}
		c.RunE = func(cmd *cobra.Command, args []string) error {
			s, err := pilot.OpenCheckStore(cmd.Context())
			if err != nil {
				return err
			}
			defer s.Close()
			enc := json.NewEncoder(cmd.OutOrStdout())
			if mode == "list" {
				jobs, err := s.ScheduledChecks(cmd.Context())
				if err != nil {
					return err
				}
				return enc.Encode(jobs)
			}
			if mode == "remove" {
				return s.RemoveScheduledCheck(cmd.Context(), args[0])
			}
			job, err := s.ScheduledCheck(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if mode == "show" {
				runs, err := s.ScheduledCheckRuns(cmd.Context(), job.ID)
				if err != nil {
					return err
				}
				return enc.Encode(struct {
					Check store.ScheduledCheck      `json:"check"`
					Runs  []store.ScheduledCheckRun `json:"runs"`
				}{job, runs})
			}
			job.Enabled = mode == "enable"
			return s.SaveScheduledCheck(cmd.Context(), &job, false)
		}
		root.AddCommand(c)
	}
	return root
}
