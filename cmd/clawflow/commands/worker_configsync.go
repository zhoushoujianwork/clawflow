// Periodic config sync — automate the `clawflow connect sync pull` step.
//
// Before this, a dashboard toggle (e.g. flipping `auto_evaluate_all_issues`
// on) only reached the worker after the user manually ran
// `clawflow connect sync pull`. Easy to forget, and the worker would
// otherwise keep running with stale config indefinitely.
//
// Now the worker does the pull itself on a 5-minute ticker plus one
// immediate pass at boot. Stay quiet when nothing changed; emit one line
// when something did, so `clawflow worker logs` shows the transitions
// that actually matter.
package commands

import (
	"fmt"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// configSyncInterval is how often the worker pulls /sync/config. 5 minutes
// mirrors healthCheckInterval — these are both "periodically reconcile
// with SaaS" loops and deserve similar cadence.
const configSyncInterval = 5 * time.Minute

// configSyncLoop runs pullConfig on a ticker for the lifetime of the
// worker. Best-effort: a failed pull is logged but doesn't stop the loop,
// since a transient SaaS outage shouldn't kill the worker's ability to
// pick up config later.
func configSyncLoop(stopCh <-chan struct{}) {
	// Immediate pass at boot so the first discover tick sees fresh config
	// rather than whatever was last written to ~/.clawflow/config/config.yaml.
	// Wrapping in a goroutine-safe closure would be overkill — this is
	// called exactly once from runWorker's `go configSyncLoop(...)`.
	runConfigSyncPass()

	t := time.NewTicker(configSyncInterval)
	defer t.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-t.C:
			runConfigSyncPass()
		}
	}
}

func runConfigSyncPass() {
	saas, err := config.LoadSaasConfig()
	if err != nil || saas == nil || saas.URL == "" || saas.SyncToken == "" {
		// Worker isn't `clawflow connect`ed — nothing to sync. Silent: the
		// legacy pre-SaaS workflows (manual yaml + GitLab-only) are still
		// valid, and we don't want to nag users who chose that path.
		return
	}
	n, err := pullConfig(saas)
	if err != nil {
		fmt.Printf("[sync] config pull failed: %v\n", err)
		return
	}
	if n > 0 {
		fmt.Printf("[sync] pulled %d repo config update(s) from SaaS\n", n)
	}
}
