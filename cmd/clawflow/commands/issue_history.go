package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// localRunSummary deliberately excludes raw output, errors and tool events.
type localRunSummary struct {
	ID        string     `json:"id"`
	Operator  string     `json:"operator"`
	Status    string     `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	PRURL     string     `json:"pr_url,omitempty"`
}

type issueHistoryOutput struct {
	*vcs.IssueHistory
	LocalRuns  []localRunSummary `json:"local_runs"`
	LocalScope string            `json:"local_scope"`
	LocalError string            `json:"local_error,omitempty"`
}

func loadLocalIssueRuns(repo string, number int) ([]localRunSummary, error) {
	root := filepath.Dir(snapshot.RunDir(repo, number, time.Time{}))
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []localRunSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []localRunSummary{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), "meta.json"))
		if err != nil {
			return out, err
		}
		var m snapshot.RunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			return out, err
		}
		if m.Repo != repo || m.IssueNumber != number {
			continue
		}
		out = append(out, localRunSummary{ID: entry.Name(), Operator: m.Operator, Status: m.Status, StartedAt: m.StartedAt, EndedAt: m.EndedAt, PRURL: m.PRUrl})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

func newIssueHistoryCmd() *cobra.Command {
	var repo string
	var asJSON bool
	cmd := &cobra.Command{
		Use: "history <id>", Short: "Read issue discussion, lifecycle, linked deliveries and local run summaries",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.Atoi(args[0])
			if err != nil || n <= 0 {
				return fmt.Errorf("issue id must be a positive number")
			}
			client, resolved, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			h, historyErr := vcs.LoadIssueHistory(client, resolved, n)
			out := issueHistoryOutput{IssueHistory: h, LocalScope: "Local-only metadata from this machine; excludes raw tool output and provider transcripts."}
			out.LocalRuns, err = loadLocalIssueRuns(resolved, n)
			if err != nil {
				out.LocalError = err.Error()
			}
			if renderErr := renderIssueHistory(cmd.OutOrStdout(), out, asJSON); renderErr != nil {
				return renderErr
			}
			if historyErr != nil {
				return historyErr
			}
			return err
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output complete history as JSON (partial data is marked incomplete)")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func renderIssueHistory(w io.Writer, out issueHistoryOutput, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if _, err := fmt.Fprint(w, out.IssueHistory.Text()); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "\n"+out.LocalScope); err != nil {
		return err
	}
	for _, r := range out.LocalRuns {
		if _, err := fmt.Fprintf(w, "Run %s: %s %s at %s; PR: %s\n", r.ID, r.Operator, r.Status, r.StartedAt.Format(time.RFC3339), r.PRURL); err != nil {
			return err
		}
	}
	if out.LocalError != "" {
		_, err := fmt.Fprintln(w, "Local history incomplete: "+out.LocalError)
		return err
	}
	return nil
}
