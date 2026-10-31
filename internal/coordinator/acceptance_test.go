package coordinator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAcceptanceRouting(t *testing.T) {
	for _, policy := range []string{"", "different", "any"} {
		t.Run("policy="+policy, func(t *testing.T) {
			task := Task{Kind: "acceptance", Tags: []string{"github", "review"}}
			task.Payload, _ = json.Marshal(AcceptanceRouting{AcceptanceWorker: policy, DevelopmentWorker: "dev"})
			good := Worker{ID: "reviewer", Roles: []string{"acceptance"}, Tags: []string{"github", "review"}}
			for _, tc := range []struct {
				name   string
				worker Worker
				want   bool
			}{
				{"reviewer", good, true},
				{"developer", Worker{ID: "dev", Roles: good.Roles, Tags: good.Tags}, false},
				{"operator", Worker{ID: "other", Roles: []string{"operator"}, Tags: good.Tags}, false},
				{"missing tag", Worker{ID: "other", Roles: good.Roles, Tags: []string{"github"}}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if acceptanceAllowed(task, tc.worker) != tc.want {
						t.Fatal("incorrect routing")
					}
				})
			}
			task.Payload, _ = json.Marshal(AcceptanceRouting{AcceptanceWorker: policy})
			if acceptanceAllowed(task, good) {
				t.Fatal("missing developer identity allowed review")
			}
		})
	}
}

func TestClaimAcceptanceRoleAndIndependence(t *testing.T) {
	for _, policy := range []string{"", "different", "any"} {
		t.Run("policy="+policy, func(t *testing.T) {
			s, db, _ := setup(t)
			defer db.Close()
			p, _ := json.Marshal(AcceptanceRouting{AcceptanceWorker: policy, DevelopmentWorker: "dev"})
			queued, err := s.Enqueue(ctx, Task{Kind: "acceptance", Repo: "o/r", PR: 1, Operator: "acceptance", Tags: []string{"review"}, Payload: p})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RegisterWorker(ctx, Worker{ID: "dev", MaxParallel: 1, Roles: []string{"acceptance"}, Tags: []string{"review"}}); err != nil {
				t.Fatal(err)
			}
			secret, err := s.CreateToken(ctx, "dev")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.VerifyToken(ctx, secret); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				if _, err := s.Claim(ctx, "dev"); err != ErrNoTask {
					t.Fatalf("developer claim: %v", err)
				}
			}
			saved, err := s.Task(ctx, queued.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.State != "pending" || saved.Attempt != 0 || saved.FencingToken != 0 || saved.LeaseOwner != "" {
				t.Fatalf("waiting task mutated: %+v", saved)
			}
			var attempts int64
			if err := db.DB().Model(&TaskAttempt{}).Where("task_id = ?", queued.ID).Count(&attempts).Error; err != nil || attempts != 0 {
				t.Fatalf("attempts=%d: %v", attempts, err)
			}
			board, err := s.Board(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(board.Tasks) != 1 || board.Tasks[0].WaitingReason != "waiting for an independent reviewer" {
				t.Fatalf("board: %+v", board)
			}
			data, _ := json.Marshal(board)
			if strings.Contains(string(data), "development_worker") || strings.Contains(string(data), "payload") {
				t.Fatal("board exposed routing payload")
			}
			for _, w := range []Worker{
				{ID: "operator", MaxParallel: 1, Roles: []string{"operator"}, Tags: []string{"review"}},
				{ID: "wrong-tag", MaxParallel: 1, Roles: []string{"acceptance"}, Tags: []string{"other"}},
				{ID: "reviewer", MaxParallel: 1, Roles: []string{"acceptance"}, Tags: []string{"review"}},
			} {
				if err := s.RegisterWorker(ctx, w); err != nil {
					t.Fatal(err)
				}
				task, err := s.Claim(ctx, w.ID)
				if w.ID != "reviewer" {
					if err != ErrNoTask {
						t.Fatalf("ineligible %s claim: %v", w.ID, err)
					}
				} else if err != nil || task.ID != queued.ID {
					t.Fatalf("reviewer claim: %v", err)
				}
			}
		})
	}
}

func TestDefaultAcceptancePolicyIsIndependent(t *testing.T) {
	for _, policy := range []string{"", "any", "different", "invalid"} {
		payload, _ := json.Marshal(AcceptanceRouting{AcceptanceWorker: policy, DevelopmentWorker: "dev"})
		task := Task{Kind: "acceptance", Payload: payload}
		err := resolveAcceptancePolicy(&task)
		if policy == "invalid" {
			if err != ErrInvalid {
				t.Fatalf("invalid policy accepted: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var p AcceptanceRouting
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			t.Fatal(err)
		}
		want := policy
		if want == "" {
			want = "different"
		}
		if p.AcceptanceWorker != want || p.DevelopmentWorker != "dev" {
			t.Fatalf("policy %q: %+v", policy, p)
		}
	}
}

func TestDevelopmentWorkerAndSupersededAcceptance(t *testing.T) {
	s, db, _ := setup(t)
	defer db.Close()
	enqueue(t, s, 1)
	register(t, s, "dev", 1)
	task := claim(t, s, "dev")
	result := json.RawMessage(`{"status":"success","head_sha":"head-a","pr_number":5}`)
	if err := s.Complete(ctx, task.ID, "dev", task.FencingToken, result); err != nil {
		t.Fatal(err)
	}
	dev, err := s.LatestDevelopment(ctx, "owner/repo", 1, 5, "head-a")
	if err != nil || dev.LeaseOwner != "dev" {
		t.Fatalf("development lookup: %+v %v", dev, err)
	}
	if _, err := s.LatestDevelopment(ctx, "owner/repo", 1, 5, "head-b"); err == nil {
		t.Fatal("wrong head matched")
	}
	old := json.RawMessage(`{"head_sha":"head-a"}`)
	p := json.RawMessage(`{"acceptance_worker":"different","development_worker":"dev","acceptance":{"head_sha":"head-a"}}`)
	queued, err := s.Enqueue(ctx, Task{Kind: "acceptance", Repo: "owner/repo", Issue: 1, PR: 5, Operator: "acceptance", Payload: p})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(ctx, Worker{ID: "reviewer", MaxParallel: 1, RunUntagged: true, Roles: []string{"acceptance"}}); err != nil {
		t.Fatal(err)
	}
	review := claim(t, s, "reviewer")
	if err := s.CancelSupersededAcceptance(ctx, "owner/repo", 5, old); err != nil {
		t.Fatal(err)
	}
	still, err := s.Task(ctx, queued.ID)
	if err != nil || still.State != "leased" {
		t.Fatal("unchanged contract cancelled")
	}
	if err := s.CancelSupersededAcceptance(ctx, "owner/repo", 5, json.RawMessage(`{"head_sha":"head-b"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, review.ID, "reviewer", review.FencingToken, nil); err != ErrStaleLease {
		t.Fatalf("obsolete lease accepted: %v", err)
	}
}

func TestLatestDevelopmentRetainsTestingEvidence(t *testing.T) {
	s, db, _ := setup(t)
	defer db.Close()
	payload := json.RawMessage(`{"testing":{"sop_required":true,"sop_digest":"approved-revision"}}`)
	_, err := s.Enqueue(ctx, Task{Repo: "owner/repo", Issue: 42, PR: 11, Kind: "repair", Operator: "repair-ci", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	register(t, s, "developer", 1)
	task := claim(t, s, "developer")
	result := json.RawMessage(`{"status":"success","head_sha":"submitted-head","pr_number":11,"sop_digest":"actually-read-revision"}`)
	if err := s.Complete(ctx, task.ID, "developer", task.FencingToken, result); err != nil {
		t.Fatal(err)
	}
	dev, err := s.LatestDevelopment(ctx, "owner/repo", 42, 11, "submitted-head")
	if err != nil {
		t.Fatal(err)
	}
	var declaration struct {
		Testing struct {
			Required bool   `json:"sop_required"`
			Digest   string `json:"sop_digest"`
		} `json:"testing"`
	}
	var proof struct {
		Digest string `json:"sop_digest"`
	}
	if err := json.Unmarshal(dev.Payload, &declaration); err != nil {
		t.Fatalf("acceptance lost development testing declaration: %v", err)
	}
	if err := json.Unmarshal(dev.Result, &proof); err != nil {
		t.Fatalf("acceptance lost development testing result: %v", err)
	}
	if !declaration.Testing.Required || declaration.Testing.Digest != "approved-revision" || proof.Digest != "actually-read-revision" || dev.LeaseOwner != "developer" {
		t.Fatalf("incomplete development baseline: %+v, %+v, worker=%q", declaration, proof, dev.LeaseOwner)
	}
}

func TestDefaultAcceptancePolicyExcludesRevokedWorkers(t *testing.T) {
	s, db, _ := setup(t)
	defer db.Close()
	for _, id := range []string{"dev", "reviewer"} {
		if err := s.RegisterWorker(ctx, Worker{ID: id, MaxParallel: 1, RunUntagged: true, Roles: []string{"acceptance"}}); err != nil {
			t.Fatal(err)
		}
		secret, err := s.CreateToken(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.VerifyToken(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.Enqueue(ctx, Task{Kind: "acceptance", Repo: "o/r", PR: 1, Operator: "acceptance", Payload: json.RawMessage(`{"development_worker":"dev"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "dev"); err != ErrNoTask {
		t.Fatalf("default did not exclude developer: %v", err)
	}
	if err := s.RevokeToken(ctx, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "dev"); err != ErrNoTask {
		t.Fatalf("revocation allowed developer to claim: %v", err)
	}
	board, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Tasks) != 1 || board.Tasks[0].WaitingReason != "waiting for an independent reviewer" {
		t.Fatalf("board: %+v", board)
	}
}
