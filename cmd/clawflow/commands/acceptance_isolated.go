package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/candidate"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Seams for tests: the isolated run itself and the changed-file listing.
var (
	acceptanceRunIsolated     = candidate.RunIsolated
	acceptanceIsolatedTimeout = 25 * time.Minute
)

// acceptanceFileCache remembers the changed-file list of one head/base pair. The
// contract is rebuilt many times per review (inspection, publication, merge), and
// the list cannot change for a fixed head and base, so it is read once.
var acceptanceFileCache = struct {
	sync.Mutex
	files map[string][]string
}{files: map[string][]string{}}

// changedFiles lists the paths a PR changes, from the provider. A provider that
// cannot list them is an error: guessing "no runtime change" would let a runtime
// change skip the run.
func changedFiles(client vcs.Client, repo string, pr vcs.PR) ([]string, error) {
	key := strings.Join([]string{repo, strconv.Itoa(pr.Number), pr.HeadSHA, pr.BaseSHA}, "|")
	acceptanceFileCache.Lock()
	cached, ok := acceptanceFileCache.files[key]
	acceptanceFileCache.Unlock()
	if ok {
		return cached, nil
	}
	provider, ok := client.(vcs.PRFileProvider)
	if !ok {
		return nil, errors.New("the provider cannot list changed files")
	}
	files, err := provider.ListPRFiles(repo, pr.Number)
	if err != nil {
		return nil, err
	}
	acceptanceFileCache.Lock()
	if len(acceptanceFileCache.files) > 256 {
		acceptanceFileCache.files = map[string][]string{}
	}
	acceptanceFileCache.files[key] = files
	acceptanceFileCache.Unlock()
	return files, nil
}

// isolatedRunNeeded reports whether a PR must be run in an isolated candidate
// before it can be accepted, from the provider's changed-file list and never
// from PR text or the reviewer. Pure documentation, SKILL text, front-end and
// test changes do not trigger. A candidate never starts another candidate.
func isolatedRunNeeded(client vcs.Client, rc config.Repo, repo string, pr vcs.PR) (bool, error) {
	if !rc.AcceptanceIsolatedRun || devmode.Active() {
		return false, nil
	}
	files, err := changedFiles(client, repo, pr)
	if err != nil {
		return false, acceptanceReadError{"acceptance changed-file list unavailable: " + err.Error()}
	}
	return len(candidate.RuntimeChange(files)) > 0, nil
}

// isolatedRunner builds the hook Runner.Isolated calls. workspace is the review
// checkout, used only as the source to clone the head commit from.
func isolatedRunner(client vcs.Client, repo string, pr vcs.PR) func(context.Context, acceptance.Contract, string) (acceptance.IsolatedRun, error) {
	return func(ctx context.Context, c acceptance.Contract, workspace string) (acceptance.IsolatedRun, error) {
		started := time.Now().UTC()
		files, err := changedFiles(client, repo, pr)
		if err != nil {
			return acceptance.IsolatedRun{}, err
		}
		triggers := candidate.RuntimeChange(files)
		base, err := os.MkdirTemp("", "clawflow-isolated-")
		if err != nil {
			return acceptance.IsolatedRun{}, err
		}
		defer removeReadOnlyTree(base)
		runCtx, cancel := context.WithTimeout(ctx, acceptanceIsolatedTimeout)
		defer cancel()
		res, err := acceptanceRunIsolated(runCtx, candidate.IsolatedInput{
			Commit:      c.HeadSHA,
			Source:      workspace,
			Base:        filepath.Join(base, "candidates"),
			EvidenceDir: filepath.Join(base, "evidence"),
			Stable:      stableHost(),
			Probe:       candidate.DefaultStableProbe(config.RootDir(), filepath.Join(base, "candidates")),
			LockPath:    filepath.Join(os.TempDir(), "clawflow-isolated", "run.lock"),
			GoCache:     isolatedGoCache(),
			ModCache:    isolatedModCache(),
		})
		if err != nil {
			return acceptance.IsolatedRun{}, err
		}
		return isolatedRecord(c, res, triggers, started), nil
	}
}

// removeReadOnlyTree deletes a tree whose module cache is read-only.
func removeReadOnlyTree(root string) {
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(root)
}

// isolatedGoCache is the build cache shared by isolated runs, kept apart from the
// user's own cache so a run can never read or poison it.
func isolatedGoCache() string {
	return filepath.Join(os.TempDir(), "clawflow-isolated-go", "build")
}

// isolatedModCache lets the build reuse already-downloaded modules read-only.
func isolatedModCache() string {
	out, err := goEnv("GOMODCACHE")
	if err != nil || out == "" {
		return ""
	}
	return out
}

// isolatedRecord converts candidate evidence into the acceptance record's run.
// Its status is recomputed from the evidence, so it cannot disagree with it.
func isolatedRecord(c acceptance.Contract, res candidate.IsolatedResult, triggers []string, started time.Time) acceptance.IsolatedRun {
	ev := res.Evidence
	run := acceptance.IsolatedRun{
		Commit: c.HeadSHA, RuntimeID: ev.ID, Triggers: triggers, Status: acceptance.IsolatedFailed,
		Digest: res.Digest, StartedAt: started, CompletedAt: time.Now().UTC(),
	}
	if ev.Task != nil {
		task := *ev.Task
		task.Clip()
		run.Summary, run.Failure = task.Summary(), task.Failure
		run.ResidualProcesses, run.ResidualLocks = task.ResidualProcesses, task.ResidualLocks
		for _, s := range task.Steps {
			run.Steps = append(run.Steps, acceptance.IsolatedStep{Name: s.Name, Passed: s.Passed, Evidence: s.Evidence})
		}
	}
	if run.Summary == "" {
		run.Summary = "isolated run of candidate " + ev.ID + " recorded no task"
	}
	if res.Passed() {
		run.Status = acceptance.IsolatedPassed
		return run
	}
	if run.Failure == "" && len(run.ResidualProcesses) == 0 && len(run.ResidualLocks) == 0 {
		run.Failure = strings.TrimSpace("candidate evidence status " + ev.Status + " " + strings.Join(failedLayers(ev), ", "))
	}
	return run
}

func failedLayers(ev *candidate.Evidence) []string {
	var out []string
	for name, state := range ev.Layers {
		if state == candidate.Failed || state == candidate.Blocked {
			out = append(out, name+"="+state)
		}
	}
	return out
}
