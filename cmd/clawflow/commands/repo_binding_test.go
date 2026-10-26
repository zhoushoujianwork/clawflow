package commands

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestRepoSetBoundMachine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   []string
		binding string
	}{
		{"set", []string{"--bound-machine", "Work-Mac.local"}, "Work-Mac.local"},
		{"clear", []string{"--bound-machine="}, ""},
		{"omitted", nil, "old-host"},
	} {
		for _, automation := range []bool{false, true} {
			t.Run(tc.name+"/automation="+map[bool]string{false: "off", true: "on"}[automation], func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				cfg := &config.Config{Repos: map[string]config.Repo{"ns/repo": {
					Enabled: automation, AutoMerge: automation,
					BoundMachine:   "old-host",
					PrimaryProject: "existing-project", BaseBranch: "develop",
					LocalPath: "/isolated/checkout", Description: "keep description",
					Labels: map[string]string{"trigger": "ready"},
				}}}
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
				want := cfg.Repos["ns/repo"]
				want.BoundMachine = tc.binding
				cmd := newRepoSetCmd()
				cmd.SetArgs(append([]string{"ns/repo"}, tc.flags...))
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				loaded, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				got := loaded.Repos["ns/repo"]
				if got.UpdatedAt.IsZero() {
					t.Fatal("repository update was not stamped")
				}
				got.UpdatedAt = time.Time{}
				got.UpdatedBy = ""
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("repository settings changed unexpectedly:\ngot  %+v\nwant %+v", got, want)
				}
			})
		}
	}
}

func TestRepoSetBoundMachinePreservedWithAutomationFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{"ns/repo": {
		Enabled: false, BoundMachine: "Work-Mac.local", AutoMerge: true,
		PrimaryProject: "existing-project",
	}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cmd := newRepoSetCmd()
	cmd.SetArgs([]string{"ns/repo", "--auto-merge", "off"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := loaded.Repos["ns/repo"]
	if r.BoundMachine != "Work-Mac.local" || r.Enabled || r.AutoMerge || r.PrimaryProject != "existing-project" {
		t.Fatalf("unexpected repository settings: %+v", r)
	}
}

func TestRepoSetBoundMachineRejectsInvalidWithoutMutation(t *testing.T) {
	for _, value := range []string{"two hosts", " host", "host\tname", "host\nname", "host\x00name", "host\x7fname", "host\u00a0name", "host/name", `host\name`} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			cfg := &config.Config{Repos: map[string]config.Repo{"ns/repo": {
				BoundMachine: "old-host", AutoMerge: true,
			}}}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(config.ConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			cmd := newRepoSetCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs([]string{"ns/repo", "--bound-machine", value, "--auto-merge", "off"})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--bound-machine") {
				t.Fatalf("expected binding validation error, got %v", err)
			}
			after, err := os.ReadFile(config.ConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("rejected binding changed the config file")
			}
		})
	}
}
