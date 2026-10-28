package operator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func TestDevelopmentNativeResume(t *testing.T) {
	for _, op := range []string{"implement", "repair-ci"} {
		t.Run(op, func(t *testing.T) {
			recorded := false
			opts := RunOptions{Repo: "o/r", SessionKey: "native", ResumePrompt: "timeout; remaining acceptance criteria", OnExecutionResult: func(r execution.Result, err error, resumed bool) {
				recorded = resumed && r.NativeSessionID == "native" && r.Profile == "dev" && r.Backend == "codex"
			}, ExecuteFunc: func(_ context.Context, prompt string, o execution.Options) (execution.Result, error) {
				if !o.Resume || o.SessionKey != "native" || prompt != "timeout; remaining acceptance criteria" || o.PermissionMode != "bypassPermissions" {
					t.Fatalf("request: %q %+v", prompt, o)
				}
				return execution.Result{NativeSessionID: "native", Profile: "dev", Backend: "codex", Text: "done\n<!-- clawflow:outcome=agent-implemented -->"}, nil
			}}
			_, _, err := Run(context.Background(), &Operator{Name: op}, &Subject{Number: 1}, newFakeVCS(), opts)
			if err != nil || !recorded {
				t.Fatalf("recorded=%v err=%v", recorded, err)
			}
		})
	}
}

func TestDevelopmentResumeFallbackOnce(t *testing.T) {
	for _, kind := range []string{"unknown_session", "session_expired", "session_busy", "ownership_write", "backend_mismatch", "profile_mismatch", "resume_rejected", "transport"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			var logged string
			opts := RunOptions{SessionKey: "old", ResumePrompt: "delta", OnSessionResume: func(outcome, reason string) { logged = outcome + ":" + reason }, OnExecutionResult: func(r execution.Result, err error, resumed bool) {
				if resumed || r.NativeSessionID != "fresh" {
					t.Fatalf("incorrect final identity: %+v", r)
				}
			}, ExecuteFunc: func(_ context.Context, prompt string, o execution.Options) (execution.Result, error) {
				calls++
				if calls == 1 {
					if !o.Resume || prompt != "delta" {
						t.Fatal("missing resume")
					}
					return execution.Result{}, &execution.Error{Kind: kind}
				}
				if calls != 2 || o.Resume || o.SessionKey != "" || prompt != "full prompt and worktree context" {
					t.Fatal("invalid fallback")
				}
				return execution.Result{NativeSessionID: "fresh"}, &execution.Error{Kind: "transport"}
			}}
			_, err := runDevelopment(context.Background(), "full prompt and worktree context", opts)
			if err == nil || calls != 2 || logged != "fresh:"+kind {
				t.Fatalf("calls=%d log=%s err=%v", calls, logged, err)
			}
		})
	}
}

func TestDevelopmentResumeDoesNotReplayWorkOrCancellation(t *testing.T) {
	for _, mode := range []string{"output", "progress", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			opts := RunOptions{SessionKey: "old", ResumePrompt: "delta", ExecuteFunc: func(_ context.Context, _ string, o execution.Options) (execution.Result, error) {
				calls++
				result := execution.Result{}
				if mode == "output" {
					result.Text = "partial work"
				}
				if mode == "progress" {
					_ = json.NewEncoder(o.Events).Encode(execution.Event{Type: "progress"})
				}
				if mode == "cancelled" {
					cancel()
				}
				return result, context.DeadlineExceeded
			}}
			_, err := runDevelopment(ctx, "full", opts)
			if calls != 1 || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestFreshDevelopmentRecordsIdentity(t *testing.T) {
	recorded := false
	opts := RunOptions{OnExecutionResult: func(r execution.Result, err error, resumed bool) { recorded = !resumed && r.NativeSessionID == "new" }, ExecuteFunc: func(_ context.Context, p string, o execution.Options) (execution.Result, error) {
		if o.Resume || o.SessionKey != "" || !strings.Contains(p, "full") {
			t.Fatal("fresh request changed")
		}
		return execution.Result{NativeSessionID: "new"}, context.DeadlineExceeded
	}}
	_, _ = runDevelopment(context.Background(), "full", opts)
	if !recorded {
		t.Fatal("timeout identity lost")
	}
}

func TestDevelopmentResumeProgressBeforeTransportFailure(t *testing.T) {
	for _, tc := range []struct {
		nativeType string
		wantCalls  int
	}{
		{"thread.started", 2},
		{"turn.started", 2},
		{"item.started", 1},
		{"item.completed", 1},
		{"unknown", 1},
		{"", 1},
	} {
		t.Run(tc.nativeType, func(t *testing.T) {
			calls, resets := 0, 0
			var logged string
			var forwarded strings.Builder
			opts := RunOptions{
				SessionKey: "old", ResumePrompt: "delta", EventWriter: &forwarded,
				PrepareFreshSession: func() error { resets++; return nil },
				OnSessionResume:     func(outcome, reason string) { logged = outcome + ":" + reason },
				ExecuteFunc: func(_ context.Context, prompt string, o execution.Options) (execution.Result, error) {
					calls++
					if calls == 1 {
						if !o.Resume || o.SessionKey != "old" || prompt != "delta" {
							t.Fatal("invalid resume request")
						}
						if err := json.NewEncoder(o.Events).Encode(execution.Event{Type: "progress", NativeType: tc.nativeType}); err != nil {
							t.Fatal(err)
						}
					} else if calls != 2 || o.Resume || o.SessionKey != "" || prompt != "full prompt and worktree context" {
						t.Fatal("invalid fresh fallback request")
					}
					return execution.Result{}, &execution.Error{Kind: "transport"}
				},
			}
			_, err := runDevelopment(context.Background(), "full prompt and worktree context", opts)
			if !execution.IsKind(err, "transport") || calls != tc.wantCalls || resets != tc.wantCalls-1 {
				t.Fatalf("calls=%d resets=%d err=%v", calls, resets, err)
			}
			wantLog := "resumed:eligible"
			if tc.wantCalls == 2 {
				wantLog = "fresh:transport"
			}
			if logged != wantLog || !strings.Contains(forwarded.String(), `"type":"progress"`) {
				t.Fatalf("log=%q forwarded=%q", logged, forwarded.String())
			}
		})
	}
}
