package upgrade

import (
	"context"
	"errors"
	"fmt"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
)

// Observation is what the host can see of controller ownership right now.
type Observation struct {
	// Running means a controller process is serving.
	Running bool
	// RunningSHA is the SHA-256 of the binary that process was started from.
	// Empty means unknown, which the engine treats as ambiguous.
	RunningSHA string
	// RunningPath is the executable the process was started from.
	RunningPath string
	// LockHeld means the controller ownership lock is held by some process.
	LockHeld bool
}

// Host is the engine's only way to touch the world. The production
// implementation uses launchd, the controller lock and the coordinator
// database. Tests substitute a fixture backed by an isolated database.
type Host interface {
	// Preserve stores an immutable copy of a (keyed by content hash) and
	// returns the stored artifact. It verifies the content hash.
	Preserve(ctx context.Context, a Artifact) (Artifact, error)
	// Describe runs the artifact's manifest and checks it matches a.
	Describe(ctx context.Context, a Artifact) (Manifest, error)
	// Applied lists the schema versions recorded in the shared database.
	Applied(ctx context.Context) ([]int, error)
	// MigrateExpand applies pending migrations using the target. It returns
	// the versions applied. Each migration is transactional.
	MigrateExpand(ctx context.Context, target Artifact) ([]int, error)
	// Snapshot reads scheduling state without renewing or expiring a lease.
	Snapshot(ctx context.Context, watch []string) (coordinator.UpgradeSnapshot, error)
	// SetPaused stops (true) or restores (false) new assignments to a worker.
	SetPaused(ctx context.Context, worker string, paused bool) error
	// Probe is an authenticated API health check.
	Probe(ctx context.Context) error
	// ViewerProbe fetches the endpoint the native viewer reads.
	ViewerProbe(ctx context.Context) error
	Observe(ctx context.Context) (Observation, error)
	// StopController stops the service and returns once the process is gone
	// and the ownership lock is free. It must not leave a supervisor able to
	// restart the old binary.
	StopController(ctx context.Context) error
	// Fence takes the controller ownership lock so no other controller can
	// start while the service definition changes. release frees it.
	Fence(ctx context.Context) (release func(), err error)
	// Install points the service definition at a without starting it.
	Install(ctx context.Context, a Artifact) error
	StartController(ctx context.Context) error
	// RollWorker starts a new worker manager generation from a.
	RollWorker(ctx context.Context, descriptor string, a Artifact) error
}

// ErrInterrupted simulates (in tests) or reports a process that stopped at a
// stage boundary. The engine leaves the record non-terminal and performs no
// compensation, exactly as a crash would.
var ErrInterrupted = errors.New("upgrade interrupted")

// unchangedError marks a failure after which the running service is still the
// prior one, untouched.
type unchangedError struct{ err error }

func (e *unchangedError) Error() string { return e.err.Error() }
func (e *unchangedError) Unwrap() error { return e.err }

// postponeError marks a deadline that expired before any destructive step.
type postponeError struct{ reason string }

func (e *postponeError) Error() string { return e.reason }

func unchanged(format string, args ...any) error {
	return &unchangedError{fmt.Errorf(format, args...)}
}
