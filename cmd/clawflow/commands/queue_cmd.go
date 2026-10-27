package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
)

// NewQueueCmd wires `clawflow queue`: the unified cross-repo work queue.
func NewQueueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Show and reorder the unified work queue across all repositories",
	}
	cmd.AddCommand(newQueueListCmd(), newQueueMoveCmd(), newQueueImportCmd())
	return cmd
}

func newQueueListCmd() *cobra.Command {
	var repo string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List queued issues in global order (state is from the last run pass)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := queueListRows(repo)
			if err != nil {
				return err
			}
			return writeQueueList(cmd.OutOrStdout(), rows, asJSON)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "only this repository")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func queueListRows(repo string) ([]queue.ListEntry, error) {
	order, err := queue.LoadRank()
	if err != nil {
		return nil, err
	}
	return queue.BuildList(queue.ListStates(), order, repo), nil
}

func writeQueueList(w io.Writer, rows []queue.ListEntry, asJSON bool) error {
	if asJSON {
		if rows == nil {
			rows = []queue.ListEntry{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "queue is empty (no pass has recorded candidates yet, or nothing is open)")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tKEY\tTIER\tRANK\tSTATE\tTITLE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", r.Position, r.Key, r.Tier, queue.RankLabel(r.Rank), r.State, r.Title)
	}
	return tw.Flush()
}

func newQueueMoveCmd() *cobra.Command {
	var top, bottom bool
	var before, after string
	cmd := &cobra.Command{
		Use:   "move <key> (--top | --bottom | --before <key> | --after <key>)",
		Short: "Change the manual rank of an issue (the priority label still decides the tier)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			p := queue.Placement{Top: top, Bottom: bottom}
			if before != "" {
				k, err := queue.ParseKey(before)
				if err != nil {
					return err
				}
				p.Before = &k
			}
			if after != "" {
				k, err := queue.ParseKey(after)
				if err != nil {
					return err
				}
				p.After = &k
			}
			order, err := moveQueueItem(cfg, args[0], p)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "moved %s; %d item(s) ranked\n", args[0], len(order))
			return nil
		},
	}
	cmd.Flags().BoolVar(&top, "top", false, "move to the top of the rank")
	cmd.Flags().BoolVar(&bottom, "bottom", false, "move to the bottom of the rank")
	cmd.Flags().StringVar(&before, "before", "", "place directly before this key")
	cmd.Flags().StringVar(&after, "after", "", "place directly after this key")
	return cmd
}

// moveQueueItem edits the rank file only. Keys of repos that are not
// configured are rejected; closed issues are pruned by the next pass.
func moveQueueItem(cfg *config.Config, key string, p queue.Placement) ([]queue.Key, error) {
	k, err := queue.ParseKey(key)
	if err != nil {
		return nil, err
	}
	if _, ok := cfg.Repos[k.Repo]; !ok {
		return nil, fmt.Errorf("repository %q is not configured", k.Repo)
	}
	for _, anchor := range []*queue.Key{p.Before, p.After} {
		if anchor != nil {
			if _, ok := cfg.Repos[anchor.Repo]; !ok {
				return nil, fmt.Errorf("repository %q is not configured", anchor.Repo)
			}
		}
	}
	return queue.UpdateRank(func(order []queue.Key) ([]queue.Key, error) {
		return queue.Move(order, k, p)
	}, func(k queue.Key) bool {
		_, ok := cfg.Repos[k.Repo]
		return ok
	})
}

func newQueueImportCmd() *cobra.Command {
	var repo string
	var from int
	cmd := &cobra.Command{
		Use:   "import --repo <owner/repo> --from-issue <N>",
		Short: "Append the open sub-issues of a legacy queue issue to the rank, in their current order",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" || from < 1 {
				return fmt.Errorf("--repo and --from-issue (>= 1) are required")
			}
			client, _, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			added, err := importQueueIssue(client, repo, from)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "imported %d item(s) from %s#%d\n", len(added), repo, from)
			for _, k := range added {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", k)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository of the queue issue")
	cmd.Flags().IntVar(&from, "from-issue", 0, "queue issue number whose sub-issues are imported")
	return cmd
}
