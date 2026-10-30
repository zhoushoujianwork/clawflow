package acceptance

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLatestPlanForTask(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := contract()
	for i, attempt := range []string{"older", "newer"} {
		r := passing()
		r.AttemptID, r.Plan.AttemptID = attempt, attempt
		r.Plan.Text = "Verify `internal/" + attempt + ".go`"
		r.Plan.Digest = digest(r.Plan.Text)
		r.Plan.CompletedAt = r.Plan.CompletedAt.Add(time.Duration(i) * time.Hour)
		if err := s.SavePlan(c, r.Plan); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.LatestPlanForTask(c.Repository, c.TaskID, c.Requirements)
	if err != nil || p.AttemptID != "newer" {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	for _, query := range [][3]string{
		{"other/repo", c.TaskID, c.Requirements},
		{c.Repository, "other-task", c.Requirements},
		{c.Repository, c.TaskID, "changed requirements"},
	} {
		p, err := s.LatestPlanForTask(query[0], query[1], query[2])
		if err != nil || p.Text != "" {
			t.Fatalf("unrelated plan returned: %+v, %v", p, err)
		}
	}
	// A valid envelope cannot make a stale plan/contract binding applicable.
	r := passing()
	r.Plan.ContractDigest = "wrong"
	if err := s.SaveRecord(r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestPlanForTask(c.Repository, c.TaskID, c.Requirements); err == nil {
		t.Fatal("invalid plan binding must fail closed")
	}
}
