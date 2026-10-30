package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// requireStore is the required-database gate used by commands that touch
// state (`run`, `web`). It is a variable only so tests of those commands can
// substitute a stub; production code must not reassign it.
var requireStore = func(ctx context.Context) (*store.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return store.EnsureReady(ctx, cfg)
}

// activeStore is the Store opened by the gate for the current command. Later
// slices read it; it is closed when the command exits.
var activeStore *store.Store

// gateStore runs requireStore, records the Store in activeStore and returns a
// release func that must be deferred by the caller.
func gateStore(ctx context.Context) (release func(), err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := requireStore(ctx)
	if err != nil {
		return func() {}, err
	}
	activeStore = s
	snapshot.BindRunStore(s)
	return func() {
		activeStore = nil
		snapshot.BindRunStore(nil)
		_ = s.Close()
	}, nil
}

// NewDBCmd wires `clawflow db`: the embedded SQLite store and the one-time
// PostgreSQL import.
func NewDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Manage the embedded SQLite store (status, migrate, backup, PostgreSQL import)",
		Long: `ClawFlow keeps durable state in an embedded SQLite file owned by the
controller (<state dir>/data/clawflow.sqlite3). A fresh host needs no database
server or DSN. Hosts that still use PostgreSQL move over once with
plan, import, verify and cutover; see docs/sqlite-host-runbook.md.`,
	}
	cmd.AddCommand(newDBMigrateCmd(), newDBStatusCmd(), newDBImportRunsCmd(), newDBBackupCmd(),
		newDBPlanCmd(), newDBImportCmd(), newDBVerifyCmd(), newDBCutoverCmd(), newDBRollbackCmd())
	return cmd
}

// openDB opens the host database without requiring a completed migration, for
// `db migrate`, `db status` and `db backup`.
func openDB(cmd *cobra.Command) (*store.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	path, err := store.ResolvePath(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	return store.Open(ctx, path)
}

func newDBMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "migrate",
		Short:        "Apply pending database migrations",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openDB(cmd)
			if err != nil {
				return err
			}
			defer s.Close()
			fmt.Fprintf(cmd.OutOrStdout(), "Database: %s\n", s.Path())
			applied, err := s.Migrate(cmd.Context(), store.Migrations)
			for _, v := range applied {
				fmt.Fprintf(cmd.OutOrStdout(), "applied migration %d\n", v)
			}
			if err != nil {
				return err
			}
			if len(applied) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "database is up to date")
			}
			return importRunHistoryIfEmpty(cmd, s)
		},
	}
}

func newDBStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show database location, integrity and migration state",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openDB(cmd)
			if err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "Database:     unavailable")
				return err
			}
			defer s.Close()
			ctx := cmd.Context()
			fmt.Fprintf(cmd.OutOrStdout(), "Database:     SQLite %s\n", s.Path())
			if err := s.IntegrityCheck(ctx); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Integrity:    failed: %v\n", err)
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Integrity:    ok")
			if rec, err := store.LoadCutover(); err == nil && rec != nil {
				state := "active"
				if rec.RolledBack {
					state = "rolled back"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Cutover:      %s at %s (source %s)\n", state, rec.CutoverAt.Format(time.RFC3339), rec.SourceDSN)
			}
			statuses, err := s.Status(ctx, store.Migrations)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Migrations:")
			for _, st := range statuses {
				state := "pending"
				if st.Applied {
					state = "applied"
					if st.AppliedAt != nil {
						state += " " + st.AppliedAt.UTC().Format(time.RFC3339)
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %03d %-24s %s\n", st.Version, st.Name, state)
			}
			return nil
		},
	}
}

func newDBBackupCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "backup <destination>",
		Short:        "Write a consistent backup (WAL included) of the live database",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openDB(cmd)
			if err != nil {
				return err
			}
			defer s.Close()
			if err := s.Backup(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "backup written to %s\n", args[0])
			return nil
		},
	}
}

func newDBImportRunsCmd() *cobra.Command {
	return &cobra.Command{Use: "import-runs", Short: "Import existing run metadata files into the database", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		release, err := gateStore(cmd.Context())
		if err != nil {
			return err
		}
		defer release()
		counts, err := activeStore.ImportRuns(cmd.Context(), snapshot.DataDir(), reportRunImport(cmd))
		fmt.Fprintf(cmd.OutOrStdout(), "imported=%d updated=%d skipped=%d invalid=%d\n", counts.Imported, counts.Updated, counts.Skipped, counts.Invalid)
		return err
	}}
}

// The upgrade candidate also runs db migrate, so both paths share this backfill.
func importRunHistoryIfEmpty(cmd *cobra.Command, s *store.Store) error {
	var exists bool
	if err := s.DB().WithContext(cmd.Context()).Raw("SELECT EXISTS (SELECT 1 FROM runs)").Scan(&exists).Error; err != nil {
		return fmt.Errorf("check run history: %w", err)
	}
	if exists {
		return nil
	}
	counts, err := s.ImportRuns(cmd.Context(), snapshot.DataDir(), reportRunImport(cmd))
	fmt.Fprintf(cmd.OutOrStdout(), "imported=%d updated=%d skipped=%d invalid=%d\n", counts.Imported, counts.Updated, counts.Skipped, counts.Invalid)
	return err
}

func reportRunImport(cmd *cobra.Command) func(store.ImportDiagnostic) {
	return func(d store.ImportDiagnostic) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", d.Status, d.Path, d.Reason)
	}
}
