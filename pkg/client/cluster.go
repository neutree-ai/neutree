package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type ClusterProfileUpsertResult struct {
	Operation string `json:"operation"`
}

// ClusterProfileVersion identifies one imported ClusterProfile without
// exposing the profile's component image material.
type ClusterProfileVersion struct {
	Version     string `json:"version"`
	ClusterType string `json:"cluster_type"`
}

type clusterProfileVersionsResponse struct {
	Profiles []ClusterProfileVersion `json:"profiles"`
}

// ClustersService owns calls to Cluster-specific helper endpoints.
type ClustersService struct {
	client *Client
}

func NewClustersService(client *Client) *ClustersService {
	return &ClustersService{client: client}
}

// UpsertClusterProfile sends the only internal cluster profile write request.
// The control plane permits creation and exact-content replay only.
func (service *ClustersService) UpsertClusterProfile(profile *v1.ClusterProfile) (*ClusterProfileUpsertResult, error) {
	payload, err := json.Marshal(struct {
		Profile *v1.ClusterProfile `json:"profile"`
	}{Profile: profile})
	if err != nil {
		return nil, err
	}

	requestURL := fmt.Sprintf("%s/api/v1/clusters/profile_upsert", service.client.baseURL)

	req, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := service.client.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err = expectStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	result := &ClusterProfileUpsertResult{}
	if err = json.NewDecoder(resp.Body).Decode(result); err != nil {
		return nil, err
	}

	return result, nil
}

// ListClusterProfileVersions returns imported ClusterProfile identities for
// upgrade preflight checks. The endpoint is restricted to system admins.
func (service *ClustersService) ListClusterProfileVersions() ([]ClusterProfileVersion, error) {
	requestURL := fmt.Sprintf("%s/api/v1/clusters/profile_versions", service.client.baseURL)

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

	var result clusterProfileVersionsResponse
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Profiles, nil
}
