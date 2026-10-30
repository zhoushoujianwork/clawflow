package gitlab

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPRCommentIntentReconciliation(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/merge_requests/7/notes") {
			t.Errorf("wrong target: %s", r.URL.Path)
			http.Error(w, "wrong target", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			if writes == 0 {
				_, _ = w.Write([]byte(`[]`))
			} else {
				_, _ = w.Write([]byte(`[{"body":"hello <!-- clawflow:run-id=effect-stable -->"}]`))
			}
		case "POST":
			writes++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	client := New("test-token", server.URL)
	for i := 0; i < 2; i++ {
		if err := client.PostPRCommentIdempotent("o/r", 7, "hello", "effect-stable"); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 1 {
		t.Fatalf("logical comments=%d", writes)
	}
}
