// Package actions observes externally executed tests without allocating an agent.
// Policies are immutable inputs from the control plane, not approval decisions.
package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var sha = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var repo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Policy must be supplied by an independently authorized caller. Observing a
// successful candidate never activates a policy or changes the merge gate.
type Policy struct {
	Version        int           `json:"version"`
	RepositoryID   int64         `json:"repository_id"`
	WorkflowID     int64         `json:"workflow_id"`
	WorkflowPath   string        `json:"workflow_path"`
	WorkflowDigest string        `json:"workflow_digest"` // SHA-256 of approved file bytes
	Jobs           []RequiredJob `json:"jobs"`
	Artifacts      []string      `json:"artifacts"`
}

type RequiredJob struct {
	Name  string   `json:"name"`
	Steps []string `json:"steps"`
}

func (p Policy) Validate() error {
	if p.Version != 1 || p.RepositoryID <= 0 || p.WorkflowID <= 0 || p.WorkflowPath != ".github/workflows/ci.yml" || !digest.MatchString(p.WorkflowDigest) || len(p.Jobs) == 0 {
		return errors.New("invalid Actions policy: pinned repository/workflow/version and required jobs are mandatory")
	}
	seen := map[string]bool{}
	for _, j := range p.Jobs {
		if j.Name == "" || seen[j.Name] || len(j.Steps) == 0 {
			return errors.New("missing or duplicate required job")
		}
		seen[j.Name] = true
		steps := map[string]bool{}
		for _, s := range j.Steps {
			if s == "" || steps[s] {
				return errors.New("missing or duplicate required step")
			}
			steps[s] = true
		}
	}
	seen = map[string]bool{}
	for _, a := range p.Artifacts {
		if a == "" || seen[a] {
			return errors.New("invalid artifact selector")
		}
		seen[a] = true
	}
	return nil
}

type Request struct {
	Contract acceptance.Contract `json:"contract"`
	Policy   Policy              `json:"policy"`
}

func (r Request) Key() (string, error) {
	if _, err := r.Contract.Digest(); err != nil {
		return "", err
	}
	if err := r.Policy.Validate(); err != nil {
		return "", err
	}
	if !repo.MatchString(r.Contract.Repository) || !sha.MatchString(r.Contract.HeadSHA) || !sha.MatchString(r.Contract.BaseSHA) || !sha.MatchString(r.Contract.IntegrationSHA) {
		return "", errors.New("Actions requires full head/base/test-merge SHAs")
	}
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

type Step struct{ Name, Status, Conclusion string }
type Job struct {
	ID                       int64     `json:"id"`
	RunID                    int64     `json:"run_id"`
	Attempt                  int       `json:"run_attempt"`
	CheckID                  int64     `json:"check_id"`
	HeadSHA                  string    `json:"head_sha"`
	CheckSuiteID             int64     `json:"check_suite_id"`
	CheckoutLogURL           string    `json:"checkout_log_url,omitempty"`
	CheckoutStartedAt        time.Time `json:"checkout_started_at,omitempty"`
	CheckoutEndedAt          time.Time `json:"checkout_ended_at,omitempty"`
	Name, Status, Conclusion string
	Steps                    []Step
}
type Artifact struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	URL         string    `json:"archive_download_url"`
	Digest      string    `json:"digest"`
	Expired     bool      `json:"expired"`
	CreatedAt   time.Time `json:"created_at"`
	WorkflowRun struct {
		ID      int64  `json:"id"`
		HeadSHA string `json:"head_sha"`
	} `json:"workflow_run"`
}

type Observation struct {
	Key          string     `json:"key"`
	Request      Request    `json:"request"`
	State        string     `json:"state"` // waiting_external, passed, failed, unknown, infra_error
	Reason       string     `json:"reason,omitempty"`
	RunID        int64      `json:"run_id"`
	Attempt      int        `json:"run_attempt"`
	RunURL       string     `json:"run_url"`
	RunHeadSHA   string     `json:"run_head_sha"`
	TestedSHA    string     `json:"tested_sha"`
	WorkflowBlob string     `json:"workflow_blob"`
	Status       string     `json:"status"`
	Conclusion   string     `json:"conclusion"`
	Jobs         []Job      `json:"jobs"`
	Artifacts    []Artifact `json:"artifacts"`
	ObservedAt   time.Time  `json:"observed_at"`
}

type Provider interface {
	ObserveActions(context.Context, Request, Observation) (Observation, error)
}

// Ledger stores both the restart attachment and every run/attempt observation.
// It is deliberately not a second task scheduler or a merge authority.
type Ledger struct{ DB *gorm.DB }
type record struct {
	Key     string          `gorm:"primaryKey"`
	RunID   int64           `gorm:"primaryKey"`
	Attempt int             `gorm:"primaryKey"`
	Payload json.RawMessage `gorm:"serializer:json"`
}

func (record) TableName() string { return "actions_evidence" }

type attachment struct {
	Key     string          `gorm:"primaryKey"`
	Payload json.RawMessage `gorm:"serializer:json"`
}

func (attachment) TableName() string { return "actions_attachments" }

func (s Ledger) Load(ctx context.Context, key string) (Observation, error) {
	var row attachment
	err := s.DB.WithContext(ctx).First(&row, "key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Observation{}, nil
	}
	if err != nil {
		return Observation{}, err
	}
	var o Observation
	err = json.Unmarshal(row.Payload, &o)
	return o, err
}

// Pending returns the oldest waiting attachment for bounded coordinator polling.
// Terminal evidence requires a fresh explicit observation; it never starts work.
func (s Ledger) Pending(ctx context.Context) ([]Request, error) {
	var rows []attachment
	if err := s.DB.WithContext(ctx).Where("json_extract(payload, '$.state') IN ('waiting_external','infra_error')").Order("json_extract(payload, '$.observed_at'), key").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	var requests []Request
	for _, row := range rows {
		var o Observation
		if err := json.Unmarshal(row.Payload, &o); err != nil {
			return nil, err
		}
		key, err := o.Request.Key()
		if err != nil || key != row.Key {
			return nil, errors.New("invalid persisted Actions request")
		}
		requests = append(requests, o.Request)
	}
	return requests, nil
}

// Reconcile does one bounded read-only provider observation, commits it, and
// returns. A caller must release its slot on waiting_external; no sleep, agent
// session, workflow dispatch or rerun is launched here. Errors invalidate a
// previously passing attachment instead of allowing cached success to pass.
func (s Ledger) Reconcile(ctx context.Context, p Provider, r Request) (Observation, error) {
	key, err := r.Key()
	if err != nil {
		return Observation{}, err
	}
	// The provider observation is a network call and must not run inside a
	// SQLite write transaction (it would hold the single writer lock). Observe
	// first, then persist atomically. Two pollers for one key may both observe;
	// the observation is read-only, and the later committed row wins.
	var out Observation
	previous, err := s.Load(ctx, key)
	if err != nil {
		return Observation{}, err
	}
	out, err = p.ObserveActions(ctx, r, previous)
	if err != nil {
		if out.RunID == 0 {
			out = previous
		}
		out.State, out.Reason = "infra_error", "Actions evidence unavailable; retry observation without dispatch"
	}
	out.Key, out.Request, out.ObservedAt = key, r, time.Now().UTC()
	payload, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"payload"})}).Create(&attachment{key, payload}).Error; e != nil {
			return e
		}
		if out.RunID > 0 {
			return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}, {Name: "run_id"}, {Name: "attempt"}}, DoUpdates: clause.AssignmentColumns([]string{"payload"})}).Create(&record{key, out.RunID, out.Attempt, payload}).Error
		}
		return nil
	})
	return out, err
}
