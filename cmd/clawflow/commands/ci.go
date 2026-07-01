package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

// NewCICmd exposes GitLab CI/CD read operations: runners, pipelines, and job
// logs. These are GitLab-only (GitHub Actions has no equivalent object model),
// so every subcommand resolves a concrete *gitlab.Client and errors on
// non-GitLab repos.
func NewCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Query GitLab CI/CD runners, pipelines, and job logs",
		Long:  "GitLab-only. Inspect project runners, list/view pipelines, and read job logs (traces).",
	}
	cmd.AddCommand(newCIRunnerCmd())
	cmd.AddCommand(newCIPipelineCmd())
	cmd.AddCommand(newCIJobCmd())
	return cmd
}

// ---- runner ----

func newCIRunnerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runner",
		Short: "Inspect CI/CD runners",
	}
	cmd.AddCommand(newCIRunnerListCmd())
	return cmd
}

func newCIRunnerListCmd() *cobra.Command {
	var repo string
	var asJSON bool

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List runners available to a project",
		Example: "  clawflow ci runner list --repo group/proj",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, err := newGitLabClientForRepo(repo)
			if err != nil {
				return err
			}
			runners, err := client.ListProjectRunners(repo)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(runners)
			}
			if len(runners) == 0 {
				fmt.Println("no runners")
				return nil
			}
			fmt.Printf("%-8s  %-8s  %-9s  %-8s  %s\n", "ID", "ONLINE", "STATUS", "TYPE", "DESCRIPTION")
			for _, r := range runners {
				online := "-"
				if r.Online {
					online = "online"
				}
				// runner_type is absent on older GitLab (≤11.x); fall back to
				// the is_shared bool so the column is never blank.
				rtype := r.RunnerType
				if rtype == "" {
					if r.IsShared {
						rtype = "shared"
					} else {
						rtype = "project"
					}
				}
				desc := r.Description
				if r.Name != "" && desc == "" {
					desc = r.Name
				}
				if len(r.TagList) > 0 {
					desc = fmt.Sprintf("%s  [%s]", desc, strings.Join(r.TagList, ","))
				}
				fmt.Printf("%-8d  %-8s  %-9s  %-8s  %s\n", r.ID, online, r.Status, rtype, desc)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "GitLab group/project or URL (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

// ---- pipeline ----

func newCIPipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "List and view CI/CD pipelines",
	}
	cmd.AddCommand(newCIPipelineListCmd())
	cmd.AddCommand(newCIPipelineViewCmd())
	return cmd
}

func newCIPipelineListCmd() *cobra.Command {
	var repo, ref, status string
	var limit int
	var asJSON bool

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List recent pipelines (newest first)",
		Example: "  clawflow ci pipeline list --repo group/proj --ref main --status failed --limit 20",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, err := newGitLabClientForRepo(repo)
			if err != nil {
				return err
			}
			pipelines, err := client.ListPipelines(repo, gitlab.PipelineListOpts{
				Ref:    ref,
				Status: status,
				Limit:  limit,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(pipelines)
			}
			if len(pipelines) == 0 {
				fmt.Println("no pipelines")
				return nil
			}
			// created_at is omitted from the pipeline *list* response on
			// GitLab 11.x — only the single-pipeline GET returns it — so the
			// list shows SHA (always present) and `pipeline view` shows the
			// timestamp.
			fmt.Printf("%-8s  %-10s  %-24s  %s\n", "ID", "STATUS", "REF", "SHA")
			for _, p := range pipelines {
				fmt.Printf("%-8d  %-10s  %-24s  %s\n", p.ID, p.Status, truncate(p.Ref, 24), shortSHA(p.SHA))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "GitLab group/project or URL (required)")
	cmd.Flags().StringVar(&ref, "ref", "", "filter by branch/tag")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (success/failed/running/pending/...)")
	cmd.Flags().IntVar(&limit, "limit", 20, "max pipelines to list (1-100)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func newCIPipelineViewCmd() *cobra.Command {
	var repo string
	var id int64
	var asJSON bool

	cmd := &cobra.Command{
		Use:     "view",
		Short:   "View a pipeline and its jobs",
		Example: "  clawflow ci pipeline view --repo group/proj --id 12345",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, err := newGitLabClientForRepo(repo)
			if err != nil {
				return err
			}
			p, err := client.GetPipeline(repo, id)
			if err != nil {
				return err
			}
			jobs, err := client.ListPipelineJobs(repo, id)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(struct {
					Pipeline gitlab.Pipeline `json:"pipeline"`
					Jobs     []gitlab.Job    `json:"jobs"`
				}{p, jobs})
			}
			fmt.Printf("pipeline #%d  [%s]\n", p.ID, p.Status)
			fmt.Printf("ref:     %s\n", p.Ref)
			fmt.Printf("sha:     %s\n", p.SHA)
			if p.CreatedAt != "" {
				fmt.Printf("created: %s\n", p.CreatedAt)
			}
			if p.WebURL != "" {
				fmt.Printf("url:     %s\n", p.WebURL)
			}
			fmt.Println()
			if len(jobs) == 0 {
				fmt.Println("no jobs")
				return nil
			}
			fmt.Printf("%-10s  %-10s  %-12s  %s\n", "JOB", "STATUS", "STAGE", "NAME")
			for _, j := range jobs {
				fmt.Printf("%-10d  %-10s  %-12s  %s\n", j.ID, j.Status, truncate(j.Stage, 12), j.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "GitLab group/project or URL (required)")
	cmd.Flags().Int64Var(&id, "id", 0, "pipeline id (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// ---- job ----

func newCIJobCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Inspect CI/CD jobs",
	}
	cmd.AddCommand(newCIJobLogCmd())
	return cmd
}

func newCIJobLogCmd() *cobra.Command {
	var repo string
	var job int64
	var raw bool

	cmd := &cobra.Command{
		Use:     "log",
		Short:   "Print the log (trace) of a job",
		Example: "  clawflow ci job log --repo group/proj --job 67890",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, err := newGitLabClientForRepo(repo)
			if err != nil {
				return err
			}
			log, err := client.GetJobLog(repo, job)
			if err != nil {
				return err
			}
			if strings.TrimSpace(log) == "" {
				fmt.Fprintln(os.Stderr, "(empty log — job may not have started)")
				return nil
			}
			// GitLab traces are peppered with ANSI color / cursor escapes and
			// section_start/section_end fold markers. Strip both by default so
			// redirecting to a file yields clean text; --raw keeps everything
			// for terminal viewing.
			if !raw {
				log = stripANSI(log)
				log = stripSections(log)
			}
			fmt.Print(log)
			if !strings.HasSuffix(log, "\n") {
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "GitLab group/project or URL (required)")
	cmd.Flags().Int64Var(&job, "job", 0, "job id (required)")
	cmd.Flags().BoolVar(&raw, "raw", false, "keep ANSI color/cursor escape codes (default strips them)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("job")
	return cmd
}

// ---- helpers ----

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// ansiEscape matches CSI escape sequences (color, cursor moves, line erase)
// that GitLab embeds in job traces, e.g. "\x1b[0m", "\x1b[32;1m", "\x1b[0K".
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// sectionMarker matches GitLab fold markers left in a trace once the trailing
// ANSI erase is gone, e.g. "section_start:1620629123:step_script\r" or
// "section_end:1620629130:step_script[collapsed=true]\r". The optional
// trailing \r is consumed so the following log line isn't clobbered on redraw.
var sectionMarker = regexp.MustCompile(`section_(?:start|end):\d+:[^\r\n]*\r?`)

func stripSections(s string) string {
	return sectionMarker.ReplaceAllString(s, "")
}
