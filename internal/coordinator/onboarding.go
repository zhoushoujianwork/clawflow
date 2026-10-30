package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Invitation struct {
	Name      string    `gorm:"primaryKey" json:"name"`
	Hash      []byte    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
	Consumed  bool      `json:"-"`
}

func (Invitation) TableName() string { return "coordinator_invitations" }

type JoinOffer struct {
	Name      string    `json:"name"`
	Secret    string    `json:"secret"`
	ExpiresAt time.Time `json:"expires_at"`
}

var workerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

// Invite never replaces an existing identity. A lost response can be inspected
// and a new name used, but cannot silently rotate an active worker credential.
func (s *Store) Invite(ctx context.Context, name string) (JoinOffer, error) {
	if !workerName.MatchString(name) {
		return JoinOffer{}, ErrInvalid
	}
	secret, err := randomID()
	if err != nil {
		return JoinOffer{}, err
	}
	hash := sha256.Sum256([]byte(secret))
	offer := JoinOffer{Name: name, Secret: secret, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&Token{}).Where("name = ?", name).Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			return ErrInvalid
		}
		if err := tx.Create(&Invitation{Name: name, Hash: hash[:], ExpiresAt: offer.ExpiresAt}).Error; err != nil {
			return err
		}
		return audit(tx, name, "", "invite")
	})
	if err != nil {
		return JoinOffer{}, err
	}
	return offer, nil
}

// Exchange atomically consumes the invitation and issues a distinct credential.
// Invited workers start paused; registration cannot override that operator choice.
func (s *Store) Exchange(ctx context.Context, secret string) (map[string]string, error) {
	hash := sha256.Sum256([]byte(secret))
	credential, err := randomID()
	if err != nil {
		return nil, err
	}
	credentialHash := sha256.Sum256([]byte(credential))
	var invitation Invitation
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("hash = ? AND consumed = false AND expires_at > clock_timestamp()", hash[:]).First(&invitation).Error; err != nil {
			return ErrToken
		}
		if err := tx.Create(&storedToken{Token: Token{Name: invitation.Name, Scope: ScopeWorker, CreatedAt: time.Now().UTC()}, Hash: credentialHash[:]}).Error; err != nil {
			return err
		}
		if err := tx.Create(&Worker{ID: invitation.Name, Paused: true, MaxParallel: 1, Tags: []string{}, Roles: []string{}}).Error; err != nil {
			return err
		}
		if err := tx.Model(&invitation).Update("consumed", true).Error; err != nil {
			return err
		}
		return audit(tx, invitation.Name, "", "invite-exchange")
	})
	if err != nil {
		return nil, err
	}
	return map[string]string{"name": invitation.Name, "token": credential}, nil
}

func (s *Store) SetWorkerPaused(ctx context.Context, name string, paused bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var worker Worker
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&worker, "id = ?", name).Error; err != nil {
			return ErrInvalid
		}
		if !paused {
			var token Token
			if err := tx.First(&token, "name = ? AND revoked = false", name).Error; err != nil {
				return ErrToken
			}
		}
		if err := tx.Model(&worker).Update("paused", paused).Error; err != nil {
			return err
		}
		action := "resume"
		if paused {
			action = "drain"
		}
		return audit(tx, name, "", action)
	})
}

// RemoveWorker requires a paused, fully settled worker. Revocation is not a
// claim that a disconnected process stopped; unfinished attempts block removal.
func (s *Store) RemoveWorker(ctx context.Context, name string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var worker Worker
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&worker, "id = ?", name).Error; err != nil {
			return ErrInvalid
		}
		if !worker.Paused {
			return ErrInvalid
		}
		var n int64
		if err := tx.Model(&Task{}).Where("lease_owner = ? AND state = 'leased'", name).Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			return ErrInvalid
		}
		if err := tx.Model(&Token{}).Where("name = ?", name).Update("revoked", true).Error; err != nil {
			return err
		}
		return audit(tx, name, "", "worker-remove")
	})
}

// JoinHandler contains no task or admin capabilities. Only possession of an
// unexpired invitation authorizes the single exchange, over the pinned TLS listener.
func JoinHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		var input struct {
			Secret string `json:"secret"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if err := d.Decode(&input); err != nil {
			http.Error(w, "invalid invitation", 400)
			return
		}
		var extra any
		if d.Decode(&extra) != io.EOF || len(input.Secret) != 64 {
			http.Error(w, "invalid invitation", 400)
			return
		}
		out, err := s.Exchange(ctx, input.Secret)
		if err != nil {
			http.Error(w, "invitation unavailable or already used", 401)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}
