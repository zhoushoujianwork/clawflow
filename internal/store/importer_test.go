package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

// seedSource fills the PostgreSQL fixture with one row of every kind. IDs and
// hashes are fixed so identity preservation can be asserted.
func seedSource(t *testing.T, db *gorm.DB) {
	t.Helper()
	at := "2026-10-01T10:00:00Z"
	hash := "\\x" + strings.Repeat("ab", 32)
	for _, q := range []string{
		`INSERT INTO coordinator_workers (id,tags,run_untagged,max_parallel,roles,backend,model,created_at,updated_at,paused,durable) VALUES ('w1','["codex","host-a"]',false,2,'["developer"]','codex','m','` + at + `','` + at + `',true,true)`,
		`INSERT INTO coordinator_tasks (id,kind,repo,issue,pr,operator,payload,tags,priority,queue_position,state,attempt,lease_owner,lease_expires_at,fencing_token,result,created_at,updated_at,application_attempts,application_error,result_digest)
		 VALUES ('t-done','implement','o/r',1,0,'implement','{"k":{"a":1}}','["codex"]',1,3,'done',2,'w1',NULL,7,'{"status":"success","head_sha":"abc"}','` + at + `','` + at + `',1,'','dg1'),
		        ('t-pend','implement','o/r',2,0,'implement','{}','[]',0,4,'pending',0,'',NULL,0,NULL,'` + at + `','` + at + `',0,'','')`,
		`INSERT INTO coordinator_tokens (name,hash,created_at,last_seen,revoked,scope) VALUES ('tok','` + hash + `','` + at + `','` + at + `',true,'admin')`,
		`INSERT INTO coordinator_invitations (name,hash,expires_at,consumed) VALUES ('inv','` + hash + `','` + at + `',true)`,
		`INSERT INTO coordinator_audit (worker,task,action,created_at) VALUES ('w1','t-done','claim','` + at + `'),('w1','t-done','complete','` + at + `')`,
		`INSERT INTO task_attempts (task_id,attempt_number,lease_epoch,worker_id,kind,status,started_at,ended_at,error_kind,result_digest) VALUES ('t-done',1,6,'w1','infra','failed','` + at + `','` + at + `','lease','' ),('t-done',2,7,'w1','product','done','` + at + `','` + at + `','','dg1')`,
		`INSERT INTO inbox_events (source,external_id,task_id,received_at,processed_at) VALUES ('github','e1','t-done','` + at + `','` + at + `')`,
		`INSERT INTO effect_intents (id,task_id,lease_epoch,"position",kind,repo,number,idempotency_key,payload_digest,payload,status,applied_reference,updated_at) VALUES ('e-1','t-done',7,0,'comment','o/r',1,'idem-1','pd','{"body":"x"}','applied','c-9','` + at + `')`,
		`INSERT INTO actions_attachments (key,payload) VALUES ('k1','{"a":true}')`,
		`INSERT INTO actions_evidence (key,run_id,attempt,payload) VALUES ('k1',5,1,'{"b":[1,2]}')`,
		`INSERT INTO runs (run_dir,repo,project,issue_number,operator,status,started_at,duration_ms,cost_usd,input_tokens,output_tokens,cache_read_input_tokens,cache_creation_input_tokens,usage_reported,events_path,log_path,payload,machine,offload_run_id)
		 VALUES ('runs/a','o/r','p',1,'implement','success','` + at + `',1500,0.123457,100,50,10,5,true,'runs/a/events.jsonl','runs/a/log','{"status":"success"}','m1','off-1'),
		        ('runs/b','o/r','p',2,'implement','failed','` + at + `',10,NULL,NULL,NULL,NULL,NULL,false,'','','{"status":"failed"}','m1','')`,
		`INSERT INTO pilot_runs (run_dir,repo,operator,status,started_at,cost_usd,input_tokens,usage_reported,payload) VALUES ('pilot/a','o/r','pilot','success','` + at + `',0.5,7,true,'{}')`,
		`INSERT INTO run_model_usage (run_dir,kind,model,cost_usd,input_tokens,output_tokens) VALUES ('runs/a','run','gpt',0.123457,100,50)`,
		`INSERT INTO scheduled_checks (id,cron,timezone,prompt,ops,timeout_seconds,enabled,owner,created_at,updated_at,failures) VALUES ('daily','0 9 * * *','UTC','p',2,60,true,'pilot','` + at + `','` + at + `',0)`,
		`INSERT INTO scheduled_check_runs (id,check_id,started_at,ended_at,deadline,status,summary,actions,usage) VALUES ('daily-1','daily','` + at + `','` + at + `','` + at + `','success','ok','[{"outcome":"applied"}]','{"input_tokens":10}')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
}

func isolatedHost(t *testing.T) {
	t.Helper()
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	t.Setenv(store.LegacyDSNEnv, "")
}

func scratch(t *testing.T) *store.Store {
	t.Helper()
	return storetest.New(t)
}

func mustMigrate(t *testing.T, s *store.Store) *store.Store {
	t.Helper()
	if _, err := s.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	return s
}

func openSource(t *testing.T, dsn string) *store.Source {
	t.Helper()
	src, err := store.OpenSource(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

func TestImportPreservesRecordsAndIdentities(t *testing.T) {
	isolatedHost(t)
	dsn, pg := storetest.PostgresSource(t)
	seedSource(t, pg)
	ctx := context.Background()
	src := openSource(t, dsn)
	plan, err := src.Inspect(ctx, mustMigrate(t, scratch(t)))
	if err != nil || len(plan.Blockers) != 0 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	res, err := src.Import(ctx, plan, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := store.Open(ctx, res.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()

	report, err := dest.Verify(ctx, src, t.TempDir())
	if err != nil || !report.OK() {
		t.Fatalf("verify: %+v %v", report, err)
	}
	var row struct {
		State        string
		FencingToken int64
		Attempt      int
		LeaseOwner   string
		ResultDigest string
	}
	if err := dest.DB().Raw("SELECT state, fencing_token, attempt, lease_owner, result_digest FROM coordinator_tasks WHERE id='t-done'").Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "done" || row.FencingToken != 7 || row.Attempt != 2 || row.LeaseOwner != "w1" || row.ResultDigest != "dg1" {
		t.Fatalf("task identity/epoch lost: %+v", row)
	}
	var token struct {
		Hash    []byte
		Revoked bool
		Scope   string
	}
	if err := dest.DB().Raw("SELECT hash, revoked, scope FROM coordinator_tokens WHERE name='tok'").Scan(&token).Error; err != nil {
		t.Fatal(err)
	}
	if len(token.Hash) != 32 || token.Hash[0] != 0xab || !token.Revoked || token.Scope != "admin" {
		t.Fatalf("token semantics changed: %+v", token)
	}
	var typ string
	dest.DB().Raw("SELECT typeof(hash) FROM coordinator_tokens WHERE name='tok'").Scan(&typ)
	if typ != "blob" {
		t.Fatalf("token hash stored as %s, want blob", typ)
	}
	var consumed bool
	dest.DB().Raw("SELECT consumed FROM coordinator_invitations WHERE name='inv'").Scan(&consumed)
	if !consumed {
		t.Fatal("consumed invitation became reusable")
	}
	var attempts, intents, inbox, audits int64
	dest.DB().Raw("SELECT count(*) FROM task_attempts").Scan(&attempts)
	dest.DB().Raw("SELECT count(*) FROM effect_intents WHERE idempotency_key='idem-1' AND status='applied' AND applied_reference='c-9'").Scan(&intents)
	dest.DB().Raw("SELECT count(*) FROM inbox_events WHERE source='github' AND external_id='e1'").Scan(&inbox)
	dest.DB().Raw("SELECT count(*) FROM coordinator_audit").Scan(&audits)
	if attempts != 2 || intents != 1 || inbox != 1 || audits != 2 {
		t.Fatalf("attempts=%d intents=%d inbox=%d audits=%d", attempts, intents, inbox, audits)
	}
	// Usage is copied exactly once: totals equal the source, nothing doubled.
	var in, cost int64
	dest.DB().Raw("SELECT SUM(input_tokens), CAST(round(SUM(cost_usd)*1000000) AS INTEGER) FROM runs").Row().Scan(&in, &cost)
	if in != 100 || cost != 123457 {
		t.Fatalf("usage changed: tokens=%d cost=%d", in, cost)
	}
	var nulls int64
	dest.DB().Raw("SELECT count(*) FROM runs WHERE run_dir='runs/b' AND input_tokens IS NULL AND cost_usd IS NULL").Scan(&nulls)
	if nulls != 1 {
		t.Fatal("missing usage was invented")
	}
	snapshot, _, err := dest.Provenance(ctx)
	if err != nil || snapshot != plan.SnapshotID {
		t.Fatalf("provenance %q %v", snapshot, err)
	}
	var version int
	dest.DB().Raw("SELECT source_schema_version FROM import_provenance").Scan(&version)
	if version != store.SourceSchemaVersion {
		t.Fatalf("source schema version = %d", version)
	}
}

func TestImportTimestampsStayUTC(t *testing.T) {
	isolatedHost(t)
	dsn, pg := storetest.PostgresSource(t)
	seedSource(t, pg)
	pg.Exec(`UPDATE coordinator_tasks SET lease_expires_at = '2026-10-01 18:00:00+08' WHERE id='t-done'`)
	ctx := context.Background()
	src := openSource(t, dsn)
	plan, _ := src.Inspect(ctx, mustMigrate(t, scratch(t)))
	res, err := src.Import(ctx, plan, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := store.Open(ctx, res.Path)
	defer dest.Close()
	var raw string
	dest.DB().Raw("SELECT CAST(lease_expires_at AS TEXT) FROM coordinator_tasks WHERE id='t-done'").Scan(&raw)
	if raw != "2026-10-01T10:00:00Z" {
		t.Fatalf("lease stored as %q, want 2026-10-01T10:00:00Z", raw)
	}
}

func TestImportBlockers(t *testing.T) {
	isolatedHost(t)
	ctx := context.Background()
	t.Run("leased task", func(t *testing.T) {
		dsn, pg := storetest.PostgresSource(t)
		seedSource(t, pg)
		pg.Exec(`UPDATE coordinator_tasks SET state='leased', lease_owner='w1', lease_expires_at=now()+interval '1 hour' WHERE id='t-pend'`)
		plan, err := openSource(t, dsn).Inspect(ctx, mustMigrate(t, scratch(t)))
		if err != nil || len(plan.Blockers) == 0 || !strings.Contains(strings.Join(plan.Blockers, ";"), "leased") {
			t.Fatalf("plan %+v %v", plan, err)
		}
		if _, err := openSource(t, dsn).Import(ctx, plan, false, nil); err == nil {
			t.Fatal("import must refuse a source with executing tasks")
		}
	})
	t.Run("unsupported table", func(t *testing.T) {
		dsn, pg := storetest.PostgresSource(t)
		pg.Exec(`CREATE TABLE surprise (id int)`)
		plan, _ := openSource(t, dsn).Inspect(ctx, mustMigrate(t, scratch(t)))
		if !strings.Contains(strings.Join(plan.Blockers, ";"), `"surprise"`) {
			t.Fatalf("unsupported table not reported: %+v", plan.Blockers)
		}
	})
	t.Run("unsupported column", func(t *testing.T) {
		dsn, pg := storetest.PostgresSource(t)
		pg.Exec(`ALTER TABLE runs ADD COLUMN extra text`)
		plan, _ := openSource(t, dsn).Inspect(ctx, mustMigrate(t, scratch(t)))
		if !strings.Contains(strings.Join(plan.Blockers, ";"), "runs.extra") {
			t.Fatalf("omitted column not reported: %+v", plan.Blockers)
		}
	})
	t.Run("old schema", func(t *testing.T) {
		dsn, pg := storetest.PostgresSource(t)
		pg.Exec(`DELETE FROM schema_migrations WHERE version = 11`)
		plan, _ := openSource(t, dsn).Inspect(ctx, mustMigrate(t, scratch(t)))
		if !strings.Contains(strings.Join(plan.Blockers, ";"), "migration 11") {
			t.Fatalf("%+v", plan.Blockers)
		}
	})
	t.Run("rejected row is a blocker not a skip", func(t *testing.T) {
		dsn, pg := storetest.PostgresSource(t)
		seedSource(t, pg)
		pg.Exec(`ALTER TABLE coordinator_tasks DROP CONSTRAINT coordinator_tasks_state_check`)
		pg.Exec(`UPDATE coordinator_tasks SET state='mystery' WHERE id='t-pend'`)
		src := openSource(t, dsn)
		plan, _ := src.Inspect(ctx, mustMigrate(t, scratch(t)))
		_, err := src.Import(ctx, plan, false, nil)
		if err == nil || !strings.Contains(err.Error(), "blocker") || !strings.Contains(err.Error(), "coordinator_tasks") {
			t.Fatalf("want a coordinator_tasks blocker, got %v", err)
		}
		if _, statErr := os.Stat(store.DefaultPath()); statErr == nil {
			t.Fatal("a failed import must not publish the controller database")
		}
	})
}

func TestImportResumesAndDetectsSourceChange(t *testing.T) {
	isolatedHost(t)
	dsn, pg := storetest.PostgresSource(t)
	seedSource(t, pg)
	ctx := context.Background()
	src := openSource(t, dsn)
	plan, _ := src.Inspect(ctx, mustMigrate(t, scratch(t)))

	// Simulate a crash after two tables: import only a prefix, then rerun.
	partial := *plan
	partial.Tables = plan.Tables[:2]
	if _, err := src.Import(ctx, &partial, false, nil); err != nil {
		t.Fatal(err)
	}
	// The prefix import finished provenance; remove it as a crash would have.
	st, _ := store.Open(ctx, store.StagingPath(plan.SnapshotID))
	st.DB().Exec("DELETE FROM import_provenance")
	st.DB().Exec(`CREATE TABLE IF NOT EXISTS import_progress (snapshot_id text NOT NULL, table_name text PRIMARY KEY, rows integer NOT NULL, digest text NOT NULL)`)
	for _, tb := range plan.Tables[:2] {
		st.DB().Exec(`INSERT INTO import_progress VALUES (?,?,?,?)`, plan.SnapshotID, tb.Name, tb.Rows, tb.Digest)
	}
	st.Close()

	res, err := src.Import(ctx, plan, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resumed) != 2 {
		t.Fatalf("resumed %v, want the 2 completed tables skipped", res.Resumed)
	}
	dest, _ := store.Open(ctx, res.Path)
	if report, err := dest.Verify(ctx, src); err != nil || !report.OK() {
		t.Fatalf("verify after resume: %+v %v", report, err)
	}
	dest.Close()

	// A changed source yields a different snapshot identity and a new staging file.
	pg.Exec(`INSERT INTO coordinator_audit (worker,task,action,created_at) VALUES ('w1','t-done','extra',now())`)
	src2 := openSource(t, dsn)
	plan2, _ := src2.Inspect(ctx, mustMigrate(t, scratch(t)))
	if plan2.SnapshotID == plan.SnapshotID {
		t.Fatal("a changed source must change the snapshot identity")
	}
	old, _ := store.Open(ctx, store.StagingPath(plan.SnapshotID))
	defer old.Close()
	report, err := old.Verify(ctx, src2)
	if err != nil || report.OK() {
		t.Fatalf("a stale staging import must fail verification against the changed source: %+v %v", report, err)
	}
}

func TestVerifyDetectsTamperingAndMissingProvenance(t *testing.T) {
	isolatedHost(t)
	dsn, pg := storetest.PostgresSource(t)
	seedSource(t, pg)
	ctx := context.Background()
	src := openSource(t, dsn)
	plan, _ := src.Inspect(ctx, mustMigrate(t, scratch(t)))
	res, err := src.Import(ctx, plan, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := store.Open(ctx, res.Path)
	defer dest.Close()
	for name, mutate := range map[string]string{
		"payload": `UPDATE coordinator_tasks SET result_digest='forged' WHERE id='t-done'`,
		"usage":   `UPDATE runs SET input_tokens = input_tokens + 1 WHERE run_dir='runs/a'`,
		"rows":    `DELETE FROM coordinator_audit WHERE id = (SELECT min(id) FROM coordinator_audit)`,
	} {
		if err := dest.DB().Exec(mutate).Error; err != nil {
			t.Fatal(err)
		}
		report, err := dest.Verify(ctx, src)
		if err != nil || report.OK() {
			t.Fatalf("%s tamper not detected: %+v %v", name, report, err)
		}
		// restore by re-importing
		if _, err := src.Import(ctx, plan, true, nil); err != nil {
			t.Fatal(err)
		}
		dest.Close()
		dest, _ = store.Open(ctx, res.Path)
	}
	fresh := mustMigrate(t, storetest.New(t))
	report, _ := fresh.Verify(ctx, nil)
	if report.OK() || !strings.Contains(strings.Join(report.Blockers, ";"), "provenance") {
		t.Fatalf("a database without a completed import must not verify: %+v", report)
	}
}

func TestCutoverPublishesSwitchesConfigAndRollsBack(t *testing.T) {
	isolatedHost(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dsn, pg := storetest.PostgresSource(t)
	seedSource(t, pg)
	ctx := context.Background()

	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {Enabled: true}}}
	cfg.Settings.Database.DSN = dsn
	cfg.Settings.PollInterval = 7
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	t.Setenv(store.LegacyDSNEnv, "")
	original, _ := os.ReadFile(config.ConfigPath())

	// Before the cutover the host refuses to start on an empty SQLite store.
	loaded, _ := config.Load()
	if _, err := store.EnsureReady(ctx, loaded); !errors.Is(err, store.ErrCutoverRequired) {
		t.Fatalf("want ErrCutoverRequired, got %v", err)
	}

	src := openSource(t, dsn)
	plan, _ := src.Inspect(ctx, mustMigrate(t, scratch(t)))
	if _, err := store.Cutover(ctx, plan, src, store.CutoverOptions{SourceDSN: dsn}); err == nil {
		t.Fatal("cutover without a staging import must fail and leave the host unchanged")
	}
	if after, _ := os.ReadFile(config.ConfigPath()); string(after) != string(original) {
		t.Fatal("failed cutover changed the configuration")
	}
	if _, err := src.Import(ctx, plan, false, nil); err != nil {
		t.Fatal(err)
	}
	rec, err := store.Cutover(ctx, plan, src, store.CutoverOptions{SourceDSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(rec.ConfigBackup)
	if err != nil || string(backup) != string(original) {
		t.Fatalf("source configuration backup is not the untouched original: %v", err)
	}
	if info, _ := os.Stat(rec.ConfigBackup); info.Mode().Perm()&0o077 != 0 {
		t.Fatal("config backup must be private")
	}
	switched, _ := config.Load()
	if switched.Settings.Database.Engine != store.EngineSQLite || switched.Settings.Database.DSN != "" || switched.Settings.PollInterval != 7 || !switched.Repos["o/r"].Enabled {
		t.Fatalf("config not switched cleanly: %+v", switched.Settings.Database)
	}
	ready, err := store.EnsureReady(ctx, switched)
	if err != nil {
		t.Fatalf("host does not start on SQLite after cutover: %v", err)
	}
	var tasks int64
	ready.DB().Raw("SELECT count(*) FROM coordinator_tasks").Scan(&tasks)
	ready.Close()
	if tasks != 2 {
		t.Fatalf("tasks = %d", tasks)
	}

	// An old DSN environment variable now contradicts the configuration.
	t.Setenv(store.LegacyDSNEnv, dsn)
	if _, err := store.EnsureReady(ctx, switched); err == nil {
		t.Fatal("stale CLAWFLOW_DATABASE_DSN must be refused after cutover")
	}
	t.Setenv(store.LegacyDSNEnv, "")

	// The source database was never modified.
	var n int64
	pg.Raw("SELECT count(*) FROM coordinator_tasks").Scan(&n)
	if n != 2 {
		t.Fatal("source changed")
	}

	// Repeating the cutover is refused rather than overwriting the live store.
	if _, err := store.Cutover(ctx, plan, src, store.CutoverOptions{SourceDSN: dsn}); err == nil {
		t.Fatal("second cutover must be refused")
	}

	// Rollback while SQLite accepted no new work restores the configuration.
	back, err := store.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(config.ConfigPath())
	if string(restored) != string(original) {
		t.Fatal("rollback did not restore the original configuration")
	}
	if _, err := os.Stat(back.SQLitePath); err != nil {
		t.Fatalf("rolled-back SQLite file must be kept: %v", err)
	}
	if _, err := os.Stat(store.DefaultPath()); err == nil {
		t.Fatal("rolled-back store must not remain at the live path")
	}
}

func TestRollbackAfterNewWorkRequiresReconciliation(t *testing.T) {
	isolatedHost(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	dsn, pg := storetest.PostgresSource(t)
	seedSource(t, pg)
	ctx := context.Background()
	cfg := &config.Config{Repos: map[string]config.Repo{}}
	cfg.Settings.Database.DSN = dsn
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	src := openSource(t, dsn)
	plan, _ := src.Inspect(ctx, mustMigrate(t, scratch(t)))
	if _, err := src.Import(ctx, plan, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cutover(ctx, plan, src, store.CutoverOptions{SourceDSN: dsn}); err != nil {
		t.Fatal(err)
	}
	live, _ := store.Open(ctx, store.DefaultPath())
	// SQLite completes the pending task: this work does not exist in PostgreSQL.
	if err := live.DB().Exec(`UPDATE coordinator_tasks SET state='done', result='{"status":"success"}', updated_at=CURRENT_TIMESTAMP WHERE id='t-pend'`).Error; err != nil {
		t.Fatal(err)
	}
	live.Close()
	if _, err := store.Rollback(ctx); !errors.Is(err, store.ErrReconcileRequired) {
		t.Fatalf("want ErrReconcileRequired, got %v", err)
	}
	if cfgNow, _ := config.Load(); cfgNow.Settings.Database.Engine != store.EngineSQLite {
		t.Fatal("refused rollback must leave the SQLite configuration in place")
	}
}

func TestImportSourceErrorsDoNotLeakPassword(t *testing.T) {
	const pw = "sup3r-s3cret-pw"
	for _, dsn := range []string{
		"postgres://u:" + pw + "@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		"host=127.0.0.1 port=1 user=u password=" + pw + " dbname=db sslmode=disable connect_timeout=2",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, err := store.OpenSource(ctx, dsn)
		cancel()
		if err == nil {
			t.Fatalf("OpenSource(%q) unexpectedly succeeded", store.MaskDSN(dsn))
		}
		if strings.Contains(err.Error(), pw) {
			t.Errorf("error leaks password: %v", err)
		}
	}
}

func TestImportedDatabaseKeepsPrivatePermissions(t *testing.T) {
	isolatedHost(t)
	dsn, pg := storetest.PostgresSource(t)
	seedSource(t, pg)
	ctx := context.Background()
	src := openSource(t, dsn)
	plan, _ := src.Inspect(ctx, mustMigrate(t, scratch(t)))
	res, err := src.Import(ctx, plan, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(res.Path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("staging must be private: %v %v", info, err)
	}
	if info, _ := os.Stat(filepath.Dir(res.Path)); info.Mode().Perm()&0o077 != 0 {
		t.Fatal("database directory must be private")
	}
}
