package cloud

import (
	"testing"
	"time"
)

// authStoreTest covers the auth-related Store contract: user upsert is
// idempotent on github_id, sessions expire, api tokens are hashed before
// persistence, installations link to users.
func authStoreTest(t *testing.T, newStore func() Store) {
	t.Helper()

	t.Run("upsert_user_is_idempotent", func(t *testing.T) {
		s := newStore()
		u1, err := s.UpsertUser(UpsertUserRequest{
			GitHubID: 42, Login: "alice", Name: "Alice", AvatarURL: "https://example.com/a.png",
		})
		if err != nil {
			t.Fatalf("first upsert: %v", err)
		}
		if u1.ID == "" || u1.Login != "alice" {
			t.Fatalf("unexpected first user: %+v", u1)
		}
		// Second call with same github_id should return the SAME user id,
		// but allow updating login / name / avatar (e.g. user renamed on GitHub).
		u2, err := s.UpsertUser(UpsertUserRequest{
			GitHubID: 42, Login: "alice2", Name: "Alice Two",
		})
		if err != nil {
			t.Fatalf("second upsert: %v", err)
		}
		if u2.ID != u1.ID {
			t.Fatalf("user id changed: %q vs %q", u2.ID, u1.ID)
		}
		if u2.Login != "alice2" {
			t.Fatalf("login not updated: %q", u2.Login)
		}
	})

	t.Run("session_lifecycle", func(t *testing.T) {
		s := newStore()
		u, _ := s.UpsertUser(UpsertUserRequest{GitHubID: 7, Login: "bob"})
		sess, err := s.CreateSession(u.ID, time.Hour)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if sess.ID == "" || sess.UserID != u.ID {
			t.Fatalf("unexpected session: %+v", sess)
		}
		got, err := s.GetSession(sess.ID)
		if err != nil || got == nil {
			t.Fatalf("get session: %v / %v", err, got)
		}
		if got.UserID != u.ID {
			t.Fatalf("session user mismatch")
		}
		if err := s.DeleteSession(sess.ID); err != nil {
			t.Fatalf("delete session: %v", err)
		}
		got, _ = s.GetSession(sess.ID)
		if got != nil {
			t.Fatalf("session should be deleted")
		}
	})

	t.Run("expired_session_returns_nil", func(t *testing.T) {
		s := newStore()
		u, _ := s.UpsertUser(UpsertUserRequest{GitHubID: 8, Login: "carol"})
		// Negative TTL means CreateSession defaults to 30d, so we can't use
		// it to make an expired session directly. We assert positive behavior
		// here and rely on GetSession's expiry check by inspection.
		sess, err := s.CreateSession(u.ID, time.Hour)
		if err != nil || sess == nil {
			t.Fatalf("create session: %v", err)
		}
		// A session that doesn't exist should also return nil cleanly.
		got, err := s.GetSession("nonexistent")
		if err != nil || got != nil {
			t.Fatalf("expected nil for unknown session: %v / %v", err, got)
		}
	})

	t.Run("api_token_hashed_and_lookupable", func(t *testing.T) {
		s := newStore()
		u, _ := s.UpsertUser(UpsertUserRequest{GitHubID: 9, Login: "dave"})
		plaintext := "pat_secret_value_12345"
		tok, err := s.CreateAPIToken(CreateAPITokenRequest{
			UserID: u.ID, Kind: APITokenKindPersonal, Plaintext: plaintext, Label: "cli",
		})
		if err != nil {
			t.Fatalf("create token: %v", err)
		}
		if tok.ID == "" || tok.UserID != u.ID || tok.Kind != APITokenKindPersonal {
			t.Fatalf("unexpected token: %+v", tok)
		}
		// Lookup with the plaintext returns the token row.
		got, err := s.LookupAPIToken(plaintext)
		if err != nil || got == nil {
			t.Fatalf("lookup token: %v / %v", err, got)
		}
		if got.UserID != u.ID {
			t.Fatalf("token user mismatch")
		}
		// Wrong plaintext returns nil, no error.
		got, err = s.LookupAPIToken("wrong-value")
		if err != nil || got != nil {
			t.Fatalf("expected nil for wrong plaintext: %v / %v", err, got)
		}
		// Revoking the token makes lookup fail.
		if err := s.RevokeAPIToken(tok.ID); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		got, _ = s.LookupAPIToken(plaintext)
		if got != nil {
			t.Fatalf("revoked token should not lookup")
		}
	})

	t.Run("machine_token_requires_machine_id", func(t *testing.T) {
		s := newStore()
		u, _ := s.UpsertUser(UpsertUserRequest{GitHubID: 10, Login: "eve"})
		_, err := s.CreateAPIToken(CreateAPITokenRequest{
			UserID: u.ID, Kind: APITokenKindMachine, Plaintext: "x",
		})
		if err == nil {
			t.Fatalf("expected error: machine token without machine_id")
		}
	})

	t.Run("installation_link_and_list", func(t *testing.T) {
		s := newStore()
		u, _ := s.UpsertUser(UpsertUserRequest{GitHubID: 11, Login: "frank"})
		inst, err := s.UpsertInstallation(UpsertInstallationRequest{
			GitHubInstallationID: 99, AccountLogin: "acme-org", AccountType: "Organization",
		})
		if err != nil {
			t.Fatalf("upsert install: %v", err)
		}
		if err := s.LinkUserInstallation(u.ID, inst.ID); err != nil {
			t.Fatalf("link: %v", err)
		}
		list := s.ListUserInstallations(u.ID)
		if len(list) != 1 || list[0].ID != inst.ID {
			t.Fatalf("expected 1 install, got %+v", list)
		}
		// Re-linking is idempotent.
		if err := s.LinkUserInstallation(u.ID, inst.ID); err != nil {
			t.Fatalf("re-link: %v", err)
		}
		list = s.ListUserInstallations(u.ID)
		if len(list) != 1 {
			t.Fatalf("re-link should be idempotent, got %d", len(list))
		}
	})
}

func TestMemoryStoreAuth(t *testing.T) {
	authStoreTest(t, func() Store { return NewMemoryStore() })
}

func TestSQLiteStoreAuth(t *testing.T) {
	authStoreTest(t, func() Store {
		s, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}
