package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

type releaseTestTransport func(*http.Request) (*http.Response, error)

func (f releaseTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func seedReleaseCredentials(t *testing.T, contents, envToken string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GH_TOKEN", envToken)
	if contents == "" {
		return
	}
	path := config.CredentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFetchLatestReleaseCredentials(t *testing.T) {
	for _, tc := range []struct{ name, stored, env, auth string }{
		{"anonymous", "", "", ""},
		{"stored token", "gh_token: stored-test-token\n", "", "Bearer stored-test-token"},
		{"environment overrides file", "gh_token: stored-test-token\n", "env-test-token", "Bearer env-test-token"},
		{"environment without file", "", "env-test-token", "Bearer env-test-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedReleaseCredentials(t, tc.stored, tc.env)
			client := &http.Client{Transport: releaseTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.String() != "https://api.github.com/repos/"+githubRepo+"/releases/latest" {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
				}
				if req.Header.Get("Authorization") != tc.auth {
					t.Error("unexpected authorization header")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.73.0","assets":[]}`)), Header: http.Header{}}, nil
			})}
			release, err := fetchLatestRelease(client)
			if err != nil || release.TagName != "v0.73.0" {
				t.Fatalf("unexpected release: %+v, %v", release, err)
			}
		})
	}
}

func TestFetchLatestReleaseErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, remaining, want string
		status                      int
	}{
		{"rate limited", `{"message":"API rate limit exceeded; secret-test-token"}`, "0", "GitHub API rate limit exceeded (HTTP 403)", 403},
		{"too many requests", `{"message":"secret-test-token"}`, "", "GitHub API rate limit exceeded (HTTP 429)", 429},
		{"invalid token", `{"message":"secret-test-token"}`, "", "HTTP 401", 401},
		{"forbidden", `{"message":"secret-test-token"}`, "", "HTTP 403", 403},
		{"server error", `{"message":"secret-test-token"}`, "", "HTTP 500", 500},
		{"invalid JSON", `not json`, "", "decode latest GitHub release", 200},
		{"missing tag", `{}`, "", "missing tag_name", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedReleaseCredentials(t, "", "secret-test-token")
			client := &http.Client{Transport: releaseTestTransport(func(req *http.Request) (*http.Response, error) {
				header := http.Header{}
				header.Set("X-RateLimit-Remaining", tc.remaining)
				return &http.Response{StatusCode: tc.status, Header: header, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			_, err := fetchLatestRelease(client)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "secret-test-token") || strings.Contains(err.Error(), "no binary asset") {
				t.Fatal("API error leaked response data or was misreported as a missing asset")
			}
		})
	}
}

func TestReleaseLookupCallersUseToken(t *testing.T) {
	seedReleaseCredentials(t, "gh_token: stored-test-token\n", "")
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	calls := 0
	http.DefaultTransport = releaseTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != "Bearer stored-test-token" {
			t.Error("release lookup did not use configured token")
		}
		body, err := json.Marshal(map[string]any{"tag_name": "v0.73.0", "assets": []map[string]string{{"name": binaryAssetName(), "browser_download_url": "https://github.com/release/binary"}}})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	tag, url, err := latestReleaseAsset()
	if err != nil || tag != "v0.73.0" || url != "https://github.com/release/binary" {
		t.Fatalf("unexpected asset: %q %q %v", tag, url, err)
	}
	if got := FetchLatestTag(); got != "v0.73.0" {
		t.Fatalf("background version check returned %q", got)
	}
	if calls != 2 {
		t.Fatalf("expected two API requests, got %d", calls)
	}
}

// IsNewerVersion is load-bearing for `clawflow update`'s "skip if already at
// latest" short-circuit — one wrong case and users either re-download the
// same binary repeatedly or get stuck on an old version. These tests lock
// the semantics in.
func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		// Happy path — a real upgrade
		{"older minor", "v0.37.0", "v0.38.0", true},
		{"older patch", "v0.38.0", "v0.38.1", true},
		{"older major", "v0.38.2", "v1.0.0", true},

		// Equal — do not upgrade
		{"exact match", "v0.38.1", "v0.38.1", false},
		{"exact match no-prefix", "0.38.1", "0.38.1", false},

		// Current is ahead — do not downgrade
		{"ahead minor", "v0.38.0", "v0.37.0", false},
		{"ahead patch", "v0.38.1", "v0.38.0", false},
		{"ahead major", "v1.0.0", "v0.99.0", false},

		// Describe builds retain their distance from the base tag.
		{"git describe same tag", "v0.38.1-5-gabc123", "v0.38.1", false},
		{"git describe older tag", "v0.37.0-5-gabc123", "v0.38.0", true},

		// Unknown builds fail closed.
		{"dev build", "dev", "v0.38.1", false},
		{"empty current", "", "v0.38.1", false},

		// Both unparseable → equal → not newer
		{"both dev", "dev", "dev", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsNewerVersion(c.current, c.latest)
			if got != c.want {
				t.Errorf("IsNewerVersion(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
			}
		})
	}
}

// resolveBinaryPath underpins the issue #230 fix: `clawflow update` must
// replace the binary at its REAL location (following symlinks), not a symlink
// pointing at it, so the update stays on PATH and doesn't leave a stale copy.
func TestResolveBinaryPath(t *testing.T) {
	dir := t.TempDir()

	real := filepath.Join(dir, "clawflow")
	if err := os.WriteFile(real, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The canonical path of the real file — on macOS the temp dir itself sits
	// under a /tmp -> /private/tmp symlink, so normalize via EvalSymlinks
	// rather than comparing against the raw path.
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}

	// A plain regular file resolves to its canonical self.
	if got := resolveBinaryPath(real); got != want {
		t.Errorf("resolveBinaryPath(regular) = %q, want %q", got, want)
	}

	// A symlink resolves to its target — this is the /usr/local/bin/clawflow
	// -> ~/.clawflow/bin/clawflow case we must follow on update.
	link := filepath.Join(dir, "clawflow-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if resolved := resolveBinaryPath(link); resolved != want {
		t.Errorf("resolveBinaryPath(symlink) = %q, want %q", resolved, want)
	}

	// An unresolvable (nonexistent) path falls back to the raw input rather
	// than erroring out.
	missing := filepath.Join(dir, "does-not-exist")
	if resolved := resolveBinaryPath(missing); resolved != missing {
		t.Errorf("resolveBinaryPath(missing) = %q, want %q", resolved, missing)
	}
}

// permissionDeniedErr must surface the destination path and an actionable
// sudo hint so users who installed into /usr/local/bin know how to recover.
func TestPermissionDeniedErr(t *testing.T) {
	err := permissionDeniedErr("/usr/local/bin/clawflow", os.ErrPermission)
	msg := err.Error()
	if !strings.Contains(msg, "/usr/local/bin/clawflow") {
		t.Errorf("error message missing destination path: %q", msg)
	}
	if !strings.Contains(msg, "sudo clawflow update") {
		t.Errorf("error message missing sudo hint: %q", msg)
	}
}

func TestDelayedReleaseCannotDowngradeInstalledBinary(t *testing.T) {
	for _, tc := range []struct {
		name, version, channel, wantError string
		explicit, allow, busy             bool
	}{
		{name: "newer release", version: "v3.0.0", channel: "release", wantError: "refusing downgrade"},
		{name: "source switch requires authorization", version: "v1.0.0", channel: "source", wantError: "--release"},
		{name: "downgrade permission does not authorize channel switch", version: "v3.0.0", channel: "source", allow: true, wantError: "--release"},
		{name: "source ancestry uses installed commit", version: "v1.0.0", channel: "source", explicit: true, wantError: "refusing release downgrade"},
		{name: "unknown version", version: "unknown", channel: "release", wantError: "refusing downgrade"},
		{name: "shared lock busy", version: "v1.0.0", channel: "release", busy: true, wantError: "another upgrade"},
		{name: "upgrade", version: "v1.0.0", channel: "release"},
		{name: "explicit downgrade", version: "v3.0.0", channel: "release", allow: true},
		{name: "explicit source downgrade", version: "v3.0.0", channel: "source", explicit: true, allow: true},
		{name: "already installed", version: "v2.0.0", channel: "release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedReleaseCredentials(t, "", "")
			oldVersion, oldChannel, oldTransport := Version, BuildChannel, http.DefaultTransport
			Version, BuildChannel = "v1.0.0", "release"
			t.Cleanup(func() { Version, BuildChannel, http.DefaultTransport = oldVersion, oldChannel, oldTransport })
			dest := filepath.Join(t.TempDir(), "clawflow")
			commit := ""
			if tc.name == "source ancestry uses installed commit" {
				repo := t.TempDir()
				git := func(args ...string) string {
					t.Helper()
					out, err := runSelfUpgradeCmd(context.Background(), repo, "git", args...)
					if err != nil {
						t.Fatal(err)
					}
					return strings.TrimSpace(string(out))
				}
				git("init", "-b", "main")
				git("config", "user.name", "Test")
				git("config", "user.email", "test@example.com")
				git("commit", "--allow-empty", "-m", "release")
				git("tag", "v2.0.0")
				git("commit", "--allow-empty", "-m", "newer source")
				commit = git("rev-parse", "HEAD")
				git("remote", "add", "origin", repo)
				path := filepath.Join(filepath.Dir(config.CredentialsPath()), "install.yaml")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("repo_dir: "+repo+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			installed := "#!/bin/sh\nprintf '%s\\n' '" + tc.version + "' 'channel=" + tc.channel + " commit=" + commit + " branch='\n"
			http.DefaultTransport = releaseTestTransport(func(req *http.Request) (*http.Response, error) {
				body := `{"tag_name":"v2.0.0","assets":[{"name":"clawflow","browser_download_url":"https://example.test/binary"}]}`
				if req.URL.Host == "example.test" {
					// Another updater finished while this stale process downloaded.
					if err := os.WriteFile(dest, []byte(installed), 0755); err != nil {
						t.Fatal(err)
					}
					body = "downloaded release"
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})
			if tc.busy {
				release, ok, err := acquireSelfUpgradeLock()
				if err != nil || !ok {
					t.Fatalf("acquire competing lock: %v", err)
				}
				defer release()
			}
			u := newSelfUpgrader()
			u.Executable = func() (string, error) { return dest, nil }
			u.Run = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
				// The installed identity must be read under the shared source lock.
				release, ok, err := acquireSelfUpgradeLock()
				if ok {
					release()
					t.Fatal("installed identity read without shared lock")
				}
				if err != nil {
					t.Fatal(err)
				}
				return runSelfUpgradeCmd(ctx, dir, name, args...)
			}
			err := u.downloadReleaseBinary(tc.explicit, tc.allow)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q, got %v", tc.wantError, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := "downloaded release"
			if tc.wantError != "" || tc.version == "v2.0.0" {
				want = installed
			}
			got, readErr := os.ReadFile(dest)
			if readErr != nil || string(got) != want {
				t.Fatalf("destination: error=%v content=%q want=%q", readErr, got, want)
			}
			if !tc.busy {
				release, ok, err := acquireSelfUpgradeLock()
				if err != nil || !ok {
					t.Fatalf("upgrade lock leaked: %v", err)
				}
				release()
			}
		})
	}
}
