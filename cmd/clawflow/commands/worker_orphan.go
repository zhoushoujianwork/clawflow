package commands

import (
	"context"
	"errors"
)

type AbandonState string

const (
	AbandonEnded        AbandonState = "ended"
	AbandonAlreadyEnded AbandonState = "already_ended"
	AbandonUnknown      AbandonState = "unknown"
)

// abandonOrphanedRun is the public-package integration seam for Offload run
// cleanup. offload#100 is not released yet, so recovery must remain conservative.
var abandonOrphanedRun = func(context.Context, string) (AbandonState, error) {
	return AbandonUnknown, errors.New("Offload orphan cleanup is unavailable until zhoushoujianwork/offload#100 is released")
}
