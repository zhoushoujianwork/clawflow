package api

import (
	"net/http"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

const retiredProviderMessage = "ClawFlow provider configuration is retired; use offload config to configure credentials/models, then set settings.execution.profile or CLAWFLOW_OFFLOAD_PROFILE"

// HandleListProviders reports local readiness without reading legacy credentials.
func HandleListProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	c, err := execution.LoadConfig()
	readiness := "ready"
	message := "Offload profile selected; live execution unverified"
	if err == nil {
		err = execution.ValidateConfig(c)
	}
	if err != nil {
		readiness = "blocked"
		message = err.Error()
	}
	writeJSON(w, 200, map[string]any{"engine": "offload", "profile": c.Profile, "role_profiles": c.RoleProfiles, "pilot_profile": execution.ProfileForRole(c, config.RolePilot), "codex_sandbox": c.CodexSandbox, "readiness": readiness, "message": message})
}

// HandleAddProvider rejects the retired ClawFlow provider operation.
func HandleAddProvider(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusGone, map[string]string{"error": retiredProviderMessage})
}

// HandleUpdateProvider rejects the retired ClawFlow provider operation.
func HandleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusGone, map[string]string{"error": retiredProviderMessage})
}

// HandleDeleteProvider rejects the retired ClawFlow provider operation.
func HandleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusGone, map[string]string{"error": retiredProviderMessage})
}

// HandleReorderProviders rejects the retired ClawFlow provider operation.
func HandleReorderProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusGone, map[string]string{"error": retiredProviderMessage})
}

// HandleRevealProviderKey rejects the retired ClawFlow provider operation.
func HandleRevealProviderKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusGone, map[string]string{"error": retiredProviderMessage})
}

// HandleTestProvider rejects the retired ClawFlow provider operation.
func HandleTestProvider(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusGone, map[string]string{"error": retiredProviderMessage})
}
