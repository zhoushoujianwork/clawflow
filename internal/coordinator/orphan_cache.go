package coordinator

import (
	"context"
	"log"
	"time"
)

const (
	// DefaultOrphanRefreshInterval is how often the background refresh queries VCS providers.
	DefaultOrphanRefreshInterval = 5 * time.Minute
	// DefaultOrphanRefreshTimeout bounds one refresh, independent of any board read.
	DefaultOrphanRefreshTimeout = 60 * time.Second
)

// RefreshOrphanedPRs runs OrphanedPRs once under its own timeout and caches the result.
// On failure the previous result is kept and one log line is written.
func (s *Store) RefreshOrphanedPRs(ctx context.Context) {
	if s.OrphanedPRs == nil {
		return
	}
	timeout := s.OrphanRefreshTimeout
	if timeout <= 0 {
		timeout = DefaultOrphanRefreshTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	prs, err := s.OrphanedPRs(rctx)
	if err != nil {
		log.Printf("coordinator: orphaned PR refresh failed, keeping previous result: %v", err)
		return
	}
	s.orphanMu.Lock()
	s.orphanCache = append([]OrphanedPR(nil), prs...)
	s.orphanMu.Unlock()
}

// RunOrphanRefresh refreshes immediately and then every interval until ctx ends.
// It never runs from board reads.
func (s *Store) RunOrphanRefresh(ctx context.Context) {
	interval := s.OrphanRefreshInterval
	if interval <= 0 {
		interval = DefaultOrphanRefreshInterval
	}
	s.RefreshOrphanedPRs(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.RefreshOrphanedPRs(ctx)
		}
	}
}

// cachedOrphanedPRs returns the last refreshed result; nil before the first success.
func (s *Store) cachedOrphanedPRs() []OrphanedPR {
	s.orphanMu.RLock()
	defer s.orphanMu.RUnlock()
	return s.orphanCache
}
