package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// ClusterReleaseInfoReference is the public ReleaseInfo identity returned by
// Cluster preflight endpoints. It intentionally excludes component refs.
type ClusterReleaseInfoReference struct {
	Baseline string `json:"baseline"`
	Revision string `json:"revision"`
}

// ClusterUpgradePreflight is the public result of a matrix-only upgrade check.
type ClusterUpgradePreflight struct {
	Allowed       bool                        `json:"allowed"`
	SourceVersion string                      `json:"source_version"`
	TargetVersion string                      `json:"target_version,omitempty"`
	UpgradeTo     []string                    `json:"upgrade_to"`
	ReleaseInfo   ClusterReleaseInfoReference `json:"release_info"`
}

// ClustersService owns calls to Cluster-specific helper endpoints.
type ClustersService struct {
	client *Client
}

func NewClustersService(client *Client) *ClustersService {
	return &ClustersService{client: client}
}

// UpgradePreflight checks a Cluster version edge against the current
// ReleaseInfo. An empty targetVersion returns the available targets instead.
func (service *ClustersService) UpgradePreflight(workspace, name, targetVersion string) (*ClusterUpgradePreflight, error) {
	query := url.Values{}
	query.Set("workspace", workspace)
	query.Set("name", name)

	if targetVersion != "" {
		query.Set("target_version", targetVersion)
	}

	requestURL := fmt.Sprintf("%s/api/v1/clusters/upgrade_preflight?%s", service.client.baseURL, query.Encode())

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := service.client.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err = expectStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	result := &ClusterUpgradePreflight{}
	if err = json.NewDecoder(resp.Body).Decode(result); err != nil {
		return nil, err
	}

	return result, nil
}
