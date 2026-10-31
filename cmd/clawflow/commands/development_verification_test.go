package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestMissingDevelopmentSchedulesRealVerification(t *testing.T) {
	for _, mode := range []string{"updated-head", "missing-history"} {
		t.Run(mode, func(t *testing.T) {
			f, _, reg, cfg := repairAcceptanceFixture(t, "passed")
			f.pr.HeadSHA = strings.Repeat("f", 40)
			f.openPRs = []vcs.PR{f.pr}
			if mode == "missing-history" {
				parent := filepath.Dir(snapshot.RunDir("o/r", 42, time.Now()))
				if err := os.RemoveAll(parent); err != nil {
					t.Fatal(err)
				}
			}
			jobs := scanRepair(t, reg, f, cfg, 0)
			if len(jobs) != 1 || jobs[0].repair.reason() != "development_verification" {
				t.Fatalf("missing provenance remained parked: jobs=%v", jobs)
			}
			// Scheduling an actual developer must not manufacture provenance.
			if _, _, _, _, err := acceptanceContract(context.Background(), f, "o/r", 11); err == nil {
				t.Fatal("source gate opened before a real verification run completed")
			}
		})
	}
}

func TestDevelopmentVerificationPreservesDeliveryGates(t *testing.T) {
	for _, mode := range []string{"verified", "running", "corrupt", "CI-pending", "CI-absent", "skip", "fork", "disabled", "spent-budget"} {
		t.Run(mode, func(t *testing.T) {
			f, _, reg, cfg := repairAcceptanceFixture(t, "passed")
			if mode != "verified" {
				f.pr.HeadSHA = strings.Repeat("f", 40)
			}
			switch mode {
			case "running":
				writeAcceptanceDevelopment(t, "running", time.Now(), nil)
			case "corrupt":
				parent := filepath.Dir(snapshot.RunDir("o/r", 42, time.Now()))
				entries, err := os.ReadDir(parent)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(parent, entries[0].Name(), "meta.json"), []byte("not json"), 0600); err != nil {
					t.Fatal(err)
				}
			case "CI-pending":
				f.ciStatus = vcs.CIStatusPending
			case "CI-absent":
				f.ciStatus = vcs.CIStatusNone
			case "skip":
				f.issue.Labels = append(f.issue.Labels, "clawflow-skip")
			case "fork":
				f.pr.HeadRepo = "other/r"
			case "disabled":
				cfg.AutoMerge = false
			case "spent-budget":
				for i := 0; i < ciRepairMaxAttempts; i++ {
					if _, err := reserveRepair(snapshot.DataDir(), "o/r", f.pr, repairReasonCI, time.Now().Add(time.Duration(i-10)*ciRepairCooldown)); err != nil {
						t.Fatal(err)
					}
				}
			}
			f.openPRs = []vcs.PR{f.pr}
			if jobs := scanRepair(t, reg, f, cfg, 0); len(jobs) != 0 {
				t.Fatalf("%s unexpectedly started a new developer: %v", mode, jobs)
			}
		})
	}
}

func TestDevelopmentVerificationRechecksBeforeReservation(t *testing.T) {
	for _, mode := range []string{"verified", "head-moved", "CI-pending", "skip"} {
		t.Run(mode, func(t *testing.T) {
			f, _, reg, cfg := repairAcceptanceFixture(t, "passed")
			f.pr.HeadSHA = strings.Repeat("f", 40)
			f.openPRs = []vcs.PR{f.pr}
			jobs := scanRepair(t, reg, f, cfg, 0)
			if len(jobs) != 1 {
				t.Fatal(jobs)
			}
			switch mode {
			case "verified":
				dir := writeAcceptanceDevelopment(t, "success", time.Now(), nil)
				updateDevelopmentHead(t, dir, f.pr.HeadSHA)
			case "head-moved":
				f.pr.HeadSHA = strings.Repeat("d", 40)
			case "CI-pending":
				f.ciStatus = vcs.CIStatusPending
			case "skip":
				f.issue.Labels = append(f.issue.Labels, "clawflow-skip")
			}
			if err := prepareCIRepair(jobs[0], time.Now()); err == nil {
				t.Fatal("stale verification was reserved")
			}
			_, path := ciRepairLedgerPath(snapshot.DataDir(), "o/r", f.pr.Number)
			ledger, err := loadCIRepairLedger(path)
			if err != nil || len(ledger.Attempts) != 0 {
				t.Fatalf("ineligible verification spent budget: %+v, %v", ledger, err)
			}
		})
	}
}

func TestDevelopmentVerificationRequiresNativeToolActivity(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		valid        bool
	}{
		{"tool", "{\"type\":\"progress\",\"tool_category\":\"shell\"}\n", true},
		{"text-only", "{\"type\":\"final\"}\n", false},
		{"corrupt", "not json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(p, []byte(tc.events), 0600); err != nil {
				t.Fatal(err)
			}
			if err := validateDevelopmentVerificationActivity(p); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}
