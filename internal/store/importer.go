package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The importer is the only code that still talks to PostgreSQL. It reads the
// source inside one REPEATABLE READ read-only transaction (a consistent
// snapshot), copies every table into a separate temporary SQLite file and
// never writes to the source.

// SourceSchemaVersion is the newest PostgreSQL migration the importer
// understands. The SQLite schema carries the same logical tables.
const SourceSchemaVersion = 11

// ImportTables lists the copied tables in foreign-key order.
var ImportTables = []string{
	"coordinator_workers", "coordinator_tasks", "coordinator_tokens", "coordinator_invitations",
	"coordinator_audit", "task_attempts", "inbox_events", "effect_intents",
	"actions_attachments", "actions_evidence",
	"runs", "pilot_runs", "run_model_usage",
	"scheduled_checks", "scheduled_check_runs",
}

// TableStat identifies a table's content. Digest is an order-independent
// multiset hash of row digests, so source and destination compare equal
// regardless of row order or collation.
type TableStat struct {
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
	Digest string `json:"digest"`
}

// Plan is the read-only result of inspecting the source.
type Plan struct {
	SnapshotID    string      `json:"snapshot_id"`
	SourceVersion int         `json:"source_schema_version"`
	Tables        []TableStat `json:"tables"`
	Usage         Usage       `json:"usage"`
	Blockers      []string    `json:"blockers"`
	Warnings      []string    `json:"warnings"`
}

// Usage holds totals compared between source and destination.
type Usage struct {
	Runs         UsageTotals `json:"runs"`
	PilotRuns    UsageTotals `json:"pilot_runs"`
	ModelUsage   UsageTotals `json:"run_model_usage"`
	ArtifactRefs int64       `json:"artifact_refs"`
}

// UsageTotals are exact integer sums plus the cost sum in micro-dollars.
type UsageTotals struct {
	Rows          int64 `json:"rows"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CostMicroUSD  int64 `json:"cost_micro_usd"`
}

// Source is an open, consistent PostgreSQL snapshot.
type Source struct {
	conn *sql.DB
	tx   *sql.Tx
	dsn  string
}

// OpenSource connects and starts the snapshot transaction. Errors never carry
// the raw DSN password.
func OpenSource(ctx context.Context, dsn string) (*Source, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, openSourceError(dsn, err)
	}
	conn, err := db.DB()
	if err != nil {
		return nil, openSourceError(dsn, err)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		_ = conn.Close()
		return nil, openSourceError(dsn, err)
	}
	return &Source{conn: conn, tx: tx, dsn: dsn}, nil
}

func openSourceError(dsn string, err error) error {
	return &redactedError{msg: fmt.Sprintf("connect to source %s: %s", MaskDSN(dsn), redact(err.Error(), dsn)), err: err}
}

// Close ends the read-only snapshot.
func (s *Source) Close() error {
	if s == nil {
		return nil
	}
	_ = s.tx.Rollback()
	return s.conn.Close()
}

type column struct{ name, udt string }

func (s *Source) columns(ctx context.Context, table string) ([]column, error) {
	rows, err := s.tx.QueryContext(ctx, `SELECT column_name, udt_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.udt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sqliteColumn describes a destination column.
type sqliteColumn struct {
	Name string
	Type string
}

func sqliteColumns(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, table string) ([]sqliteColumn, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sqliteColumn
	for rows.Next() {
		var c sqliteColumn
		if err := rows.Scan(&c.Name, &c.Type); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// selectExpr maps a PostgreSQL column to a value the SQLite driver stores
// natively: jsonb as validated text, numeric as a float.
func selectExpr(c column) string {
	q := `"` + c.name + `"`
	switch c.udt {
	case "jsonb", "json":
		return q + "::text"
	case "numeric":
		return q + "::float8"
	}
	return q
}

// Inspect builds the plan: schema compatibility, per-table statistics, the
// snapshot identity, usage totals and blockers.
func (s *Source) Inspect(ctx context.Context, dest *Store) (*Plan, error) {
	p := &Plan{}
	block := func(format string, a ...any) { p.Blockers = append(p.Blockers, fmt.Sprintf(format, a...)) }

	// Source schema versions must be exactly the ones this importer knows.
	versions := map[int]bool{}
	rows, err := s.tx.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		block("source has no readable schema_migrations: %v (is this a ClawFlow PostgreSQL database?)", redact(err.Error(), s.dsn))
		return p, nil
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		versions[v] = true
		if v > p.SourceVersion {
			p.SourceVersion = v
		}
	}
	rows.Close()
	if p.SourceVersion > SourceSchemaVersion {
		block("source schema version %d is newer than the importer supports (%d)", p.SourceVersion, SourceSchemaVersion)
	}
	for v := 1; v <= SourceSchemaVersion; v++ {
		if !versions[v] {
			block("source migration %d is not applied; run the previous ClawFlow release's `db migrate` first", v)
		}
	}

	// Tables the importer does not copy would be silently dropped: refuse.
	known := map[string]bool{"schema_migrations": true}
	for _, t := range ImportTables {
		known[t] = true
	}
	trows, err := s.tx.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for trows.Next() {
		var name string
		if err := trows.Scan(&name); err != nil {
			trows.Close()
			return nil, err
		}
		present[name] = true
		if !known[name] {
			block("source table %q is not supported by the importer; its rows would be omitted", name)
		}
	}
	trows.Close()

	for _, table := range ImportTables {
		if !present[table] {
			block("source table %q is missing", table)
			continue
		}
		src, err := s.columns(ctx, table)
		if err != nil {
			return nil, err
		}
		dst, err := sqliteColumns(ctx, dest.sql, table)
		if err != nil {
			return nil, err
		}
		have := map[string]bool{}
		for _, c := range src {
			have[c.name] = true
		}
		want := map[string]bool{}
		for _, c := range dst {
			want[c.Name] = true
			if !have[c.Name] {
				block("source table %s lacks column %s", table, c.Name)
			}
		}
		for _, c := range src {
			if !want[c.name] {
				block("source column %s.%s has no SQLite destination and would be omitted", table, c.name)
			}
		}
	}
	if len(p.Blockers) > 0 {
		return p, nil
	}

	for _, table := range ImportTables {
		stat, err := s.tableStat(ctx, dest, table)
		if err != nil {
			return nil, err
		}
		p.Tables = append(p.Tables, stat)
	}
	p.SnapshotID = snapshotID(p.SourceVersion, p.Tables)
	if p.Usage, err = s.usage(ctx); err != nil {
		return nil, err
	}

	var leased, pending, uncertain int64
	_ = s.tx.QueryRowContext(ctx, `SELECT count(*) FROM coordinator_tasks WHERE state = 'leased'`).Scan(&leased)
	_ = s.tx.QueryRowContext(ctx, `SELECT count(*) FROM coordinator_tasks WHERE state = 'pending'`).Scan(&pending)
	_ = s.tx.QueryRowContext(ctx, `SELECT count(*) FROM effect_intents WHERE status IN ('pending','uncertain')`).Scan(&uncertain)
	if leased > 0 {
		block("%d task(s) are leased (executing); drain the host and wait for them to finish before importing", leased)
	}
	if pending > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d pending task(s) are imported unchanged; dispatch stays stopped until the cutover completes", pending))
	}
	if uncertain > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d effect intent(s) are pending or uncertain; they keep their identities and are reconciled after cutover", uncertain))
	}
	return p, nil
}

func snapshotID(version int, tables []TableStat) string {
	h := sha256.New()
	fmt.Fprintf(h, "v%d\n", version)
	for _, t := range tables {
		fmt.Fprintf(h, "%s %d %s\n", t.Name, t.Rows, t.Digest)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Source) tableStat(ctx context.Context, dest *Store, table string) (TableStat, error) {
	dst, err := sqliteColumns(ctx, dest.sql, table)
	if err != nil {
		return TableStat{}, err
	}
	src, err := s.columns(ctx, table)
	if err != nil {
		return TableStat{}, err
	}
	udt := map[string]column{}
	for _, c := range src {
		udt[c.name] = c
	}
	exprs := make([]string, len(dst))
	for i, c := range dst {
		exprs[i] = selectExpr(udt[c.Name])
	}
	rows, err := s.tx.QueryContext(ctx, `SELECT `+strings.Join(exprs, ", ")+` FROM "`+table+`"`)
	if err != nil {
		return TableStat{}, err
	}
	defer rows.Close()
	return foldRows(table, rows, len(dst))
}

func foldRows(table string, rows *sql.Rows, n int) (TableStat, error) {
	acc := newMultiset()
	var count int64
	vals := make([]any, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return TableStat{}, err
		}
		acc.add(rowDigest(vals))
		count++
	}
	if err := rows.Err(); err != nil {
		return TableStat{}, err
	}
	return TableStat{Name: table, Rows: count, Digest: acc.hex()}, nil
}

// multiset is an order-independent accumulator of 32-byte digests.
type multiset [4]uint64

func newMultiset() *multiset { return &multiset{} }

func (m *multiset) add(d [32]byte) {
	for i := range m {
		m[i] += binary.BigEndian.Uint64(d[i*8:])
	}
}

func (m *multiset) hex() string {
	var b [32]byte
	for i, v := range m {
		binary.BigEndian.PutUint64(b[i*8:], v)
	}
	return hex.EncodeToString(b[:])
}

// rowDigest hashes normalized values with type tags and length prefixes, so a
// NULL, an empty string and a zero never collide.
func rowDigest(vals []any) [32]byte {
	h := sha256.New()
	for _, v := range vals {
		tag, s := normalize(v)
		fmt.Fprintf(h, "%s%d:", tag, len(s))
		h.Write([]byte(s))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// normalize renders a driver value identically for both databases. Booleans
// and 0/1 integers are the same value (SQLite has no boolean storage class).
func normalize(v any) (string, string) {
	switch x := v.(type) {
	case nil:
		return "n", ""
	case bool:
		if x {
			return "i", "1"
		}
		return "i", "0"
	case int64:
		return "i", strconv.FormatInt(x, 10)
	case int32:
		return "i", strconv.FormatInt(int64(x), 10)
	case int:
		return "i", strconv.Itoa(x)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return "f", strconv.FormatFloat(x, 'f', -1, 64)
		}
		return "f", strconv.FormatFloat(x, 'g', -1, 64)
	case time.Time:
		return "t", x.UTC().Format(time.RFC3339Nano)
	case []byte:
		return "b", hex.EncodeToString(x)
	case string:
		return "s", x
	}
	return "s", fmt.Sprint(v)
}

// usage sums the usage-bearing tables with exact integer arithmetic. Cost is
// compared in micro-dollars to avoid float accumulation differences.
func (s *Source) usage(ctx context.Context) (Usage, error) {
	var u Usage
	for _, e := range []struct {
		table string
		dst   *UsageTotals
	}{{"runs", &u.Runs}, {"pilot_runs", &u.PilotRuns}, {"run_model_usage", &u.ModelUsage}} {
		err := s.tx.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(input_tokens),0)::bigint, COALESCE(SUM(output_tokens),0)::bigint,
 COALESCE(SUM(cache_read_input_tokens),0)::bigint, COALESCE(SUM(cache_creation_input_tokens),0)::bigint,
 COALESCE(SUM(round(cost_usd*1000000)),0)::bigint FROM `+e.table).Scan(&e.dst.Rows, &e.dst.InputTokens, &e.dst.OutputTokens, &e.dst.CacheRead, &e.dst.CacheCreation, &e.dst.CostMicroUSD)
		if err != nil {
			return u, err
		}
	}
	err := s.tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM runs WHERE COALESCE(events_path,'') <> '' OR COALESCE(log_path,'') <> '') + (SELECT count(*) FROM pilot_runs WHERE COALESCE(events_path,'') <> '' OR COALESCE(log_path,'') <> '')`).Scan(&u.ArtifactRefs)
	return u, err
}

// destUsage computes the same totals from SQLite.
func destUsage(ctx context.Context, d *Store) (Usage, error) {
	var u Usage
	for _, e := range []struct {
		table string
		dst   *UsageTotals
	}{{"runs", &u.Runs}, {"pilot_runs", &u.PilotRuns}, {"run_model_usage", &u.ModelUsage}} {
		err := d.sql.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
 COALESCE(SUM(cache_read_input_tokens),0), COALESCE(SUM(cache_creation_input_tokens),0),
 COALESCE(SUM(CAST(round(cost_usd*1000000) AS INTEGER)),0) FROM `+e.table).Scan(&e.dst.Rows, &e.dst.InputTokens, &e.dst.OutputTokens, &e.dst.CacheRead, &e.dst.CacheCreation, &e.dst.CostMicroUSD)
		if err != nil {
			return u, err
		}
	}
	err := d.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM runs WHERE COALESCE(events_path,'') <> '' OR COALESCE(log_path,'') <> '') + (SELECT count(*) FROM pilot_runs WHERE COALESCE(events_path,'') <> '' OR COALESCE(log_path,'') <> '')`).Scan(&u.ArtifactRefs)
	return u, err
}

const progressDDL = `CREATE TABLE IF NOT EXISTS import_progress (
 snapshot_id text NOT NULL, table_name text PRIMARY KEY, rows integer NOT NULL, digest text NOT NULL
)`

// ImportResult describes a completed staging import.
type ImportResult struct {
	Plan    *Plan
	Path    string
	Resumed []string // tables skipped because a previous run completed them
}

// StagingPath is the temporary SQLite file for a snapshot. It sits in the
// destination directory so the final rename is atomic.
func StagingPath(snapshotID string) string {
	return DefaultPath() + ".import-" + snapshotID[:16]
}

// Import copies the snapshot into the staging file. It is restartable: a
// table is committed together with its progress row, so a crash leaves only
// whole tables. The staging file name embeds the snapshot identity, so a
// changed source never resumes another snapshot's partial copy. restart
// deletes this snapshot's staging file (ClawFlow's own temporary copy).
func (s *Source) Import(ctx context.Context, plan *Plan, restart bool, progress func(string)) (*ImportResult, error) {
	if len(plan.Blockers) > 0 {
		return nil, fmt.Errorf("import blocked: %s", strings.Join(plan.Blockers, "; "))
	}
	if progress == nil {
		progress = func(string) {}
	}
	path := StagingPath(plan.SnapshotID)
	if restart {
		removeDBFiles(path)
	}
	dest, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer dest.Close()
	if _, err := dest.Migrate(ctx, Migrations); err != nil {
		return nil, err
	}
	if err := dest.sqlExec(ctx, progressDDL); err != nil {
		return nil, err
	}
	res := &ImportResult{Plan: plan, Path: path}

	done := map[string]TableStat{}
	prows, err := dest.sql.QueryContext(ctx, `SELECT table_name, rows, digest FROM import_progress`)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var t TableStat
		if err := prows.Scan(&t.Name, &t.Rows, &t.Digest); err != nil {
			prows.Close()
			return nil, err
		}
		done[t.Name] = t
	}
	prows.Close()

	var finished int
	if err := dest.sql.QueryRowContext(ctx, `SELECT count(*) FROM import_provenance WHERE source_snapshot_id = ?`, plan.SnapshotID).Scan(&finished); err != nil {
		return nil, err
	}
	if finished > 0 {
		progress("staging import already complete")
		return res, nil
	}

	for _, stat := range plan.Tables {
		if d, ok := done[stat.Name]; ok && d.Rows == stat.Rows && d.Digest == stat.Digest {
			res.Resumed = append(res.Resumed, stat.Name)
			progress(fmt.Sprintf("%s: %d rows already imported", stat.Name, stat.Rows))
			continue
		}
		if err := s.copyTable(ctx, dest, plan, stat); err != nil {
			return nil, err
		}
		progress(fmt.Sprintf("%s: %d rows", stat.Name, stat.Rows))
	}

	tables, _ := json.Marshal(struct {
		Tables []TableStat `json:"tables"`
		Source []int       `json:"source_migrations"`
	}{plan.Tables, sourceMigrationList(plan.SourceVersion)})
	tx, err := dest.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_provenance (source_kind, source_schema_version, source_snapshot_id, imported_at, tables) VALUES ('postgres', ?, ?, ?, ?)`,
		plan.SourceVersion, plan.SnapshotID, time.Now().UTC(), string(tables)); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE import_progress`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

func sourceMigrationList(max int) []int {
	out := make([]int, 0, max)
	for v := 1; v <= max; v++ {
		out = append(out, v)
	}
	return out
}

func (s *Store) sqlExec(ctx context.Context, q string, args ...any) error {
	_, err := s.sql.ExecContext(ctx, q, args...)
	return err
}

// copyTable replaces any partial copy of the table and inserts all source rows
// in one transaction. A row the destination rejects (CHECK/FK) aborts the
// table and is reported with its table and ordinal, never skipped.
func (s *Source) copyTable(ctx context.Context, dest *Store, plan *Plan, stat TableStat) error {
	dst, err := sqliteColumns(ctx, dest.sql, stat.Name)
	if err != nil {
		return err
	}
	src, err := s.columns(ctx, stat.Name)
	if err != nil {
		return err
	}
	udt := map[string]column{}
	for _, c := range src {
		udt[c.name] = c
	}
	names := make([]string, len(dst))
	exprs := make([]string, len(dst))
	marks := make([]string, len(dst))
	for i, c := range dst {
		names[i] = `"` + c.Name + `"`
		exprs[i] = selectExpr(udt[c.Name])
		marks[i] = "?"
	}
	rows, err := s.tx.QueryContext(ctx, `SELECT `+strings.Join(exprs, ", ")+` FROM "`+stat.Name+`"`)
	if err != nil {
		return err
	}
	defer rows.Close()

	tx, err := dest.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM "`+stat.Name+`"`); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx, `INSERT INTO "`+stat.Name+`" (`+strings.Join(names, ", ")+`) VALUES (`+strings.Join(marks, ", ")+`)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	vals := make([]any, len(dst))
	ptrs := make([]any, len(dst))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var n int64
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		args := make([]any, len(vals))
		for i, v := range vals {
			args[i] = sqliteArg(v)
		}
		n++
		if _, err := insert.ExecContext(ctx, args...); err != nil {
			return fmt.Errorf("blocker: %s row %d was rejected by SQLite and was not imported: %w", stat.Name, n, err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n != stat.Rows {
		return fmt.Errorf("blocker: %s changed during import (%d rows read, %d expected)", stat.Name, n, stat.Rows)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO import_progress (snapshot_id, table_name, rows, digest) VALUES (?, ?, ?, ?)`, plan.SnapshotID, stat.Name, stat.Rows, stat.Digest); err != nil {
		return err
	}
	return tx.Commit()
}

// sqliteArg stores times in UTC, so lease comparisons are consistent.
func sqliteArg(v any) any {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	return v
}

func removeDBFiles(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

// DiscardStaging removes a staging file (ClawFlow's own temporary copy).
func DiscardStaging(snapshotID string) { removeDBFiles(StagingPath(snapshotID)) }

// VerifyReport is the outcome of validating a staged or published store.
type VerifyReport struct {
	SnapshotID string      `json:"snapshot_id"`
	Tables     []TableStat `json:"tables"`
	Blockers   []string    `json:"blockers"`
	Warnings   []string    `json:"warnings"`
}

// OK reports whether verification found no blocker.
func (r *VerifyReport) OK() bool { return len(r.Blockers) == 0 }

// Provenance returns the snapshot recorded by a completed import, or "" when
// the database was not produced by the importer.
func (s *Store) Provenance(ctx context.Context) (snapshot string, tables []TableStat, err error) {
	var raw string
	err = s.sql.QueryRowContext(ctx, `SELECT source_snapshot_id, tables FROM import_provenance ORDER BY id DESC LIMIT 1`).Scan(&snapshot, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var doc struct {
		Tables []TableStat `json:"tables"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", nil, err
	}
	return snapshot, doc.Tables, nil
}

// TableStats computes statistics for the given tables from SQLite.
func (s *Store) TableStats(ctx context.Context, tables []string) ([]TableStat, error) {
	var out []TableStat
	for _, table := range tables {
		cols, err := sqliteColumns(ctx, s.sql, table)
		if err != nil {
			return nil, err
		}
		names := make([]string, len(cols))
		for i, c := range cols {
			names[i] = `"` + c.Name + `"`
		}
		rows, err := s.sql.QueryContext(ctx, `SELECT `+strings.Join(names, ", ")+` FROM "`+table+`"`)
		if err != nil {
			return nil, err
		}
		stat, err := foldRows(table, rows, len(cols))
		rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, stat)
	}
	return out, nil
}

// Verify validates a destination store: SQLite integrity and foreign keys,
// migrations, recorded row counts and payload digests, usage totals and
// artifact paths. When src is non-nil the destination is also compared with a
// fresh read of the source, which proves the source did not change.
func (s *Store) Verify(ctx context.Context, src *Source, artifactRoots ...string) (*VerifyReport, error) {
	r := &VerifyReport{}
	block := func(format string, a ...any) { r.Blockers = append(r.Blockers, fmt.Sprintf(format, a...)) }
	if err := s.IntegrityCheck(ctx); err != nil {
		block("%v", err)
	}
	statuses, err := s.Status(ctx, Migrations)
	if err != nil {
		return nil, err
	}
	for _, st := range statuses {
		if !st.Applied {
			block("SQLite migration %d (%s) is not applied", st.Version, st.Name)
		}
	}
	var unfinished int
	if err := s.sql.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='import_progress'`).Scan(&unfinished); err == nil && unfinished > 0 {
		block("import is not finished (import_progress still exists)")
	}
	snapshot, recorded, err := s.Provenance(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot == "" {
		block("no import provenance: this database was not produced by a completed import")
		return r, nil
	}
	r.SnapshotID = snapshot
	actual, err := s.TableStats(ctx, ImportTables)
	if err != nil {
		return nil, err
	}
	r.Tables = actual
	byName := map[string]TableStat{}
	for _, t := range recorded {
		byName[t.Name] = t
	}
	for _, a := range actual {
		want, ok := byName[a.Name]
		switch {
		case !ok:
			block("table %s is absent from the import provenance", a.Name)
		case want.Rows != a.Rows:
			block("table %s has %d rows, source snapshot had %d", a.Name, a.Rows, want.Rows)
		case want.Digest != a.Digest:
			block("table %s payload digest differs from the source snapshot", a.Name)
		}
	}
	if src != nil {
		plan, err := src.Inspect(ctx, s)
		if err != nil {
			return nil, err
		}
		r.Blockers = append(r.Blockers, plan.Blockers...)
		if len(plan.Blockers) == 0 {
			if plan.SnapshotID != snapshot {
				block("the source changed after the snapshot was imported; discard the staging file and import again")
			}
			du, err := destUsage(ctx, s)
			if err != nil {
				return nil, err
			}
			if du != plan.Usage {
				block("usage totals differ: source %+v, SQLite %+v", plan.Usage, du)
			}
		}
	}
	missing, err := s.missingArtifacts(ctx, artifactRoots)
	if err != nil {
		return nil, err
	}
	if missing > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d run artifact path(s) do not exist on this host; they are kept as recorded and not recreated", missing))
	}
	sort.Strings(r.Blockers)
	return r, nil
}

// missingArtifacts counts non-empty artifact paths that do not resolve to a
// file. Relative paths resolve against each root.
func (s *Store) missingArtifacts(ctx context.Context, roots []string) (int, error) {
	missing := 0
	for _, table := range []string{"runs", "pilot_runs"} {
		rows, err := s.sql.QueryContext(ctx, `SELECT COALESCE(events_path,''), COALESCE(log_path,'') FROM `+table)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var a, b string
			if err := rows.Scan(&a, &b); err != nil {
				rows.Close()
				return 0, err
			}
			for _, p := range []string{a, b} {
				if p != "" && !artifactExists(p, roots) {
					missing++
				}
			}
		}
		rows.Close()
	}
	return missing, nil
}

func artifactExists(p string, roots []string) bool {
	if strings.HasPrefix(p, "/") {
		_, err := os.Stat(p)
		return err == nil
	}
	for _, r := range roots {
		if _, err := os.Stat(strings.TrimRight(r, "/") + "/" + strings.TrimPrefix(p, "./")); err == nil {
			return true
		}
	}
	return false
}

// Fingerprint summarizes every imported table. A cutover records it; a later
// difference proves SQLite accepted new work, so rollback needs reconciliation.
func (s *Store) Fingerprint(ctx context.Context) (string, error) {
	stats, err := s.TableStats(ctx, ImportTables)
	if err != nil {
		return "", err
	}
	return snapshotID(0, stats), nil
}
