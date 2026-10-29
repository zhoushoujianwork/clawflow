package commands

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// This subprocess holds exactly the inherited reservations used by real reviews.
func TestHarvestChildProcess(t *testing.T) {
	dir := os.Getenv("CLAWFLOW_HARVEST_HELPER")
	if dir == "" {
		return
	}
	pr := ""
	for i, arg := range os.Args {
		if arg == "--pr" && i+1 < len(os.Args) {
			pr = os.Args[i+1]
		}
	}
	if pr == "" {
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(dir, pr+"-"+strconv.Itoa(os.Getpid())), []byte("started"), 0600); err != nil {
		os.Exit(3)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitHarvestCondition(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for acceptance subprocess")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHarvestLaunchesConcurrentReviewsWithoutWaiting(t *testing.T) {
	for _, tc := range []struct{ machine, repo, want int }{{3, 3, 3}, {2, 3, 2}, {4, 1, 1}} {
		t.Run(strconv.Itoa(tc.machine)+"-"+strconv.Itoa(tc.repo), func(t *testing.T) {
			f, _, _ := harvestFixture(t)
			setHarvestRepo(t, func(r *config.Repo) { r.AcceptanceMaxParallel = tc.repo })
			acceptanceClientFactory = func(config.Repo) (vcs.Client, error) { return harvestMultiFake{f}, nil }
			f.openPRs = []vcs.PR{f.pr, f.pr, f.pr}
			for i := range f.openPRs {
				f.openPRs[i].Number = 11 + i
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Settings.MaxConcurrentAgents = tc.machine
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			t.Setenv("CLAWFLOW_HARVEST_HELPER", dir)
			oldCmd := harvestCommand
			t.Cleanup(func() { harvestCommand = oldCmd })
			harvestCommand = func(_ string, args ...string) *exec.Cmd {
				return exec.Command(os.Args[0], append([]string{"-test.run=^TestHarvestChildProcess$", "--"}, args...)...)
			}
			launchHarvestFn = launchHarvest
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
				waitHarvestCondition(t, func() bool {
					var held []*os.File
					defer func() {
						for _, f := range held {
							filelock.Release(f)
						}
					}()
					for i := 0; i < tc.want; i++ {
						f, err := tryHarvestSlot("agents", tc.want)
						if err != nil {
							return false
						}
						held = append(held, f)
					}
					return true
				})
			})
			started := time.Now()
			if failures := runHarvestPhase(context.Background(), cfg, "", 0, 30*time.Minute, time.Now().Add(time.Hour)); len(failures) > 0 {
				t.Fatal(failures)
			}
			if time.Since(started) > 5*time.Second {
				t.Fatal("scheduler waited for review")
			}
			waitHarvestCondition(t, func() bool { files, _ := os.ReadDir(dir); return len(files) == tc.want })
			// A second pass cannot launch duplicates, including before a real review
			// has acquired its own per-PR acceptance lock.
			if failures := runHarvestPhase(context.Background(), cfg, "", 0, 30*time.Minute, time.Now().Add(time.Hour)); len(failures) > 0 {
				t.Fatal(failures)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != tc.want {
				t.Fatalf("reviews=%d want=%d", len(files), tc.want)
			}
			if tc.machine == tc.want {
				if slot, err := tryAgentSlot(tc.machine); !errors.Is(err, errHarvestBusy) {
					if slot != nil {
						filelock.Release(slot)
					}
					t.Fatalf("machine cap not held: %v", err)
				}
				if release, err := configuredAgentSlot(context.Background()); err == nil {
					release()
					t.Fatal("operator admitted beyond machine cap")
				}
			}
		})
	}
}

func TestHarvestRoundRobinAcrossRepos(t *testing.T) {
	harvestFixture(t)
	var queues [][]harvestTask
	for _, repo := range []string{"o/a", "o/b", "o/r"} {
		queues = append(queues, []harvestTask{{repo: repo}, {repo: repo}, {repo: repo}})
	}
	var order []string
	launchHarvestFn = func(_ context.Context, task harvestTask, _ int) error { order = append(order, task.repo); return nil }
	dispatchHarvestQueues(context.Background(), queues, 3)
	if strings.Join(order, ",") != "o/a,o/b,o/r,o/a,o/b,o/r,o/a,o/b,o/r" {
		t.Fatalf("launch order: %v", order)
	}
}

func TestHarvestDefersMergeAndSerializesRepoMerge(t *testing.T) {
	f, _, _ := harvestFixture(t)
	cfg, _ := config.Load()
	rc := cfg.Repos["o/r"]
	rc.AutoMerge = false
	if failure := harvestRun(context.Background(), f, func(context.Context) {}, "o/r", rc, f.pr, 42, 30*time.Minute); failure != nil {
		t.Fatal(failure)
	}
	if len(f.expected) != 0 {
		t.Fatal("background review merged")
	}
	release, err := mergeRepoLock("o/r")
	if err != nil {
		t.Fatal(err)
	}
	rc.AutoMerge = true
	if result := attemptAutoMergeResult(autoMergeAttempt{ctx: context.Background(), client: f, repo: "o/r", repoCfg: rc, prNum: 12}); result.Reason != "repo_merge_busy" {
		t.Fatalf("concurrent merge: %+v", result)
	}
	release()
	// A sibling merge moves the base; the completed old verdict must not merge.
	f.pr.BaseSHA = strings.Repeat("d", 40)
	if attemptAutoMerge(autoMergeAttempt{ctx: context.Background(), client: f, repo: "o/r", repoCfg: rc, prNum: 11}) || len(f.expected) != 0 {
		t.Fatal("stale verdict merged after base moved")
	}
}

func TestAcceptanceAgentCapPreservesPRLock(t *testing.T) {
	f, _ := acceptanceFixture(t)
	release, err := acceptanceLock("o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := runAcceptance(context.Background(), f, "o/r", 11); err == nil {
		t.Fatal("duplicate PR review admitted")
	}
}

func TestRepoSetAcceptanceMaxParallel(t *testing.T) {
	for _, value := range []string{"0", "-1", "1", "5"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {AcceptanceMaxParallel: 3, AutoMerge: true}}}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			cmd := newRepoSetCmd()
			cmd.SetArgs([]string{"o/r", "--acceptance-max-parallel", value})
			err := cmd.Execute()
			n, _ := strconv.Atoi(value)
			if (err != nil) != (n < 1) {
				t.Fatalf("value=%s error=%v", value, err)
			}
			loaded, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			want := n
			if n < 1 {
				want = 3
			}
			if loaded.Repos["o/r"].AcceptanceMaxParallel != want || !loaded.Repos["o/r"].AutoMerge {
				t.Fatalf("unexpected config: %+v", loaded.Repos["o/r"])
			}
		})
	}
}

func TestHarvestPinsHeadFromSummaryOnlyProvider(t *testing.T) {
	f, _, _ := harvestFixture(t)
	summary := f.pr
	summary.HeadSHA = ""
	f.openPRs = []vcs.PR{summary}
	var head string
	launchHarvestFn = func(_ context.Context, task harvestTask, _ int) error { head = task.pr.HeadSHA; return nil }
	if failures := harvest(t, "", 0); len(failures) != 0 {
		t.Fatal(failures)
	}
	if head != f.pr.HeadSHA {
		t.Fatalf("unpinned summary: %q", head)
	}
}
