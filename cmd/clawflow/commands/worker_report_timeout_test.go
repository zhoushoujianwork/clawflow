package commands

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type reportDeadlineTransport func(*http.Request) (*http.Response, error)

func (f reportDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkerReportDeadlineDoesNotChangeHeartbeatTimeout(t *testing.T) {
	for _, tc := range []struct {
		path string
		want time.Duration
	}{
		{"tasks/task/complete", 90 * time.Second},
		{"tasks/task/fail", 90 * time.Second},
		{"register", 10 * time.Second},
		{"tasks/task/heartbeat", 10 * time.Second},
	} {
		t.Run(tc.path, func(t *testing.T) {
			client := &http.Client{Timeout: 10 * time.Second}
			client.Transport = reportDeadlineTransport(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				remaining := time.Until(deadline)
				if !ok || remaining < tc.want-time.Second || remaining > tc.want {
					t.Errorf("deadline remaining %v, want %v", remaining, tc.want)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
			})
			api := &workerAPI{client: client, base: "https://controller", token: "fixture"}
			if err := api.postOnce(context.Background(), tc.path, struct{}{}, nil); err != nil {
				t.Fatal(err)
			}
			if client.Timeout != 10*time.Second {
				t.Fatal("report changed shared client timeout")
			}
		})
	}
}

func TestWorkerReportPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api := &workerAPI{base: "https://controller", token: "fixture", client: &http.Client{Timeout: 10 * time.Second, Transport: reportDeadlineTransport(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})}}
	if err := api.postOnce(ctx, "tasks/task/complete", struct{}{}, nil); err == nil {
		t.Fatal("cancelled caller accepted")
	}
}
