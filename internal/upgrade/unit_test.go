package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

const goodCommit = "2222222222222222222222222222222222222222"

func TestCheckTransition(t *testing.T) {
	prior := Manifest{Version: "p", Commit: strings.Repeat("1", 40), SchemaLatest: 3, WorkerProtocol: 1}
	cases := []struct {
		name    string
		target  Manifest
		applied []int
		want    string
	}{
		{"same schema", Manifest{Commit: goodCommit, SchemaLatest: 3, WorkerProtocol: 1}, []int{1, 2, 3}, ""},
		{"additive migration", Manifest{Commit: goodCommit, SchemaLatest: 4, WorkerProtocol: 1}, []int{1, 2, 3}, ""},
		{"contract migration", Manifest{Commit: goodCommit, SchemaLatest: 4, WorkerProtocol: 1, ContractVersions: []int{4}}, []int{1, 2, 3}, "contract"},
		{"already applied contract is not pending", Manifest{Commit: goodCommit, SchemaLatest: 4, WorkerProtocol: 1, ContractVersions: []int{2}}, []int{1, 2, 3}, ""},
		{"database newer than target", Manifest{Commit: goodCommit, SchemaLatest: 3, WorkerProtocol: 1}, []int{1, 2, 3, 4}, "downgrade"},
		{"target older than running", Manifest{Commit: goodCommit, SchemaLatest: 2, WorkerProtocol: 1}, []int{1, 2}, "older"},
		{"protocol regression", Manifest{Commit: goodCommit, SchemaLatest: 3, WorkerProtocol: 0}, []int{1, 2, 3}, "protocol"},
		{"short commit", Manifest{Commit: "abc", SchemaLatest: 3, WorkerProtocol: 1}, []int{1, 2, 3}, "40-character"},
		{"no schema reported", Manifest{Commit: goodCommit, WorkerProtocol: 1}, []int{1}, "schema version"},
	}
	for _, c := range cases {
		err := CheckTransition(prior, c.target, c.applied)
		if c.want == "" && err != nil {
			t.Errorf("%s: unexpected %v", c.name, err)
		}
		if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

func TestCheckRollbackFailsClosed(t *testing.T) {
	prior := Manifest{SchemaLatest: 3, WorkerProtocol: 1}
	target := Manifest{SchemaLatest: 4, WorkerProtocol: 1}
	if e := CheckRollback(prior, target, []int{1, 2, 3, 4}, true); !e.Eligible {
		t.Fatalf("an expand-only upgrade must be rollback eligible: %+v", e)
	}
	contract := Manifest{SchemaLatest: 4, WorkerProtocol: 1, ContractVersions: []int{4}}
	if e := CheckRollback(prior, contract, []int{1, 2, 3, 4}, false); e.Eligible || !strings.Contains(e.Reason, "contract") {
		t.Fatalf("a contract migration blocks rollback: %+v", e)
	}
	if e := CheckRollback(Manifest{}, target, []int{1, 2, 3, 4}, false); e.Eligible {
		t.Fatal("unknown prior schema must fail closed")
	}
	if e := CheckRollback(prior, target, []int{1, 2, 3, 4, 5}, false); e.Eligible {
		t.Fatal("a schema newer than the target must fail closed")
	}
	newer := Manifest{SchemaLatest: 4, WorkerProtocol: 2}
	if e := CheckRollback(prior, newer, []int{1, 2, 3, 4}, true); e.Eligible {
		t.Fatal("older protocol with durable work must fail closed")
	}
	if e := CheckRollback(prior, newer, []int{1, 2, 3, 4}, false); !e.Eligible {
		t.Fatalf("older protocol with no durable work is safe: %+v", e)
	}
}

func TestLocalManifestReportsContractVersions(t *testing.T) {
	migs := []store.Migration{{Version: 1}, {Version: 2, Contract: true}}
	m := LocalManifest("v", goodCommit, migs)
	if m.SchemaLatest != 2 || len(m.ContractVersions) != 1 || m.ContractVersions[0] != 2 || m.WorkerProtocol != WorkerProtocol {
		t.Fatalf("%+v", m)
	}
	for _, mig := range store.Migrations {
		if mig.Contract {
			t.Fatalf("migration %d: shipped migrations must be expand-only until a contract release is planned", mig.Version)
		}
	}
}

func TestRecordsAreRedactedAndPrivate(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir}
	r := &Record{ID: NewID(time.Now()), Kind: KindController, ResultNote: "dsn=postgres://u:hunter2@host/db token: abcd Bearer xyz",
		Recovery: strings.Repeat("a", 64), Stages: []StageRecord{{Name: "x", Detail: "password=hunter2"}}}
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, r.ID+".json"))
	for _, secret := range []string{"hunter2", "abcd", "xyz", strings.Repeat("a", 64)} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("record leaked %q:\n%s", secret, data)
		}
	}
	info, _ := os.Stat(filepath.Join(dir, r.ID+".json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	got, err := s.Load(r.ID)
	if err != nil || got.ID != r.ID {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := s.Load("../etc/passwd"); err == nil {
		t.Fatal("path traversal id must be rejected")
	}
}

func TestPreserveFileIsImmutableAndVerified(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "bin")
	if err := os.WriteFile(src, []byte("v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	sum, _ := FileSHA(src)
	store := filepath.Join(dir, "store")
	a, err := PreserveFile(store, Artifact{SHA256: sum, Path: src})
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the source afterwards must not change the preserved copy.
	_ = os.WriteFile(src, []byte("tampered"), 0o700)
	b, err := PreserveFile(store, Artifact{SHA256: sum, Path: src})
	if err != nil || b.Path != a.Path {
		t.Fatalf("%v %v", b, err)
	}
	if got, _ := FileSHA(a.Path); got != sum {
		t.Fatal("preserved copy changed")
	}
	if _, err := PreserveFile(filepath.Join(dir, "other"), Artifact{SHA256: sum, Path: src}); err == nil {
		t.Fatal("a source that no longer matches its hash must be refused")
	}
	// Corrupting the store entry is an error, not a silent substitution.
	_ = os.Chmod(a.Path, 0o700)
	_ = os.WriteFile(a.Path, []byte("x"), 0o700)
	if _, err := PreserveFile(store, Artifact{SHA256: sum, Path: src}); err == nil {
		t.Fatal("corrupt preserved artifact must be detected")
	}
}
