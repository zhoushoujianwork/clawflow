package coordinator

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/coordination"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestCoordinationEnqueueSerializesConcurrentSnapshots(t *testing.T) {
	s, _, _ := setup(t)

	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, n := range []int{10, 11} {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			payload, _ := json.Marshal(struct {
				Coordination coordination.Snapshot `json:"coordination"`
			}{coordination.Snapshot{Version: coordination.Version, Repository: "owner/repo", Operation: "implement", Issue: vcs.Issue{Number: n}}})
			_, e := s.Enqueue(ctx, Task{Repo: "owner/repo", Issue: n, Kind: "implement", Operator: "implement", Payload: payload})
			results <- e
		}(n)
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, stale := 0, 0
	for e := range results {
		if e == nil {
			succeeded++
		} else if errors.Is(e, ErrCoordinationChanged) {
			stale++
		} else {
			t.Fatal(e)
		}
	}
	if succeeded != 1 || stale != 1 {
		t.Fatalf("succeeded=%d stale=%d", succeeded, stale)
	}
	active, e := s.Active(ctx)
	if e != nil || len(active) != 1 {
		t.Fatalf("active=%v err=%v", active, e)
	}
	a := active[0]
	approved := coordination.Snapshot{Version: coordination.Version, Repository: a.Repo, Operation: "implement", Issue: vcs.Issue{Number: 12}, Tasks: []coordination.Task{{ID: a.ID, Issue: a.Issue, PR: a.PR, Kind: a.Kind, State: a.State, Priority: a.Priority}}}
	payload, _ := json.Marshal(struct {
		Coordination coordination.Snapshot `json:"coordination"`
	}{approved})
	if _, e = s.Enqueue(ctx, Task{Repo: a.Repo, Issue: 12, Kind: "implement", Operator: "implement", Payload: payload}); e != nil {
		t.Fatalf("fresh compatible assessment rejected: %v", e)
	}
}
