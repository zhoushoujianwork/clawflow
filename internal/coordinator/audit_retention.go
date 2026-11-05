package coordinator

import (
	"context"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	DefaultAuditRetention         = 30 * 24 * time.Hour
	DefaultAuditSecurityRetention = 180 * 24 * time.Hour
	DefaultAuditBatch             = 1000
	DefaultAuditSampleWindow      = 10 * time.Minute
	defaultAuditBatchPause        = 50 * time.Millisecond
)

// AuditRetention configures purging of coordinator_audit. Zero fields use defaults.
type AuditRetention struct {
	// Routine covers high-volume operational rows (claim, heartbeat, successful HTTP/admin calls).
	Routine time.Duration
	// Security covers everything else: task state changes, cancel, token and
	// worker lifecycle, and failed (non-2xx) requests.
	Security time.Duration
	Batch    int
	// Pause is the wait between batches, during which the write lock is free.
	Pause time.Duration
	// afterBatch is a test hook called after each batch committed.
	afterBatch func(deleted int64)
}

// routineAuditSQL matches rows with short retention. Heartbeat rows are routine,
// but the newest per worker is kept so Board's last-heartbeat query is unchanged.
const routineAuditSQL = `(a.action IN ('claim','register','token-verify','http:claim:200') ` +
	`OR (a.action LIKE 'http:%:200') OR (a.action LIKE 'admin:%:200') ` +
	`OR (a.action = 'heartbeat' AND EXISTS (SELECT 1 FROM coordinator_audit n WHERE n.worker = a.worker AND n.action = 'heartbeat' AND n.id > a.id)))`

// PurgeAudit deletes expired audit rows in bounded batches and returns the total deleted.
func (s *Store) PurgeAudit(ctx context.Context, now time.Time, cfg AuditRetention) (int64, error) {
	if cfg.Routine <= 0 {
		cfg.Routine = DefaultAuditRetention
	}
	if cfg.Security <= 0 {
		cfg.Security = DefaultAuditSecurityRetention
	}
	if cfg.Security < cfg.Routine {
		cfg.Security = cfg.Routine
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultAuditBatch
	}
	if cfg.Pause <= 0 {
		cfg.Pause = defaultAuditBatchPause
	}
	routineCut, securityCut := now.UTC().Add(-cfg.Routine), now.UTC().Add(-cfg.Security)
	var total, cursor int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		// Select a bounded id window past the cursor so rows that are kept
		// (e.g. old security records) are not rescanned by later batches.
		var ids []int64
		err := s.db.WithContext(ctx).Raw(`SELECT a.id FROM coordinator_audit a WHERE a.id > ? AND `+
			`((`+routineAuditSQL+` AND a.created_at < ?) OR (NOT `+routineAuditSQL+` AND a.created_at < ?)) `+
			`ORDER BY a.id LIMIT ?`, cursor, routineCut, securityCut, cfg.Batch).Scan(&ids).Error
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		cursor = ids[len(ids)-1]
		r := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&Audit{})
		if r.Error != nil {
			return total, r.Error
		}
		total += r.RowsAffected
		if cfg.afterBatch != nil {
			cfg.afterBatch(r.RowsAffected)
		}
		if len(ids) < cfg.Batch {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(cfg.Pause):
		}
	}
}

// lowValueAudit reports actions whose individual rows carry little audit value.
func lowValueAudit(action string) bool {
	return action == "token-verify" || action == "http:claim:200" || action == "admin:dashboard:200" ||
		(strings.HasPrefix(action, "admin:status:") && strings.HasSuffix(action, ":200"))
}

type auditSampler struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// allow returns true at most once per window for each (worker, action).
func (a *auditSampler) allow(worker, action string, window time.Duration, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last == nil {
		a.last = map[string]time.Time{}
	}
	k := worker + "\x00" + action
	if t, ok := a.last[k]; ok && now.Sub(t) < window {
		return false
	}
	if len(a.last) > 10000 {
		a.last = map[string]time.Time{}
	}
	a.last[k] = now
	return true
}

// auditSampled writes high-frequency rows at most once per window per worker and action.
func (s *Store) auditSampled(tx *gorm.DB, worker, task, action string) error {
	window := s.AuditSampleWindow
	if window == 0 {
		window = DefaultAuditSampleWindow
	}
	if lowValueAudit(action) && window > 0 && !s.sampler.allow(worker, action, window, time.Now()) {
		return nil
	}
	return audit(tx, worker, task, action)
}
