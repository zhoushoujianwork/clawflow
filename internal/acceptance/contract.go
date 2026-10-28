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
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
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

// Execution policies required of each acceptance role. The planner is
// read-only in both isolation modes. The reviewer runs commands in a disposable
// checkout, so it needs the same command-capable policy implement uses.
const (
	PlannerPolicy    = "default"
	ToolsDisabled    = "tools-disabled"
	IsolatedReadonly = "isolated-readonly"
	ReviewerPolicy   = "managed-development"
)

// validate requires successful native execution under exactly the given policy.
func (n Native) validate(dev Identity, policy string) error {
	if !nonempty(n.SessionID, n.RunID, n.Profile, n.Backend) || n.Status != "ok" || n.ExitCode != 0 || n.Policy != policy || n.ErrorKind != "" || n.Error != "" {
		return fmt.Errorf("acceptance requires successful %s-policy native execution", policy)
	}
	if n.SessionID == dev.SessionID || n.RunID == dev.RunID {
		return errors.New("development actor cannot accept its own work")
	}
	return nil
}

type Plan struct {
	PlannerIsolation string    `json:"planner_isolation"`
	Version          int       `json:"version"`
	AttemptID        string    `json:"attempt_id"`
	ContractDigest   string    `json:"contract_digest"`
	Text             string    `json:"text"`
	Digest           string    `json:"digest"`
	Planner          Native    `json:"planner"`
	StartedAt        time.Time `json:"started_at"`
	CompletedAt      time.Time `json:"completed_at"`
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
	if p.PlannerIsolation != ToolsDisabled && p.PlannerIsolation != IsolatedReadonly {
		return errors.New("invalid planner isolation mode")
	}
	return p.Planner.validate(c.Development, PlannerPolicy)
}

// Source is the immutability proof of a review checkout. Clean means the local
// HEAD equals the contract head and no commit beyond it exists; untracked or
// modified build and test artifacts are allowed in the disposable checkout.
type Source struct {
	HeadSHA        string `json:"head_sha"`
	BaseSHA        string `json:"base_sha"`
	IntegrationSHA string `json:"integration_sha,omitempty"`
	Clean          bool   `json:"clean"`
}

func (s Source) matches(c Contract) bool {
	return s.Clean && s.HeadSHA == c.HeadSHA && s.BaseSHA == c.BaseSHA && s.IntegrationSHA == c.IntegrationSHA
}

// Finding separates actionable requirement/defect evidence from review advice.
// Requirement names a contract requirement; Defect names a correctness/safety
// violation in changed code. Advisory items cannot make acceptance fail.
type Finding struct {
	Kind        string `json:"kind"`
	Summary     string `json:"summary"`
	Requirement string `json:"requirement,omitempty"`
	Defect      string `json:"defect,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
}

func (f Finding) Failing() bool {
	return f.Kind == "requirement" || f.Kind == "defect"
}

func validateFindings(findings []Finding) (bool, error) {
	failing := false
	for _, f := range findings {
		if !nonempty(f.Summary) {
			return false, errors.New("finding requires summary")
		}
		switch f.Kind {
		case "advisory":
		case "requirement":
			if !nonempty(f.Requirement, f.Evidence) {
				return false, errors.New("requirement finding requires requirement and concrete evidence")
			}
			failing = true
		case "defect":
			if !nonempty(f.Defect, f.Evidence) {
				return false, errors.New("defect finding requires defect and concrete evidence")
			}
			failing = true
		default:
			return false, errors.New("unknown finding kind")
		}
	}
	return failing, nil
}

const (
	FullSuite  = "full-suite"
	CIVerified = "ci-verified"
)

type Record struct {
	ReviewMode string          `json:"review_mode,omitempty"`
	CI         *vcs.CIEvidence `json:"ci,omitempty"`

	Version         int       `json:"version"`
	Contract        Contract  `json:"contract"`
	ContractDigest  string    `json:"contract_digest"`
	Plan            Plan      `json:"plan"`
	AttemptID       string    `json:"attempt_id"`
	Role            string    `json:"role"`
	Reviewer        Native    `json:"reviewer"`
	Verdict         Verdict   `json:"verdict"`
	Report          string    `json:"report"`
	Findings        []Finding `json:"findings,omitempty"`
	Evidence        []string  `json:"evidence"`
	Before          Source    `json:"before"`
	After           Source    `json:"after"`
	Completed       bool      `json:"completed"`
	Cancelled       bool      `json:"cancelled"`
	StartedAt       time.Time `json:"started_at"`
	ReviewStartedAt time.Time `json:"review_started_at"`
	CompletedAt     time.Time `json:"completed_at"`
}

// EvidenceCommandPrefix marks an evidence entry that records an executed command
// and its result, for example "command: go test ./... => ok".
const EvidenceCommandPrefix = "command:"

// hasExecutedCommand reports whether any entry records a command with content.
func hasExecutedCommand(evidence []string) bool {
	for _, e := range evidence {
		t := strings.TrimSpace(e)
		if len(t) > len(EvidenceCommandPrefix) && strings.EqualFold(t[:len(EvidenceCommandPrefix)], EvidenceCommandPrefix) && nonempty(t[len(EvidenceCommandPrefix):]) {
			return true
		}
	}
	return false
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
	switch r.ReviewMode {
	case "", FullSuite: // Empty is the legacy full-suite record format.
		if r.CI != nil {
			return errors.New("full-suite record contains CI substitution evidence")
		}
	case CIVerified:
		if r.CI == nil || !r.CI.SuccessfulFor(expected.HeadSHA) {
			return errors.New("CI verification does not match the acceptance head")
		}
	default:
		return errors.New("unknown acceptance review mode")
	}
	if !r.Completed || r.Cancelled || r.Verdict != Passed || r.Role != "acceptance" || !nonempty(r.AttemptID, r.Report) || len(r.Evidence) == 0 {
		return errors.New("acceptance attempt is incomplete or nonpassing")
	}
	if failing, err := validateFindings(r.Findings); err != nil || failing {
		return errors.New("passing record contains invalid or failing findings")
	}
	for _, e := range r.Evidence {
		if !nonempty(e) {
			return errors.New("missing acceptance evidence")
		}
	}
	if !hasExecutedCommand(r.Evidence) {
		return errors.New("acceptance evidence lists no executed command")
	}
	if err := r.Plan.validate(expected); err != nil {
		return err
	}
	if r.Plan.AttemptID != r.AttemptID {
		return errors.New("acceptance plan belongs to another attempt")
	}
	if err := r.Reviewer.validate(expected.Development, ReviewerPolicy); err != nil {
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
