// Package upgrade implements the CLI-owned rolling upgrade lifecycle for the
// single-host controller and its workers. The engine is driven through small
// ports so the same state machine runs against launchd in production and
// against isolated fixtures in tests.
package upgrade

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// WorkerProtocol is the durable worker protocol this build speaks.
const WorkerProtocol = 1

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidCommit reports whether s is a full lowercase 40-hex commit SHA.
func ValidCommit(s string) bool { return commitPattern.MatchString(s) }

// Manifest is what a binary reports about itself. It contains no paths or
// credentials, so it can be stored in upgrade records.
type Manifest struct {
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	SchemaLatest     int    `json:"schema_latest"`
	ContractVersions []int  `json:"contract_versions"`
	WorkerProtocol   int    `json:"worker_protocol"`
	// Inferred is set when a binary predates the manifest command and its
	// schema level was taken from the database it ran against.
	Inferred bool `json:"inferred,omitempty"`
}

// LocalManifest describes the running build.
func LocalManifest(version, commit string, migs []store.Migration) Manifest {
	return Manifest{Version: version, Commit: commit, SchemaLatest: store.LatestVersion(migs),
		ContractVersions: store.ContractVersions(migs), WorkerProtocol: WorkerProtocol}
}

func maxInt(values []int) int {
	m := 0
	for _, v := range values {
		if v > m {
			m = v
		}
	}
	return m
}

func (m Manifest) isContract(v int) bool {
	for _, c := range m.ContractVersions {
		if c == v {
			return true
		}
	}
	return false
}

// CheckTransition rejects an upgrade from prior to target against a database
// whose applied migration versions are applied. It runs before anything is
// changed. Rolling upgrades share one database between the old and new
// process, so only additive (expand) migrations are allowed to be pending.
func CheckTransition(prior, target Manifest, applied []int) error {
	if !ValidCommit(target.Commit) {
		return fmt.Errorf("target commit %q is not a full 40-character SHA", target.Commit)
	}
	if target.SchemaLatest <= 0 {
		return fmt.Errorf("target does not report a schema version")
	}
	dbLevel := maxInt(applied)
	if dbLevel > target.SchemaLatest {
		return fmt.Errorf("database schema %d is newer than the target's %d; refusing a downgrade", dbLevel, target.SchemaLatest)
	}
	if target.SchemaLatest < prior.SchemaLatest {
		return fmt.Errorf("target schema %d is older than the running schema %d", target.SchemaLatest, prior.SchemaLatest)
	}
	if target.WorkerProtocol < prior.WorkerProtocol {
		return fmt.Errorf("target worker protocol %d is older than the running protocol %d", target.WorkerProtocol, prior.WorkerProtocol)
	}
	if target.WorkerProtocol < 1 {
		return fmt.Errorf("target does not support durable worker protocol 1")
	}
	for v := dbLevel + 1; v <= target.SchemaLatest; v++ {
		if target.isContract(v) {
			return fmt.Errorf("migration %d is a contract (destructive) change; it cannot run while the running binary shares the database. Apply it in a later release after the expand release is promoted", v)
		}
	}
	return nil
}

// RollbackEligibility explains whether the prior binary may run again on the
// database as it stands now. Binary rollback is not database rollback: the
// schema stays at its current level, so the prior binary must tolerate it.
type RollbackEligibility struct {
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

// CheckRollback fails closed: anything it cannot positively establish makes
// the rollback ineligible. durableWork is true while durable task leases or
// unsettled results exist.
func CheckRollback(prior, target Manifest, applied []int, durableWork bool) RollbackEligibility {
	no := func(format string, args ...any) RollbackEligibility {
		return RollbackEligibility{Reason: fmt.Sprintf(format, args...)}
	}
	if prior.SchemaLatest <= 0 {
		return no("the prior binary's schema level is unknown")
	}
	dbLevel := maxInt(applied)
	if dbLevel > target.SchemaLatest {
		return no("database schema %d is newer than the upgraded binary's %d; something else migrated it", dbLevel, target.SchemaLatest)
	}
	var offenders []int
	for _, v := range applied {
		if v > prior.SchemaLatest && target.isContract(v) {
			offenders = append(offenders, v)
		}
	}
	if len(offenders) > 0 {
		sort.Ints(offenders)
		return no("contract migration(s) %v were applied after the prior binary's schema %d; it cannot run on this database", offenders, prior.SchemaLatest)
	}
	if prior.WorkerProtocol < target.WorkerProtocol && durableWork {
		return no("the prior binary speaks worker protocol %d, below %d, and durable tasks or unsettled results exist", prior.WorkerProtocol, target.WorkerProtocol)
	}
	return RollbackEligibility{Eligible: true, Reason: fmt.Sprintf("only expand migrations were applied after schema %d", prior.SchemaLatest)}
}
