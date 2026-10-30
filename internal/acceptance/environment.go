package acceptance

import "strings"

// RuntimeProbe is private machine evidence. CLI paths must never be serialized
// into public reports. Adapters supply observations, not model-derived matches.
type RuntimeProbe struct {
	Candidate        string
	LoadedCandidate  string
	ActualCLIPath    string `json:"-"`
	PathCLIPath      string `json:"-"`
	HostCLIPath      string `json:"-"`
	DaemonVersion    string
	ConnectorVersion string
	LoggedIn         bool
	TargetCount      int
	TargetID         string // Fresh opaque reference, never an application document ID.
	Generation       string // Fresh connection nonce, changed on every reconnect.
}

// TargetBinding captures the window/document binding established by a probe.
// A new connection nonce invalidates it even if the same target reconnects.
type TargetBinding struct {
	TargetID   string `json:"target_id"`
	Generation string `json:"generation"`
}

type EnvironmentLayer struct {
	Binding  *TargetBinding `json:"binding,omitempty"` // Required for live operations.
	Status   string         `json:"status"`            // passed, failed, blocked, not-run
	Evidence string         `json:"evidence"`          // Sanitized observation/reference.
}

// EnvironmentLayers deliberately has no omitempty: failures and unrun checks
// survive storage and transport independently of the overall outcome.
type EnvironmentLayers struct {
	EnvironmentReady EnvironmentLayer `json:"environment-ready"`
	CandidateLoaded  EnvironmentLayer `json:"candidate-loaded"`
	Offline          EnvironmentLayer `json:"offline"`
	LiveWrite        EnvironmentLayer `json:"live-write"`
	SaveReload       EnvironmentLayer `json:"save-reload"`
}

// EnvironmentEvidence is the public, sanitized projection of private probes.
// It is evidence, not authorization to operate a host or acquire a resource.
type EnvironmentEvidence struct {
	Candidate        string            `json:"candidate"`
	LoadedCandidate  string            `json:"loaded_candidate"`
	CLIMatch         bool              `json:"cli_match"`
	DaemonVersion    string            `json:"daemon_version"`
	ConnectorVersion string            `json:"connector_version"`
	LoggedIn         bool              `json:"logged_in"`
	TargetCount      int               `json:"target_count"`
	Current          TargetBinding     `json:"current"`
	Binding          TargetBinding     `json:"binding"`
	Layers           EnvironmentLayers `json:"layers"`
}

func NewEnvironmentEvidence(p RuntimeProbe, binding TargetBinding, layers EnvironmentLayers) EnvironmentEvidence {
	return EnvironmentEvidence{
		Candidate: p.Candidate, LoadedCandidate: p.LoadedCandidate,
		CLIMatch:      nonempty(p.ActualCLIPath, p.PathCLIPath, p.HostCLIPath) && p.ActualCLIPath == p.PathCLIPath && p.ActualCLIPath == p.HostCLIPath,
		DaemonVersion: p.DaemonVersion, ConnectorVersion: p.ConnectorVersion,
		LoggedIn: p.LoggedIn, TargetCount: p.TargetCount,
		Current: TargetBinding{p.TargetID, p.Generation}, Binding: binding, Layers: layers,
	}
}

// Missing recomputes the gate on every use, including after deserialization.
// Callers must freshly probe immediately before a live operation; an old report
// cannot establish that the host has remained connected since collection.
func (e EnvironmentEvidence) Missing(head string) []string {
	var missing []string
	if !e.CLIMatch {
		missing = append(missing, "cli-path-match")
	}
	if !e.LoggedIn {
		missing = append(missing, "login")
	}
	if !nonempty(e.DaemonVersion) {
		missing = append(missing, "daemon-version")
	}
	if !nonempty(e.ConnectorVersion) {
		missing = append(missing, "connector-version")
	}
	if e.TargetCount != 1 || !nonempty(e.Current.TargetID) {
		missing = append(missing, "unique-target")
	}
	if !nonempty(e.Current.Generation, e.Binding.Generation, e.Binding.TargetID) || e.Current != e.Binding {
		missing = append(missing, "fresh-target-binding")
	}
	if !nonempty(head, e.Candidate, e.LoadedCandidate) || e.Candidate != head || e.LoadedCandidate != e.Candidate {
		missing = append(missing, "candidate-provenance")
	}
	for _, layer := range []struct {
		name  string
		value EnvironmentLayer
	}{
		{"environment-ready", e.Layers.EnvironmentReady}, {"candidate-loaded", e.Layers.CandidateLoaded},
		{"offline", e.Layers.Offline}, {"live-write", e.Layers.LiveWrite}, {"save-reload", e.Layers.SaveReload},
	} {
		if layer.value.Status != "passed" || !nonempty(layer.value.Evidence) {
			missing = append(missing, layer.name)
		}
	}

	for _, layer := range []struct {
		name  string
		value EnvironmentLayer
	}{
		{"live-write", e.Layers.LiveWrite}, {"save-reload", e.Layers.SaveReload},
	} {
		if layer.value.Binding == nil || *layer.value.Binding != e.Current {
			missing = append(missing, layer.name+"-binding")
		}
	}
	return missing
}

func (e EnvironmentEvidence) Result(head string) string {
	if len(e.Missing(head)) == 0 {
		return "live-verified"
	}
	return "blocked"
}

func (e EnvironmentEvidence) missingText(head string) string {
	return strings.Join(e.Missing(head), ", ")
}
