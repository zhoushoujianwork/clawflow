package commands

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

// workerTranscriptSink relays the execution output of one fenced task to the
// controller over the existing authenticated worker channel. Task, worker and
// fence come from the lease; page bodies cannot choose another identity.
type workerTranscriptSink struct {
	api   *workerAPI
	path  string
	fence int64
}

func newWorkerTranscriptSink(api *workerAPI, t *coordinator.Task) *workerTranscriptSink {
	return &workerTranscriptSink{api: api, path: "tasks/" + url.PathEscape(t.ID) + "/transcript", fence: t.FencingToken}
}

func (s *workerTranscriptSink) Deliver(ctx context.Context, p execution.TranscriptPage) error {
	upload := coordinator.TranscriptUpload{FencingToken: s.fence, CallerRunID: p.CallerRunID, RunID: p.RunID, CaptureID: p.CaptureID,
		Availability: p.Availability, State: p.State, Completeness: p.Completeness, Reason: p.Reason, Summary: p.Summary,
		CursorIn: p.CursorIn, CursorOut: p.CursorOut, HasMore: p.HasMore, EndOfStream: p.EndOfStream, Events: p.Events}
	if upload.Events == nil {
		upload.Events = nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// One attempt: the pump retries the same cursor, so a disconnect never skips content.
	err := s.api.postOnce(callCtx, s.path, upload, nil)
	var status workerHTTPError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &status) && (status == 409 || status == 422 || status == 413 || status == 400 || status == 404):
		// Superseded lease, identity conflict or unacceptable page: retrying cannot help.
		return execution.ErrTranscriptRejected
	case errors.Is(err, errWorkerAuth), errors.Is(err, errWorkerTrust):
		return execution.ErrTranscriptRejected
	}
	return err
}
