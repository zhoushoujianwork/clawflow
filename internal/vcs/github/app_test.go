package github

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestAppRequestsAndIdentityBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Settings: config.Settings{GitHubApp: config.GitHubAppSettings{AppID: 12, PrivateKeyPath: keyPath}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	var comment string
	posts, updates := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/installation":
			fmt.Fprint(w, `{"id":31,"app_slug":"clawflow-test","repository_selection":"selected","permissions":{"issues":"write"}}`)
			return
		case "/repos/o/outside/installation":
			w.WriteHeader(404)
			return
		case "/app/installations/31/access_tokens":
			json.NewEncoder(w).Encode(map[string]any{"token": "app-test-secret", "expires_at": time.Now().Add(time.Hour)})
			return
		}
		want := "Bearer app-test-secret"
		if r.URL.Path == "/repos/o/outside/issues" {
			want = "Bearer personal-test-secret"
		}
		if r.Header.Get("Authorization") != want {
			t.Error("incorrect request identity")
		}
		if r.URL.Path == "/user" {
			t.Error("installation must not call user endpoint")
		}
		if r.URL.Path == "/repos/o/r/issues/1/comments" {
			if r.Method == "GET" {
				if comment == "" {
					fmt.Fprint(w, `[]`)
				} else {
					json.NewEncoder(w).Encode([]any{map[string]any{"id": 7, "body": comment, "user": map[string]string{"login": "clawflow-test[bot]"}}})
				}
				return
			}
			posts++
			var input struct{ Body string }
			json.NewDecoder(r.Body).Decode(&input)
			comment = input.Body
			fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Path == "/repos/o/r/issues/comments/7" {
			updates++
			fmt.Fprint(w, `{}`)
			return
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "3999")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	root := t.TempDir()
	client := New("personal-test-secret", server.URL)
	client.SetRepository("o/r")
	client.UseCacheDir(root)
	if _, _, err := client.doOnce("GET", "/repos/o/r/issues", nil); err != nil {
		t.Fatal(err)
	}
	if client.Budget().Remaining != 3999 {
		t.Fatal("app budget was not observed")
	}
	another := New("different-personal-token", server.URL)
	another.SetRepository("o/r")
	another.UseCacheDir(root)
	if another.Budget().Remaining != 3999 {
		t.Fatal("installation budget not shared across clients")
	}
	if _, _, err := client.doOnce("POST", "/graphql", map[string]string{"query": "query { viewer { login } }"}); err != nil {
		t.Fatal(err)
	}
	report := vcs.AcceptanceComment{Attempt: "a", Contract: "c", Head: "head", Started: time.Now(), Completed: time.Now().Add(time.Minute), Body: "first"}
	if err := client.PublishAcceptanceComment("o/r", 1, report); err != nil {
		t.Fatal(err)
	}
	report.Attempt = "b"
	report.Started = report.Started.Add(2 * time.Minute)
	report.Completed = report.Completed.Add(2 * time.Minute)
	report.Body = "second"
	if err := client.PublishAcceptanceComment("o/r", 1, report); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || updates != 1 {
		t.Fatal("app-owned acceptance comment was not updated")
	}
	outside := New("personal-test-secret", server.URL)
	outside.SetRepository("o/outside")
	outside.UseCacheDir(root)
	if outside.Budget().Remaining != 0 {
		t.Fatal("personal identity inherited app budget")
	}
	if _, _, err := outside.doOnce("GET", "/repos/o/outside/issues", nil); err != nil {
		t.Fatal(err)
	}
}
