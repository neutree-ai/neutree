package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/utils"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// needsServedModelDiscovery reports whether the names recorded in the status were
// read for a different spec. The fingerprint is written only after a successful
// read, so a failed read leaves it behind and is retried.
func needsServedModelDiscovery(status *v1.EndpointStatus, specHash string) bool {
	if status == nil {
		return true
	}

	return status.ObservedSpecHash != specHash
}

// endpointSpecHash is deterministic for equal specs: encoding/json sorts map keys
// and EndpointSpec holds only JSON-representable values.
func endpointSpecHash(endpoint *v1.Endpoint) string {
	data, err := json.Marshal(endpoint.Spec)
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func (c *EndpointController) getServedModels(endpoint *v1.Endpoint) ([]string, error) {
	clusters, err := c.storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(endpoint.Spec.Cluster)},
			{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(endpoint.Metadata.Workspace)},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to get endpoint cluster")
	}

	if len(clusters) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	return fetchServedModels(&clusters[0], endpoint)
}

// servedModelsClient reaches the cluster directly. The target is a cluster-internal
// address, and a proxy configured in the control plane's environment would answer in
// place of the endpoint.
var servedModelsClient = func() *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}

	transport := base.Clone()
	transport.Proxy = nil

	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}()

// servedModelIDs keeps the ids an engine reports, in order and without duplicates.
// A blank id is dropped instead of failing the observation: an engine may list a
// placeholder entry, and rejecting the response over it would leave the endpoint
// with no names at all.
func servedModelIDs(ids []string) []string {
	models := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))

	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}

		if !seen[id] {
			models = append(models, id)
			seen[id] = true
		}
	}

	return models
}

func fetchServedModels(cluster *v1.Cluster, endpoint *v1.Endpoint) ([]string, error) {
	serviceURL, err := utils.FormatServiceURL(cluster, endpoint)
	if err != nil {
		return nil, err
	}

	// Probe the same cluster route Kong forwards to, without API-key filtering.
	modelsURL := serviceURL + "/v1/models"

	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}

	ids, err := utils.FetchModelIDs(servedModelsClient, req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch served models")
	}

	return servedModelIDs(ids), nil
}
