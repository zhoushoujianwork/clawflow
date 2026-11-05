package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// The PostgreSQL import runs offline: it holds the controller lock for its
// whole duration, so no controller can dispatch or accept results, and it
// refuses a source that still has executing (leased) tasks.

// importSession is the shared setup of plan/import/verify/cutover.
type importSession struct {
	cmd     *cobra.Command
	source  *store.Source
	scratch *store.Store
	plan    *store.Plan
	release func()
	dsn     string
}

func (s *importSession) Close() {
	if s.source != nil {
		_ = s.source.Close()
	}
	if s.scratch != nil {
		_ = s.scratch.Close()
	}
	if s.release != nil {
		s.release()
	}
}

// openImportSession acquires the controller lock, connects to the source and
// builds the plan. The scratch SQLite store only supplies the destination
// schema for comparison.
func openImportSession(cmd *cobra.Command, sourceDSN string) (*importSession, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.Settings.Database.Engine == store.EngineSQLite {
		if rec, _ := store.LoadCutover(); rec != nil && !rec.RolledBack {
			return nil, errors.New("this host already uses SQLite; run `clawflow db rollback` first if you need to import again")
		}
	}
	dsn := strings.TrimSpace(sourceDSN)
	if dsn == "" {
		if dsn, err = store.ResolveDSN(cfg); err != nil {
			return nil, err
		}
	}
	lock, err := controllerLock()
	if err != nil {
		return nil, fmt.Errorf("a controller is active, refusing to import: %w", err)
	}
	sess := &importSession{cmd: cmd, dsn: dsn, release: func() { filelock.Release(lock) }}
	ok := false
	defer func() {
		if !ok {
			sess.Close()
		}
	}()
	scratchDir, err := os.MkdirTemp("", "clawflow-import-scratch-")
	if err != nil {
		return nil, err
	}
	sess.release = func() { filelock.Release(lock); _ = os.RemoveAll(scratchDir) }
	if sess.scratch, err = store.Open(cmd.Context(), filepath.Join(scratchDir, store.FileName)); err != nil {
		return nil, err
	}
	if _, err = sess.scratch.Migrate(cmd.Context(), store.Migrations); err != nil {
		return nil, err
	}
	if sess.source, err = store.OpenSource(cmd.Context(), dsn); err != nil {
		return nil, err
	}
	if sess.plan, err = sess.source.Inspect(cmd.Context(), sess.scratch); err != nil {
		return nil, err
	}
	ok = true
	return sess, nil
}

func printPlan(cmd *cobra.Command, p *store.Plan, asJSON bool) error {
	out := cmd.OutOrStdout()
	if asJSON {
		return json.NewEncoder(out).Encode(p)
	}
	fmt.Fprintf(out, "Source schema version: %d\n", p.SourceVersion)
	if p.SnapshotID != "" {
		fmt.Fprintf(out, "Source snapshot:       %s\n", p.SnapshotID)
	}
	for _, t := range p.Tables {
		fmt.Fprintf(out, "  %-24s %8d rows\n", t.Name, t.Rows)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	for _, b := range p.Blockers {
		fmt.Fprintf(out, "BLOCKER: %s\n", b)
	}
	return nil
}

func planError(p *store.Plan) error {
	if len(p.Blockers) > 0 {
		return fmt.Errorf("%d blocker(s); nothing was imported or changed", len(p.Blockers))
	}
	return nil
}

func newDBPlanCmd() *cobra.Command {
	var dsn string
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "plan",
		Short:        "Inspect the PostgreSQL source and report what an import would copy (read-only)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sess, err := openImportSession(cmd, dsn)
			if err != nil {
				return err
			}
			defer sess.Close()
			if err := printPlan(cmd, sess.plan, asJSON); err != nil {
				return err
			}
			return planError(sess.plan)
		},
	}
	cmd.Flags().StringVar(&dsn, "source-dsn", "", "PostgreSQL DSN (default: CLAWFLOW_DATABASE_DSN or settings.database.dsn)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the plan as JSON")
	return cmd
}

func newDBImportCmd() *cobra.Command {
	var dsn string
	var restart bool
	cmd := &cobra.Command{
		Use:          "import",
		Short:        "Copy the PostgreSQL snapshot into a staging SQLite file (resumable; does not switch the host)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sess, err := openImportSession(cmd, dsn)
			if err != nil {
				return err
			}
			defer sess.Close()
			if err := printPlan(cmd, sess.plan, false); err != nil {
				return err
			}
			if err := planError(sess.plan); err != nil {
				return err
			}
			res, err := sess.source.Import(cmd.Context(), sess.plan, restart, func(m string) { fmt.Fprintln(cmd.OutOrStdout(), m) })
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Staging database: %s\nNext: clawflow db verify, then clawflow db cutover\n", res.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&dsn, "source-dsn", "", "PostgreSQL DSN (default: CLAWFLOW_DATABASE_DSN or settings.database.dsn)")
	cmd.Flags().BoolVar(&restart, "restart", false, "Discard an existing staging file and import again")
	return cmd
}

func newDBVerifyCmd() *cobra.Command {
	var dsn string
	cmd := &cobra.Command{
		Use:          "verify",
		Short:        "Validate the staging database against a fresh read of the source",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sess, err := openImportSession(cmd, dsn)
			if err != nil {
				return err
			}
			defer sess.Close()
			if err := planError(sess.plan); err != nil {
				_ = printPlan(cmd, sess.plan, false)
				return err
			}
			staging := store.StagingPath(sess.plan.SnapshotID)
			if _, err := os.Stat(staging); err != nil {
				return fmt.Errorf("no staging import matches the current source snapshot %s; run `clawflow db import`", sess.plan.SnapshotID[:12])
			}
			s, err := store.Open(cmd.Context(), staging)
			if err != nil {
				return err
			}
			defer s.Close()
			report, err := s.Verify(cmd.Context(), sess.source, snapshot.DataDir(), config.StateDir())
			if err != nil {
				return err
			}
			for _, w := range report.Warnings {
				fmt.Fprintf(cmd.OutOrStdout(), "warning: %s\n", w)
			}
			for _, b := range report.Blockers {
				fmt.Fprintf(cmd.OutOrStdout(), "BLOCKER: %s\n", b)
			}
			if !report.OK() {
				return fmt.Errorf("verification found %d blocker(s)", len(report.Blockers))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Verification passed. Next: clawflow db cutover")
			return nil
		},
	}
	cmd.Flags().StringVar(&dsn, "source-dsn", "", "PostgreSQL DSN (default: CLAWFLOW_DATABASE_DSN or settings.database.dsn)")
	return cmd
}

func newDBCutoverCmd() *cobra.Command {
	var dsn string
	cmd := &cobra.Command{
		Use:          "cutover",
		Short:        "Verify, publish the staging database and switch the host configuration to SQLite",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sess, err := openImportSession(cmd, dsn)
			if err != nil {
				return err
			}
			defer sess.Close()
			if err := planError(sess.plan); err != nil {
				_ = printPlan(cmd, sess.plan, false)
				return err
			}
			rec, err := store.Cutover(cmd.Context(), sess.plan, sess.source, store.CutoverOptions{SourceDSN: sess.dsn})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Cutover complete.\n  SQLite:        %s\n  Config backup: %s\n  Source:        %s (untouched)\n", rec.SQLitePath, rec.ConfigBackup, rec.SourceDSN)
			fmt.Fprintln(out, "Start the controller and run the acceptance checks, then retire the PostgreSQL LaunchAgent with macos/install-host.py --retire-postgres.")
			return nil
		},
	}
	cmd.Flags().StringVar(&dsn, "source-dsn", "", "PostgreSQL DSN (default: CLAWFLOW_DATABASE_DSN or settings.database.dsn)")
	return cmd
}

func newDBRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "rollback",
		Short:        "Restore the pre-cutover configuration if SQLite has accepted no new work",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lock, err := controllerLock()
			if err != nil {
				return fmt.Errorf("a controller is active, refusing to roll back: %w", err)
			}
			defer filelock.Release(lock)
			rec, err := store.Rollback(context.Background())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Rolled back. Configuration restored from %s; SQLite file kept at %s.\n", rec.ConfigBackup, rec.SQLitePath)
			return nil
		},
	}
}
