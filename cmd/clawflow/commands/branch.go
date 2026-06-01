package commands

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/branch"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// NewBranchCmd groups branch hygiene subcommands: analyze and clean up branches
// that have already been merged into the repo's base branch.
func NewBranchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "branch",
		Short: "Analyze and clean up merged local/remote branches",
		Long: `Scan a configured repo's local clone for branches that have already been
merged into the base branch (e.g. leftover fix/issue-* branches) and clean
them up. 'list' is read-only; 'delete' defaults to a dry-run preview and only
removes branches when --yes is passed.`,
	}
	cmd.AddCommand(newBranchListCmd())
	cmd.AddCommand(newBranchDeleteCmd())
	return cmd
}

// resolveBranchContext loads config, resolves the local clone path and base
// branch, and optionally fetches+prunes so the remote-tracking view is fresh.
func resolveBranchContext(repo string, fetch bool) (localPath, base string, err error) {
	cfg, err := config.Load()
	if err != nil {
		return "", "", err
	}
	repoCfg, ok := cfg.Repos[repo]
	if !ok {
		return "", "", fmt.Errorf("repo %q not found in config", repo)
	}
	localPath, err = resolveLocalPath(cfg, repo, repoCfg)
	if err != nil {
		return "", "", err
	}
	base = repoCfg.BaseBranch
	if base == "" {
		base = "main"
	}
	// Fetching matters most when we report on remote-tracking branches; for a
	// local-only view it is best-effort and a failure should not abort.
	if fetch {
		if ferr := branch.Fetch(localPath); ferr != nil {
			fmt.Fprintf(os.Stderr, "warn: fetch failed, using cached refs: %v\n", ferr)
		}
	}
	return localPath, base, nil
}

func newBranchListCmd() *cobra.Command {
	var repo string
	var includeRemote bool
	var noFetch bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List merged branches eligible for cleanup",
		Example: `  # Merged local branches
  clawflow branch list --repo owner/repo

  # Include merged remote-tracking branches
  clawflow branch list --repo owner/repo --remote`,
		RunE: func(cmd *cobra.Command, args []string) error {
			localPath, base, err := resolveBranchContext(repo, !noFetch && includeRemote)
			if err != nil {
				return err
			}
			branches, err := branch.ListMerged(localPath, base, includeRemote)
			if err != nil {
				return err
			}
			if len(branches) == 0 {
				fmt.Printf("no merged branches to clean up (base: %s)\n", base)
				return nil
			}
			printBranches(cmd.OutOrStdout(), branches)
			fmt.Printf("\n%d merged branch(es) eligible for cleanup (base: %s)\n", len(branches), base)
			fmt.Println("run 'clawflow branch delete --repo " + repo + "' to preview deletion")
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().BoolVar(&includeRemote, "remote", false, "also list merged remote-tracking branches")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "skip 'git fetch --prune' before analyzing remote branches")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func newBranchDeleteCmd() *cobra.Command {
	var repo string
	var includeRemote bool
	var apply bool
	var force bool
	var staleDays int
	var noFetch bool

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete merged branches (dry-run unless --yes)",
		Long: `Delete branches that are merged into the base branch. Without --yes this is
a dry-run that only previews what would be deleted. Local branches are removed
via 'git branch -d' (use --force for -D); remote branches are removed via the
VCS API. The base branch and protected branches (main/master/develop) are
never touched.`,
		Example: `  # Preview which merged local branches would be deleted
  clawflow branch delete --repo owner/repo

  # Actually delete merged local branches
  clawflow branch delete --repo owner/repo --yes

  # Delete merged local + remote branches older than 30 days
  clawflow branch delete --repo owner/repo --remote --stale 30 --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			localPath, base, err := resolveBranchContext(repo, !noFetch && includeRemote)
			if err != nil {
				return err
			}
			branches, err := branch.ListMerged(localPath, base, includeRemote)
			if err != nil {
				return err
			}

			// Apply scope and staleness filters.
			cutoff := time.Time{}
			if staleDays > 0 {
				cutoff = time.Now().AddDate(0, 0, -staleDays)
			}
			var targets []branch.Branch
			for _, b := range branches {
				if staleDays > 0 && (b.LastCommit.IsZero() || b.LastCommit.After(cutoff)) {
					continue
				}
				targets = append(targets, b)
			}

			if len(targets) == 0 {
				fmt.Printf("no branches match the cleanup criteria (base: %s)\n", base)
				return nil
			}

			printBranches(cmd.OutOrStdout(), targets)

			if !apply {
				fmt.Printf("\n%d branch(es) would be deleted — re-run with --yes to apply\n", len(targets))
				return nil
			}

			var client interface {
				DeleteBranch(repo, branch string) error
			}
			if includeRemote {
				c, _, cerr := newVCSClientForRepo(repo)
				if cerr != nil {
					return cerr
				}
				client = c
			}

			deleted, failed := 0, 0
			for _, b := range targets {
				if b.Remote {
					if client == nil {
						continue
					}
					if derr := client.DeleteBranch(repo, b.Name); derr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warn: delete remote %s: %v\n", b.Name, derr)
						failed++
						continue
					}
					fmt.Printf("deleted remote: %s\n", b.Name)
					deleted++
					continue
				}
				if derr := branch.DeleteLocal(localPath, b.Name, force); derr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: delete local %s: %v\n", b.Name, derr)
					failed++
					continue
				}
				fmt.Printf("deleted local: %s\n", b.Name)
				deleted++
			}
			fmt.Printf("\n%d deleted, %d failed (base: %s)\n", deleted, failed, base)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().BoolVar(&includeRemote, "remote", false, "also delete merged remote branches (via VCS API)")
	cmd.Flags().BoolVar(&apply, "yes", false, "actually delete (default is a dry-run preview)")
	cmd.Flags().BoolVar(&force, "force", false, "use 'git branch -D' for local branches (force-delete)")
	cmd.Flags().IntVar(&staleDays, "stale", 0, "only branches whose last commit is older than N days")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "skip 'git fetch --prune' before analyzing remote branches")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func printBranches(w io.Writer, branches []branch.Branch) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SCOPE\tBRANCH\tMERGED\tLAST COMMIT")
	for _, b := range branches {
		last := "unknown"
		if !b.LastCommit.IsZero() {
			last = b.LastCommit.Format("2006-01-02")
		}
		merged := "no"
		if b.Merged {
			merged = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.Scope(), b.Name, merged, last)
	}
	_ = tw.Flush()
}
