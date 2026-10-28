package commands

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type harvestOrderFake struct {
	vcs.Client
	issues map[int]*vcs.Issue
}

func (f harvestOrderFake) GetIssue(_ string, n int) (*vcs.Issue, error) {
	return f.issues[n], nil
}

func TestHarvestQueueOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := queue.UpdateRank(func([]queue.Key) ([]queue.Key, error) {
		return []queue.Key{{Repo: "o/r", Number: 2}, {Repo: "o/r", Number: 1}, {Repo: "o/r", Number: 3}}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := harvestOrderFake{issues: map[int]*vcs.Issue{
		1: {Labels: []string{"P2"}, CreatedAt: "2020-01-01T00:00:00Z"},
		2: {Labels: []string{"P2"}, CreatedAt: "2021-01-01T00:00:00Z"},
		3: {Labels: []string{"P0"}},
	}}
	prs := []vcs.PR{
		{Number: 50, Body: "Fixes #5"}, {Number: 30, Body: "Fixes #3"},
		{Number: 10, Body: "Fixes #1"}, {Number: 40, Body: "Fixes #4"},
		{Number: 20, Body: "Fixes #2"},
	}
	ordered, err := orderHarvest(client, "o/r", prs)
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, pr := range ordered {
		got = append(got, pr.Number)
	}
	if !reflect.DeepEqual(got, []int{30, 20, 10, 40, 50}) {
		t.Fatalf("order = %v", got)
	}
}

type harvestMultiFake struct{ *acceptanceFake }

func (f harvestMultiFake) GetPR(_ string, n int) (vcs.PR, error) {
	pr := f.pr
	pr.Number = n
	return pr, nil
}

func TestHarvestFairnessAndSkipLogsAcrossPasses(t *testing.T) {
	f, readLog, _ := harvestFixture(t)
	acceptanceClientFactory = func(config.Repo) (vcs.Client, error) { return harvestMultiFake{f}, nil }
	older := f.pr
	older.Number = 10
	f.openPRs = []vcs.PR{f.pr, older}
	var runs []int
	runAcceptanceFn = func(_ context.Context, _ vcs.Client, _ string, pr int) (acceptance.Record, error) {
		runs = append(runs, pr)
		return acceptance.Record{}, errors.New("fake infrastructure failure")
	}
	// No stored verdict: the same PR remains eligible for re-harvest.
	harvest(t, "", 0)
	if !strings.Contains(readLog(), "reason=harvest_slot_busy") {
		t.Fatalf("missing busy log: %s", readLog())
	}
	harvest(t, "", 0)
	if !reflect.DeepEqual(runs, []int{10, 11}) {
		t.Fatalf("runs = %v", runs)
	}
	if !strings.Contains(readLog(), "reason=deferred_fairness") {
		t.Fatalf("missing fairness log: %s", readLog())
	}
	// Once both heads were harvested, normal ordering resumes.
	harvest(t, "", 0)
	if !reflect.DeepEqual(runs, []int{10, 11, 10}) {
		t.Fatalf("runs = %v", runs)
	}

}

func TestHarvestNewHeadGetsFairTurn(t *testing.T) {
	prs := []vcs.PR{{Number: 10, HeadSHA: "a"}, {Number: 11, HeadSHA: "b"}}
	st := harvestSchedule{Previous: 10, Heads: map[int]string{10: "a", 11: "b"}}
	if got := selectHarvest(prs, st); got != 0 {
		t.Fatalf("unchanged heads: %d", got)
	}
	prs[1].HeadSHA = "c"
	if got := selectHarvest(prs, st); got != 1 {
		t.Fatalf("new waiting head: %d", got)
	}
}

func TestHarvestSharesWorkerPool(t *testing.T) {
	for _, workers := range []int{1, 2} {
		t.Run(string(rune('0'+workers)), func(t *testing.T) {
			withRunLog(t)
			started := make(chan struct{}, 4)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			stubRunOneOperator(t, func(*runJob) (bool, bool, bool) {
				started <- struct{}{}
				<-release
				return true, false, false
			})
			harvestStarted := make(chan struct{})
			h := &runJob{harvest: func(context.Context) []firedKey {
				close(harvestStarted)
				<-release
				return []firedKey{{Repo: "o/r", IssueNumber: 42, Operator: "acceptance"}}
			}}
			done := make(chan []firedKey, 1)
			go func() {
				_, failed := runJobsParallel(context.Background(), []*runJob{h,
					testJob("o/r", 1, "implement"), testJob("o/r", 2, "implement")}, workers, time.Minute)
				done <- failed
			}()
			select {
			case <-harvestStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("harvest did not start")
			}
			if workers == 2 {
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("implement did not overlap harvest")
				}
			}
			select {
			case <-started:
				t.Fatal("worker budget exceeded")
			case <-time.After(30 * time.Millisecond):
			}
			unblock()
			select {
			case failed := <-done:
				if len(failed) != 1 || failed[0].Operator != "acceptance" {
					t.Fatalf("failures = %v", failed)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("pool did not finish")
			}
		})
	}
}
