package chat

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EnsureClone returns an absolute path to a local clone of `repo` (an
// "owner/name" slug). If the clone exists, runs `git pull --ff-only`
// best-effort. If not, looks up the VCSConnection via cfg.Store, mints
// a GitHub App installation token, and clones via
// https://x-access-token:<token>@github.com/<repo>.git.
//
// Safe to call concurrently for the same repo — per-repo mutex
// serialises the actual filesystem work.
func (h *Handler) EnsureClone(ctx context.Context, repo string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}

	path := filepath.Join(h.cfg.ClonesDir, owner, name)
	mu := h.cloneLock(repo)
	mu.Lock()
	defer mu.Unlock()

	if st, err := os.Stat(filepath.Join(path, ".git")); err == nil && st.IsDir() {
		if err := runGit(ctx, path, "pull", "--ff-only"); err != nil {
			// Slightly-stale clone beats no clone; pull can fail for
			// benign reasons (no network, divergent history).
			fmt.Fprintf(os.Stderr, "clawflow chat: git pull %s: %v\n", repo, err)
		}
		return path, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("mkdir clones parent: %w", err)
	}

	cloneURL := fmt.Sprintf("https://github.com/%s.git", repo)
	if h.cfg.Store != nil && h.cfg.GitHubAppID != 0 && h.appPrivateKey != nil {
		conn := h.cfg.Store.GetConnectionByRepo(repo)
		if conn != nil && conn.GitHubApp != nil && conn.GitHubApp.InstallationID != 0 {
			tok, terr := h.installationToken(ctx, conn.GitHubApp.InstallationID)
			if terr != nil {
				return "", fmt.Errorf("mint installation token: %w", terr)
			}
			// `x-access-token` is GitHub's documented username for
			// installation-token git auth.
			cloneURL = fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", tok, repo)
		}
	}

	if err := runGit(ctx, "", "clone", "--depth=50", cloneURL, path); err != nil {
		return "", fmt.Errorf("git clone %s: %w", repo, scrubURL(err, cloneURL))
	}
	return path, nil
}

func (h *Handler) cloneLock(repo string) *sync.Mutex {
	h.cloneLocksMu.Lock()
	defer h.cloneLocksMu.Unlock()
	mu, ok := h.cloneLocks[repo]
	if !ok {
		mu = &sync.Mutex{}
		h.cloneLocks[repo] = mu
	}
	return mu
}

// splitRepo parses "owner/name" and rejects path-traversal attempts.
// Each segment must be non-empty, not "." or "..", and contain only
// [A-Za-z0-9._-] — conservative vs GitHub's own rules.
func splitRepo(repo string) (owner, name string, err error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", "", errors.New("empty repo")
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("repo %q: want owner/name", repo)
	}
	owner, name = parts[0], parts[1]
	if !isSafeSegment(owner) || !isSafeSegment(name) {
		return "", "", fmt.Errorf("repo %q contains unsafe characters", repo)
	}
	return owner, name, nil
}

func isSafeSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// scrubURL redacts the installation token from a clone-failure error
// so it doesn't leak into logs or HTTP responses.
func scrubURL(err error, url string) error {
	if err == nil || !strings.Contains(url, "x-access-token:") {
		return err
	}
	atIdx := strings.Index(url, "@")
	if atIdx < 0 {
		return err
	}
	redacted := "https://[redacted]" + url[atIdx:]
	return errors.New(strings.ReplaceAll(err.Error(), url, redacted))
}

func runGit(ctx context.Context, workDir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	// No interactive prompts — credentials come via the URL or the
	// repo is public.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/echo",
	)
	var combined bytes.Buffer
	cmd.Stderr = &combined
	cmd.Stdout = &combined
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(combined.String()))
	}
	return nil
}

// ---- GitHub App installation token ----

type installationTokenEntry struct {
	Token     string
	ExpiresAt time.Time
}

// installationToken returns a usable installation access token for
// `installationID`, minting + caching it via the App-level JWT flow.
// Cache TTL = (GitHub-supplied expires_at - 1 minute).
func (h *Handler) installationToken(ctx context.Context, installationID int64) (string, error) {
	now := h.cfg.Now()

	h.tokenMu.Lock()
	entry, ok := h.tokenCache[installationID]
	h.tokenMu.Unlock()
	if ok && now.Before(entry.ExpiresAt) {
		return entry.Token, nil
	}

	jwt, err := signGitHubAppJWT(h.appPrivateKey, h.cfg.GitHubAppID, now)
	if err != nil {
		return "", fmt.Errorf("sign app jwt: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := h.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("github access_tokens %d: %s", resp.StatusCode, string(body))
	}

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("github returned empty token")
	}

	exp := out.ExpiresAt.Add(-1 * time.Minute)
	h.tokenMu.Lock()
	h.tokenCache[installationID] = installationTokenEntry{Token: out.Token, ExpiresAt: exp}
	h.tokenMu.Unlock()
	return out.Token, nil
}

// loadRSAPrivateKey reads a PEM-encoded RSA private key from path.
// Accepts PKCS#1 ("RSA PRIVATE KEY") and PKCS#8 ("PRIVATE KEY").
func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	return parseRSAPrivateKey(data)
}

func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("PKCS#8 key is not RSA")
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type %q", block.Type)
	}
}

// signGitHubAppJWT produces a JWT for the GitHub App installations
// API. Algorithm RS256 as required by GitHub. iat 30 s in the past for
// clock-skew tolerance; exp 9 minutes ahead (GitHub max is 10).
//
// Hand-rolled — stdlib has every primitive, no point pulling in a JWT
// library for one call site.
func signGitHubAppJWT(key *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	if key == nil {
		return "", errors.New("nil private key")
	}
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	payload := map[string]any{
		"iat": now.Add(-30 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	}

	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	signingInput := b64url(hb) + "." + b64url(pb)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + b64url(sig), nil
}

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
