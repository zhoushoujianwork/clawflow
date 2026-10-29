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
	cmd.AddCommand(newQueueListCmd(), newQueueMoveCmd(), newQueueAddCmd(), newQueueRemoveCmd())
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
			if err := writeQueueList(cmd.OutOrStdout(), rows, asJSON); err != nil {
				return err
			}
			if !asJSON {
				for _, st := range queue.ListStates() {
					if repo == "" || st.Repo == repo {
						fmt.Fprintf(cmd.OutOrStdout(), "%s: skipped: %d open issue(s) with clawflow-skip (last pass)\n", st.Repo, st.Skipped)
					}
				}
			}
			return nil
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

func newQueueAddCmd() *cobra.Command {
	var top bool
	var before, after string
	cmd := &cobra.Command{
		Use:   "add <key> [--top | --before <key> | --after <key>]",
		Short: "Add manual rank (does not change eligibility) (bottom by default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			p := queue.Placement{Top: top, Bottom: !top && before == "" && after == ""}
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
			k, err := queue.ParseKey(args[0])
			if err != nil {
				return err
			}
			if _, ok := cfg.Repos[k.Repo]; !ok {
				return fmt.Errorf("repository %q is not configured", k.Repo)
			}
			// Validate placement before changing the rank.
			current, err := queue.LoadRank()
			if err != nil {
				return err
			}
			if _, err := queue.Move(current, k, p); err != nil {
				return err
			}
			for _, anchor := range []*queue.Key{p.Before, p.After} {
				if anchor != nil {
					if _, ok := cfg.Repos[anchor.Repo]; !ok {
						return fmt.Errorf("repository %q is not configured", anchor.Repo)
					}
				}
			}
			order, err := moveQueueItem(cfg, args[0], p)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added %s; %d item(s) ranked\n", args[0], len(order))
			return nil
		},
	}
	cmd.Flags().BoolVar(&top, "top", false, "add at the top instead of the bottom")
	cmd.Flags().StringVar(&before, "before", "", "place directly before this key")
	cmd.Flags().StringVar(&after, "after", "", "place directly after this key")
	return cmd
}

func newQueueRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <key>",
		Short: "Remove manual rank (does not exclude the issue)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := queue.ParseKey(args[0])
			if err != nil {
				return err
			}
			if _, err := queue.UpdateRank(func(order []queue.Key) ([]queue.Key, error) {
				return queue.Remove(order, k), nil
			}, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", k)
			return nil
		},
	}
}
