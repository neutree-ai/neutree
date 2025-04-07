package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type DashboardService interface {
	GetClusterMetadata() (*ClusterMetadataResponse, error)
	ListNodes() ([]v1.NodeSummary, error)
	GetClusterAutoScaleStatus() (v1.AutoscalerReport, error)
}

type Client struct {
	dashboardURL string
	client       *http.Client
}

type NewDashboardServiceFunc func(dashboardURL string) DashboardService

var (
	NewDashboardService NewDashboardServiceFunc = new
)

func new(dashboardURL string) DashboardService {
	return &Client{
		dashboardURL: dashboardURL,
		client: &http.Client{
			Timeout: time.Second * 30,
		},
	}
}

func (c *Client) doRequest(method, path string, result interface{}) error {
	req, err := http.NewRequest(method, c.dashboardURL+path, nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API request failed: %s", resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(result)
}

type ClusterMetadataResponse struct {
	Result  bool                      `json:"result"`
	Message string                    `json:"message"`
	Data    v1.RayClusterMetadataData `json:"data"`
}

func (c *Client) GetClusterMetadata() (*ClusterMetadataResponse, error) {
	var result ClusterMetadataResponse
	err := c.doRequest("GET", "/api/v0/cluster_metadata", &result)

	return &result, err
}

type NodeListResponse struct {
	Data NodeListData `json:"data"`
}

type NodeListData struct {
	Summary []v1.NodeSummary `json:"summary"`
}

func (c *Client) ListNodes() ([]v1.NodeSummary, error) {
	var result NodeListResponse
	err := c.doRequest("GET", "/nodes?view=summary", &result)

	return result.Data.Summary, err
}

type ClusterStatusResponse struct {
	Result bool              `json:"result"`
	Msg    string            `json:"msg"`
	Data   ClusterStatusData `json:"data"`
}

type ClusterStatusData struct {
	AutoscalingStatus string                       `json:"autoscalingStatus"`
	ClusterStatus     v1.RayClusterAutoScaleStatus `json:"clusterStatus"`
}

func (c *Client) GetClusterAutoScaleStatus() (v1.AutoscalerReport, error) {
	var result ClusterStatusResponse
	err := c.doRequest("GET", "/api/cluster_status?format=0", &result)

	return result.Data.ClusterStatus.AutoscalerReport, err
}
