package zcache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Client is the small subset of the ZCache REST API used by the Neutree PoC.
// Authentication is delegated to the supplied HTTP client.
type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

type ClusterNode struct {
	Name       string `json:"name"`
	Ready      bool   `json:"ready"`
	Selectable bool   `json:"selectable"`
}
type ClusterNodesResponse struct {
	Nodes []ClusterNode `json:"nodes"`
}
type RuntimeEndpoint struct {
	Address string `json:"address"`
	Port    int32  `json:"port"`
}
type RuntimeResponse struct {
	Ready    bool             `json:"ready"`
	Endpoint *RuntimeEndpoint `json:"endpoint,omitempty"`
	Nodes    []string         `json:"nodes,omitempty"`
}
type ValidateNodeResult struct {
	NodeName string   `json:"nodeName"`
	Valid    bool     `json:"valid"`
	Reasons  []string `json:"reasons"`
}
type ValidateResponse struct {
	Valid bool                 `json:"valid"`
	Nodes []ValidateNodeResult `json:"nodes"`
}
type RuntimeImage struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	PullPolicy string `json:"pullPolicy"`
}
type ResourceConfig struct {
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
}
type RuntimeRequest struct {
	Mode                    string              `json:"mode"`
	TargetNodes             []string            `json:"targetNodes"`
	DevicesByNode           map[string][]string `json:"devicesByNode"`
	AllowFormat             bool                `json:"allowFormat"`
	AllowWipeSignatures     bool                `json:"allowWipeSignatures"`
	AllowManagedDiskCleanup bool                `json:"allowManagedDiskCleanup"`
	Image                   RuntimeImage        `json:"image"`
	Resources               ResourceConfig      `json:"resources"`
	ServicePort             int32               `json:"servicePort"`
	MetricsPort             int32               `json:"metricsPort"`
	L1SizeGB                int32               `json:"l1SizeGb"`
}
type ValidateRequest struct {
	LMCache        RuntimeRequest `json:"lmcache"`
	IdempotencyKey string         `json:"idempotencyKey,omitempty"`
}
type ApplyResponse struct {
	OperationID string `json:"operationId"`
}
type OperationResponse struct {
	ID      string `json:"id"`
	Phase   string `json:"phase"`
	Summary string `json:"summary"`
	Reason  string `json:"reason"`
}

func (c *Client) request(ctx context.Context, method, endpoint string, body any, out any) error {
	var payload *bytes.Reader
	if body == nil {
		payload = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("zcache API %s %s: %s", method, endpoint, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
func (c *Client) ClusterNodes(ctx context.Context) (ClusterNodesResponse, error) {
	var out ClusterNodesResponse
	err := c.request(ctx, http.MethodGet, "/api/v1/zcache/cluster/nodes", nil, &out)
	return out, err
}
func (c *Client) Runtime(ctx context.Context) (RuntimeResponse, error) {
	var out RuntimeResponse
	err := c.request(ctx, http.MethodGet, "/api/v1/zcache/runtime", nil, &out)
	return out, err
}
func (c *Client) Validate(ctx context.Context, request ValidateRequest) (ValidateResponse, error) {
	var out ValidateResponse
	err := c.request(ctx, http.MethodPost, "/api/v1/zcache/validate", request, &out)
	return out, err
}
func (c *Client) Apply(ctx context.Context, request ValidateRequest) (ApplyResponse, error) {
	var out ApplyResponse
	err := c.request(ctx, http.MethodPost, "/api/v1/zcache/apply", request, &out)
	return out, err
}
func (c *Client) Operation(ctx context.Context, id string) (OperationResponse, error) {
	var out OperationResponse
	err := c.request(ctx, http.MethodGet, "/api/v1/zcache/operations/"+id, nil, &out)
	return out, err
}
