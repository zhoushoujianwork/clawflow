package cloud

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// authMem holds the in-memory tables for identity / sessions / tokens that
// MemoryStore uses. Kept out of MemoryStore's struct definition to minimise
// diff to existing code; values are protected by authMu.
type authMem struct {
	mu sync.Mutex

	Users             map[string]*User         // by user id
	usersByGitHubID   map[int64]string         // github id → user id
	Sessions          map[string]*Session      // by session id
	APITokens         map[string]*APIToken     // by token id
	apiTokenByHash    map[string]string        // hash → token id
	Installations     map[string]*Installation // by installation id
	installByGitHubID map[int64]string         // github installation id → installation id
	userInstalls      map[string]map[string]struct{}
}

func newAuthMem() *authMem {
	return &authMem{
		Users:             map[string]*User{},
		usersByGitHubID:   map[int64]string{},
		Sessions:          map[string]*Session{},
		APITokens:         map[string]*APIToken{},
		apiTokenByHash:    map[string]string{},
		Installations:     map[string]*Installation{},
		installByGitHubID: map[int64]string{},
		userInstalls:      map[string]map[string]struct{}{},
	}
}

// memoryAuth is a package-level lazy singleton so we can extend MemoryStore
// without changing its constructor or struct fields. Each MemoryStore instance
// gets its own authMem via the keyed map below.
var (
	memAuthMu sync.Mutex
	memAuths  = map[*MemoryStore]*authMem{}
)

func authFor(s *MemoryStore) *authMem {
	memAuthMu.Lock()
	defer memAuthMu.Unlock()
	if a, ok := memAuths[s]; ok {
		return a
	}
	a := newAuthMem()
	memAuths[s] = a
	return a
}

// hashToken returns the lowercase hex SHA-256 of the supplied plaintext.
// Tokens are 32 random bytes, so a fast hash is appropriate; we are not
// protecting user-chosen passwords.
func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// constantTimeEqHash returns true iff a and b are equal hex hashes. Both
// inputs are constant-length (64 chars), so this is purely defensive.
func constantTimeEqHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---- MemoryStore: identity / users ----

func (s *MemoryStore) UpsertUser(req UpsertUserRequest) (*User, error) {
	if req.GitHubID == 0 {
		return nil, fmt.Errorf("github_id is required")
	}
	if req.Login == "" {
		return nil, fmt.Errorf("login is required")
	}
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	if id, ok := a.usersByGitHubID[req.GitHubID]; ok {
		u := a.Users[id]
		u.Login = req.Login
		u.Name = req.Name
		u.AvatarURL = req.AvatarURL
		u.UpdatedAt = now
		cp := *u
		return &cp, nil
	}
	id := newID("user")
	u := &User{
		ID:        id,
		GitHubID:  req.GitHubID,
		Login:     req.Login,
		Name:      req.Name,
		AvatarURL: req.AvatarURL,
		CreatedAt: now,
		UpdatedAt: now,
	}
	a.Users[id] = u
	a.usersByGitHubID[req.GitHubID] = id
	cp := *u
	return &cp, nil
}

func (s *MemoryStore) GetUserByGitHubID(githubID int64) (*User, error) {
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.usersByGitHubID[githubID]
	if !ok {
		return nil, nil
	}
	cp := *a.Users[id]
	return &cp, nil
}

func (s *MemoryStore) GetUser(id string) (*User, error) {
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	u, ok := a.Users[id]
	if !ok {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}

// ---- MemoryStore: sessions ----

func (s *MemoryStore) CreateSession(userID string, ttl time.Duration) (*Session, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.Users[userID]; !ok {
		return nil, fmt.Errorf("user not found")
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:        newID("sess"),
		UserID:    userID,
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
	}
	a.Sessions[sess.ID] = sess
	cp := *sess
	return &cp, nil
}

func (s *MemoryStore) GetSession(id string) (*Session, error) {
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	sess, ok := a.Sessions[id]
	if !ok {
		return nil, nil
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		delete(a.Sessions, id)
		return nil, nil
	}
	cp := *sess
	return &cp, nil
}

func (s *MemoryStore) DeleteSession(id string) error {
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.Sessions, id)
	return nil
}

// ---- MemoryStore: api_tokens ----

func (s *MemoryStore) CreateAPIToken(req CreateAPITokenRequest) (*APIToken, error) {
	if req.UserID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if req.Plaintext == "" {
		return nil, fmt.Errorf("plaintext is required")
	}
	if req.Kind != APITokenKindPersonal && req.Kind != APITokenKindMachine {
		return nil, fmt.Errorf("kind must be %q or %q", APITokenKindPersonal, APITokenKindMachine)
	}
	if req.Kind == APITokenKindMachine && req.MachineID == "" {
		return nil, fmt.Errorf("machine token requires machine_id")
	}
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.Users[req.UserID]; !ok {
		return nil, fmt.Errorf("user not found")
	}
	hash := hashToken(req.Plaintext)
	if _, exists := a.apiTokenByHash[hash]; exists {
		return nil, fmt.Errorf("token hash collision")
	}
	tok := &APIToken{
		ID:        newID("tok"),
		UserID:    req.UserID,
		Kind:      req.Kind,
		MachineID: req.MachineID,
		Label:     req.Label,
		CreatedAt: time.Now().UTC(),
	}
	a.APITokens[tok.ID] = tok
	a.apiTokenByHash[hash] = tok.ID
	cp := *tok
	return &cp, nil
}

func (s *MemoryStore) LookupAPIToken(plaintext string) (*APIToken, error) {
	if plaintext == "" {
		return nil, nil
	}
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	hash := hashToken(plaintext)
	id, ok := a.apiTokenByHash[hash]
	if !ok {
		return nil, nil
	}
	tok := a.APITokens[id]
	if tok == nil || tok.RevokedAt != nil {
		return nil, nil
	}
	if !constantTimeEqHash(hash, hashToken(plaintext)) {
		return nil, nil
	}
	now := time.Now().UTC()
	tok.LastUsedAt = &now
	cp := *tok
	return &cp, nil
}

func (s *MemoryStore) RevokeAPIToken(id string) error {
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	tok, ok := a.APITokens[id]
	if !ok {
		return fmt.Errorf("token not found")
	}
	if tok.RevokedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	tok.RevokedAt = &now
	return nil
}

// ---- MemoryStore: installations ----

func (s *MemoryStore) UpsertInstallation(req UpsertInstallationRequest) (*Installation, error) {
	if req.GitHubInstallationID == 0 {
		return nil, fmt.Errorf("github_installation_id is required")
	}
	if req.AccountLogin == "" {
		return nil, fmt.Errorf("account_login is required")
	}
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	if id, ok := a.installByGitHubID[req.GitHubInstallationID]; ok {
		inst := a.Installations[id]
		inst.AccountLogin = req.AccountLogin
		inst.AccountType = req.AccountType
		inst.UpdatedAt = now
		cp := *inst
		return &cp, nil
	}
	id := newID("inst")
	inst := &Installation{
		ID:                   id,
		GitHubInstallationID: req.GitHubInstallationID,
		AccountLogin:         req.AccountLogin,
		AccountType:          req.AccountType,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	a.Installations[id] = inst
	a.installByGitHubID[req.GitHubInstallationID] = id
	cp := *inst
	return &cp, nil
}

func (s *MemoryStore) LinkUserInstallation(userID, installationID string) error {
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.Users[userID]; !ok {
		return fmt.Errorf("user not found")
	}
	if _, ok := a.Installations[installationID]; !ok {
		return fmt.Errorf("installation not found")
	}
	links, ok := a.userInstalls[userID]
	if !ok {
		links = map[string]struct{}{}
		a.userInstalls[userID] = links
	}
	links[installationID] = struct{}{}
	return nil
}

func (s *MemoryStore) ListUserInstallations(userID string) []*Installation {
	a := authFor(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	links := a.userInstalls[userID]
	out := make([]*Installation, 0, len(links))
	for id := range links {
		if inst, ok := a.Installations[id]; ok {
			cp := *inst
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountLogin < out[j].AccountLogin })
	return out
}
