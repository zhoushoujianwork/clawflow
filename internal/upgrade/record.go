package upgrade

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Stage names. Controller stages run in this order; rollback stages follow a
// failed verification.
const (
	StagePreflight  = "preflight"
	StageArtifacts  = "artifacts"
	StageMigrate    = "migrate-expand"
	StageQuiesce    = "quiesce"
	StageSettle     = "settle"
	StageStop       = "stop-old"
	StageFence      = "fence"
	StageInstall    = "install"
	StageStart      = "start-new"
	StageVerify     = "verify"
	StageResume     = "resume"
	StageRbStop     = "rollback-stop"
	StageRbFence    = "rollback-fence"
	StageRbInstall  = "rollback-install"
	StageRbStart    = "rollback-start"
	StageRbVerify   = "rollback-verify"
	StageRbResume   = "rollback-resume"
	StageWorkerStep = "worker"
)

// Results are terminal outcomes. An empty result means the upgrade is still
// in progress or was interrupted.
const (
	ResultSucceeded       = "succeeded"
	ResultPostponed       = "postponed"
	ResultFailed          = "failed"
	ResultRolledBack      = "rolled-back"
	ResultRollbackBlocked = "rollback-blocked"
)

// Kinds of upgrade.
const (
	KindController = "controller"
	KindWorkers    = "workers"
)

// StageRecord is one persisted stage transition.
type StageRecord struct {
	Name      string     `json:"name"`
	Status    string     `json:"status"` // running, done, failed, postponed
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Deadline  *time.Time `json:"deadline,omitempty"`
	Detail    string     `json:"detail,omitempty"`
}

// Measurements are observed, not estimated. Resolution is the poll interval:
// the true interruption lies within [value - resolution, value].
type Measurements struct {
	ResolutionMS int64 `json:"resolution_ms"`
	// LastHealthyBefore is the last successful API probe before the old owner
	// was stopped; interruption is measured from it.
	LastHealthyBefore *time.Time `json:"last_healthy_before,omitempty"`
	APIInterruptionMS int64      `json:"api_interruption_ms"`
	APIRecovered      bool       `json:"api_recovered"`
	// ViewerEndpointMS covers the dashboard endpoint the native viewer reads.
	// The native app itself is a separate client that this CLI neither
	// restarts nor observes.
	ViewerEndpointMS        int64 `json:"viewer_endpoint_recovery_ms"`
	ViewerEndpointRecovered bool  `json:"viewer_endpoint_recovered"`
	ViewerAppObserved       bool  `json:"viewer_app_observed"`
	// RollbackInterruptionMS is the second interruption when a rollback ran.
	RollbackInterruptionMS int64  `json:"rollback_interruption_ms,omitempty"`
	Note                   string `json:"note,omitempty"`
}

// WorkerStep tracks one worker in a sequential worker promotion.
type WorkerStep struct {
	Name       string     `json:"name"`
	Descriptor string     `json:"descriptor"`
	Status     string     `json:"status"` // pending, draining, rolled, ready, postponed, failed
	WasPaused  bool       `json:"was_paused"`
	Deadline   *time.Time `json:"deadline,omitempty"`
	RolledAt   *time.Time `json:"rolled_at,omitempty"`
	Detail     string     `json:"detail,omitempty"`
}

// LeaseRef identifies a lease and the retry counters that must not move.
type LeaseRef struct {
	TaskID              string `json:"task_id"`
	Owner               string `json:"owner"`
	Fence               int64  `json:"fence"`
	Attempt             int    `json:"attempt"`
	ApplicationAttempts int    `json:"application_attempts"`
}

// Artifact identifies one immutable binary. Paths are local; no credentials.
type Artifact struct {
	Commit   string   `json:"commit,omitempty"`
	SHA256   string   `json:"sha256"`
	Path     string   `json:"path"`
	Manifest Manifest `json:"manifest"`
}

// Gates records the promotion approvals. The engine verifies they are present
// and consistent with the target; CI and acceptance are attestations by
// reference, never inferred from a local run.
type Gates struct {
	EvidenceDigest string `json:"candidate_evidence_sha256"`
	CIRef          string `json:"ci_ref"`
	AcceptanceRef  string `json:"acceptance_ref"`
	ApprovedSHA    string `json:"approved_sha"`
}

// Record is the durable, secret-free account of one upgrade.
type Record struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Direction string    `json:"direction"` // forward or rollback
	Stage     string    `json:"stage"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Target Artifact `json:"target"`
	Prior  Artifact `json:"prior"`
	Gates  Gates    `json:"gates"`

	SettleTimeoutSec int64 `json:"settle_timeout_sec"`
	VerifyTimeoutSec int64 `json:"verify_timeout_sec"`

	SchemaBefore int      `json:"schema_before"`
	SchemaAfter  int      `json:"schema_after"`
	LiveBefore   []string `json:"live_workers_before,omitempty"`
	// Quiesced are the workers this upgrade paused. Only these are resumed;
	// a worker the operator paused earlier stays paused.
	Quiesced []string `json:"quiesced_by_upgrade,omitempty"`
	// LeasesBefore are the leases that must be intact after the cutover.
	LeasesBefore []LeaseRef `json:"leases_before,omitempty"`

	// ServingSince is when the current owner was last started. Workers must
	// be seen after it to count as reconnected.
	ServingSince time.Time `json:"serving_since,omitempty"`

	Stages   []StageRecord        `json:"stages"`
	Rollback *RollbackEligibility `json:"rollback_eligibility,omitempty"`
	Measured Measurements         `json:"measured"`
	Workers  []WorkerStep         `json:"workers,omitempty"`

	Result     string `json:"result,omitempty"`
	ResultNote string `json:"result_note,omitempty"`
	Recovery   string `json:"recovery,omitempty"`
}

// Terminal reports whether the record has a final result.
func (r *Record) Terminal() bool { return r.Result != "" }

// stage returns the latest record for name, or nil.
func (r *Record) stage(name string) *StageRecord {
	for i := len(r.Stages) - 1; i >= 0; i-- {
		if r.Stages[i].Name == name {
			return &r.Stages[i]
		}
	}
	return nil
}

func (r *Record) stageDone(name string) bool {
	s := r.stage(name)
	return s != nil && s.Status == "done"
}

// Store persists records atomically below Dir with owner-only permissions.
type Store struct{ Dir string }

var idPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

// NewID returns a sortable unique identifier.
func NewID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

func (s Store) path(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("invalid upgrade id %q", id)
	}
	return filepath.Join(s.Dir, id+".json"), nil
}

var (
	urlPassword = regexp.MustCompile(`(://[^/?#@:\s]*:)([^/?#\s]*)@`)
	kvPassword  = regexp.MustCompile(`(?i)((?:password|token|secret|dsn)\s*[=:]\s*)([^\s",]+)`)
	bearer      = regexp.MustCompile(`(?i)(bearer\s+)[^\s"]+`)
	longHex     = regexp.MustCompile(`\b[0-9a-fA-F]{64,}\b`)
)

// Redact removes credential-shaped text. Records are written from messages
// that may contain command output, so every free-text field passes through it.
func Redact(s string) string {
	s = urlPassword.ReplaceAllString(s, "${1}****@")
	s = kvPassword.ReplaceAllString(s, "${1}****")
	s = bearer.ReplaceAllString(s, "${1}****")
	return longHex.ReplaceAllString(s, "****")
}

func (r *Record) scrub() {
	r.ResultNote, r.Recovery = Redact(r.ResultNote), Redact(r.Recovery)
	for i := range r.Stages {
		r.Stages[i].Detail = Redact(r.Stages[i].Detail)
	}
	for i := range r.Workers {
		r.Workers[i].Detail = Redact(r.Workers[i].Detail)
	}
	if r.Rollback != nil {
		r.Rollback.Reason = Redact(r.Rollback.Reason)
	}
}

// Save writes the record durably (temp file, fsync, rename).
func (s Store) Save(r *Record) error {
	path, err := s.path(r.ID)
	if err != nil {
		return err
	}
	r.scrub()
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Load reads one record.
func (s Store) Load(id string) (*Record, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("corrupt upgrade record %s: %w", id, err)
	}
	if r.ID != id {
		return nil, fmt.Errorf("upgrade record %s names a different id", id)
	}
	return &r, nil
}

// List returns record ids, newest last.
func (s Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if id := strings.TrimSuffix(e.Name(), ".json"); !e.IsDir() && id != e.Name() && idPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
