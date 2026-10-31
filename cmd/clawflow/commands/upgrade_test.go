package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/devmode"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/upgrade"
)

func TestUpgradeManifestReportsSchemaAndProtocol(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"upgrade", "manifest"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var m upgrade.Manifest
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if m.SchemaLatest != store.LatestVersion(store.Migrations) || m.WorkerProtocol != upgrade.WorkerProtocol || m.ContractVersions == nil {
		t.Fatalf("%+v", m)
	}
	if strings.Contains(out.String(), os.Getenv("HOME")) && os.Getenv("HOME") != "" && os.Getenv("HOME") != "/" {
		t.Fatal("manifest must not contain local paths")
	}
}

func TestUpgradeRefusesPromotionWithoutExecuteOrGates(t *testing.T) {
	for _, args := range [][]string{
		{"upgrade", "controller"},
		{"upgrade", "controller", "--execute"},
		{"upgrade", "plan"},
		{"upgrade", "workers", "--execute"},
	} {
		root := NewRootCmd()
		root.SetArgs(args)
		root.SilenceErrors, root.SilenceUsage = true, true
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		if err := root.Execute(); err == nil {
			t.Errorf("%v: expected a refusal", args)
		}
	}
}

func TestUpgradeIsHostMutatingInCandidateMode(t *testing.T) {
	candidateHome(t)
	for _, args := range [][]string{{"upgrade", "plan"}, {"upgrade", "controller"}, {"upgrade", "resume", "20260101T000000Z-00000000"}} {
		root := NewRootCmd()
		root.SetArgs(args)
		root.SilenceErrors, root.SilenceUsage = true, true
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		if err := root.Execute(); err == nil {
			t.Errorf("%v: a candidate must not promote the stable host", args)
		}
	}
	if !errors.Is(refuseInCandidate(), devmode.ErrBlocked) {
		t.Fatal("refuseInCandidate must block inside a candidate")
	}
}
