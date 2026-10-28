package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if _, err = run(true); err == nil || !strings.Contains(err.Error(), "not_implemented") {
		t.Fatalf("%v", err)
	}
}
