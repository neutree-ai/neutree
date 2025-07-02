package dashboard

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"maps"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// RayServeApplication represents the structure expected by the Ray Serve API.
type RayServeApplication struct {
	Name        string                 `json:"name"`
	RuntimeEnv  map[string]interface{} `json:"runtime_env,omitempty"`
	RoutePrefix string                 `json:"route_prefix"`
	ImportPath  string                 `json:"import_path"`
	Args        map[string]interface{} `json:"args"`
}

// RayServeApplicationsRequest represents the payload for updating applications.
type RayServeApplicationsRequest struct {
	Applications []RayServeApplication `json:"applications"`
}

// RayServeApplicationStatus represents the status part of the response from Ray Serve API.
type RayServeApplicationStatus struct {
	Status            string               `json:"status"`
	Message           string               `json:"message"`
	DeployedAppConfig *RayServeApplication `json:"deployed_app_config"` // Used when getting current apps
}

// RayServeApplicationsResponse represents the full response when getting applications.
type RayServeApplicationsResponse struct {
	Applications map[string]RayServeApplicationStatus `json:"applications"`
}

// endpointToApplication converts Neutree Endpoint and ModelRegistry to a RayServeApplication.
func EndpointToApplication(endpoint *v1.Endpoint, modelRegistry *v1.ModelRegistry) RayServeApplication {
	accelerator := map[string]float64{}

	for key, value := range endpoint.Spec.Resources.Accelerator {
		if key != "-" && value > 0 {
			accelerator[key] = value
		}
	}

	endpoint.Spec.DeploymentOptions["backend"] = map[string]interface{}{
		"num_replicas": endpoint.Spec.Replicas.Num,
		"num_cpus":     endpoint.Spec.Resources.CPU,
		"num_gpus":     endpoint.Spec.Resources.GPU,
		"memory":       endpoint.Spec.Resources.Memory,
		"resources":    accelerator,
	}

	endpoint.Spec.DeploymentOptions["controller"] = map[string]interface{}{
		"num_replicas": 1,
		"num_cpus":     0.1,
		"num_gpus":     0,
	}

	args := map[string]interface{}{
		"model": map[string]interface{}{
			"registry_type": modelRegistry.Spec.Type,
			"name":          endpoint.Spec.Model.Name,
			"file":          endpoint.Spec.Model.File,
			"version":       endpoint.Spec.Model.Version,
			"task":          endpoint.Spec.Model.Task,
		},
		"deployment_options": endpoint.Spec.DeploymentOptions,
	}

	maps.Copy(args, endpoint.Spec.Variables)

	app := RayServeApplication{
		Name:        endpoint.Metadata.Name,
		RoutePrefix: fmt.Sprintf("/%s", endpoint.Metadata.Name),
		ImportPath:  fmt.Sprintf("serve.%s.%s.app:app_builder", strings.ReplaceAll(endpoint.Spec.Engine.Engine, "-", "_"), endpoint.Spec.Engine.Version),
		Args:        args,
	}

	applicationEnv := map[string]string{}

	if modelRegistry.Spec.Type == v1.BentoMLModelRegistryType {
		url, _ := url.Parse(modelRegistry.Spec.Url) // nolint: errcheck
		// todo: support local file type env set
		if url != nil && url.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			applicationEnv[v1.BentoMLHomeEnv] = filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name)
		}
	} else if modelRegistry.Spec.Type == v1.HuggingFaceModelRegistryType {
		applicationEnv[v1.HFEndpoint] = strings.TrimSuffix(modelRegistry.Spec.Url, "/")
		if modelRegistry.Spec.Credentials != "" {
			applicationEnv[v1.HFTokenEnv] = modelRegistry.Spec.Credentials
		}
	}

	app.RuntimeEnv = map[string]interface{}{
		"env_vars": applicationEnv,
	}

	return app
}

// GetServeApplications retrieves the current Ray Serve applications.
func (c *Client) GetServeApplications() (*RayServeApplicationsResponse, error) {
	var appsResp RayServeApplicationsResponse

	err := c.doRequest(http.MethodGet, "/api/serve/applications/", nil, &appsResp)
	if err != nil {
		return nil, errors.Wrap(err, "failed to execute request to get serve applications")
	}

	return &appsResp, nil
}

// UpdateServeApplications updates the Ray Serve applications configuration.
func (c *Client) UpdateServeApplications(appsReq RayServeApplicationsRequest) error {
	err := c.doRequest(http.MethodPut, "/api/serve/applications/", &appsReq, nil)

	if err != nil {
		// Consider reading body for more error details
		return errors.Wrapf(err, "failed to update serve applications")
	}

	return nil
}

// formatServiceURL constructs the service URL for an endpoint.
func FormatServiceURL(cluster *v1.Cluster, endpoint *v1.Endpoint) (string, error) {
	if cluster.Status == nil || cluster.Status.DashboardURL == "" {
		return "", errors.New("cluster dashboard URL is not available")
	}

	dashboardURL, err := url.Parse(cluster.Status.DashboardURL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse cluster dashboard URL")
	}
	// Ray serve applications are typically exposed on port 8000 by default
	return fmt.Sprintf("%s://%s:8000/%s", dashboardURL.Scheme, dashboardURL.Hostname(), endpoint.Metadata.Name), nil
}
