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

var queueEnrollmentClient = newVCSClientForRepo

// NewQueueCmd wires `clawflow queue`: the unified cross-repo work queue.
func NewQueueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Show and reorder the unified work queue across all repositories",
	}
	cmd.AddCommand(newQueueListCmd(), newQueueMoveCmd(), newQueueAddCmd(), newQueueRemoveCmd(), newQueueImportCmd())
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
				count := 0
				for _, st := range queue.ListStates() {
					if repo == "" || st.Repo == repo {
						count += st.NotEnrolled
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "not enrolled: %d open issue(s) without the clawflow label (last pass)\n", count)
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
	var cfg *config.Config
	if c, cErr := config.Load(); cErr == nil {
		cfg = c
	}
	scopeOf := func(st queue.RepoState) string {
		if cfg == nil {
			return st.Scope
		}
		rc := cfg.Repos[st.Repo]
		s, _ := queue.EffectiveScope(rc.QueueScope, cfg.Settings.Queue.Scope, rc.QueueIssue > 0 || st.Imported)
		return s
	}
	return queue.BuildList(queue.ListStates(), order, repo, scopeOf), nil
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
	fmt.Fprintln(tw, "#\tKEY\tTIER\tRANK\tSCOPE\tSTATE\tTITLE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Position, r.Key, r.Tier, queue.RankLabel(r.Rank), r.Scope, r.State, r.Title)
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

func newQueueAddCmd() *cobra.Command {
	var top bool
	var before, after string
	cmd := &cobra.Command{
		Use:   "add <key> [--top | --before <key> | --after <key>]",
		Short: "Add the clawflow label and manual rank (bottom by default)",
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
			// Validate placement before changing remote enrollment.
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
			client, _, _, err := queueEnrollmentClient(k.Repo)
			if err != nil {
				return err
			}
			if err := client.AddLabel(k.Repo, k.Number, queue.ScopeLabel); err != nil {
				return err
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
		Short: "Remove the clawflow label and manual rank",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := queue.ParseKey(args[0])
			if err != nil {
				return err
			}
			client, _, _, err := queueEnrollmentClient(k.Repo)
			if err != nil {
				return err
			}
			if err := client.RemoveLabel(k.Repo, k.Number, queue.ScopeLabel); err != nil {
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
