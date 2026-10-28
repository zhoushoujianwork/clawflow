package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func TestPilotApplyCLI(t *testing.T) {
	queueCmdSetup(t)
	file := filepath.Join(t.TempDir(), "actions.json")
	if err := os.WriteFile(file, []byte(`{"run_id":"manual","actions":[{"id":"note","type":"note","body":"decision"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(dry bool) ([]pilot.ActionRecord, error) {
		cmd := NewPilotCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		args := []string{"apply", "--repo", "o/r", "--file", file}
		if dry {
			args = append(args, "--dry-run")
		}
		cmd.SetArgs(args)
		err := cmd.Execute()
		var records []pilot.ActionRecord
		if err == nil {
			if decodeErr := json.Unmarshal(out.Bytes(), &records); decodeErr != nil {
				t.Fatal(decodeErr)
			}
		}
		return records, err
	}
	records, err := run(true)
	if err != nil || len(records) != 1 || records[0].Outcome != "valid" || !strings.HasPrefix(records[0].Actor, "cli:") {
		t.Fatalf("%+v %v", records, err)
	}
	audit := filepath.Join(snapshot.DataDir(), "pilot-actions.jsonl")
	if _, err := os.Stat(audit); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote audit: %v", err)
	}
	records, err = run(false)
	if err != nil || records[0].Outcome != "applied" {
		t.Fatalf("%+v %v", records, err)
	}
	records, err = run(false)
	if err != nil || records[0].Outcome != "already_applied" {
		t.Fatalf("%+v %v", records, err)
	}
	if err := os.WriteFile(file, []byte(`{"run_id":"manual2","actions":[{"id":"merge","type":"override_merge"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = run(true); err == nil || !strings.Contains(err.Error(), "pr_and_head_required") {
		t.Fatalf("%v", err)
	}
}

func TestPilotMergeUpgrade(t *testing.T) {
	old := pilotUpgrade
	defer func() { pilotUpgrade = old }()
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {SelfUpgrade: true}, "o/other": {SelfUpgrade: true}}}
	called := 0
	pilotUpgrade = func(_ context.Context, selected *config.Config) error {
		called++
		if len(selected.Repos) != 1 || !selected.Repos["o/r"].SelfUpgrade {
			t.Fatalf("wrong repos: %+v", selected.Repos)
		}
		return nil
	}
	for _, outcome := range []string{"failed", "valid", "already_applied", "applied"} {
		err := upgradePilotMerges(context.Background(), cfg, []pilot.ActionRecord{{Action: pilot.Action{Type: "override_merge", Repo: "o/r"}, Outcome: outcome}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if called != 1 {
		t.Fatalf("upgrade calls %d", called)
	}
}

func TestPilotNoVerdictReason(t *testing.T) {
	ctx := context.Background()
	t.Run("provenance", func(t *testing.T) {
		f, _ := acceptanceFixture(t)
		// Replace the successful development run with one that did not succeed.
		os.RemoveAll(filepath.Dir(snapshot.RunDir("o/r", 42, time.Now())))
		writeAcceptanceDevelopment(t, "failed", time.Now().Add(-time.Minute), nil)
		got, err := pilotNoVerdictReason(ctx, f, "o/r", 11)
		if err != nil || got != pilot.NoVerdictProvenance {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("infra exhausted", func(t *testing.T) {
		f, _ := acceptanceFixture(t)
		failProvider()
		for i := 0; i < harvestMaxInfra; i++ {
			if got, err := pilotNoVerdictReason(ctx, f, "o/r", 11); err != nil || got != "" {
				t.Fatalf("before exhaustion (%d attempts): %q, %v", i, got, err)
			}
			if _, err := runAcceptance(ctx, f, "o/r", 11); err == nil {
				t.Fatal("provider failure should be an error")
			}
		}
		got, err := pilotNoVerdictReason(ctx, f, "o/r", 11)
		if err != nil || got != pilot.NoVerdictInfraBudget {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("never attempted", func(t *testing.T) {
		f, _ := acceptanceFixture(t)
		if got, err := pilotNoVerdictReason(ctx, f, "o/r", 11); err != nil || got != "" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("other contract error", func(t *testing.T) {
		f, _ := acceptanceFixture(t)
		f.pr.Draft = true
		if got, err := pilotNoVerdictReason(ctx, f, "o/r", 11); err == nil || got != "" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}
