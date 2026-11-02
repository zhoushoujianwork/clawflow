package candidate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// Readiness is the result of the pre-start verification. A candidate is
// started only when Status is "passed"; otherwise the concrete missing items
// and the owner who must act are reported. Nothing is inferred from a build.
type Readiness struct {
	Status    string   `json:"status"` // passed | blocked
	Missing   []string `json:"missing,omitempty"`
	NextOwner string   `json:"next_owner,omitempty"`
	Action    string   `json:"next_action,omitempty"`
}

// SOPDigest is the SHA-256 of the exact approved testing.md bytes, the same
// digest the coordinator and workers pin.
func SOPDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// CheckReadiness compares the pinned contract with what this machine has: the
// local SOP text (empty if absent) and the capabilities it declares.
func CheckReadiness(pin Pin, sop string, machine []string) Readiness {
	var missing []string
	owner := ""
	set := func(o string) {
		if owner == "" {
			owner = o
		}
	}
	have := strings.TrimSpace(sop) != ""
	if (pin.SOPRequired || pin.SOPDigest != "") && !have {
		missing = append(missing, "machine-local testing SOP is missing")
		set("project-maintainer")
	}
	if pin.SOPRequired && pin.SOPDigest == "" {
		missing = append(missing, "a required SOP has no approved digest")
		set("project-maintainer")
	}
	if have && pin.SOPDigest != "" && SOPDigest(sop) != pin.SOPDigest {
		missing = append(missing, "testing SOP digest differs from the pinned revision")
		set("project-maintainer")
	}
	for _, c := range pin.Capabilities {
		if !slices.Contains(machine, c) {
			missing = append(missing, "capability not declared on this machine: "+c)
			set("environment-maintainer")
		}
	}
	if len(missing) == 0 {
		return Readiness{Status: "passed"}
	}
	return Readiness{Status: "blocked", Missing: missing, NextOwner: owner,
		Action: "resolve the missing items, then run the check again; offline results do not replace them"}
}

// Err converts a blocked result into an error.
func (r Readiness) Err() error {
	if r.Status == "passed" {
		return nil
	}
	return fmt.Errorf("blocked (%s): %s", r.NextOwner, strings.Join(r.Missing, "; "))
}
