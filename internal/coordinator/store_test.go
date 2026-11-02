package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"gopkg.in/yaml.v3"
)

var ctx = context.Background()

func setup(t *testing.T) (*Store, *store.Store, string) {
	t.Helper()
	db, dsn := storetest.NewWithPath(t)
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	return New(db.DB()), db, dsn
}
func enqueue(t *testing.T, s *Store, issue int, tags ...string) *Task {
	t.Helper()
	task, err := s.Enqueue(ctx, Task{Kind: "implement", Repo: "owner/repo", Issue: issue, Operator: "implement", Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func register(t *testing.T, s *Store, id string, slots int, tags ...string) {
	t.Helper()
	if err := s.RegisterWorker(ctx, Worker{ID: id, Tags: tags, RunUntagged: true, MaxParallel: slots}); err != nil {
		t.Fatal(err)
	}
}
func claim(t *testing.T, s *Store, id string) *Task {
	t.Helper()
	task, err := s.Claim(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func TestEligibility(t *testing.T) {
	for _, tc := range []struct {
		tags, worker   []string
		untagged, want bool
	}{
		{nil, nil, false, false}, {nil, nil, true, true},
		{[]string{"a", "b"}, []string{"a"}, true, false},
		{[]string{"a", "b"}, []string{"b", "c", "a"}, false, true},
	} {
		if got := eligible(tc.tags, Worker{Tags: tc.worker, RunUntagged: tc.untagged}); got != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
}
func TestDedupeAndReopen(t *testing.T) {
	s, db, dsn := setup(t)
	first := enqueue(t, s, 1)
	if got := enqueue(t, s, 1); got.ID != first.ID {
		t.Fatal("duplicate task")
	}
	pr, err := s.Enqueue(ctx, Task{Repo: "owner/repo", Issue: 2, PR: 1, Kind: "implement", Operator: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.Enqueue(ctx, Task{Repo: "owner/repo", Issue: 3, PR: 1, Kind: "implement", Operator: "implement"})
	if err != nil || same.ID != pr.ID || pr.ID == first.ID {
		t.Fatalf("PR dedupe: %v", err)
	}
	other, err := s.Enqueue(ctx, Task{Repo: "owner/repo", Issue: 1, Kind: "reply", Operator: "reply"})
	if err != nil || other.ID == first.ID {
		t.Fatalf("kind isolation: %v", err)
	}
	register(t, s, "w", 1)
	leased := claim(t, s, "w")
	if err := s.Complete(ctx, leased.ID, "w", leased.FencingToken, nil); err != nil {
		t.Fatal(err)
	}
	if next := enqueue(t, s, 1); next.ID == first.ID {
		t.Fatal("terminal task deduped")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var saved Task
	if err := reopened.DB().First(&saved, "id = ?", leased.ID).Error; err != nil || saved.State != "done" {
		t.Fatalf("persistence: %+v %v", saved, err)
	}
}
func TestClaimOrderTagsCapacity(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 4, "linux")
	blocked := enqueue(t, s, 1, "macos")
	a := enqueue(t, s, 2, "linux")
	b := enqueue(t, s, 3, "linux")
	c := enqueue(t, s, 4)
	d := enqueue(t, s, 5, "linux")
	// Lower numeric priority wins, then queue position, then age.
	for _, x := range []struct {
		task *Task
		p    int
		q    int64
		age  time.Time
	}{
		{blocked, -1, 0, time.Now()}, {a, 1, 0, time.Now()}, {b, 0, 2, time.Now()},
		{c, 0, 1, time.Now()}, {d, 0, 1, time.Now().Add(-time.Hour)},
	} {
		if err := db.DB().Model(&Task{}).Where("id = ?", x.task.ID).Updates(map[string]interface{}{"priority": x.p, "queue_position": x.q, "created_at": x.age}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []*Task{d, c, b, a} {
		if got := claim(t, s, "w"); got.ID != want.ID {
			t.Fatalf("want %s got %s", want.ID, got.ID)
		}
	}
	enqueue(t, s, 6)
	if _, err := s.Claim(ctx, "w"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("capacity: %v", err)
	}
	register(t, s, "tagged", 1)
	if err := s.RegisterWorker(ctx, Worker{ID: "tagged", MaxParallel: 1, RunUntagged: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "tagged"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("untagged: %v", err)
	}
}
func TestLeaseFencingAndAudit(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	first := claim(t, s, "w")
	if first.FencingToken != 1 {
		t.Fatal("first fence")
	}
	if err := s.Heartbeat(ctx, first.ID, "w", first.FencingToken); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, first.ID, "other", first.FencingToken, nil); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Second)
	if err := db.DB().Model(&Task{}).Where("id = ?", first.ID).Update("lease_expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, first.ID, "w", first.FencingToken, nil); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expired result: %v", err)
	}
	expired, err := s.ExpireLeases(ctx, time.Now())
	if err != nil || len(expired) != 1 || expired[0].Attempt != 1 || expired[0].State != "pending" {
		t.Fatalf("expire %+v %v", expired, err)
	}
	second := claim(t, s, "w")
	if second.FencingToken != 2 || second.Attempt != 1 {
		t.Fatalf("reclaim %+v", second)
	}
	for _, f := range []func() error{
		func() error { return s.Heartbeat(ctx, first.ID, "w", 1) },
		func() error { return s.Complete(ctx, first.ID, "w", 1, nil) },
		func() error { return s.Fail(ctx, first.ID, "w", 1, nil) },
	} {
		if err := f(); !errors.Is(err, ErrStaleLease) {
			t.Fatalf("stale fence: %v", err)
		}
	}
	if err := s.Fail(ctx, second.ID, "w", 2, json.RawMessage(`{"error":"test"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, second.ID, "w", 2, nil); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("terminal: %v", err)
	}
	var audits []Audit
	if err := db.DB().Order("id").Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, a := range audits {
		actions = append(actions, a.Action)
	}
	if got := strings.Join(actions, ","); got != "register,enqueue,claim,heartbeat,expire,claim,failed" {
		t.Fatal(got)
	}
}
func TestConcurrentClaim(t *testing.T) {
	for _, sameWorker := range []bool{false, true} {
		t.Run(map[bool]string{false: "different-workers", true: "same-worker"}[sameWorker], func(t *testing.T) {
			s, _, _ := setup(t)
			register(t, s, "a", 1)
			register(t, s, "b", 1)
			enqueue(t, s, 1)
			enqueue(t, s, 2)
			start := make(chan struct{})
			ids := make(chan string, 2)
			var wg sync.WaitGroup
			for _, worker := range []string{"a", "b"} {
				if sameWorker {
					worker = "a"
				}
				wg.Add(1)
				go func(w string) {
					defer wg.Done()
					<-start
					task, err := s.Claim(ctx, w)
					if errors.Is(err, ErrNoTask) {
						return
					}
					if err != nil {
						t.Error(err)
						return
					}
					ids <- task.ID
				}(worker)
			}
			close(start)
			wg.Wait()
			close(ids)
			seen := map[string]bool{}
			for id := range ids {
				if seen[id] {
					t.Fatal("double claim")
				}
				seen[id] = true
			}
			want := 2
			if sameWorker {
				want = 1
			}
			if len(seen) != want {
				t.Fatalf("claims %d want %d", len(seen), want)
			}
		})
	}
}
func TestTokens(t *testing.T) {
	s, db, _ := setup(t)
	secret, err := s.CreateToken(ctx, "worker")
	if err != nil || len(secret) != 64 {
		t.Fatalf("create %v", err)
	}
	if _, err := s.CreateToken(ctx, "worker"); err == nil {
		t.Fatal("duplicate name")
	}
	token, err := s.VerifyToken(ctx, secret)
	if err != nil || token.Name != "worker" || token.LastSeen == nil {
		t.Fatalf("verify %v", err)
	}
	if _, err := s.VerifyToken(ctx, "wrong"); !errors.Is(err, ErrToken) {
		t.Fatal(err)
	}
	var row storedToken
	if err := db.DB().First(&row).Error; err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(secret))
	if string(row.Hash) != string(hash[:]) {
		t.Fatal("not hash-only")
	}
	listed, err := s.ListTokens(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list %v", err)
	}
	out, _ := json.Marshal(listed)
	if strings.Contains(string(out), secret) || strings.Contains(string(out), "Hash") {
		t.Fatal("secret listed")
	}
	if err := s.RevokeToken(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyToken(ctx, secret); !errors.Is(err, ErrToken) {
		t.Fatal(err)
	}
}

func TestConcurrentDedupe(t *testing.T) {
	s, _, _ := setup(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := s.Enqueue(ctx, Task{Repo: "r", Issue: 1, Kind: "reply", Operator: "reply"})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- task.ID
		}()
	}
	wg.Wait()
	close(ids)
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatalf("dedupe created %d tasks", len(unique))
	}
}

func TestPreparedEnqueueDeduplicatesBeforeReservation(t *testing.T) {
	s, _, _ := setup(t)
	calls := 0
	input := Task{Kind: "repair", Repo: "o/r", Issue: 7, PR: 8, Operator: "repair-ci"}
	prepare := func(task *Task) error {
		calls++
		task.Payload = json.RawMessage(`{"task_id":"` + task.ID + `","attempt":1}`)
		return nil
	}
	first, err := s.EnqueuePrepared(ctx, input, prepare)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnqueuePrepared(ctx, input, prepare)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first.ID != second.ID || !strings.Contains(string(second.Payload), second.ID) {
		t.Fatal(calls, first, second)
	}
}

func TestAcceptedResultStaysFencedUntilEffectsFinish(t *testing.T) {
	s, _, _ := setup(t)
	enqueue(t, s, 7)
	register(t, s, "w", 1)
	leased := claim(t, s, "w")
	raw := json.RawMessage(`{"status":"success"}`)
	if err := s.AcceptResult(ctx, leased.ID, "w", leased.FencingToken-1, raw); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	if err := s.AcceptResult(ctx, leased.ID, "w", leased.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptResult(ctx, leased.ID, "w", leased.FencingToken, raw); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	if err := s.Heartbeat(ctx, leased.ID, "w", leased.FencingToken); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	if tasks, err := s.ExpireLeases(ctx, time.Now().Add(time.Hour)); err != nil || len(tasks) != 0 {
		t.Fatal(tasks, err)
	}
	active, err := s.Active(ctx)
	if err != nil || len(active) != 1 || !strings.Contains(string(active[0].Result), `"success"`) {
		t.Fatal(active, err)
	}
	if err := s.FinishResult(ctx, leased.ID, "w", leased.FencingToken, false); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishResult(ctx, leased.ID, "w", leased.FencingToken, false); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	active, err = s.Active(ctx)
	if err != nil || len(active) != 0 {
		t.Fatal(active, err)
	}
}

func TestMigratedHostEligibility(t *testing.T) {
	var repo config.Repo
	if err := yaml.Unmarshal([]byte("bound_machine: Work-Mac.local\nworker_tags: [codex]\n"), &repo); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"Work-Mac.local", "other.local"} {
		worker := Worker{Tags: []string{"codex", config.HostTag(host)}, RunUntagged: true}
		if got := eligible(repo.WorkerTags, worker); got != (host == "Work-Mac.local") {
			t.Fatalf("host %s eligibility = %v", host, got)
		}
	}
}

func TestPlatformLeaseLimitsAndCredentialTags(t *testing.T) {
	s, _, _ := setup(t)
	total := 2
	s.LeaseLimits = func() (int, map[string]int, error) { return total, map[string]int{"owner/repo": 1}, nil }
	register(t, s, "local-worker", 3, "github")
	register(t, s, "laptop", 3, "github", "creds-laptop")
	tagged := enqueue(t, s, 1, "github", "creds-laptop")
	if _, err := s.Claim(ctx, "local-worker"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("local worker claimed credential task: %v", err)
	}
	if got := claim(t, s, "laptop"); got.ID != tagged.ID {
		t.Fatalf("wrong task: %+v", got)
	}
	enqueue(t, s, 2, "github")
	if _, err := s.Claim(ctx, "local-worker"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("repo capacity bypassed: %v", err)
	}
	other, err := s.Enqueue(ctx, Task{Kind: "implement", Repo: "other/repo", Issue: 1, Operator: "implement", Tags: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	total = 1
	if _, err := s.Claim(ctx, "local-worker"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("global capacity bypassed: %v", err)
	}
	total = 2
	if got := claim(t, s, "local-worker"); got.ID != other.ID {
		t.Fatalf("wrong task: %+v", got)
	}
}

// A worker that discovers it cannot run Offload hands the lease back: the task
// stays pending, no attempt is consumed, and no result or failure is recorded.
func TestReleaseReturnsTaskWithoutConsumingAttempt(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w1", 1)
	register(t, s, "w2", 1)
	task := enqueue(t, s, 1)
	claimed := claim(t, s, "w1")
	if claimed.ID != task.ID {
		t.Fatal("claim mismatch")
	}
	if err := s.Release(ctx, task.ID, "w2", claimed.FencingToken, "offload-unavailable"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("foreign release: %v", err)
	}
	if err := s.Release(ctx, task.ID, "w1", claimed.FencingToken, "offload-unavailable"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Task(ctx, task.ID)
	if err != nil || got.State != "pending" || got.Attempt != 0 || got.LeaseOwner != "" || len(got.Result) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	var attempts int64
	if err := s.db.Model(&TaskAttempt{}).Where("task_id = ?", task.ID).Count(&attempts).Error; err != nil || attempts != 0 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	if err := s.Release(ctx, task.ID, "w1", claimed.FencingToken, "x"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale release: %v", err)
	}
	// A healthy worker can claim it next, with a fresh fencing token.
	again := claim(t, s, "w2")
	if again.ID != task.ID || again.FencingToken <= claimed.FencingToken {
		t.Fatalf("%+v", again)
	}
}
