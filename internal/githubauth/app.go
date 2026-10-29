// Package githubauth selects machine-local GitHub App installation identities.
package githubauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// Identity contains only safe diagnostic metadata. Token must never be serialized.
type Identity struct {
	Login       string            `json:"login,omitempty"`
	Name        string            `json:"identity"`
	Token       string            `json:"-"`
	Permissions map[string]string `json:"permissions,omitempty"`
	Selection   string            `json:"repository_selection,omitempty"`
	Expires     time.Time         `json:"-"`
}
type installation struct {
	AppSlug     string            `json:"app_slug"`
	ID          int64             `json:"id"`
	Permissions map[string]string `json:"permissions"`
	Selection   string            `json:"repository_selection"`
	Account     struct {
		Login string `json:"login"`
	} `json:"account"`
}
type coverage struct {
	installation
	until time.Time
}

// Provider serializes refreshes so concurrent clients share one token mint.
type Provider struct {
	mu       sync.Mutex
	settings config.GitHubAppSettings
	base     string
	http     *http.Client
	now      func() time.Time
	tokens   map[int64]Identity
	repos    map[string]coverage
}

func New(s config.GitHubAppSettings, base string) *Provider {
	if base == "" {
		base = "https://api.github.com"
	}
	return &Provider{settings: s, base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: time.Now, tokens: map[int64]Identity{}, repos: map[string]coverage{}}
}

func signJWT(s config.GitHubAppSettings, now time.Time) (string, error) {
	data, err := s.ReadPrivateKey()
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", fmt.Errorf("github_app: invalid RSA private key")
	}
	var key *rsa.PrivateKey
	if k, e := x509.ParsePKCS1PrivateKey(block.Bytes); e == nil {
		key = k
	} else if k, e := x509.ParsePKCS8PrivateKey(block.Bytes); e == nil {
		key, _ = k.(*rsa.PrivateKey)
	}
	if key == nil {
		return "", fmt.Errorf("github_app: invalid RSA private key")
	}
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": s.AppID})
	enc := base64.RawURLEncoding.EncodeToString
	unsigned := enc([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc(claims)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("github_app: signing failed")
	}
	return unsigned + "." + enc(signature), nil
}
func (p *Provider) request(ctx context.Context, method, path, token string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, nil)
	if err != nil {
		return 0, fmt.Errorf("github_app: invalid endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := p.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("github_app: authentication transport unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return 404, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("github_app: authentication HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("github_app: invalid authentication response")
	}
	return resp.StatusCode, nil
}

// Resolve verifies repository coverage using the app-authenticated repository
// installation endpoint. Only absence of coverage falls back; outages fail closed.
func (p *Provider) Resolve(ctx context.Context, repo, personal string) (Identity, error) {
	fallback := Identity{Name: "personal", Token: personal}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fallback, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	cov, ok := p.repos[strings.ToLower(repo)]
	jwt := ""
	getJWT := func() (string, error) {
		if jwt != "" {
			return jwt, nil
		}
		var err error
		jwt, err = signJWT(p.settings, now)
		return jwt, err
	}
	if !ok || !now.Before(cov.until) {
		j, err := getJWT()
		if err != nil {
			return Identity{}, err
		}
		var inst installation
		status, err := p.request(ctx, "GET", "/repos/"+url.PathEscape(parts[0])+"/"+url.PathEscape(parts[1])+"/installation", j, &inst)
		if err != nil {
			return Identity{}, err
		}
		if status == 404 || inst.ID == 0 || (p.settings.InstallationID != 0 && p.settings.InstallationID != inst.ID) {
			inst = installation{}
		}
		cov = coverage{inst, now.Add(5 * time.Minute)}
		p.repos[strings.ToLower(repo)] = cov
	}
	if cov.ID == 0 {
		return fallback, nil
	}
	if token, ok := p.tokens[cov.ID]; ok && now.Add(5*time.Minute).Before(token.Expires) {
		return token, nil
	}
	j, err := getJWT()
	if err != nil {
		return Identity{}, err
	}
	var response struct {
		Token   string    `json:"token"`
		Expires time.Time `json:"expires_at"`
	}
	_, err = p.request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", cov.ID), j, &response)
	if err != nil {
		return Identity{}, err
	}
	if response.Token == "" || !response.Expires.After(now.Add(5*time.Minute)) {
		return Identity{}, fmt.Errorf("github_app: invalid installation token expiry")
	}
	identity := Identity{Login: cov.AppSlug + "[bot]", Name: fmt.Sprintf("app:%d:installation:%d", p.settings.AppID, cov.ID), Token: response.Token, Permissions: cov.Permissions, Selection: cov.Selection, Expires: response.Expires}
	p.tokens[cov.ID] = identity
	return identity, nil
}

var providers sync.Map

// Resolve shares the provider across REST clients and Git subprocesses. Workers
// use their own local settings; no private key enters a coordinator payload.
func Resolve(ctx context.Context, repo, personal, base string) (Identity, error) {
	cfg, err := config.Load()
	if err != nil {
		if _, e := os.Stat(config.ConfigPath()); os.IsNotExist(e) {
			return Identity{Name: "personal", Token: personal}, nil
		}
		return Identity{}, err
	}
	s := cfg.Settings.GitHubApp
	if !s.Enabled() {
		return Identity{Name: "personal", Token: personal}, nil
	}
	key := fmt.Sprintf("%d:%d:%s:%s", s.AppID, s.InstallationID, s.PrivateKeyPath, base)
	v, _ := providers.LoadOrStore(key, New(s, base))
	return v.(*Provider).Resolve(ctx, repo, personal)
}
