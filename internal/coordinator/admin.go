package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/housekeeping"
)

// RepoRequest limits remote mutations to enrollment and existing repo options.
type RepoRequest struct {
	Action  string            `json:"action"`
	Repo    string            `json:"repo"`
	Options map[string]string `json:"options,omitempty"`
}

type PlatformStatus struct {
	Disk             *housekeeping.Disk   `json:"disk,omitempty"`
	TempDisk         *housekeeping.Disk   `json:"temp_disk,omitempty"`
	LastHousekeeping *housekeeping.Result `json:"last_housekeeping,omitempty"`
	DiskError        string               `json:"disk_error,omitempty"`
	Version          string               `json:"version"`
	Queue            map[string]int       `json:"queue"`
	Workers          []BoardWorker        `json:"workers"`
}

// AdminService reuses the host's config mutation paths, without exposing credentials.
type AdminService interface {
	Repos(context.Context, RepoRequest) (any, error)
	Status(context.Context) (PlatformStatus, error)
}

// NewPlatformHandler mounts admin and worker APIs on the same listener.
func NewPlatformHandler(store WorkerStore, admin AdminService) http.Handler {
	workers := NewHandler(store)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/admin/v1/") {
			workers.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		status, actor, action, target := 200, "", "unknown", ""
		quietRead := false
		var out any
		handle := func() error {
			headers := r.Header.Values("Authorization")
			if len(headers) != 1 {
				status = 401
				return ErrToken
			}
			fields := strings.Fields(headers[0])
			if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
				status = 401
				return ErrToken
			}
			token, err := store.VerifyToken(ctx, fields[1])
			if err != nil {
				status = 401
				return err
			}
			actor = token.Name
			if !token.HasScope(ScopeAdmin) {
				status = 403
				return ErrToken
			}
			if id, ok := taskLogPath(r.URL.Path); ok {
				action = "task-logs"
				target = id
				if r.Method != "GET" {
					status = 405
					return nil
				}
				reader, ok := store.(interface {
					ReadLog(context.Context, string, int64, int64, int) (LogRead, error)
				})
				if !ok {
					status = 404
					return nil
				}
				q := r.URL.Query()
				epoch, e1 := queryInt(q.Get("epoch"))
				after, e2 := queryInt(q.Get("after"))
				limit, e3 := queryInt(q.Get("limit"))
				if e1 != nil || e2 != nil || e3 != nil || len(q) > 3 {
					status = 400
					return ErrInvalid
				}
				// Opening a log is audited; follow-up increments are not, to keep polling cheap.
				quietRead = after > 0
				if out, err = reader.ReadLog(ctx, id, epoch, after, int(limit)); errors.Is(err, ErrStaleLease) {
					status = 404
				} else if errors.Is(err, ErrInvalid) {
					status = 400
				}
				return err
			}
			switch r.URL.Path {
			case "/admin/v1/dashboard":
				action = "dashboard"
				if r.Method != "GET" {
					status = 405
					return nil
				}
				dashboard, ok := admin.(DashboardService)
				if !ok {
					status = 404
					return nil
				}
				out, err = dashboard.Dashboard(ctx)
			case "/admin/v1/dashboard/action":
				action = "dashboard-action"
				if r.Method != "POST" {
					status = 405
					return nil
				}
				dashboard, ok := admin.(DashboardService)
				if !ok {
					status = 404
					return nil
				}
				var input DashboardRequest
				d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
				d.DisallowUnknownFields()
				if err = d.Decode(&input); err != nil {
					status = 400
					return err
				}
				var extra any
				if d.Decode(&extra) != io.EOF {
					status = 400
					return ErrInvalid
				}
				switch input.Action {
				case "invite", "drain", "resume", "remove", "events":
				default:
					status = 400
					return ErrInvalid
				}
				if len(input.Name) == 0 || len(input.Name) > 128 {
					status = 400
					return ErrInvalid
				}
				action = "dashboard-" + input.Action
				if err = store.AppendAudit(ctx, actor, "", "admin:"+action+":attempt"); err != nil {
					return err
				}
				out, err = dashboard.DashboardAction(ctx, input)
				if err != nil {
					status = 400
				}
			case "/admin/v1/status":
				action = "status"
				if r.Method != "GET" {
					status = 405
					return nil
				}
				out, err = admin.Status(ctx)
			case "/admin/v1/repos":
				action = "repo-list"
				input := RepoRequest{Action: "list"}
				if r.Method == "POST" {
					r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
					d := json.NewDecoder(r.Body)
					d.DisallowUnknownFields()
					if err = d.Decode(&input); err != nil {
						status = 400
						return err
					}
					var extra any
					if err = d.Decode(&extra); err != io.EOF {
						status = 400
						return ErrInvalid
					}
					switch input.Action {
					case "add", "enable", "disable", "set":
						action = "repo-" + input.Action
					default:
						status = 400
						return ErrInvalid
					}
				} else if r.Method != "GET" {
					status = 405
					return nil
				}
				target = repoAuditTarget(input.Repo)
				// Record the authorized intent before changing config. A failed audit prevents a write.
				if r.Method == "POST" {
					if err = store.AppendAudit(ctx, actor, target, "admin:"+action+":attempt"); err != nil {
						return err
					}
				}
				out, err = admin.Repos(ctx, input)
				if err != nil {
					status = 400
				}
			default:
				status = 404
			}
			return err
		}
		if err := handle(); err != nil && status == 200 {
			status = 500
		}
		auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer auditCancel()
		if !quietRead || status != 200 {
			if err := store.AppendAudit(auditCtx, actor, target, fmt.Sprintf("admin:%s:%d", action, status)); err != nil {
				status = 500
			}
		}
		if status >= 400 {
			out = map[string]string{"error": http.StatusText(status)}
		}
		if status == 401 {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(out)
	})
}

// taskLogPath matches /admin/v1/tasks/{id}/logs for a bounded, plain task id.
func taskLogPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/admin/v1/tasks/")
	if !ok {
		return "", false
	}
	id, tail, ok := strings.Cut(rest, "/")
	if !ok || tail != "logs" || id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\ \t\r\n") {
		return "", false
	}
	return id, true
}

func queryInt(v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, ErrInvalid
	}
	return n, nil
}

// repoAuditTarget records repository identity, excluding URL credentials and queries.
func repoAuditTarget(input string) string {
	key := strings.TrimSpace(input)
	if strings.HasPrefix(key, "https://") || strings.HasPrefix(key, "http://") {
		u, err := url.Parse(key)
		if err != nil {
			return ""
		}
		key = strings.TrimPrefix(u.Path, "/")
	} else if strings.HasPrefix(key, "git@") {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			return ""
		}
		key = parts[1]
	}
	key = strings.TrimSuffix(key, ".git")
	parts := strings.Split(key, "/")
	if len(parts) < 2 || len(key) > 512 {
		return ""
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ""
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
				return ""
			}
		}
	}
	return key
}
