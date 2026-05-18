package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// githubAccessToken is the response from GitHub's
// https://github.com/login/oauth/access_token endpoint.
type githubAccessToken struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int    `json:"expires_in,omitempty"`
	RefreshToken          string `json:"refresh_token,omitempty"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in,omitempty"`
	TokenType             string `json:"token_type,omitempty"`
	Scope                 string `json:"scope,omitempty"`
	Error                 string `json:"error,omitempty"`
	ErrorDescription      string `json:"error_description,omitempty"`
}

// exchangeCode trades an OAuth authorization code for a user access token.
// The GitHub App's "User authorization callback URL" must match redirectURI
// exactly, or GitHub returns redirect_uri_mismatch.
func (h *Handler) exchangeCode(ctx context.Context, code, redirectURI string) (*githubAccessToken, error) {
	form := url.Values{
		"client_id":     {h.cfg.ClientID},
		"client_secret": {h.cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	return h.postForm(ctx, "https://github.com/login/oauth/access_token", form)
}

// startDeviceFlow asks GitHub for a device code. For GitHub Apps the App's
// "Enable Device Flow" must be checked.
type deviceCodeResp struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Error           string `json:"error,omitempty"`
}

func (h *Handler) startDeviceFlow(ctx context.Context) (*deviceCodeResp, error) {
	form := url.Values{
		"client_id": {h.cfg.ClientID},
	}
	req, _ := http.NewRequestWithContext(ctx, "POST",
		"https://github.com/login/device/code", strings.NewReader(form.Encode()))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github device/code: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var d deviceCodeResp
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("github device/code parse: %w (body=%q)", err, string(body))
	}
	if d.Error != "" {
		return nil, fmt.Errorf("github device/code: %s", d.Error)
	}
	return &d, nil
}

// pollDeviceFlow polls GitHub's token endpoint with the device code. Returns
// (token, "") when the user has approved, (nil, "authorization_pending") while
// waiting, (nil, "slow_down") if the client should back off, and a hard error
// for terminal failures.
func (h *Handler) pollDeviceFlow(ctx context.Context, deviceCode string) (*githubAccessToken, string, error) {
	form := url.Values{
		"client_id":   {h.cfg.ClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	tok, err := h.postForm(ctx, "https://github.com/login/oauth/access_token", form)
	if err != nil {
		return nil, "", err
	}
	switch tok.Error {
	case "":
		if tok.AccessToken == "" {
			return nil, "", fmt.Errorf("empty access_token in success response")
		}
		return tok, "", nil
	case "authorization_pending", "slow_down":
		return nil, tok.Error, nil
	default:
		return nil, "", fmt.Errorf("github device poll: %s (%s)", tok.Error, tok.ErrorDescription)
	}
}

// postForm POSTs form-encoded body to url and decodes the JSON response as
// githubAccessToken (which has Error / ErrorDescription fields for the failure
// path).
func (h *Handler) postForm(ctx context.Context, url string, form url.Values) (*githubAccessToken, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(form.Encode()))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github post %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var t githubAccessToken
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("github post %s parse: %w (body=%q)", url, err, string(body))
	}
	return &t, nil
}

// githubUser is the subset of GET /user we persist.
type githubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// fetchUser calls GET /user as the authorized user.
func (h *Handler) fetchUser(ctx context.Context, userToken string) (*githubUser, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := h.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github /user: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github /user: %d %s", resp.StatusCode, string(body))
	}
	var u githubUser
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("github /user parse: %w", err)
	}
	return &u, nil
}

// githubInstallation is the subset of GET /user/installations we persist.
type githubInstallation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
}

type userInstallationsResp struct {
	TotalCount    int                  `json:"total_count"`
	Installations []githubInstallation `json:"installations"`
}

// fetchUserInstallations calls GET /user/installations as the authorized user,
// returning the cached list of orgs/users where this App is installed.
func (h *Handler) fetchUserInstallations(ctx context.Context, userToken string) ([]githubInstallation, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/user/installations?per_page=100", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := h.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github /user/installations: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github /user/installations: %d %s", resp.StatusCode, string(body))
	}
	var r userInstallationsResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("github /user/installations parse: %w", err)
	}
	return r.Installations, nil
}
