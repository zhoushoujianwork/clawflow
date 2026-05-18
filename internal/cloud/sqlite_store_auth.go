package cloud

import (
	"database/sql"
	"fmt"
	"time"
)

// ---- SQLiteStore: identity / users ----

func (s *SQLiteStore) UpsertUser(req UpsertUserRequest) (*User, error) {
	if req.GitHubID == 0 {
		return nil, fmt.Errorf("github_id is required")
	}
	if req.Login == "" {
		return nil, fmt.Errorf("login is required")
	}
	now := time.Now().UTC()
	nowStr := sqliteTime(now)

	var id string
	err := s.db.QueryRow(
		`SELECT id FROM users WHERE github_id = ?`, req.GitHubID,
	).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == sql.ErrNoRows {
		id = newID("user")
		if _, err := s.db.Exec(
			`INSERT INTO users (id, github_id, login, name, avatar_url, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, req.GitHubID, req.Login, req.Name, req.AvatarURL, nowStr, nowStr,
		); err != nil {
			return nil, fmt.Errorf("insert user: %w", err)
		}
	} else {
		if _, err := s.db.Exec(
			`UPDATE users SET login = ?, name = ?, avatar_url = ?, updated_at = ? WHERE id = ?`,
			req.Login, req.Name, req.AvatarURL, nowStr, id,
		); err != nil {
			return nil, fmt.Errorf("update user: %w", err)
		}
	}
	return s.GetUser(id)
}

func (s *SQLiteStore) GetUserByGitHubID(githubID int64) (*User, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM users WHERE github_id = ?`, githubID,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetUser(id)
}

func (s *SQLiteStore) GetUser(id string) (*User, error) {
	var u User
	var createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT id, github_id, login, name, avatar_url, created_at, updated_at
		 FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.GitHubID, &u.Login, &u.Name, &u.AvatarURL, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &u, nil
}

// ---- SQLiteStore: sessions ----

func (s *SQLiteStore) CreateSession(userID string, ttl time.Duration) (*Session, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	var count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM users WHERE id = ?`, userID,
	).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fmt.Errorf("user not found")
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:        newID("sess"),
		UserID:    userID,
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
	}
	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		sess.ID, sess.UserID, sqliteTime(sess.ExpiresAt), sqliteTime(sess.CreatedAt),
	); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return sess, nil
}

func (s *SQLiteStore) GetSession(id string) (*Session, error) {
	var sess Session
	var expiresAt, createdAt string
	err := s.db.QueryRow(
		`SELECT id, user_id, expires_at, created_at FROM sessions WHERE id = ?`, id,
	).Scan(&sess.ID, &sess.UserID, &expiresAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
	sess.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if time.Now().UTC().After(sess.ExpiresAt) {
		_ = s.DeleteSession(id)
		return nil, nil
	}
	return &sess, nil
}

func (s *SQLiteStore) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// ---- SQLiteStore: api_tokens ----

func (s *SQLiteStore) CreateAPIToken(req CreateAPITokenRequest) (*APIToken, error) {
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
	var count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM users WHERE id = ?`, req.UserID,
	).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fmt.Errorf("user not found")
	}
	now := time.Now().UTC()
	tok := &APIToken{
		ID:        newID("tok"),
		UserID:    req.UserID,
		Kind:      req.Kind,
		MachineID: req.MachineID,
		Label:     req.Label,
		CreatedAt: now,
	}
	hash := hashToken(req.Plaintext)
	if _, err := s.db.Exec(
		`INSERT INTO api_tokens (id, user_id, hash, kind, machine_id, label, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tok.ID, tok.UserID, hash, tok.Kind, tok.MachineID, tok.Label, sqliteTime(tok.CreatedAt),
	); err != nil {
		return nil, fmt.Errorf("insert api_token: %w", err)
	}
	return tok, nil
}

func (s *SQLiteStore) LookupAPIToken(plaintext string) (*APIToken, error) {
	if plaintext == "" {
		return nil, nil
	}
	hash := hashToken(plaintext)
	var tok APIToken
	var lastUsed, revoked sql.NullString
	var createdAt string
	err := s.db.QueryRow(
		`SELECT id, user_id, kind, machine_id, label, last_used_at, created_at, revoked_at
		 FROM api_tokens WHERE hash = ?`, hash,
	).Scan(&tok.ID, &tok.UserID, &tok.Kind, &tok.MachineID, &tok.Label,
		&lastUsed, &createdAt, &revoked)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if revoked.Valid {
		return nil, nil
	}
	tok.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if lastUsed.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
		tok.LastUsedAt = &t
	}
	now := time.Now().UTC()
	_, _ = s.db.Exec(
		`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, sqliteTime(now), tok.ID,
	)
	tok.LastUsedAt = &now
	return &tok, nil
}

func (s *SQLiteStore) RevokeAPIToken(id string) error {
	now := sqliteTime(time.Now().UTC())
	res, err := s.db.Exec(
		`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		now, id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE id = ?`, id).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("token not found")
		}
	}
	return nil
}

// ---- SQLiteStore: installations ----

func (s *SQLiteStore) UpsertInstallation(req UpsertInstallationRequest) (*Installation, error) {
	if req.GitHubInstallationID == 0 {
		return nil, fmt.Errorf("github_installation_id is required")
	}
	if req.AccountLogin == "" {
		return nil, fmt.Errorf("account_login is required")
	}
	now := time.Now().UTC()
	nowStr := sqliteTime(now)

	var id string
	err := s.db.QueryRow(
		`SELECT id FROM installations WHERE github_installation_id = ?`, req.GitHubInstallationID,
	).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == sql.ErrNoRows {
		id = newID("inst")
		if _, err := s.db.Exec(
			`INSERT INTO installations (id, github_installation_id, account_login, account_type, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, req.GitHubInstallationID, req.AccountLogin, req.AccountType, nowStr, nowStr,
		); err != nil {
			return nil, fmt.Errorf("insert installation: %w", err)
		}
	} else {
		if _, err := s.db.Exec(
			`UPDATE installations SET account_login = ?, account_type = ?, updated_at = ? WHERE id = ?`,
			req.AccountLogin, req.AccountType, nowStr, id,
		); err != nil {
			return nil, fmt.Errorf("update installation: %w", err)
		}
	}
	return s.getInstallationByID(id)
}

func (s *SQLiteStore) getInstallationByID(id string) (*Installation, error) {
	var inst Installation
	var createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT id, github_installation_id, account_login, account_type, created_at, updated_at
		 FROM installations WHERE id = ?`, id,
	).Scan(&inst.ID, &inst.GitHubInstallationID, &inst.AccountLogin, &inst.AccountType,
		&createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	inst.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	inst.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &inst, nil
}

func (s *SQLiteStore) LinkUserInstallation(userID, installationID string) error {
	var userCount, instCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&userCount); err != nil {
		return err
	}
	if userCount == 0 {
		return fmt.Errorf("user not found")
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM installations WHERE id = ?`, installationID).Scan(&instCount); err != nil {
		return err
	}
	if instCount == 0 {
		return fmt.Errorf("installation not found")
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO user_installations (user_id, installation_id) VALUES (?, ?)`,
		userID, installationID,
	)
	return err
}

func (s *SQLiteStore) ListUserInstallations(userID string) []*Installation {
	rows, err := s.db.Query(
		`SELECT i.id, i.github_installation_id, i.account_login, i.account_type, i.created_at, i.updated_at
		 FROM installations i
		 INNER JOIN user_installations ui ON ui.installation_id = i.id
		 WHERE ui.user_id = ?
		 ORDER BY i.account_login`, userID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Installation
	for rows.Next() {
		var inst Installation
		var createdAt, updatedAt string
		if err := rows.Scan(&inst.ID, &inst.GitHubInstallationID, &inst.AccountLogin, &inst.AccountType,
			&createdAt, &updatedAt); err != nil {
			continue
		}
		inst.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		inst.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, &inst)
	}
	return out
}
