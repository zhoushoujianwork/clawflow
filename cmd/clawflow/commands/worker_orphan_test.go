package commands

import (
	"context"
	"errors"
	"testing"

	offload "github.com/zhoushoujianwork/offload/pkg/execution"
)

func TestAbandonOrphanedRunMapsReclaimOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		outcome offload.ReclaimOutcome
		err     error
		want    AbandonState
		wantErr error
	}{
		{name: "terminated", outcome: offload.ReclaimTerminated, want: AbandonEnded},
		{name: "already ended", outcome: offload.ReclaimAlreadyEnded, want: AbandonAlreadyEnded},
		{name: "unconfirmed", outcome: offload.ReclaimUnconfirmed, want: AbandonUnknown},
		{name: "still owned", outcome: offload.ReclaimUnconfirmed, err: offload.ErrRunStillOwned, want: AbandonUnknown, wantErr: offload.ErrRunStillOwned},
		{name: "not found", outcome: offload.ReclaimUnconfirmed, err: offload.ErrRunNotFound, want: AbandonUnknown, wantErr: offload.ErrRunNotFound},
		{name: "start unrecorded", outcome: offload.ReclaimUnconfirmed, err: offload.ErrRunStartUnrecorded, want: AbandonUnknown, wantErr: offload.ErrRunStartUnrecorded},
		{name: "error despite success outcome", outcome: offload.ReclaimTerminated, err: errors.New("reclaim failed"), want: AbandonUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := reclaimOffloadRun
			reclaimOffloadRun = func(context.Context, string) (offload.ReclaimOutcome, error) { return tt.outcome, tt.err }
			defer func() { reclaimOffloadRun = old }()
			got, err := abandonOrphanedRun(context.Background(), "run-id")
			if got != tt.want {
				t.Fatalf("state=%q err=%v", got, err)
			}
			if tt.want == AbandonUnknown {
				if err == nil {
					t.Fatal("unknown state lost its visible reason")
				}
			} else if err != nil {
				t.Fatalf("confirmed state returned error: %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error %v does not preserve %v", err, tt.wantErr)
			}
		})
	}
}

func TestAbandonOrphanedRunRejectsEmptyRunID(t *testing.T) {
	old := reclaimOffloadRun
	called := false
	reclaimOffloadRun = func(context.Context, string) (offload.ReclaimOutcome, error) {
		called = true
		return offload.ReclaimAlreadyEnded, nil
	}
	defer func() { reclaimOffloadRun = old }()
	state, err := abandonOrphanedRun(context.Background(), "")
	if state != AbandonUnknown || err == nil || called {
		t.Fatalf("state=%q err=%v called=%v", state, err, called)
	}
}
