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

// Ranked issues use the unified queue comparator; unranked PRs follow in
// ascending PR-number order. Multiple PRs for one issue also sort oldest first.
func orderHarvest(client vcs.Client, repo string, prs []vcs.PR) ([]vcs.PR, error) {
	rank, err := queue.LoadRank()
	if err != nil {
		return nil, err
	}
	positions := queue.Positions(rank)
	var items []queue.Item
	seen := map[queue.Key]bool{}
	for _, pr := range prs {
		key := queue.Key{Repo: repo, Number: extractClosesIssue(pr.Body)}
		if _, ranked := positions[key]; !ranked || seen[key] {
			continue
		}
		issue, err := client.GetIssue(repo, key.Number)
		if err != nil {
			return nil, err
		}
		priority, _ := queue.PriorityFromLabels(issue.Labels)
		items = append(items, queue.Item{Key: key, Priority: priority, CreatedAt: issue.CreatedAt})
		seen[key] = true
	}
	ordered := queue.Order(items, positions)
	order := map[int]int{}
	for i, item := range ordered {
		order[item.Key.Number] = i
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
	return out, nil
}

func selectHarvest(prs []vcs.PR, st harvestSchedule) int {
	if len(prs) == 0 {
		return -1
	}
	if prs[0].Number == st.Previous {
		for i, pr := range prs[1:] {
			head, seen := st.Heads[pr.Number]
			if !seen || head != pr.HeadSHA {
				return i + 1
			}
		}
	}
	return 0
}
