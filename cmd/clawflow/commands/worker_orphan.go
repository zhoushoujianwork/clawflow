package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	offload "github.com/zhoushoujianwork/offload/pkg/execution"
)

type AbandonState string

const (
	AbandonEnded        AbandonState = "ended"
	AbandonAlreadyEnded AbandonState = "already_ended"
	AbandonUnknown      AbandonState = "unknown"
)

// ReclaimRun allows 5 seconds for SIGTERM and 3 seconds after SIGKILL. Leave
// enough room for both waits plus process-state polling and slot persistence.
const orphanReclaimTimeout = 10 * time.Second

var reclaimOffloadRun = offload.ReclaimRun

// abandonOrphanedRun is the public-package integration seam for Offload run cleanup.
// Offload errors are returned unwrapped so callers can tell them apart with
// errors.Is (ErrRunNotFound, ErrRunStillOwned, ErrRunStartUnrecorded).
var abandonOrphanedRun = func(ctx context.Context, runID string) (AbandonState, error) {
	if runID == "" {
		return AbandonUnknown, errors.New("library execution run ID is missing; orphan state cannot be confirmed")
	}
	reclaimCtx, cancel := context.WithTimeout(ctx, orphanReclaimTimeout)
	defer cancel()
	outcome, err := reclaimOffloadRun(reclaimCtx, runID)
	if err != nil {
		return AbandonUnknown, err
	}
	switch outcome {
	case offload.ReclaimTerminated:
		return AbandonEnded, nil
	case offload.ReclaimAlreadyEnded:
		return AbandonAlreadyEnded, nil
	default:
		return AbandonUnknown, fmt.Errorf("Offload could not confirm run %q ended: reclaim outcome %q", runID, outcome)
	}
}
