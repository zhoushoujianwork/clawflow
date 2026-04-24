// Periodic config sync — automate both the pull and push sides so the
// user never has to run `clawflow connect sync` by hand.
//
// Before this, a dashboard toggle (e.g. flipping `auto_evaluate_all_issues`
// on) only reached the worker after the user manually ran
// `clawflow connect sync pull`, and local `clawflow repo add` only
// reached the cloud dashboard after the user manually ran
// `clawflow connect sync push`. Easy to forget in both directions.
//
// Now the worker does a pull-then-push on a 5-minute ticker plus one
// immediate pass at boot. Pull-first order: SaaS is considered the
// authority on user-facing toggles (dashboard), so we pick up any
// SaaS-side changes before echoing our local state back. Stay quiet
// on no-op passes; emit a line only when pull brought something new
// or either direction errored, so `clawflow worker logs` shows the
// transitions that actually matter.
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
	// Pull first. If a dashboard toggle raced with a local edit, we'd
	// rather the dashboard value land in local config before we push our
	// view back, to avoid the push silently clobbering a SaaS-side change
	// that hadn't been pulled yet. Last-writer-wins on genuinely
	// concurrent edits — acceptable without version vectors on the
	// contract.
	if n, err := pullConfig(saas); err != nil {
		fmt.Printf("[sync] config pull failed: %v\n", err)
	} else if n > 0 {
		fmt.Printf("[sync] pulled %d repo config update(s) from SaaS\n", n)
	}
	// Then push. We always send the full repo list — SaaS treats it as an
	// upsert keyed by (org_id, platform, full_name), so noop-upserts of
	// unchanged repos are harmless. Stay silent on success: every pass
	// would otherwise print a "synced N" line even when nothing changed,
	// which drowns out the pull lines that signal actual config drift.
	if _, err := pushConfig(saas); err != nil {
		fmt.Printf("[sync] config push failed: %v\n", err)
	}
}
