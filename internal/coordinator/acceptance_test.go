package coordinator

import (
	"encoding/json"
	"testing"
)

func TestAcceptanceRouting(t *testing.T) {
	tsk := Task{Kind: "acceptance", Tags: []string{"github", "review"}}
	tsk.Payload, _ = json.Marshal(AcceptanceRouting{AcceptanceWorker: "different", DevelopmentWorker: "dev"})
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
			if acceptanceAllowed(tsk, tc.worker) != tc.want {
				t.Fatal("incorrect routing")
			}
		})
	}
	tsk.Payload, _ = json.Marshal(AcceptanceRouting{AcceptanceWorker: "any", DevelopmentWorker: "dev"})
	good.ID = "dev"
	if !acceptanceAllowed(tsk, good) {
		t.Fatal("explicit any policy rejected")
	}
}

func TestClaimAcceptanceRoleAndIndependence(t *testing.T) {
	s, db, _ := setup(t)
	defer db.Close()
	p, _ := json.Marshal(AcceptanceRouting{AcceptanceWorker: "different", DevelopmentWorker: "dev"})
	_, err := s.Enqueue(ctx, Task{Kind: "acceptance", Repo: "o/r", PR: 1, Operator: "acceptance", Tags: []string{"review"}, Payload: p})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"dev", "reviewer"} {
		if err := s.RegisterWorker(ctx, Worker{ID: id, MaxParallel: 1, Roles: []string{"acceptance"}, Tags: []string{"review"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Claim(ctx, "dev"); err != ErrNoTask {
		t.Fatalf("developer claim: %v", err)
	}
	if task, err := s.Claim(ctx, "reviewer"); err != nil || task.Kind != "acceptance" {
		t.Fatalf("reviewer claim: %v", err)
	}
}

func TestDefaultAcceptancePolicyCountsEligibleWorkers(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		task := Task{Kind: "acceptance", Tags: []string{"review"}, Payload: json.RawMessage(`{"development_worker":"dev"}`)}
		live := []Worker{{ID: "wrong-role", Roles: []string{"operator"}, Tags: []string{"review"}}, {ID: "wrong-tag", Roles: []string{"acceptance"}, Tags: []string{"other"}}}
		for i := 0; i < count; i++ {
			live = append(live, Worker{Roles: []string{"acceptance"}, Tags: []string{"review"}})
		}
		if err := resolveAcceptancePolicy(&task, live); err != nil {
			t.Fatal(err)
		}
		var p AcceptanceRouting
		_ = json.Unmarshal(task.Payload, &p)
		want := "any"
		if count > 1 {
			want = "different"
		}
		if p.AcceptanceWorker != want || p.DevelopmentWorker != "dev" {
			t.Fatalf("%d: %+v", count, p)
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
	task, err := s.Claim(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	var p AcceptanceRouting
	_ = json.Unmarshal(task.Payload, &p)
	if p.AcceptanceWorker != "any" {
		t.Fatal("revoked worker counted as live")
	}
}
