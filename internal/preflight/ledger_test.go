package preflight

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

func fixture(t *testing.T) (Store, time.Time) {
	t.Helper()
	return Store{Root: t.TempDir(), Repo: "Owner/Repo", Issue: 657}, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
}

func TestPersistentFailuresCooldownAndAuditedResume(t *testing.T) {
	s, now := fixture(t)
	repair := filepath.Join(s.Root, "ci-repair", "sentinel")
	if err := os.MkdirAll(filepath.Dir(repair), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repair, []byte("preserve repair budget"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxFailures; i++ {
		// A fresh Store represents reloading after each scheduler pass/restart.
		s = Store{Root: s.Root, Repo: s.Repo, Issue: s.Issue}
		id, err := s.Begin("coordination", now)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Finish(id, "failure", "timeout", now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err = s.Finish(id, "failure", "timeout", now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		l, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		st, err := l.Status()
		if err != nil {
			t.Fatal(err)
		}
		wantDelay := []time.Duration{5, 10, 20, 40, 60}[i] * time.Minute
		if st.NotBefore.Sub(now.Add(time.Second)) != wantDelay {
			t.Fatalf("wrong cooldown: %+v", st)
		}
		if st.Failures != i+1 {
			t.Fatalf("failures=%d", st.Failures)
		}
		_, err = s.Begin("plan", now.Add(time.Second))
		want := ErrCooldown
		if i == MaxFailures-1 {
			want = ErrExhausted
		}
		if !errors.Is(err, want) {
			t.Fatalf("retry error=%v want=%v", err, want)
		}
		now = st.NotBefore
	}
	before, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Resume("", "fixed service", now); err == nil {
		t.Fatal("missing actor accepted")
	}
	after, _ := os.ReadFile(s.Path())
	if string(before) != string(after) {
		t.Fatal("invalid resume mutated history")
	}
	if err = s.Resume("operator", "upstream response recovered", now); err != nil {
		t.Fatal(err)
	}
	l, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Events) != MaxFailures+1 {
		t.Fatal("history lost")
	}
	id, err := s.Begin("plan", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(id, "success", "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	st, err := func() (Status, error) {
		l, e := s.Load()
		if e != nil {
			return Status{}, e
		}
		return l.Status()
	}()
	if err != nil {
		t.Fatal(err)
	}
	if st.Failures != 0 || st.PendingID != "" || !st.NotBefore.IsZero() {
		t.Fatalf("status=%+v", st)
	}
	b, _ := os.ReadFile(repair)
	if string(b) != "preserve repair budget" {
		t.Fatal("repair ledger changed")
	}
}

func TestPendingRequiresOwnershipAndReconciliation(t *testing.T) {
	s, now := fixture(t)
	id, err := s.Begin("plan", now)
	if err != nil {
		t.Fatal(err)
	}
	copy := Store{Root: s.Root, Repo: s.Repo, Issue: s.Issue}
	if _, err = copy.Begin("coordination", now.Add(24*time.Hour)); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if err = copy.Resume("operator", "retry", now); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if err = copy.Finish("other", "failure", "interrupted", now); err == nil {
		t.Fatal("foreign completion accepted")
	}
	if err = copy.Finish(id, "failure", "interrupted", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = copy.Finish(id, "success", "", now.Add(time.Second)); err == nil {
		t.Fatal("failure rewritten")
	}
}

func TestNormalWaitingDoesNotResetFailures(t *testing.T) {
	s, now := fixture(t)
	id, err := s.Begin("coordination", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(id, "failure", "invalid-response", now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(Cooldown)
	id, err = s.Begin("coordination", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(id, "waiting", "dependency", now); err != nil {
		t.Fatal(err)
	}
	l, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, err := l.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Failures != 1 {
		t.Fatal("normal waiting reset budget")
	}
}

func TestMalformedHistoryFailsClosed(t *testing.T) {
	for _, data := range []string{"{", "{}", `{"version":2,"repo":"owner/repo","issue":657}`, `{"version":1,"repo":"another/repo","issue":657}`, `{"version":1,"repo":"owner/repo","issue":657,"events":[{}]}`} {
		t.Run(data, func(t *testing.T) {
			s, now := fixture(t)
			if err := os.MkdirAll(filepath.Dir(s.Path()), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.Path(), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Begin("plan", now); err == nil {
				t.Fatal("corrupt history accepted")
			}
			b, _ := os.ReadFile(s.Path())
			if string(b) != data {
				t.Fatal("corrupt evidence overwritten")
			}
		})
	}
}

func TestConcurrentBeginHasOneWinner(t *testing.T) {
	s, now := fixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Begin("plan", now); results <- err }()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrPending) && !errors.Is(err, filelock.ErrHeld) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("winners=%d", won)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", info.Mode())
	}
}
