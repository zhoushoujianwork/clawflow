// Package cloud contains the client-side protocol used by ClawFlow SaaS
// workers and cloud-aware CLI commands. It intentionally has no server or DB
// dependency so the local CLI can compile before the SaaS API is implemented.
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

const DefaultBaseURL = "https://app.clawflow.dev"

// Config is the cloud connection state persisted in credentials.yaml.
type Config struct {
	BaseURL     string
	AccessToken string
	MachineID   string
	WorkerID    string
	WorkerToken string
}

// LoadConfig reads cloud credentials and applies defaults.
func LoadConfig() (Config, error) {
	creds, err := config.LoadCredentials()
	if err != nil {
		return Config{}, err
	}
	return FromCredentials(creds), nil
}

// FromCredentials converts local credentials into a cloud client config.
func FromCredentials(creds *config.Credentials) Config {
	if creds == nil {
		return Config{BaseURL: DefaultBaseURL}
	}
	baseURL := strings.TrimSpace(creds.CloudURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return Config{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		AccessToken: creds.CloudAccessToken,
		MachineID:   creds.CloudMachineID,
		WorkerID:    creds.CloudWorkerID,
		WorkerToken: creds.CloudWorkerToken,
	}
}

// ApplyToCredentials writes this cloud config back into credentials.yaml data.
func (c Config) ApplyToCredentials(creds *config.Credentials) *config.Credentials {
	if creds == nil {
		creds = &config.Credentials{}
	}
	creds.CloudURL = strings.TrimRight(c.BaseURL, "/")
	creds.CloudAccessToken = c.AccessToken
	creds.CloudMachineID = c.MachineID
	creds.CloudWorkerID = c.WorkerID
	creds.CloudWorkerToken = c.WorkerToken
	return creds
}

// Client is a small JSON HTTP client for the cloud API.
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

// NewClient constructs a cloud API client from Config.
func NewClient(cfg Config) (*Client, error) {
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("invalid cloud URL %q: %w", baseURL, err)
	}
	token := cfg.WorkerToken
	if token == "" {
		token = cfg.AccessToken
	}
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		token: token,
	}, nil
}

// RegisterWorker registers or refreshes a machine/worker identity.
func (c *Client) RegisterWorker(ctx context.Context, req RegisterWorkerRequest) (*RegisterWorkerResponse, error) {
	var resp RegisterWorkerResponse
	if err := c.post(ctx, "/api/worker/register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Heartbeat reports the worker's current state.
func (c *Client) Heartbeat(ctx context.Context, req HeartbeatRequest) (*HeartbeatResponse, error) {
	var resp HeartbeatResponse
	if err := c.post(ctx, "/api/worker/heartbeat", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Lease asks the cloud for one runnable job.
func (c *Client) Lease(ctx context.Context, req LeaseRequest) (*LeaseResponse, error) {
	var resp LeaseResponse
	if err := c.post(ctx, "/api/worker/lease", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AppendRunEvents sends append-only run log events to the cloud.
func (c *Client) AppendRunEvents(ctx context.Context, runID string, req RunEventsRequest) error {
	return c.post(ctx, "/api/worker/runs/"+url.PathEscape(runID)+"/events", req, nil)
}

// FinishRun records a run's final state.
func (c *Client) FinishRun(ctx context.Context, runID string, req FinishRunRequest) error {
	return c.post(ctx, "/api/worker/runs/"+url.PathEscape(runID)+"/finish", req, nil)
}

// ---- Cloud config client methods ----

// GetCloudConfig returns the aggregated cloud configuration summary.
func (c *Client) GetCloudConfig(ctx context.Context) (*CloudConfigSummary, error) {
	var resp CloudConfigSummary
	if err := c.get(ctx, "/api/cloud/config", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateProject creates a new cloud project.
func (c *Client) CreateProject(ctx context.Context, req CreateProjectRequest) (*Project, error) {
	var resp Project
	if err := c.post(ctx, "/api/cloud/projects", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateRepo registers a new repository in the cloud config.
func (c *Client) CreateRepo(ctx context.Context, req CreateRepoRequest) (*Repo, error) {
	var resp Repo
	if err := c.post(ctx, "/api/cloud/repos", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdateRepo applies a partial update to a cloud repository.
func (c *Client) UpdateRepo(ctx context.Context, id string, req UpdateRepoRequest) (*Repo, error) {
	var resp Repo
	if err := c.patch(ctx, "/api/cloud/repos/"+url.PathEscape(id), req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateBinding creates a new machine binding.
func (c *Client) CreateBinding(ctx context.Context, req CreateBindingRequest) (*Binding, error) {
	var resp Binding
	if err := c.post(ctx, "/api/cloud/bindings", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdateBinding applies a partial update to an existing machine binding.
func (c *Client) UpdateBinding(ctx context.Context, id string, req UpdateBindingRequest) (*Binding, error) {
	var resp Binding
	if err := c.patch(ctx, "/api/cloud/bindings/"+url.PathEscape(id), req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListMachines returns all registered machines.
func (c *Client) ListMachines(ctx context.Context) (*ListMachinesResponse, error) {
	var resp ListMachinesResponse
	if err := c.get(ctx, "/api/cloud/machines", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListJobs returns current job queue state.
func (c *Client) ListJobs(ctx context.Context) (*ListJobsResponse, error) {
	var resp ListJobsResponse
	if err := c.get(ctx, "/api/cloud/jobs", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListRuns returns recent run records.
func (c *Client) ListRuns(ctx context.Context) (*ListRunsResponse, error) {
	var resp ListRunsResponse
	if err := c.get(ctx, "/api/cloud/runs", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) post(ctx context.Context, path string, in any, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloud API %s: HTTP %d", path, resp.StatusCode)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloud API %s: HTTP %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) patch(ctx context.Context, path string, in any, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloud API %s: HTTP %d", path, resp.StatusCode)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
