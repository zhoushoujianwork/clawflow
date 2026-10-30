package coordinator

import (
	"context"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/queue"
)

// Dashboard is the native viewer's versioned, credential-free read model.
type Dashboard struct {
	SchemaVersion int                `json:"schema_version"`
	Version       string             `json:"version"`
	Controller    string             `json:"controller"`
	Board         Board              `json:"board"`
	Projects      []DashboardProject `json:"projects"`
}
type DashboardProject struct {
	Name  string          `json:"name"`
	Repos []DashboardRepo `json:"repos"`
}
type DashboardRepo struct {
	IssueQueue         []queue.ListEntry `json:"issue_queue"`
	QueueUpdatedAt     *time.Time        `json:"queue_updated_at,omitempty"`
	QueueError         string            `json:"queue_error,omitempty"`
	SkippedIssues      int               `json:"skipped_issues"`
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	Platform           string            `json:"platform"`
	Enabled            bool              `json:"enabled"`
	CIRequired         bool              `json:"ci_required"`
	AcceptanceRequired bool              `json:"acceptance_required"`
	AutoMerge          bool              `json:"auto_merge"`
	WorkerTags         []string          `json:"worker_tags"`
}
type DashboardRequest struct {
	Action string `json:"action"`
	Name   string `json:"name"`
}
type DashboardService interface {
	Dashboard(context.Context) (Dashboard, error)
	DashboardAction(context.Context, DashboardRequest) (any, error)
}
type TaskEvent struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
}

func (s *Store) TaskEvents(ctx context.Context, id string) ([]TaskEvent, error) {
	rows := []TaskEvent{}
	err := s.db.WithContext(ctx).Model(&Audit{}).Select("created_at AS at, action").Where("task = ?", id).Order("id DESC").Limit(100).Scan(&rows).Error
	return rows, err
}
