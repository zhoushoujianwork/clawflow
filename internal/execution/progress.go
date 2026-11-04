package execution

import (
	"context"
	"sync"
	"time"
)

// Progress records when active Offload runs last produced a validated event.
// One task may run Offload several times; between runs it is not waiting on
// events, so its age is zero rather than growing through Git or CI phases.
type Progress struct {
	mu     sync.Mutex
	active int
	last   time.Time
}

type progressKey struct{}

// WithProgress makes Run report its events to p.
func WithProgress(ctx context.Context, p *Progress) context.Context {
	return context.WithValue(ctx, progressKey{}, p)
}

// ProgressFrom returns the tracker carried by ctx, or nil.
func ProgressFrom(ctx context.Context) *Progress {
	p, _ := ctx.Value(progressKey{}).(*Progress)
	return p
}

// AgeSeconds returns seconds since the active run's last event, or zero when no run is active.
func (p *Progress) AgeSeconds(now time.Time) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == 0 || now.Before(p.last) {
		return 0
	}
	return int64(now.Sub(p.last).Seconds())
}

func (p *Progress) begin() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active++
	p.last = time.Now()
}

func (p *Progress) event() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = time.Now()
}

func (p *Progress) end() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active--
}
