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
