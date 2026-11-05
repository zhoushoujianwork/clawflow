package execution

import (
	"context"
	"testing"
	"time"
)

func TestProgressAge(t *testing.T) {
	p := &Progress{}
	now := time.Now()
	if age := p.AgeSeconds(now.Add(time.Hour)); age != 0 {
		t.Fatalf("no run active: %d", age)
	}
	p.begin()
	if age := p.AgeSeconds(time.Now().Add(time.Hour)); age < 3599 {
		t.Fatalf("active run: %d", age)
	}
	if age := p.AgeSeconds(now.Add(-time.Hour)); age != 0 {
		t.Fatalf("clock moved backwards: %d", age)
	}
	p.event()
	if age := p.AgeSeconds(time.Now()); age != 0 {
		t.Fatalf("after event: %d", age)
	}
	p.end()
	if age := p.AgeSeconds(time.Now().Add(time.Hour)); age != 0 {
		t.Fatalf("between runs: %d", age)
	}
}

func TestProgressContextIsOptional(t *testing.T) {
	var missing *Progress
	missing.begin()
	missing.event()
	missing.end()
	p := &Progress{}
	if ProgressFrom(WithProgress(context.Background(), p)) != p || ProgressFrom(context.Background()) != nil {
		t.Fatal("context lookup")
	}
}

func TestRunReportsEventsToProgress(t *testing.T) {
	c, req := fixture(t)
	p := &Progress{}
	observed := 0
	_, err := Run(WithProgress(context.Background(), p), c, req, nil, nil, func(Observation) {
		observed++
		// Only an active run ages; between runs the age stays zero.
		if age := p.AgeSeconds(time.Now().Add(time.Hour)); age < 3599 {
			t.Errorf("run not active during event: %d", age)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed == 0 {
		t.Fatal("no events observed")
	}
	if p.last.IsZero() {
		t.Fatal("no event recorded")
	}
	if age := p.AgeSeconds(time.Now().Add(time.Hour)); age != 0 {
		t.Fatalf("finished run still ages: %d", age)
	}
}
