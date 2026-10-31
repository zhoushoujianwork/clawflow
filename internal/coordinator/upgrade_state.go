package coordinator

import (
	"context"
	"time"
)

// UpgradeWorker is the minimum worker state a rolling upgrade needs.
type UpgradeWorker struct {
	Name        string
	Paused      bool
	Revoked     bool
	MaxParallel int
	LastSeen    *time.Time
	Live        bool
}

// UpgradeLease describes one leased task. Durable leases run in independent
// executors that survive a controller exit. A non-durable lease belongs to a
// worker tied to the controller process and must settle before cutover.
type UpgradeLease struct {
	TaskID string
	Owner  string
	Fence  int64
	// Durable is true when the task runs in an independent durable executor.
	Durable bool
	// ResultAccepted means a result is committed and the controller still owes
	// effect application or finalization.
	ResultAccepted bool
	// UnsettledEffects counts effect intents not yet applied.
	UnsettledEffects int
}

// UpgradeTask is a point-in-time view of one task, used to prove a lease or
// result survived a cutover.
type UpgradeTask struct {
	ID                  string
	State               string
	Owner               string
	Fence               int64
	Attempt             int
	ApplicationAttempts int
}

// UpgradeSnapshot is read-only. It renews and expires nothing.
type UpgradeSnapshot struct {
	Now     time.Time
	Workers []UpgradeWorker
	Leases  []UpgradeLease
	Watched []UpgradeTask
}

// UpgradeSnapshot reads scheduling state for a rolling upgrade.
func (s *Store) UpgradeSnapshot(ctx context.Context, watch []string) (UpgradeSnapshot, error) {
	var out UpgradeSnapshot
	board, err := s.Board(ctx)
	if err != nil {
		return out, err
	}
	out.Now = board.GeneratedAt
	for _, w := range board.Workers {
		out.Workers = append(out.Workers, UpgradeWorker{Name: w.Name, Paused: w.Paused, Revoked: w.Revoked, MaxParallel: w.MaxParallel, LastSeen: w.LastSeen, Live: w.Live})
	}
	var rows []struct {
		ID, LeaseOwner string
		FencingToken   int64
		Durable        bool
		Accepted       bool
		Unsettled      int
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT t.id, t.lease_owner, t.fencing_token,
 COALESCE(t.payload->>'durable_execution' = 'true', false) AS durable,
 (t.lease_expires_at IS NULL AND t.result IS NOT NULL) AS accepted,
 (SELECT count(*) FROM effect_intents e WHERE e.task_id = t.id AND e.lease_epoch = t.fencing_token AND e.status IN ('pending','uncertain')) AS unsettled
 FROM coordinator_tasks t WHERE t.state = 'leased' ORDER BY t.id`).Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Leases = append(out.Leases, UpgradeLease{TaskID: r.ID, Owner: r.LeaseOwner, Fence: r.FencingToken, Durable: r.Durable, ResultAccepted: r.Accepted, UnsettledEffects: r.Unsettled})
	}
	if len(watch) > 0 {
		var tasks []Task
		if err := s.db.WithContext(ctx).Select("id, state, lease_owner, fencing_token, attempt, application_attempts").Where("id IN ?", watch).Order("id").Find(&tasks).Error; err != nil {
			return out, err
		}
		for _, t := range tasks {
			out.Watched = append(out.Watched, UpgradeTask{ID: t.ID, State: t.State, Owner: t.LeaseOwner, Fence: t.FencingToken, Attempt: t.Attempt, ApplicationAttempts: t.ApplicationAttempts})
		}
	}
	return out, nil
}
