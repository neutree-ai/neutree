package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// UsageService handles model-usage statistics via the get_usage_by_dimension
// PostgREST RPC.
//
// Usage is stored day-aggregated in the api_daily_usage table and exposed
// through the api.get_usage_by_dimension function (a SECURITY DEFINER RPC that
// scopes rows to the caller via auth.uid() plus workspace:usage-read). The RPC
// returns the whole result set in one response — there is no pagination.
type UsageService struct {
	client *Client
}

// NewUsageService creates a new usage service.
func NewUsageService(client *Client) *UsageService {
	return &UsageService{client: client}
}

// UsageRow is one (day × api_key × endpoint_type × endpoint_name × model)
// aggregate bucket returned by get_usage_by_dimension. Token counts (and, for
// pre-dimensional records, endpoint_type/model_name) can be null on the wire —
// modeled as pointers/omittable so an absent value is distinct from zero.
type UsageRow struct {
	Date             string `json:"date"`
	APIKeyID         string `json:"api_key_id"`
	APIKeyName       string `json:"api_key_name"`
	EndpointType     string `json:"endpoint_type"`
	EndpointName     string `json:"endpoint_name"`
	ModelName        string `json:"model_name"`
	Workspace        string `json:"workspace"`
	Usage            *int64 `json:"usage"`
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
}

// UsageFilters are the RPC parameters. StartDate/EndDate are required
// (YYYY-MM-DD, inclusive). Empty optional fields are omitted from the request
// body so the server applies its NULL default: all API keys, all endpoints, or
// all workspaces the caller may read.
type UsageFilters struct {
	StartDate    string // p_start_date (required)
	EndDate      string // p_end_date (required)
	APIKeyID     string // p_api_key_id (optional UUID)
	EndpointName string // p_endpoint_name (optional)
	Workspace    string // p_workspace (optional; empty = all workspaces)
}

// GetUsageByDimension POSTs the RPC and returns every aggregate bucket in the
// window. Unlike the trace list endpoint there is no cursor: a single request
// returns the full array.
func (s *UsageService) GetUsageByDimension(filters UsageFilters) ([]UsageRow, error) {
	if filters.StartDate == "" || filters.EndDate == "" {
		return nil, fmt.Errorf("start and end date are required")
	}

	// Only send the optional params that are set; PostgREST reads a missing
	// argument as SQL NULL, which the RPC treats as "no filter" — so an empty
	// workspace means all workspaces without any sentinel value.
	body := map[string]any{
		"p_start_date": filters.StartDate,
		"p_end_date":   filters.EndDate,
	}

	if filters.APIKeyID != "" {
		body["p_api_key_id"] = filters.APIKeyID
	}

	if filters.EndpointName != "" {
		body["p_endpoint_name"] = filters.EndpointName
	}

	if filters.Workspace != "" {
		body["p_workspace"] = filters.Workspace
	}

	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/api/v1/rpc/get_usage_by_dimension", s.client.baseURL)

	req, err := http.NewRequest("POST", reqURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)

		switch resp.StatusCode {
		case http.StatusForbidden:
			base := "permission denied: viewing another user's usage needs workspace:usage-read"
			if body := strings.TrimSpace(string(bodyBytes)); body != "" {
				return nil, fmt.Errorf("%s (server: %s)", base, body)
			}

			return nil, errors.New(base)
		default:
			return nil, fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(bodyBytes))
		}
	}

	var rows []UsageRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}

	return rows, nil
}
