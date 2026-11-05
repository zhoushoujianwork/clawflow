package commands

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/preflight"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func runPreflightCommand(args ...string) (string, error) {
	cmd := newPreflightCmd()
	var b bytes.Buffer
	cmd.SetOut(&b)
	cmd.SetErr(&b)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return b.String(), err
}

func TestPreflightCLIStatusDoesNotCreateState(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	store := preflight.Store{Root: snapshot.DataDir(), Repo: "o/r", Issue: 1}
	out, err := runPreflightCommand("status", "--repo", "o/r", "--issue", "1")
	if err != nil || !strings.Contains(out, `"failures":0`) {
		t.Fatal(out, err)
	}
	if _, err = os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Fatal("status created a ledger", err)
	}
}

func TestPreflightCLIResumeRequiresReasonAndKeepsAudit(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	old := preflightLocalActor
	t.Cleanup(func() { preflightLocalActor = old })
	preflightLocalActor = func() (string, error) { return "test-operator", nil }
	store := preflight.Store{Root: snapshot.DataDir(), Repo: "o/r", Issue: 1}
	now := time.Now().Add(-24 * time.Hour)
	for i := 0; i < preflight.MaxFailures; i++ {
		id, err := store.Begin("dispatch", now)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Finish(id, "failure", "timeout", now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Hour)
	}
	before, _ := os.ReadFile(store.Path())
	if _, err := runPreflightCommand("resume", "--repo", "o/r", "--issue", "1"); err == nil {
		t.Fatal("missing reason accepted")
	}
	after, _ := os.ReadFile(store.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("invalid resume mutated budget")
	}
	if _, err := runPreflightCommand("resume", "--repo", "o/r", "--issue", "1", "--reason", "upstream recovered"); err != nil {
		t.Fatal(err)
	}
	l, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	e := l.Events[len(l.Events)-1]
	if len(l.Events) != preflight.MaxFailures+1 || e.Actor != "test-operator" || e.Reason != "upstream recovered" {
		t.Fatal("audit lost", l)
	}
	st, _ := l.Status()
	if st.Failures != 0 {
		t.Fatal(st)
	}
	if _, err = runPreflightCommand("resume", "--repo", "o/r", "--issue", "1", "--reason", "again"); err == nil {
		t.Fatal("unexhausted budget reset")
	}
	id, err := store.Begin("dispatch", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runPreflightCommand("resume", "--repo", "o/r", "--issue", "1", "--reason", "while running"); !errors.Is(err, preflight.ErrPending) {
		t.Fatal(err)
	}
	l, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, _ = l.Status()
	if st.PendingID != id {
		t.Fatal("pending attempt changed")
	}
}
