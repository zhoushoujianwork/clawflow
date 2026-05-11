package commands

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
)

// NewPilotCmd returns the `clawflow pilot` parent command. Subcommands
// expose Pilot operations that don't naturally belong on `project`
// (which manages the project itself, not the AI driving it). Today
// there is just `wake` — the manual-trigger entry point used by the
// dashboard's "Wake now" button and reachable from a terminal.
func NewPilotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pilot",
		Short: "Manage Pilot wakes",
		Long: `Pilot is the per-project triage agent. Normally it wakes
automatically at the end of every 'clawflow run' pass, throttled by
the project's cooldown. This command lets you trigger a wake on demand.`,
	}
	cmd.AddCommand(newPilotWakeCmd())
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
