package dashboard

import (
	"net/http"

	"github.com/pkg/errors"
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
