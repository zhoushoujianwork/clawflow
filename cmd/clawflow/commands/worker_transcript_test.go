package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func transcriptTestAPI(t *testing.T, handler http.HandlerFunc) *workerAPI {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("worker-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(server.Certificate().Raw)
	api, err := newWorkerAPI(workerOptions{Coordinator: server.URL, TokenFile: tokenFile, Fingerprint: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.client.CloseIdleConnections)
	return api
}

func TestWorkerTranscriptSinkUsesLeaseIdentityAndPinnedHTTPS(t *testing.T) {
	var gotPath, gotAuth string
	var got coordinator.TranscriptUpload
	api := transcriptTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	task := &coordinator.Task{ID: "task/with space", FencingToken: 41}
	sink := newWorkerTranscriptSink(api, task)
	page := execution.TranscriptPage{CallerRunID: "call-1", RunID: "run-1", CaptureID: "cap-1", Availability: "available", State: "running",
		CursorIn: "", CursorOut: "c.1", Events: []json.RawMessage{json.RawMessage(`{"seq":1}`)}}
	if err := sink.Deliver(context.Background(), page); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/worker/v1/tasks/task/with space/transcript" && gotPath != "/worker/v1/tasks/task%2Fwith%20space/transcript" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAuth != "Bearer worker-secret" || got.FencingToken != 41 || got.CallerRunID != "call-1" || got.CursorOut != "c.1" || len(got.Events) != 1 {
		t.Fatalf("auth=%q upload=%+v", gotAuth, got)
	}
}

func TestWorkerTranscriptSinkClassifiesFailures(t *testing.T) {
	for status, wantRejected := range map[int]bool{200: false, 409: true, 422: true, 413: true, 400: true, 401: true, 500: false, 503: false} {
		api := transcriptTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{}`)
		})
		err := newWorkerTranscriptSink(api, &coordinator.Task{ID: "t", FencingToken: 1}).Deliver(context.Background(), execution.TranscriptPage{CallerRunID: "c", Availability: "available"})
		if got := errors.Is(err, execution.ErrTranscriptRejected); got != wantRejected {
			t.Errorf("status %d: rejected=%v err=%v", status, got, err)
		}
		if status != 200 && err == nil {
			t.Errorf("status %d: no error", status)
		}
	}
}

func TestWorkerTranscriptSinkDoesNotSpoolAcrossFilesystem(t *testing.T) {
	// The sink holds only task identity and the fence. Content moves over the
	// authenticated request; nothing assumes a path shared with the controller.
	sink := newWorkerTranscriptSink(&workerAPI{}, &coordinator.Task{ID: "t", FencingToken: 3})
	if sink.path != "tasks/t/transcript" || sink.fence != 3 {
		t.Fatalf("sink=%+v", sink)
	}
}
