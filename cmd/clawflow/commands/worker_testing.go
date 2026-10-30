package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// Only public declarations and a content digest cross the coordinator boundary.
// Private SOP bytes are read and pinned on the executing machine.
type testingRequirements struct {
	RevisionPinned bool     `json:"revision_pinned,omitempty"`
	SOPRequired    bool     `json:"sop_required,omitempty"`
	SOPDigest      string   `json:"sop_digest,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
}

func testingDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func localTesting(repo string) string {
	p, err := project.FindProjectByRepo(repo)
	if err != nil || p == nil {
		return ""
	}
	text, err := project.ReadTesting(p.Name)
	if err != nil {
		return ""
	}
	return text
}

func declaredTesting(repo string, rc config.Repo) testingRequirements {
	r := testingRequirements{SOPRequired: rc.TestingSOPRequired, SOPDigest: rc.TestingSOPDigest, Capabilities: rc.RequiredCapabilities}
	if text := localTesting(repo); strings.TrimSpace(text) != "" {
		r.SOPRequired = true
		if r.SOPDigest == "" {
			r.SOPDigest = testingDigest(text)
		}
	}
	return r
}

func testingBlocked(reason, action, owner string) *jobResult {
	return &jobResult{Status: "blocked", Error: reason, NextAction: action, NextOwner: owner}
}

func testingBlockedBody(r *jobResult) string {
	return fmt.Sprintf("Testing readiness: blocked\n\n%s\n\nnext_action: %s\nnext_owner: %s", r.Error, r.NextAction, r.NextOwner)
}

func workerTestingContext(ctx context.Context, t *coordinator.Task, tags []string) (context.Context, *jobResult, string) {
	var p coordinatorPayload
	if len(t.Payload) > 0 && json.Unmarshal(t.Payload, &p) != nil {
		return ctx, testingBlocked("Testing requirements could not be decoded", "Re-enqueue a task with valid testing declarations", "coordinator"), ""
	}
	r := p.Testing
	text := localTesting(t.Repo)
	digest := ""
	if strings.TrimSpace(text) != "" {
		digest = testingDigest(text)
	}
	if (r.SOPRequired || r.SOPDigest != "") && digest == "" {
		return ctx, testingBlocked("Required machine-local testing SOP is missing", "Install the approved testing.md and primary project mapping on this worker, then re-enqueue", "project-maintainer"), digest
	}
	if (r.RevisionPinned || r.SOPDigest != "") && r.SOPDigest != digest {
		return ctx, testingBlocked("Testing SOP revision changed or differs from the development revision", "Restore the approved SOP matching digest "+r.SOPDigest+" or establish a new development and review baseline", "project-maintainer"), digest
	}
	// A required SOP with no authoritative digest cannot establish a common revision.
	if r.SOPRequired && r.SOPDigest == "" {
		return ctx, testingBlocked("Required testing SOP revision has not been pinned", "Publish the approved testing_sop_digest and re-enqueue development before review", "project-maintainer"), digest
	}
	for _, capability := range r.Capabilities {
		if !slices.Contains(tags, "capability:"+capability) {
			return ctx, testingBlocked("Required testing capability is absent: "+capability, "Configure and advertise the required capability, or reassign to a capable worker, then re-enqueue", "environment-maintainer"), digest
		}
	}
	return project.WithTestingSnapshot(ctx, text), nil, digest
}

// Development result records the SOP actually read, including an absent SOP.
func inheritDevelopmentTesting(p *coordinatorPayload, dev *coordinator.Task) error {
	var development coordinatorPayload
	if err := json.Unmarshal(dev.Payload, &development); err != nil {
		return err
	}
	p.Testing = development.Testing
	var result jobResult
	if len(dev.Result) > 0 {
		if err := json.Unmarshal(dev.Result, &result); err != nil {
			return err
		}
		p.Testing.SOPDigest = result.SOPDigest
		p.Testing.RevisionPinned = true
	}
	return nil
}
