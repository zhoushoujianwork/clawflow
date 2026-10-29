package coordinator

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const MaxRequestBytes = 1 << 20

// JSON escaping can expand the capped 32 MiB native metadata up to sixfold.
const MaxReportBytes = 6*(32<<20) + MaxRequestBytes

// WorkerStore is the existing persistence contract used by the worker listener.
type WorkerStore interface {
	VerifyToken(context.Context, string) (*Token, error)
	RegisterWorker(context.Context, Worker) error
	Claim(context.Context, string) (*Task, error)
	Heartbeat(context.Context, string, string, int64) error
	Complete(context.Context, string, string, int64, json.RawMessage) error
	Fail(context.Context, string, string, int64, json.RawMessage) error
	AppendAudit(context.Context, string, string, string) error
}

func NewServer(addr string, store WorkerStore, cert tls.Certificate) *http.Server {
	return &http.Server{Addr: addr, Handler: NewHandler(store),
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}},
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
}

// NewHandler exposes only worker operations; identity always comes from the token.
func NewHandler(store WorkerStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		status, worker, task, action := http.StatusOK, "", "", "unknown"
		var output any = map[string]bool{"ok": true}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/worker/v1/"), "/")
		if strings.HasPrefix(r.URL.Path, "/worker/v1/") {
			if len(parts) == 1 && (parts[0] == "register" || parts[0] == "claim") {
				action = parts[0]
			}
			if len(parts) == 3 && parts[0] == "tasks" && len(parts[1]) <= 128 && parts[1] != "" {
				switch parts[2] {
				case "heartbeat", "complete", "fail":
					action = parts[2]
					task = parts[1]
				}
			}
		}
		handle := func() error {
			headers := r.Header.Values("Authorization")
			if len(headers) != 1 {
				return ErrToken
			}
			fields := strings.Fields(headers[0])
			if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
				return ErrToken
			}
			token, err := store.VerifyToken(ctx, fields[1])
			if err != nil {
				return err
			}
			if !token.HasScope(ScopeWorker) {
				return ErrToken
			}
			worker = token.Name
			if action == "unknown" {
				status = http.StatusNotFound
				return nil
			}
			if r.Method != http.MethodPost {
				status = http.StatusMethodNotAllowed
				return nil
			}
			limit := int64(MaxRequestBytes)
			if action == "complete" || action == "fail" {
				limit = MaxReportBytes
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			decode := func(v any) error {
				d := json.NewDecoder(r.Body)
				d.DisallowUnknownFields()
				if err := d.Decode(v); err != nil {
					return err
				}
				var extra any
				if err := d.Decode(&extra); err != io.EOF {
					if err != nil {
						return err
					}
					return ErrInvalid
				}
				return nil
			}
			switch action {
			case "register":
				input := struct {
					Tags        []string `json:"tags"`
					RunUntagged bool     `json:"run_untagged"`
					MaxParallel int      `json:"max_parallel"`
					Roles       []string `json:"roles"`
					Backend     string   `json:"backend"`
					Model       string   `json:"model"`
				}{RunUntagged: true, MaxParallel: 1}
				if err := decode(&input); err != nil {
					status = 400
					return err
				}
				err = store.RegisterWorker(ctx, Worker{ID: worker, Tags: input.Tags, RunUntagged: input.RunUntagged, MaxParallel: input.MaxParallel, Roles: input.Roles, Backend: input.Backend, Model: input.Model})
				output = map[string]string{"worker_id": worker}
			case "claim":
				if err := decode(&struct{}{}); err != nil {
					status = 400
					return err
				}
				var claimed *Task
				claimed, err = store.Claim(ctx, worker)
				output = map[string]any{"task": claimed}
			default:
				var input struct {
					FencingToken int64           `json:"fencing_token"`
					Result       json.RawMessage `json:"result"`
				}
				if err := decode(&input); err != nil {
					status = 400
					return err
				}
				switch action {
				case "heartbeat":
					err = store.Heartbeat(ctx, task, worker, input.FencingToken)
				case "complete":
					err = store.Complete(ctx, task, worker, input.FencingToken, input.Result)
				case "fail":
					err = store.Fail(ctx, task, worker, input.FencingToken, input.Result)
				}
			}
			return err
		}
		err := handle()
		var tooLarge *http.MaxBytesError
		switch {
		case errors.Is(err, ErrToken):
			status = 401
		case errors.Is(err, ErrStaleLease):
			status = 409
		case errors.Is(err, ErrNoTask):
			status = 200
			output = map[string]any{"task": nil}
		case errors.As(err, &tooLarge):
			status = 413
		case errors.Is(err, ErrInvalid):
			status = 400
		case err != nil && status == 200:
			status = 500
		}
		// A cancelled caller must not suppress its audit record. Never log secrets,
		// raw URLs, request bodies or backend error messages.
		auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer auditCancel()
		if err := store.AppendAudit(auditCtx, worker, task, fmt.Sprintf("http:%s:%d", action, status)); err != nil {
			status = 500
		}
		if status >= 400 {
			output = map[string]string{"error": http.StatusText(status)}
		}
		if status == 401 {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		if status == 405 {
			w.Header().Set("Allow", "POST")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(output)
	})
}
