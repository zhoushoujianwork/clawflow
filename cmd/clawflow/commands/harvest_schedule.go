package commands

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// The repo run lock serializes this state across passes and processes.
type harvestSchedule struct {
	Previous int            `json:"previous"`
	Heads    map[int]string `json:"heads"`
}

func harvestSchedulePath(repo string) string {
	return filepath.Join(snapshot.DataDir(), "harvest", strings.ReplaceAll(repo, "/", "@")+".json")
}

func loadHarvestSchedule(repo string) (harvestSchedule, error) {
	st := harvestSchedule{}
	data, err := os.ReadFile(harvestSchedulePath(repo))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return st, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &st); err != nil {
			return st, err
		}
	}
	if st.Heads == nil {
		st.Heads = map[int]string{}
	}
	return st, nil
}

func saveHarvestSchedule(repo string, st harvestSchedule) error {
	path := harvestSchedulePath(repo)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".harvest-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// All linked issues use the unified queue comparator. PRs without a closing
// issue follow in ascending PR-number order, as do PRs for the same issue.
func orderHarvest(client vcs.Client, repo string, prs []vcs.PR) ([]vcs.PR, map[int]queue.Priority, error) {
	rank, err := queue.LoadRank()
	if err != nil {
		return nil, nil, err
	}
	positions := queue.Positions(rank)
	var items []queue.Item
	seen := map[queue.Key]bool{}
	for _, pr := range prs {
		key := queue.Key{Repo: repo, Number: extractClosesIssue(pr.Body)}
		if key.Number == 0 || seen[key] {
			continue
		}
		issue, err := client.GetIssue(repo, key.Number)
		if err != nil {
			return nil, nil, err
		}
		priority, _ := queue.PriorityFromLabels(issue.Labels)
		items = append(items, queue.Item{Key: key, Priority: priority, CreatedAt: issue.CreatedAt})
		seen[key] = true
	}
	ordered := queue.Order(items, positions)
	order := map[int]int{}
	priorities := map[int]queue.Priority{}
	for i, item := range ordered {
		order[item.Key.Number] = i
		priorities[item.Key.Number] = item.Priority
	}
	out := slices.Clone(prs)
	slices.SortStableFunc(out, func(a, b vcs.PR) int {
		ai, aok := order[extractClosesIssue(a.Body)]
		bi, bok := order[extractClosesIssue(b.Body)]
		switch {
		case aok && !bok:
			return -1
		case !aok && bok:
			return 1
		case aok && bok && ai != bi:
			return ai - bi
		default:
			return a.Number - b.Number
		}
	})
	prPriorities := make(map[int]queue.Priority, len(prs))
	for _, pr := range prs {
		priority, ok := priorities[extractClosesIssue(pr.Body)]
		if !ok {
			priority = queue.DefaultPriority
		}
		prPriorities[pr.Number] = priority
	}
	return out, prPriorities, nil
}

func selectHarvest(prs []vcs.PR, st harvestSchedule, priorities map[int]queue.Priority) int {
	if len(prs) == 0 {
		return -1
	}
	if prs[0].Number == st.Previous && st.Heads[prs[0].Number] == prs[0].HeadSHA {
		for i, pr := range prs[1:] {
			if priorities[pr.Number] != priorities[prs[0].Number] {
				continue
			}
			head, seen := st.Heads[pr.Number]
			if !seen || head != pr.HeadSHA {
				return i + 1
			}
		}
	}
	return 0
}
