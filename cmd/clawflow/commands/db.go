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

// NewDBCmd wires `clawflow db`: PostgreSQL schema management.
func NewDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Manage the PostgreSQL store (migrate, status)",
		Long: `ClawFlow keeps durable state in PostgreSQL. Configure the DSN with
settings.database.dsn or the CLAWFLOW_DATABASE_DSN environment variable.
See docs/storage-design.md.`,
	}
	cmd.AddCommand(newDBMigrateCmd(), newDBStatusCmd(), newDBImportRunsCmd())
	return cmd
}

func openDB(cmd *cobra.Command) (*store.Store, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	dsn, err := store.ResolveDSN(cfg)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, "", err
	}
	return s, dsn, nil
}

func newDBMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "migrate",
		Short:        "Apply pending database migrations",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, dsn, err := openDB(cmd)
			if err != nil {
				return err
			}
			defer s.Close()
			fmt.Printf("Database: %s\n", store.MaskDSN(dsn))
			applied, err := s.Migrate(cmd.Context(), store.Migrations)
			for _, v := range applied {
				fmt.Printf("applied migration %d\n", v)
			}
			if err != nil {
				return err
			}
			if len(applied) == 0 {
				fmt.Println("database is up to date")
			}
			return importRunHistoryIfEmpty(cmd, s)
		},
	}
}

func newDBStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show database connectivity and migration state",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			dsn, err := store.ResolveDSN(cfg)
			if err != nil {
				return err
			}
			fmt.Printf("DSN:          %s\n", store.MaskDSN(dsn))
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			s, err := store.Open(ctx, dsn)
			if err != nil {
				fmt.Println("Connectivity: failed")
				return err
			}
			defer s.Close()
			fmt.Println("Connectivity: ok")
			statuses, err := s.Status(ctx, store.Migrations)
			if err != nil {
				return err
			}
			fmt.Println("Migrations:")
			for _, st := range statuses {
				state := "pending"
				if st.Applied {
					state = "applied"
					if st.AppliedAt != nil {
						state += " " + st.AppliedAt.UTC().Format(time.RFC3339)
					}
				}
				fmt.Printf("  %03d %-24s %s\n", st.Version, st.Name, state)
			}
			return nil
		},
	}
}

func newDBImportRunsCmd() *cobra.Command {
	return &cobra.Command{Use: "import-runs", Short: "Import existing run metadata into PostgreSQL", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
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
