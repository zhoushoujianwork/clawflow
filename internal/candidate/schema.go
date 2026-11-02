package candidate

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/devmode"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

var schemaPattern = regexp.MustCompile(`^cf_cand_[0-9a-f]{12}$`)

// PinDSN returns dsn restricted to schema through the search_path parameter.
func PinDSN(dsn, schema string) (string, error) {
	if !schemaPattern.MatchString(schema) {
		return "", fmt.Errorf("%w: %q is not a candidate schema", ErrCollision, schema)
	}
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("database DSN is not parseable")
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	return dsn + " search_path=" + schema, nil
}

// SchemaManager creates and drops the candidate schema.
type SchemaManager interface {
	// Ensure creates the schema, returning ErrCollision when it already exists
	// and is not recorded as this candidate's.
	Ensure(ctx context.Context, schema string, owned bool) error
	Drop(ctx context.Context, schema string) error
}

// PostgresSchemas manages schemas on a shared server using an administrative
// DSN. The DSN is held in memory only.
type PostgresSchemas struct{ DSN string }

func (p PostgresSchemas) admin(ctx context.Context) (*store.Store, error) {
	if devmode.Active() {
		return nil, fmt.Errorf("%w: a candidate cannot administer schemas", devmode.ErrBlocked)
	}
	return store.Open(ctx, p.DSN)
}

func (p PostgresSchemas) Ensure(ctx context.Context, schema string, owned bool) error {
	if !schemaPattern.MatchString(schema) {
		return fmt.Errorf("%w: %q is not a candidate schema", ErrCollision, schema)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, err := p.admin(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	var n int64
	if err := s.DB().WithContext(ctx).Raw("SELECT count(*) FROM information_schema.schemata WHERE schema_name = ?", schema).Scan(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		if owned {
			return nil
		}
		return collide("database schema %s already exists and is not recorded for this candidate", schema)
	}
	return s.DB().WithContext(ctx).Exec(`CREATE SCHEMA "` + schema + `"`).Error
}

func (p PostgresSchemas) Drop(ctx context.Context, schema string) error {
	if !schemaPattern.MatchString(schema) {
		return fmt.Errorf("%w: refusing to drop %q", ErrCollision, schema)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, err := p.admin(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.DB().WithContext(ctx).Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error
}
