package proxies

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestHandleTestConnectivity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		body           string
		mockServer     func() *httptest.Server
		wantHTTPStatus int
		wantSuccess    bool
		wantModels     []string
		wantErrContain string
	}{
		{
			name:           "missing body",
			body:           `{}`,
			wantHTTPStatus: http.StatusBadRequest,
			wantErrContain: "upstream.url is required",
		},
		{
			name:           "invalid json",
			body:           `{invalid`,
			wantHTTPStatus: http.StatusBadRequest,
			wantErrContain: "invalid request body",
		},
		{
			name: "upstream returns 401",
			mockServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, `{"error":"invalid api key"}`)
				}))
			},
			wantHTTPStatus: http.StatusOK,
			wantErrContain: "upstream returned HTTP 401",
		},
		{
			name: "successful with models",
			mockServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "/v1/models", r.URL.Path)
					assert.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"object": "list",
						"data": []map[string]interface{}{
							{"id": "gpt-4o", "object": "model"},
							{"id": "gpt-4o-mini", "object": "model"},
						},
					})
				}))
			},
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    true,
			wantModels:     []string{"gpt-4o", "gpt-4o-mini"},
		},
		{
			name: "successful without auth",
			mockServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Empty(t, r.Header.Get("Authorization"))
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"object": "list",
						"data":   []map[string]interface{}{},
					})
				}))
			},
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    true,
		},
		{
			name: "connection refused",
			body: `{"upstream":{"url":"http://127.0.0.1:1"}}`,
			// port 1 should be unreachable
			wantHTTPStatus: http.StatusOK,
			wantErrContain: "connection failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body

			if tt.mockServer != nil {
				server := tt.mockServer()
				defer server.Close()

				reqObj := testConnectivityRequest{
					Upstream: &v1.ExternalEndpointUpstreamSpec{
						URL: server.URL + "/v1",
					},
				}
				// Add auth for the "successful with models" case
				if tt.wantModels != nil {
					reqObj.Auth = &v1.ExternalEndpointAuthSpec{
						Type:       "bearer",
						Credential: "sk-test",
					}
				}
				b, _ := json.Marshal(reqObj)
				body = string(b)
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/external_endpoints/test_connectivity", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			handler := handleTestConnectivity()
			handler(c)

			assert.Equal(t, tt.wantHTTPStatus, w.Code)

			var resp testConnectivityResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

			assert.Equal(t, tt.wantSuccess, resp.Success)

			if tt.wantErrContain != "" {
				assert.Contains(t, resp.Error, tt.wantErrContain)
			}

			if tt.wantModels != nil {
				assert.Equal(t, tt.wantModels, resp.Models)
			}

			if tt.wantSuccess {
				assert.GreaterOrEqual(t, resp.LatencyMs, int64(0))
			}
		})
	}
}

func TestParseModelIDs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "valid response",
			body: `{"data":[{"id":"model-a"},{"id":"model-b"}]}`,
			want: []string{"model-a", "model-b"},
		},
		{
			name: "empty data",
			body: `{"data":[]}`,
			want: []string{},
		},
		{
			name: "invalid json",
			body: `not json`,
			want: nil,
		},
		{
			name: "skip empty ids",
			body: `{"data":[{"id":"model-a"},{"id":""}]}`,
			want: []string{"model-a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseModelIDs([]byte(tt.body))
			assert.Equal(t, tt.want, got)
		})
	}
}
