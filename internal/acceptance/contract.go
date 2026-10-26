// Package acceptance provides a bounded local acceptance gate. Its reference
// files are not a transactional ledger or a distributed fencing mechanism.
package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

const Version = 1

type Identity struct {
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id"`
}

// Contract is a value snapshot; callers must create a new generation when its
// requirements or pinned inputs change. Digest uses the fixed struct field
// order of encoding/json, with no maps or optional normalization.
type Contract struct {
	Version        int      `json:"version"`
	TaskID         string   `json:"task_id"`
	Generation     uint64   `json:"generation"`
	Repository     string   `json:"repository"`
	PRNumber       int      `json:"pr_number"`
	HeadSHA        string   `json:"head_sha"`
	BaseSHA        string   `json:"base_sha"`
	IntegrationSHA string   `json:"integration_sha,omitempty"`
	PolicyDigest   string   `json:"policy_digest"`
	Requirements   string   `json:"requirements"`
	Development    Identity `json:"development"`
}

func nonempty(values ...string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return false
		}
	}
	return true
}

func (c Contract) Validate() error {
	if c.Version != Version || c.Generation == 0 || c.PRNumber <= 0 ||
		!nonempty(c.TaskID, c.Repository, c.HeadSHA, c.BaseSHA, c.PolicyDigest, c.Requirements, c.Development.SessionID, c.Development.RunID) {
		return errors.New("invalid acceptance contract")
	}
	return nil
}

func digest(v any) string {
	b, _ := json.Marshal(v) // All callers use JSON-safe fixed structs or strings.
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (c Contract) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	return digest(c), nil
}

type Verdict string

const (
	Passed  Verdict = "passed"
	Failed  Verdict = "failed"
	Blocked Verdict = "blocked"
)

// Native is populated from execution.Result, never model-authored JSON.
type Native struct {
	Identity
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	Policy    string `json:"policy"`
	Profile   string `json:"profile"`
	Backend   string `json:"backend"`
	Model     string `json:"model"`
	ErrorKind string `json:"error_kind,omitempty"`
	Error     string `json:"error,omitempty"`
}

func native(r execution.Result) Native {
	return Native{Identity: Identity{r.NativeSessionID, r.RunID}, Status: r.Status, ExitCode: r.ExitCode, Policy: r.ExecutionPolicy, Profile: r.Profile, Backend: r.Backend, Model: r.Model, ErrorKind: r.ErrorKind, Error: r.Error}
}

func (n Native) validate(dev Identity) error {
	if !nonempty(n.SessionID, n.RunID, n.Profile, n.Backend) || n.Status != "ok" || n.ExitCode != 0 || n.Policy != "default" || n.ErrorKind != "" || n.Error != "" {
		return errors.New("acceptance requires successful default-policy native execution")
	}
	if n.SessionID == dev.SessionID || n.RunID == dev.RunID {
		return errors.New("development actor cannot accept its own work")
	}
	return nil
}

type Plan struct {
	Version        int       `json:"version"`
	AttemptID      string    `json:"attempt_id"`
	ContractDigest string    `json:"contract_digest"`
	Text           string    `json:"text"`
	Digest         string    `json:"digest"`
	Planner        Native    `json:"planner"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    time.Time `json:"completed_at"`
}

func (p Plan) validate(c Contract) error {
	d, err := c.Digest()
	if err != nil {
		return err
	}
	if p.Version != Version || p.ContractDigest != d || !nonempty(p.AttemptID, p.Text) || p.Digest != digest(p.Text) ||
		p.StartedAt.IsZero() || p.CompletedAt.Before(p.StartedAt) {
		return errors.New("invalid or stale acceptance plan")
	}
	return p.Planner.validate(c.Development)
}

type Source struct {
	HeadSHA        string `json:"head_sha"`
	BaseSHA        string `json:"base_sha"`
	IntegrationSHA string `json:"integration_sha,omitempty"`
	Clean          bool   `json:"clean"`
}

func (s Source) matches(c Contract) bool {
	return s.Clean && s.HeadSHA == c.HeadSHA && s.BaseSHA == c.BaseSHA && s.IntegrationSHA == c.IntegrationSHA
}

type Record struct {
	Version         int       `json:"version"`
	Contract        Contract  `json:"contract"`
	ContractDigest  string    `json:"contract_digest"`
	Plan            Plan      `json:"plan"`
	AttemptID       string    `json:"attempt_id"`
	Role            string    `json:"role"`
	Reviewer        Native    `json:"reviewer"`
	Verdict         Verdict   `json:"verdict"`
	Report          string    `json:"report"`
	Evidence        []string  `json:"evidence"`
	Before          Source    `json:"before"`
	After           Source    `json:"after"`
	Completed       bool      `json:"completed"`
	Cancelled       bool      `json:"cancelled"`
	StartedAt       time.Time `json:"started_at"`
	ReviewStartedAt time.Time `json:"review_started_at"`
	CompletedAt     time.Time `json:"completed_at"`
}

// Evaluate returns nil only for a complete passing record for exactly expected.
// Callers must obtain expected from their current authoritative inputs.
func Evaluate(expected Contract, r Record) error {
	d, err := expected.Digest()
	if err != nil {
		return err
	}
	actual, err := r.Contract.Digest()
	if err != nil || r.Version != Version || actual != d || r.ContractDigest != d {
		return errors.New("acceptance contract identity or digest mismatch")
	}
	if !r.Completed || r.Cancelled || r.Verdict != Passed || r.Role != "acceptance" || !nonempty(r.AttemptID, r.Report) || len(r.Evidence) == 0 {
		return errors.New("acceptance attempt is incomplete or nonpassing")
	}
	for _, e := range r.Evidence {
		if !nonempty(e) {
			return errors.New("missing acceptance evidence")
		}
	}
	if err := r.Plan.validate(expected); err != nil {
		return err
	}
	if r.Plan.AttemptID != r.AttemptID {
		return errors.New("acceptance plan belongs to another attempt")
	}
	if err := r.Reviewer.validate(expected.Development); err != nil {
		return err
	}
	if r.Reviewer.SessionID == r.Plan.Planner.SessionID || r.Reviewer.RunID == r.Plan.Planner.RunID {
		return errors.New("acceptance review must use a fresh native session and run")
	}
	if r.StartedAt.IsZero() || r.Plan.StartedAt.Before(r.StartedAt) || r.ReviewStartedAt.Before(r.Plan.CompletedAt) ||
		r.CompletedAt.Before(r.ReviewStartedAt) || r.CompletedAt.IsZero() {
		return errors.New("incoherent acceptance timestamps")
	}
	if !r.Before.matches(expected) || !r.After.matches(expected) {
		return fmt.Errorf("acceptance source changed or is not clean")
	}
	return nil
}
