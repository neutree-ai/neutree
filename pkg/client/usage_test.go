package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetUsageByDimensionBuildsBodyAndParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/rpc/get_usage_by_dimension", r.URL.Path)
		require.Equal(t, "api-key", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "2026-07-01", body["p_start_date"])
		require.Equal(t, "2026-07-15", body["p_end_date"])
		require.Equal(t, "key-1", body["p_api_key_id"])
		require.Equal(t, "ep-1", body["p_endpoint_name"])
		require.Equal(t, "default", body["p_workspace"])

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]UsageRow{
			{Date: "2026-07-15", APIKeyID: "key-1", Usage: ptrInt64(3)},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))

	rows, err := c.Usage.GetUsageByDimension(UsageFilters{
		StartDate:    "2026-07-01",
		EndDate:      "2026-07-15",
		APIKeyID:     "key-1",
		EndpointName: "ep-1",
		Workspace:    "default",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "key-1", rows[0].APIKeyID)
	require.Equal(t, int64(3), *rows[0].Usage)
}

func TestGetUsageByDimensionOmitsEmptyOptionalParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		// Only the two required dates are present; empty optionals are omitted so
		// PostgREST reads them as SQL NULL (no filter / all workspaces).
		_, hasKey := body["p_api_key_id"]
		_, hasEndpoint := body["p_endpoint_name"]
		_, hasWorkspace := body["p_workspace"]
		require.False(t, hasKey)
		require.False(t, hasEndpoint)
		require.False(t, hasWorkspace)

		_ = json.NewEncoder(w).Encode([]UsageRow{})
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("k"))

	_, err := c.Usage.GetUsageByDimension(UsageFilters{StartDate: "2026-07-01", EndDate: "2026-07-15"})
	require.NoError(t, err)
}

func TestGetUsageByDimensionRequiresDates(t *testing.T) {
	c := NewClient("http://example.com", WithAPIKey("k"))

	_, err := c.Usage.GetUsageByDimension(UsageFilters{StartDate: "2026-07-01"})
	require.ErrorContains(t, err, "required")
}

func TestGetUsageByDimensionErrorStatusMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "nope")
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("k"))

	_, err := c.Usage.GetUsageByDimension(UsageFilters{StartDate: "2026-07-01", EndDate: "2026-07-15"})
	require.ErrorContains(t, err, "workspace:usage-read")
	require.ErrorContains(t, err, "nope") // server body preserved for diagnosis
}

func ptrInt64(n int64) *int64 { return &n }
