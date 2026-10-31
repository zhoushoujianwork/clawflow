package coordinator

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRetireClosedAcceptancePreservesAuthority(t *testing.T) {
	for _, mode := range []string{"late", "wrong-owner", "wrong-epoch", "accepted", "other-kind"} {
		t.Run(mode, func(t *testing.T) {
			s, db, _ := setup(t)
			defer db.Close()
			payload, _ := json.Marshal(AcceptanceRouting{DevelopmentWorker: "dev", AcceptanceWorker: "different"})
			queued, err := s.Enqueue(ctx, Task{Repo: "o/r", PR: 1, Kind: "acceptance", Operator: "acceptance", Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RegisterWorker(ctx, Worker{ID: "review", Roles: []string{"acceptance"}, RunUntagged: true, MaxParallel: 1}); err != nil {
				t.Fatal(err)
			}
			task, err := s.Claim(ctx, "review")
			if err != nil {
				t.Fatal(err)
			}
			owner, epoch := "review", task.FencingToken
			switch mode {
			case "wrong-owner":
				owner = "another"
			case "wrong-epoch":
				epoch--
			case "accepted":
				if err := s.AcceptResult(ctx, task.ID, owner, epoch, json.RawMessage(`{"status":"success"}`)); err != nil {
					t.Fatal(err)
				}
			case "other-kind":
				if err := s.db.Model(&Task{}).Where("id = ?", task.ID).Update("kind", "repair").Error; err != nil {
					t.Fatal(err)
				}
			}
			err = s.RetireClosedAcceptance(ctx, queued.ID, owner, epoch)
			current, readErr := s.Task(ctx, queued.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "late" {
				if err != nil || current.State != "cancelled" || current.FencingToken != task.FencingToken+1 || current.LeaseExpiresAt != nil {
					t.Fatalf("not retired: %+v %v", current, err)
				}
				if err := s.RetireClosedAcceptance(ctx, queued.ID, owner, epoch); !errors.Is(err, ErrStaleLease) {
					t.Fatalf("second retirement changed authority: %v", err)
				}
			} else if !errors.Is(err, ErrStaleLease) || current.State != "leased" || current.FencingToken != task.FencingToken {
				t.Fatalf("unrelated authority changed: %+v %v", current, err)
			}
			if mode == "accepted" && string(current.Result) != `{"status": "success"}` && string(current.Result) != `{"status":"success"}` {
				t.Fatalf("accepted report changed: %s", current.Result)
			}
		})
	}
}
