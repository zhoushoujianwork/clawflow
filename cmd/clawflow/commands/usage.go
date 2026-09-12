package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// NewUsageCmd returns the `clawflow usage` parent command. It exposes the
// spend-accounting maintenance operations that don't belong on `run` or
// `web` — today just `backfill`.
func NewUsageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Inspect and repair spend accounting",
	}
	cmd.AddCommand(newUsageBackfillCmd())
	return cmd
}

func newUsageBackfillCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Recover token usage for runs whose meta.json has usage=null",
		Long: `Walk data/runs and data/pilot-runs looking for meta.json files with
usage=null whose events.jsonl still carries per-message token counts, and
write the recovered figures back.

These rows exist because a killed process (deadline, SIGKILL, OOM) never
emits claude's terminal "result" event, which used to be the only usage
source — so the spend of the longest, most expensive runs was recorded as
null and disappeared from every aggregate (issue #322).

Recovered token counts are exact. Cost is NOT recoverable: claude only
reports it in the result event, so those rows are flagged "estimated" and
carry cost 0 rather than a made-up number.

Defaults to a dry run because this rewrites money-bearing records. Re-run
with --apply once the plan looks right.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := snapshot.BackfillUsage(!apply)
			if err != nil {
				return fmt.Errorf("backfill: %w", err)
			}
			if len(entries) == 0 {
				fmt.Println("nothing to backfill — every run with recoverable usage already has it")
				return nil
			}
			mode := "DRY RUN (re-run with --apply to write)"
			if apply {
				mode = "APPLIED"
			}
			fmt.Printf("%d run(s) with recoverable usage — %s\n\n", len(entries), mode)
			var inTok, outTok int64
			for _, e := range entries {
				inTok += e.Usage.InputTokens
				outTok += e.Usage.OutputTokens
				fmt.Printf("  %s  %-7s %-45s status=%-10s input=%-12d output=%-9d cost=%s\n",
					e.StartedAt.Format("2006-01-02T15:04:05Z"),
					e.Kind, truncateLabel(e.Label, 45), e.Status,
					e.Usage.InputTokens, e.Usage.OutputTokens,
					costLabel(e.Usage))
			}
			fmt.Printf("\n  total recovered: input=%d output=%d tokens\n", inTok, outTok)
			if !apply {
				return nil
			}
			// usage.json is derived data — rebuild it so the dashboard shows
			// the recovered spend without waiting for the next pass.
			cfg, cerr := config.Load()
			billingDay := 1
			if cerr == nil {
				billingDay = cfg.Settings.BillingCycleDay
			}
			if err := snapshot.RefreshUsageSummary(billingDay); err != nil {
				return fmt.Errorf("refresh usage summary: %w", err)
			}
			fmt.Println("  usage.json rebuilt")
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "write the recovered usage to disk (default: dry run)")
	return cmd
}

func costLabel(u *snapshot.Usage) string {
	if u.Estimated {
		return "n/a (estimated)"
	}
	return fmt.Sprintf("$%.4f", u.TotalCostUSD)
}

func truncateLabel(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
