package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestResolveDSN(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.Database.DSN = "postgres://u:fromcfg@h/db"

	t.Run("settings value", func(t *testing.T) {
		t.Setenv(DSNEnv, "")
		got, err := ResolveDSN(cfg)
		if err != nil || got != cfg.Settings.Database.DSN {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("env overrides settings", func(t *testing.T) {
		t.Setenv(DSNEnv, "postgres://u:fromenv@h/db")
		got, err := ResolveDSN(cfg)
		if err != nil || got != "postgres://u:fromenv@h/db" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		t.Setenv(DSNEnv, "")
		for _, c := range []*config.Config{nil, {}} {
			_, err := ResolveDSN(c)
			if !errors.Is(err, ErrNoDSN) {
				t.Fatalf("want ErrNoDSN, got %v", err)
			}
			if !strings.Contains(err.Error(), "docs/storage-design.md") || !strings.Contains(err.Error(), "required") {
				t.Fatalf("message should say DSN is required and point to docs: %v", err)
			}
		}
	})
}

func TestMaskDSN(t *testing.T) {
	cases := []struct{ in, want string }{
		{"postgres://u:s3cret@h:5432/db?sslmode=disable", "postgres://u:****@h:5432/db?sslmode=disable"},
		{"postgresql://u:p%40ss@h/db", "postgresql://u:****@h/db"},
		{"postgres://u:a@b@h/db", "postgres://u:****@h/db"},
		{"postgres://h/db?user=u&password=s3cret&sslmode=disable", "postgres://h/db?user=u&password=****&sslmode=disable"},
		{"host=h user=u password=s3cret dbname=db", "host=h user=u password=**** dbname=db"},
		{"host=h user=u password='my secret' dbname=db", "host=h user=u password=**** dbname=db"},
		{"postgres://u@h/db", "postgres://u@h/db"},
		{"postgres://h/db", "postgres://h/db"},
		{"host=h user=u dbname=db", "host=h user=u dbname=db"},
		{"", ""},
	}
	for _, c := range cases {
		if got := MaskDSN(c.in); got != c.want {
			t.Errorf("MaskDSN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOpenErrorsDoNotLeakPassword(t *testing.T) {
	const pw = "sup3r-s3cret-pw"
	dsns := []string{
		"postgres://u:" + pw + "@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		"host=127.0.0.1 port=1 user=u password=" + pw + " dbname=db sslmode=disable connect_timeout=2",
		"postgres://u:" + pw + "@no-such-host.invalid/db?sslmode=disable&connect_timeout=2",
		"postgres://u:" + pw + "@127.0.0.1:1/db?sslmode=bogus",
	}
	for _, dsn := range dsns {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		s, err := Open(ctx, dsn)
		cancel()
		if err == nil {
			_ = s.Close()
			t.Fatalf("Open(%q) unexpectedly succeeded", MaskDSN(dsn))
		}
		if strings.Contains(err.Error(), pw) {
			t.Errorf("error leaks password: %v", err)
		}
	}
}
