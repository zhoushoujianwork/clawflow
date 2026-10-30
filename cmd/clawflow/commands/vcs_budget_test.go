package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

func TestSchedulerReservePreservesDelivery(t *testing.T) {
	writeTestConfig(t, &config.Config{})
	readLog := withRunLog(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "499")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		if r.URL.Path == "/repos/o/r" {
			fmt.Fprint(w, `{"id":1}`)
		} else {
			fmt.Fprint(w, `[]`)
		}
	}))
	defer server.Close()
	rc := config.Repo{Enabled: true, AutoMerge: true, BaseURL: server.URL}
	client, err := newVCSClient(rc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.(*github.Client).GetRepoID("o/r"); err != nil {
		t.Fatal(err)
	}
	var issues []snapshot.IssueEntry
	pending, jobs, err := scanRepoOnce(context.Background(), operator.NewRegistry(), "o/r", rc, true, 0, &issues)
	if !errors.Is(err, vcs.ErrPollingDeferred) || len(jobs) != 0 || len(pending) != 0 {
		t.Fatalf("pending=%v jobs=%v err=%v", pending, jobs, err)
	}
	sweepAutoMergePRs(context.Background(), client, "o/r", rc, 0)
	if calls != 1 {
		t.Fatalf("polling consumed reserve: %d", calls)
	}
	if err := client.AddLabel("o/r", 1, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.(vcs.IssueEventLister).ListIssueEvents("o/r", 1); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("delivery did not run: %d", calls)
	}
	if strings.Count(readLog(), "vcs/rate_limited") != 1 {
		t.Fatal("rate limit event not deduplicated")
	}
}

func TestSchedulerPassSharesListsWithHarvestAndSweep(t *testing.T) {
	writeTestConfig(t, &config.Config{})
	withRunLog(t)
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") == "open" {
			calls[r.URL.Path]++
		}
		if strings.Count(r.URL.Path, "/") == 3 {
			fmt.Fprint(w, `{"id":1}`)
		} else {
			fmt.Fprint(w, `[]`)
		}
	}))
	defer server.Close()
	repos := map[string]config.Repo{}
	for n := 0; n < 3; n++ {
		repos[fmt.Sprintf("o/r%d", n)] = config.Repo{Enabled: true, AutoMerge: true, BaseURL: server.URL}
	}
	if err := (&config.Config{Repos: repos}).Save(); err != nil {
		t.Fatal(err)
	}
	ctx, pass := github.BeginPass(context.Background())
	for repo, rc := range repos {
		var issues []snapshot.IssueEntry
		if _, _, err := scanRepoOnce(ctx, operator.NewRegistry(), repo, rc, true, 0, &issues); err != nil {
			t.Fatal(err)
		}
		harvestCandidates(ctx, repo, rc, 0, time.Minute, time.Now().Add(10*time.Minute))
		// A reopen guard uses the same open-PR listing as the sweep and harvest.
		client, err := newVCSClient(rc)
		if err != nil {
			t.Fatal(err)
		}
		setVCSContext(client, ctx)
		if _, err := client.ListOpenPRs(repo); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 6 {
		t.Fatalf("expected two endpoints per repo: %v", calls)
	}
	for path, n := range calls {
		if n != 1 {
			t.Fatalf("%s listed %d times", path, n)
		}
	}

	pass.Save(filepath.Join(snapshot.DataDir(), "vcs-usage.json"))
	board := buildPipeline(ctx, pipelineTestDeps(newPipelineFake(), map[string]config.Repo{}))
	if board.VCSUsage == nil || board.VCSUsage.Calls["scan"] == 0 || len(board.VCSUsage.Budgets) != 1 {
		t.Fatalf("pass usage not published to board: %+v", board.VCSUsage)
	}
}
