package component

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// ReadAdapter permits substitution of read-only probes without importing an
// executor implementation or giving the registry a dispatch method.
type ReadAdapter struct {
	Descriptor Descriptor
	Observe    func(context.Context) (Observation, error)
}

func (a ReadAdapter) Discover() Descriptor { return a.Descriptor }
func (a ReadAdapter) Probe(ctx context.Context) (Observation, error) {
	if a.Observe == nil {
		return Observation{}, ErrRefused
	}
	return a.Observe(ctx)
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", ErrRefused
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", ErrRefused
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Builtins inventories only components present in this source. References are
// symbolic configuration/code references; no paths or credential values escape.
// GitHub Actions remains unavailable until a project-scoped evidence adapter is
// supplied. Having the provider module installed is not proof of remote health.
func Builtins(loadExecution func() (execution.Config, error)) []Adapter {
	adapters := []Adapter{}
	for _, module := range []string{"project", "vcs", "operator"} {
		d := Descriptor{ID: "clawflow-" + module, Adapter: "builtin", Version: "1", Reference: "internal/" + module, Capabilities: []string{module + "-module"}}
		adapters = append(adapters, ReadAdapter{Descriptor: d, Observe: func(ctx context.Context) (Observation, error) {
			path, err := os.Executable()
			if err != nil {
				return Observation{}, ErrRefused
			}
			digest, err := fileDigest(path)
			if err != nil {
				return Observation{}, err
			}
			return Observation{Identity: d.ID, Version: digest, Protocol: Schema, Health: "available", Capabilities: d.Capabilities}, ctx.Err()
		}})
	}
	d := Descriptor{ID: "offload", Adapter: "offload-help", Version: "1", Reference: "settings.execution", Capabilities: []string{"execution-declarations"}}
	adapters = append(adapters, ReadAdapter{Descriptor: d, Observe: func(ctx context.Context) (Observation, error) {
		c, err := loadExecution()
		if err != nil {
			return Observation{}, ErrRefused
		}
		report := execution.ProbeCapabilities(ctx, c)
		if report.Status != execution.StatusReady {
			return Observation{}, ErrRefused
		}
		digest, err := fileDigest(report.Command)
		if err != nil {
			return Observation{}, err
		}
		// Pin the effective configuration without persisting profile names or paths.
		data, err := json.Marshal(c)
		if err != nil {
			return Observation{}, ErrRefused
		}
		h := sha256.Sum256(append([]byte(digest), data...))
		return Observation{Identity: "offload", Version: hex.EncodeToString(h[:]), Protocol: Schema, Health: "available", Capabilities: d.Capabilities}, nil
	}})
	adapters = append(adapters, ReadAdapter{Descriptor: Descriptor{ID: "github-actions", Adapter: "github-evidence", Version: "1", Reference: "internal/vcs/github", Capabilities: []string{"exact-revision-ci"}}})
	return adapters
}

// GitHubEvidenceSource is the read-only subset of the existing GitHub adapter.
type GitHubEvidenceSource interface {
	vcs.CIEvidenceProvider
	GetRepoID(string) (int64, error)
}

// GitHubActions observes the existing exact-revision evidence interface. The
// caller supplies a provider already scoped to the authorized project and a
// known repository; this adapter verifies its provider identity and never
// resolves or copies credentials. The provider must honor the caller context.
// A successful test result is not required for service health, but exact-head
// check identities are. No check result is promoted to acceptance here.
func GitHubActions(provider GitHubEvidenceSource, repo string, pr int, head string) Adapter {
	d := Descriptor{ID: "github-actions", Adapter: "github-evidence", Version: "1", Reference: "internal/vcs/github", Capabilities: []string{"exact-revision-ci"}}
	return ReadAdapter{Descriptor: d, Observe: func(ctx context.Context) (Observation, error) {
		if provider == nil || !validName(repo) || pr <= 0 || len(head) != 40 {
			return Observation{}, ErrRefused
		}
		if _, err := hex.DecodeString(head); err != nil {
			return Observation{}, ErrRefused
		}
		if ctx.Err() != nil {
			return Observation{}, ErrRefused
		}
		repositoryID, err := provider.GetRepoID(repo)
		if err != nil || repositoryID <= 0 {
			return Observation{}, ErrRefused
		}
		evidence, err := provider.GetCIEvidence(repo, pr)
		if err != nil || ctx.Err() != nil || evidence.HeadSHA != head || len(evidence.CheckIDs) == 0 {
			return Observation{}, ErrRefused
		}
		if evidence.Status != vcs.CIStatusSuccess && evidence.Status != vcs.CIStatusFailure && evidence.Status != vcs.CIStatusPending {
			return Observation{}, ErrRefused
		}
		seen := map[int64]bool{}
		for _, id := range evidence.CheckIDs {
			if id <= 0 || seen[id] {
				return Observation{}, ErrRefused
			}
			seen[id] = true
		}
		path, err := os.Executable()
		if err != nil {
			return Observation{}, ErrRefused
		}
		digest, err := fileDigest(path)
		if err != nil {
			return Observation{}, err
		}
		return Observation{Identity: fmt.Sprintf("github/%d/%s", repositoryID, repo), Version: digest, Protocol: Schema, Health: "available", Capabilities: d.Capabilities}, nil
	}}
}
