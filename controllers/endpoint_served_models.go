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
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/utils"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// servedModelsForStatus reports the names the inference instance serves and
// whether they were derived from specHash. The probe is skipped while the
// recorded observation already belongs to that spec, and a failed probe reports
// no observation so the status keeps what it has and the next reconcile retries.
func (c *EndpointController) servedModelsForStatus(endpoint *v1.Endpoint, specHash string) ([]string, bool) {
	if specHash != "" && endpoint.Status != nil && endpoint.Status.ObservedSpecHash == specHash {
		return endpoint.Status.ServedModels, true
	}

	models, err := c.getServedModels(endpoint)
	if err != nil {
		klog.Warningf("failed to get endpoint %s served models: %v", endpoint.Metadata.WorkspaceName(), err)

		return nil, false
	}

	return models, true
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

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	ids, err := utils.FetchModelIDs(client, req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch served models")
	}

	models := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))

	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("served models response contains an empty model id")
		}

		if !seen[id] {
			models = append(models, id)
			seen[id] = true
		}
	}

	return models, nil
}
