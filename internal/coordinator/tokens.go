package coordinator

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

var ErrToken = errors.New("invalid or revoked token")

const (
	ScopeWorker = "worker"
	ScopeAdmin  = "admin"
)

// Token exposes metadata only. Neither the hash nor the secret is listed.
type Token struct {
	Name      string `gorm:"primaryKey"`
	Scope     string `gorm:"not null;default:worker"`
	CreatedAt time.Time
	LastSeen  *time.Time
	Revoked   bool
}

func (Token) TableName() string { return "coordinator_tokens" }

type storedToken struct {
	Token `gorm:"embedded"`
	Hash  []byte
}

func (storedToken) TableName() string { return "coordinator_tokens" }

// CreateToken returns 32 random bytes encoded as hex exactly once.
func (s *Store) CreateToken(ctx context.Context, name string, scopes ...string) (string, error) {
	scope := ScopeWorker
	if len(scopes) > 1 {
		return "", ErrInvalid
	}
	if len(scopes) == 1 {
		scope = scopes[0]
	}
	if scope != ScopeWorker && scope != ScopeAdmin {
		return "", ErrInvalid
	}
	if strings.TrimSpace(name) == "" {
		return "", ErrInvalid
	}
	secret, err := randomID()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(secret))
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&storedToken{Token: Token{Name: name, Scope: scope, CreatedAt: time.Now().UTC()}, Hash: hash[:]}).Error; err != nil {
			return err
		}
		return audit(tx, name, "", "token-create")
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}
func (s *Store) VerifyToken(ctx context.Context, secret string) (*Token, error) {
	hash := sha256.Sum256([]byte(secret))
	var found *Token
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tokens []storedToken
		if err := tx.Find(&tokens).Error; err != nil {
			return err
		}
		// Compare all fixed-size hashes without early return.
		for _, t := range tokens {
			if subtle.ConstantTimeCompare(hash[:], t.Hash) == 1 && !t.Revoked {
				metadata := t.Token
				found = &metadata
			}
		}
		if found == nil {
			return ErrToken
		}
		now := time.Now().UTC()
		r := tx.Model(&Token{}).Where("name = ? AND revoked = false", found.Name).Update("last_seen", now)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrToken
		}
		found.LastSeen = &now
		return audit(tx, found.Name, "", "token-verify")
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}
func (s *Store) RevokeToken(ctx context.Context, name string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&Token{}).Where("name = ?", name).Update("revoked", true)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrToken
		}
		return audit(tx, name, "", "token-revoke")
	})
}
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	var tokens []Token
	err := s.db.WithContext(ctx).Select("name, scope, created_at, last_seen, revoked").Order("name").Find(&tokens).Error
	return tokens, err
}

// HasScope preserves worker access for tokens issued before scoped tokens existed.
func (t Token) HasScope(scope string) bool {
	return t.Scope == scope || (t.Scope == "" && scope == ScopeWorker)
}
