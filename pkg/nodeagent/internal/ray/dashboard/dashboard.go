package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// DashboardService is the private Ray Dashboard surface consumed by the
// NodeAgent runtime. It intentionally includes only read operations.
type DashboardService interface {
	ListNodes() ([]v1.NodeSummary, error)
	GetServeApplications() (*RayServeApplicationsResponse, error)
	ListActors(filters []ActorFilter, detail bool, limit int) (*ActorsResponse, error)
}

// Client implements the read-only NodeAgent Dashboard surface.
type Client struct {
	dashboardURL string
	client       *http.Client
}

// NewDashboardService creates the private NodeAgent Dashboard client.
func NewDashboardService(dashboardURL string) DashboardService {
	return &Client{
		dashboardURL: dashboardURL,
		client:       &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) doRequest(method, path string, body, result interface{}) error {
	var requestBody io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}

		requestBody = bytes.NewBuffer(encoded)
	}

	req, err := http.NewRequest(method, c.dashboardURL+path, requestBody)
	if err != nil {
		return err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	response, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("API request failed: %s", response.Status)
	}

	if result == nil {
		return nil
	}

	return json.NewDecoder(response.Body).Decode(result)
}

// ClusterMetadataResponse is retained for test fakes that mirror the broader
// Dashboard API, although NodeAgent does not request this endpoint.
type ClusterMetadataResponse struct {
	Result  bool                      `json:"result"`
	Message string                    `json:"message"`
	Data    v1.RayClusterMetadataData `json:"data"`
}

type NodeListResponse struct {
	Data NodeListData `json:"data"`
}

type NodeListData struct {
	Summary []v1.NodeSummary `json:"summary"`
}

// ListNodes returns the Ray node summary used to resolve the local node ID.
func (c *Client) ListNodes() ([]v1.NodeSummary, error) {
	var result NodeListResponse
	err := c.doRequest(http.MethodGet, "/nodes?view=summary", nil, &result)

	return result.Data.Summary, err
}

// ActorFilter is one predicate accepted by Ray's actor state API.
type ActorFilter struct {
	Key       string
	Predicate string
	Value     string
}

type ActorsResponse struct {
	Result bool               `json:"result"`
	Msg    string             `json:"msg"`
	Data   ActorsResponseData `json:"data"`
}

type ActorsResponseData struct {
	Result ActorsListResult `json:"result"`
}

type ActorsListResult struct {
	Total              int     `json:"total"`
	NumAfterTruncation int     `json:"num_after_truncation"`
	NumFiltered        int     `json:"num_filtered"`
	Result             []Actor `json:"result"`
}

// Actor is the subset of Ray state data needed for allocation and runtime use.
type Actor struct {
	ActorID           string                 `json:"actor_id"`
	ClassName         string                 `json:"class_name"`
	State             string                 `json:"state"`
	Name              string                 `json:"name"`
	NodeID            string                 `json:"node_id"`
	PID               int                    `json:"pid"`
	RequiredResources map[string]float64     `json:"required_resources,omitempty"`
	StartTime         int64                  `json:"start_time"`
	EndTime           int64                  `json:"end_time"`
	DeathCause        map[string]interface{} `json:"death_cause,omitempty"`
}

// ListActors requests Ray actor state using the supplied filters.
func (c *Client) ListActors(filters []ActorFilter, detail bool, limit int) (*ActorsResponse, error) {
	query := url.Values{}
	for _, filter := range filters {
		query.Add("filter_keys", filter.Key)
		query.Add("filter_predicates", filter.Predicate)
		query.Add("filter_values", filter.Value)
	}

	if detail {
		query.Set("detail", "true")
	}

	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	path := "/api/v0/actors"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	response := &ActorsResponse{}
	if err := c.doRequest(http.MethodGet, path, nil, response); err != nil {
		return nil, fmt.Errorf("failed to list actors: %w", err)
	}

	return response, nil
}

const (
	ApplicationStatusDeploying    = "DEPLOYING"
	ApplicationStatusNotStarted   = "NOT_STARTED"
	ApplicationStatusDeployFailed = "DEPLOY_FAILED"
	ApplicationStatusUnhealthy    = "UNHEALTHY"
	ApplicationStatusRunning      = "RUNNING"
)

type RayServeApplication struct {
	Name        string                 `json:"name"`
	RuntimeEnv  map[string]interface{} `json:"runtime_env,omitempty"`
	RoutePrefix string                 `json:"route_prefix"`
	ImportPath  string                 `json:"import_path"`
	Args        map[string]interface{} `json:"args"`
}

type RayServeApplicationsRequest struct {
	Applications []RayServeApplication `json:"applications"`
}

type RayServeApplicationStatus struct {
	Status            string                `json:"status"`
	Message           string                `json:"message"`
	DeployedAppConfig *RayServeApplication  `json:"deployed_app_config"`
	Deployments       map[string]Deployment `json:"deployments,omitempty"`
}

type Deployment struct {
	Name     string    `json:"name"`
	Status   string    `json:"status,omitempty"`
	Message  string    `json:"message,omitempty"`
	Replicas []Replica `json:"replicas,omitempty"`
}

type Replica struct {
	NodeID      string `json:"node_id"`
	ActorID     string `json:"actor_id"`
	LogFilePath string `json:"log_file_path"`
	ReplicaID   string `json:"replica_id"`
}

type ProxyStatus struct {
	Status string `json:"status,omitempty"`
}

type RayServeApplicationsResponse struct {
	Applications map[string]RayServeApplicationStatus `json:"applications"`
	Proxies      map[string]ProxyStatus               `json:"proxies"`
}

// GetServeApplications returns Ray Serve application state for NodeAgent.
func (c *Client) GetServeApplications() (*RayServeApplicationsResponse, error) {
	response := &RayServeApplicationsResponse{}
	if err := c.doRequest(http.MethodGet, "/api/serve/applications/", nil, response); err != nil {
		return nil, fmt.Errorf("failed to execute request to get serve applications: %w", err)
	}

	return response, nil
}
