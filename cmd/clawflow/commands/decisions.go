package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/decisions"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// decisionsStatusBudget bounds the Offload readiness probe behind the list.
const decisionsStatusBudget = 10 * time.Second

// NewDecisionsCmd wires `clawflow decisions`: one read-only list of what only a
// person can decide, across every enabled repository.
func NewDecisionsCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "decisions",
		Short: "List what needs a person to decide, across all enabled repositories",
		Long: `List the items ClawFlow cannot settle by itself, with the reason, a link and the
usual way to resolve each one:

  needs_confirmation  issue labelled agent-needs-confirmation
  owner_skipped       open issue labelled clawflow-skip
  acceptance_blocked  reviewer verdict blocked and not handed to the implementer
  repair_stopped      PR still failing after the shared repair budget is spent
  execution_config    Offload setup blocked, unreadable or its login rejected

The command is read-only. It reads the last pipeline board snapshot written by
the controller or 'clawflow web' (no provider calls) and the cached Offload
readiness check. It adds no label or confirmation step: resolve an item the
usual way (edit the issue, remove the label, fix the setup) and it disappears
from the next snapshot. An empty list means nothing needs you; the
"incomplete" section names sources that could not be read, whose silence is not
evidence.`,
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			report := collectDecisions(cmd.Context())
			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			printDecisions(cmd.OutOrStdout(), report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the report as JSON")
	return cmd
}

// collectDecisions reads local evidence only. A board that cannot be read is
// reported as a gap, never as an empty healthy list.
func collectDecisions(ctx context.Context) decisions.Report {
	snap, _ := snapshot.ReadPipeline() // a missing board yields an empty snapshot and a gap
	fillRepoURLs(&snap)
	var status *execution.StatusDoc
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, decisionsStatusBudget)
	defer cancel()
	if doc, ok := localExecutionStatus(probeCtx); ok {
		status = &doc
	}
	return decisions.Build(snap, status, time.Now())
}

// fillRepoURLs gives boards written before repository URLs were recorded a web
// root from the current configuration, so their links are still correct.
func fillRepoURLs(snap *pipeline.Snapshot) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	for i := range snap.Repos {
		r := &snap.Repos[i]
		if r.URL != "" {
			continue
		}
		rc, ok := cfg.Repos[r.Repo]
		if !ok {
			continue
		}
		platform := rc.Platform
		if platform == "" {
			platform = "github"
		}
		r.URL = pipeline.RepoWebURL(r.Repo, platform, rc.BaseURL)
		if r.Platform == "" {
			r.Platform = platform
		}
	}
}

// localExecutionStatus runs the same cached, bounded, read-only check as
// `clawflow execution status`. ok is false when the check did not finish.
func localExecutionStatus(ctx context.Context) (execution.StatusDoc, bool) {
	home, _ := os.UserHomeDir()
	in := statusInputs{AgentsDir: filepath.Join(home, "Library", "LaunchAgents"), Settings: loadExecutionSettings, Cache: statusCachePath()}
	if cfg, err := config.Load(); err == nil && cfg.Settings.Coordinator.LocalWorker.Enabled {
		in.LocalWorker = true
		in.ControllerEn = controllerEnvironment(in.AgentsDir)
	}
	doc := collectStatus(ctx, in)
	// A probe cut short by the budget is not a finding about the setup.
	if ctx.Err() != nil {
		return execution.StatusDoc{}, false
	}
	return doc, true
}

func printDecisions(w io.Writer, r decisions.Report) {
	if len(r.Decisions) == 0 {
		fmt.Fprintln(w, "Nothing needs your decision.")
	} else {
		fmt.Fprintf(w, "%d item(s) need your decision\n", len(r.Decisions))
		for i, d := range r.Decisions {
			fmt.Fprintf(w, "\n%d. [%s] %s\n", i+1, d.Kind, decisionSubject(d))
			fmt.Fprintf(w, "   Why:  %s\n", d.Reason)
			fmt.Fprintf(w, "   Do:   %s\n", d.Action)
			if d.URL != "" {
				fmt.Fprintf(w, "   Link: %s\n", d.URL)
			}
			if d.IssueURL != "" && d.IssueURL != d.URL {
				fmt.Fprintf(w, "   Issue: %s\n", d.IssueURL)
			}
		}
	}
	if len(r.Incomplete) > 0 {
		fmt.Fprintln(w, "\nIncomplete (these sources could not be read, so they may hide items):")
		for _, g := range r.Incomplete {
			fmt.Fprintf(w, "  - %s: %s\n", g.Source, g.Reason)
		}
	}
}

func decisionSubject(d decisions.Decision) string {
	switch {
	case d.Repo != "" && d.PR > 0:
		return fmt.Sprintf("%s PR #%d — %s", d.Repo, d.PR, d.Title)
	case d.Repo != "" && d.Issue > 0:
		return fmt.Sprintf("%s#%d — %s", d.Repo, d.Issue, d.Title)
	}
	return d.Title
}
