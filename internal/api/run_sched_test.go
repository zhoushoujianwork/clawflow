package api

import (
	"context"
	"github.com/zhoushoujianwork/clawflow/internal/agentcapacity"
	"reflect"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func schedCfg(max int, require bool, repos map[string]config.Repo) *config.Config {
	return &config.Config{Settings: config.Settings{MaxConcurrentAgents: max, RequireBinding: require}, Repos: repos}
}

func TestPlanScheduledRuns(t *testing.T) {
	on := config.Repo{Enabled: true}
	repos := map[string]config.Repo{
		"o/a": on, "o/b": on, "o/c": on,
		"o/off":    {Enabled: false},
		"o/other":  {Enabled: true, BoundMachine: "elsewhere"},
		"o/mine":   {Enabled: true, BoundMachine: "host1"},
		"o/zunset": on,
	}
	cases := []struct {
		name   string
		cfg    *config.Config
		active map[string]bool
		only   map[string]bool
		want   []string
	}{
		{"all eligible, disabled skipped", schedCfg(10, false, repos), nil, nil,
			[]string{"o/a", "o/b", "o/c", "o/mine", "o/other", "o/zunset"}},
		{"active repo skipped", schedCfg(10, false, repos), map[string]bool{"o/a": true}, nil,
			[]string{"o/b", "o/c", "o/mine", "o/other", "o/zunset"}},
		{"agent cap does not count active passes", schedCfg(3, false, repos), map[string]bool{"o/a": true}, nil,
			[]string{"o/b", "o/c", "o/mine", "o/other", "o/zunset"}},
		{"default capacity does not limit passes", schedCfg(0, false, repos), nil, nil,
			[]string{"o/a", "o/b", "o/c", "o/mine", "o/other", "o/zunset"}},
		{"active passes do not consume slots", schedCfg(2, false, repos), map[string]bool{"o/a": true, "o/b": true}, nil, []string{"o/c", "o/mine", "o/other", "o/zunset"}},
		{"passes can exceed agent slots", schedCfg(1, false, repos), map[string]bool{"o/a": true, "o/b": true}, nil, []string{"o/c", "o/mine", "o/other", "o/zunset"}},
		{"require_binding ignored", schedCfg(10, true, repos), nil, nil, []string{"o/a", "o/b", "o/c", "o/mine", "o/other", "o/zunset"}},
		{"unscoped pass blocks all", schedCfg(10, false, repos), map[string]bool{unscopedKey: true}, nil, nil},
		{"only filter", schedCfg(10, false, repos), nil, map[string]bool{"o/b": true, "o/off": true}, []string{"o/b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanScheduledRuns(tc.cfg, "host1", tc.active, tc.only)
			if len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlanScheduledRunsEmptyHostnameAcceptsAll(t *testing.T) {
	cfg := schedCfg(10, true, map[string]config.Repo{"o/a": {Enabled: true}, "o/b": {Enabled: true, BoundMachine: "x"}})
	if got := PlanScheduledRuns(cfg, "", nil, nil); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestCanStartRunLocked(t *testing.T) {
	cases := []struct {
		name   string
		active map[string]bool
		repo   string
		want   bool
	}{
		{"unscoped when idle", map[string]bool{}, "", true},
		{"unscoped blocked by repo pass", map[string]bool{"o/a": true}, "", false},
		{"repo when idle", map[string]bool{}, "o/a", true},
		{"same repo blocked", map[string]bool{"o/a": true}, "o/a", false},
		{"different repo allowed", map[string]bool{"o/a": true}, "o/b", true},
		{"repo blocked by unscoped", map[string]bool{unscopedKey: true}, "o/b", false},
	}
	for _, tc := range cases {
		if got := canStartRunLocked(tc.active, tc.repo); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestCurrentRunStatusReportsActiveRepos(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runMu.Lock()
	activeRuns = map[string]bool{"o/b": true, "o/a": true}
	runMu.Unlock()
	t.Cleanup(func() { runMu.Lock(); activeRuns = map[string]bool{}; runMu.Unlock() })
	st := currentRunStatus()
	if st.Status != "running" || !reflect.DeepEqual(st.ActiveRepos, []string{"o/a", "o/b"}) {
		t.Fatalf("status = %+v", st)
	}
	runMu.Lock()
	activeRuns = map[string]bool{unscopedKey: true}
	runMu.Unlock()
	if st := currentRunStatus(); st.Status != "running" || len(st.ActiveRepos) != 0 {
		t.Fatalf("unscoped status = %+v", st)
	}
}

func TestCurrentRunStatusAgentSlots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Settings: config.Settings{MaxConcurrentAgents: 2}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	release, err := agentcapacity.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	st := currentRunStatus()
	if st.AgentSlotsUsed != 1 || st.AgentSlotsTotal != 2 {
		t.Fatalf("status=%+v", st)
	}
	release()
	if st := currentRunStatus(); st.AgentSlotsUsed != 0 {
		t.Fatalf("released status=%+v", st)
	}
}
