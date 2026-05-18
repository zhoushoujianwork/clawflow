package auth

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// handleDeviceStart kicks off a CLI device flow.
//
//	POST /api/v1/auth/device
//	→ {"user_code":"ABCD-1234","verification_uri":"https://github.com/login/device",
//	   "expires_in":900,"interval":5}
//
// The cloud server holds the GitHub device_code in memory keyed by user_code
// so the CLI never sees the device_code itself. On poll, the CLI sends the
// user_code back and the cloud forwards the device_code to GitHub.
func (h *Handler) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ClientID == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device flow not configured"})
		return
	}
	d, err := h.startDeviceFlow(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	expiresAt := time.Now().Add(time.Duration(d.ExpiresIn) * time.Second)
	h.device.put(&deviceEntry{
		DeviceCode: d.DeviceCode,
		UserCode:   d.UserCode,
		Interval:   d.Interval,
		ExpiresAt:  expiresAt,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"user_code":        d.UserCode,
		"verification_uri": d.VerificationURI,
		"expires_in":       d.ExpiresIn,
		"interval":         d.Interval,
	})
}

// handleDevicePoll polls GitHub for an access token.
//
//	POST /api/v1/auth/device/poll  {"user_code":"ABCD-1234"}
//	→ 202 {"status":"pending"} | {"status":"slow_down"}
//	→ 200 {"status":"ok","token":"pat_...","user":{"login":"..."}}
//	→ 410 {"status":"expired"}
//
// On 200, the cloud has already upserted the user, refreshed installations,
// and minted a personal API token. The plaintext token is returned exactly
// once — the CLI persists it locally and the cloud only ever sees the hash
// after this response.
func (h *Handler) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserCode string `json:"user_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_code is required"})
		return
	}
	entry := h.device.get(req.UserCode)
	if entry == nil {
		writeJSON(w, http.StatusGone, map[string]string{"status": "expired"})
		return
	}
	tok, status, err := h.pollDeviceFlow(r.Context(), entry.DeviceCode)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if status == "authorization_pending" {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	if status == "slow_down" {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "slow_down"})
		return
	}
	// Success — finalize.
	h.device.delete(req.UserCode)
	user, err := h.loginGitHubUser(r.Context(), tok.AccessToken)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	plain := "pat_" + randomToken(24)
	if _, err := h.store.CreateAPIToken(cloud.CreateAPITokenRequest{
		UserID:    user.ID,
		Kind:      cloud.APITokenKindPersonal,
		Plaintext: plain,
		Label:     "cli",
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"token":  plain,
		"user": map[string]any{
			"id":    user.ID,
			"login": user.Login,
			"name":  user.Name,
		},
	})
}
