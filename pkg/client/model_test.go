package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestFinalizePushPostsImportedMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/workspaces/default/model_registries/registry/models/demo/finalize", r.URL.Path)
		require.Equal(t, "api-key", r.Header.Get("Authorization"))

		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "v1", body["version"])
		require.Equal(t, "2026-06-25T00:00:00Z", body["creation_time"])
		require.Equal(t, "1MB", body["size"])

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))
	err := c.Models.FinalizePush("default", "registry", "demo", &v1.ModelVersion{
		Name:         "v1",
		CreationTime: "2026-06-25T00:00:00Z",
		Size:         "1MB",
	})
	require.NoError(t, err)
}

func TestListKeepsResponseBodyAndReadsContentRange(t *testing.T) {
	// A field this build has no Go field for, to show that the raw body a caller
	// renders is the server's, not a re-encoding of what the structs captured.
	body := `[{"name":"demo","versions":[{"name":"v1","creation_time":"2026-06-25T00:00:00Z","alias":"pet","future_field":7}]}]`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/workspaces/default/model_registries/registry/models", r.URL.Path)
		require.Equal(t, "10", r.URL.Query().Get("limit"))
		require.Equal(t, "20", r.URL.Query().Get("offset"))
		require.Equal(t, "a b&c", r.URL.Query().Get("search"))

		w.Header().Set("Content-Range", "20-20/42")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))

	models, err := c.Models.List("default", "registry", ModelListOptions{Search: "a b&c", Limit: 10, Offset: 20})
	require.NoError(t, err)

	require.Equal(t, body, string(models.Raw))
	require.Equal(t, 20, models.Offset)
	require.NotNil(t, models.Total)
	require.Equal(t, 42, *models.Total)

	require.Len(t, models.Models, 1)
	require.Equal(t, "demo", models.Models[0].Name)
	require.Equal(t, "pet", models.Models[0].Versions[0].Alias)
}

func TestListOmitsUnsetQueryParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.URL.RawQuery)

		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))

	models, err := c.Models.List("default", "registry", ModelListOptions{})
	require.NoError(t, err)
	require.Empty(t, models.Models)
	require.Nil(t, models.Total)
}

// A server that names no range must not be read as one reporting the first
// page: the offset that was asked for is the only thing known about where this
// page sits, and reporting 0 instead would be a wrong number, not a missing one.
func TestListFallsBackToTheRequestedOffset(t *testing.T) {
	tests := []struct {
		name   string
		header string
		offset int
	}{
		{name: "no header", header: "", offset: 20},
		{name: "malformed header", header: "nonsense", offset: 20},
		{name: "empty page names no range", header: "*/42", offset: 20},
		// A header that does name the range wins: the server is the authority on
		// where it actually started.
		{name: "header wins", header: "17-18/42", offset: 17},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.header != "" {
					w.Header().Set("Content-Range", tt.header)
				}

				_, _ = w.Write([]byte(`[{"name":"demo","versions":[]}]`))
			}))
			defer server.Close()

			c := NewClient(server.URL, WithAPIKey("api-key"))

			models, err := c.Models.List("default", "registry", ModelListOptions{Offset: 20})
			require.NoError(t, err)
			require.Equal(t, tt.offset, models.Offset)
		})
	}
}

// A registry refusing to page from an offset is stating a limit. The message
// saying so is the useful part of the response, so it must not be buried in a
// transport-level wrapper.
func TestListSurfacesTheServersMessage(t *testing.T) {
	const message = "This model registry cannot list models this way: " +
		"operation not supported by this model registry"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"` + message + `"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))

	_, err := c.Models.List("default", "registry", ModelListOptions{Offset: 2})
	require.Error(t, err)
	require.Equal(t, message+" (HTTP 400)", err.Error())
}

// A body that carries no message still has to reach the user intact — it is all
// there is to go on.
func TestResponseErrorFallsBackToTheRawBody(t *testing.T) {
	err := responseError(http.StatusBadGateway, []byte("<html>gateway</html>"))
	require.EqualError(t, err, "server returned non-200 status: 502, body: <html>gateway</html>")

	err = responseError(http.StatusInternalServerError, []byte(`{"message":""}`))
	require.EqualError(t, err, `server returned non-200 status: 500, body: {"message":""}`)
}

func TestGetKeepsResponseBody(t *testing.T) {
	body := `{"name":"v1","size":"64 B","alias":"pet","info":{"parameter_count":"7615616512",` +
		`"field_sources":{"parameter_count":"auto"},"missing_fields":["quantization"]}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/workspaces/default/model_registries/registry/models/demo", r.URL.Path)
		require.Equal(t, "v1", r.URL.Query().Get("version"))

		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))

	detail, err := c.Models.Get("default", "registry", "demo", "v1")
	require.NoError(t, err)

	require.Equal(t, body, string(detail.Raw))
	require.Equal(t, "pet", detail.Version.Alias)
	require.Equal(t, "7615616512", detail.Version.Info.ParameterCount)
	require.Equal(t, []string{"quantization"}, detail.Version.Info.MissingFields)
}

func TestGetOmitsLatestVersionFromQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.URL.RawQuery)

		_, _ = w.Write([]byte(`{"name":"v1"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))

	detail, err := c.Models.Get("default", "registry", "demo", v1.LatestVersion)
	require.NoError(t, err)
	require.Equal(t, "v1", detail.Version.Name)
}

func TestParseContentRange(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		offset      int
		offsetKnown bool
		total       *int
	}{
		{name: "counted page", header: "0-9/57", offset: 0, offsetKnown: true, total: ptr(57)},
		{name: "later page", header: "20-29/57", offset: 20, offsetKnown: true, total: ptr(57)},
		// A public registry reports the page it served but cannot count the
		// catalogue behind it. The range is still authoritative.
		{name: "uncountable total", header: "0-9/*", offset: 0, offsetKnown: true, total: nil},
		// The converse: a counted result whose page names no range.
		{name: "empty page", header: "*/0", offset: 0, offsetKnown: false, total: ptr(0)},
		{name: "absent header", header: "", offset: 0, offsetKnown: false, total: nil},
		{name: "malformed header", header: "nonsense", offset: 0, offsetKnown: false, total: nil},
		{name: "non-numeric range", header: "a-b/57", offset: 0, offsetKnown: false, total: ptr(57)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offset, offsetKnown, total := parseContentRange(tt.header)
			require.Equal(t, tt.offset, offset)
			require.Equal(t, tt.offsetKnown, offsetKnown)

			if tt.total == nil {
				require.Nil(t, total)

				return
			}

			require.NotNil(t, total)
			require.Equal(t, *tt.total, *total)
		})
	}
}

func ptr(value int) *int {
	return &value
}
