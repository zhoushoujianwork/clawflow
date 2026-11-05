package commands

import (
	"encoding/json"
	"fmt"
	"os/user"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/preflight"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

var preflightLocalActor = func() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func newPreflightCmd() *cobra.Command {
	root := &cobra.Command{Use: "preflight", Short: "查看预检故障预算，或在处理故障后恢复已耗尽预算"}
	for _, action := range []string{"status", "resume"} {
		action := action
		var repo, reason string
		var issue int
		cmd := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repo) || issue <= 0 {
				return fmt.Errorf("需要有效的 --repo owner/repo 和正整数 --issue")
			}
			store := preflight.Store{Root: snapshot.DataDir(), Repo: repo, Issue: issue}
			if action == "resume" {
				if strings.TrimSpace(reason) == "" {
					return fmt.Errorf("恢复必须提供非空 --reason；不会修改或清除历史记录")
				}
				actor, err := preflightLocalActor()
				if err != nil {
					return err
				}
				if err = store.Resume(actor, reason, time.Now().UTC()); err != nil {
					return err
				}
				// The next scheduler scan publishes current eligibility. Do not overwrite
				// queue state after releasing the ledger lock: a new call may own it now.
				fmt.Fprintln(cmd.OutOrStdout(), "恢复记录已保存；等待下一轮调度。未启动模型，未修改代码修复预算。")
			}
			l, err := store.Load()
			if err != nil {
				return err
			}
			st, err := l.Status()
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Repository  string    `json:"repository"`
				Issue       int       `json:"issue"`
				Failures    int       `json:"failures"`
				MaxFailures int       `json:"max_failures"`
				NotBefore   time.Time `json:"not_before"`
				PendingID   string    `json:"pending_id,omitempty"`
				Exhausted   bool      `json:"exhausted"`
				Events      int       `json:"events"`
			}{l.Repo, l.Issue, st.Failures, preflight.MaxFailures, st.NotBefore, st.PendingID, st.Failures >= preflight.MaxFailures, len(l.Events)})
		}}
		cmd.Flags().StringVar(&repo, "repo", "", "仓库 owner/repo")
		cmd.Flags().IntVar(&issue, "issue", 0, "issue 编号")
		if action == "resume" {
			cmd.Short = "明确恢复已耗尽的预检预算，保留操作者和原因"
			cmd.Flags().StringVar(&reason, "reason", "", "故障已处理的说明（必填）")
		} else {
			cmd.Short = "只读查看本机预检预算"
		}
		root.AddCommand(cmd)
	}
	return root
}
