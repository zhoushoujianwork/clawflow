package commands

import (
	"context"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

// Unrelated tests run without an Offload installation; readiness-specific
// tests install their own fixtures.
func init() {
	offloadReadyFn = func(context.Context, string) execution.Readiness {
		return execution.Readiness{SchemaVersion: execution.ReadinessSchemaVersion, State: execution.StateReady}
	}
	workerGateFactory = func(string, []string) *workerGate { return nil }
}
