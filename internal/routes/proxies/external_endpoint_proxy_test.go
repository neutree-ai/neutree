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
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestHandleTestConnectivity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	deps := &Dependencies{Storage: new(storageMocks.MockStorage)}

	tests := []struct {
		name           string
		body           string
		withAuth       bool
		mockServer     func() *httptest.Server
		wantHTTPStatus int
		wantSuccess    bool
		wantModels     []string
		wantErrContain string
	}{
		{
			name:           "missing upstream and endpoint_ref",
			body:           `{}`,
			wantHTTPStatus: http.StatusBadRequest,
			wantErrContain: "either upstream.url or endpoint_ref is required",
		},
		{
			name:           "invalid json",
			body:           `{invalid`,
			wantHTTPStatus: http.StatusBadRequest,
			wantErrContain: "invalid request body",
		},
		{
			name:           "both upstream and endpoint_ref",
			body:           `{"upstream":{"url":"http://localhost"},"endpoint_ref":"my-ep","workspace":"default"}`,
			wantHTTPStatus: http.StatusBadRequest,
			wantErrContain: "mutually exclusive",
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
			name:     "successful with models",
			withAuth: true,
			mockServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "/v1/models", r.URL.Path)
					assert.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"object": "list",
						"data": []map[string]any{
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
					json.NewEncoder(w).Encode(map[string]any{
						"object": "list",
						"data": []map[string]any{
							{"id": "test-model", "object": "model"},
						},
					})
				}))
			},
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    true,
			wantModels:     []string{"test-model"},
		},
		{
			name: "empty model list",
			mockServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"object": "list",
						"data":   []map[string]any{},
					})
				}))
			},
			wantHTTPStatus: http.StatusOK,
			wantErrContain: "no models found",
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
				if tt.withAuth {
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

			handler := handleTestConnectivity(deps)
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

func TestHandleTestConnectivityEndpointRef(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Start mock server to act as internal endpoint
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/test-ws/my-endpoint/v1/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "my-local-model"},
			},
		})
	}))
	defer mockServer.Close()

	// Parse mock server URL for cluster dashboard URL
	dashboardURL := mockServer.URL

	mockStorage := new(storageMocks.MockStorage)
	deps := &Dependencies{Storage: mockStorage}

	// Mock ListEndpoint
	mockStorage.On("ListEndpoint", mock.MatchedBy(func(opt storage.ListOption) bool {
		return len(opt.Filters) == 2
	})).Return([]v1.Endpoint{
		{
			Spec: &v1.EndpointSpec{
				Cluster: "my-cluster",
			},
		},
	}, nil)

	// Mock ListCluster — return dashboard URL pointing at mock server
	mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
		return len(opt.Filters) == 2
	})).Return([]v1.Cluster{
		{
			Spec: &v1.ClusterSpec{
				Type: v1.KubernetesClusterType,
			},
			Status: &v1.ClusterStatus{
				DashboardURL: dashboardURL,
			},
		},
	}, nil)

	epRef := "my-endpoint"
	ws := "test-ws"
	reqObj := testConnectivityRequest{
		EndpointRef: &epRef,
		Workspace:   &ws,
	}

	b, _ := json.Marshal(reqObj)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/external_endpoints/test_connectivity", strings.NewReader(string(b)))
	c.Request.Header.Set("Content-Type", "application/json")

	handler := handleTestConnectivity(deps)
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp testConnectivityResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.True(t, resp.Success)
	assert.Equal(t, []string{"my-local-model"}, resp.Models)

	mockStorage.AssertExpectations(t)
}

func TestHandleTestConnectivityEndpointRefNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := new(storageMocks.MockStorage)
	deps := &Dependencies{Storage: mockStorage}

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)

	epRef := "nonexistent"
	ws := "default"
	reqObj := testConnectivityRequest{
		EndpointRef: &epRef,
		Workspace:   &ws,
	}

	b, _ := json.Marshal(reqObj)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/external_endpoints/test_connectivity", strings.NewReader(string(b)))
	c.Request.Header.Set("Content-Type", "application/json")

	handler := handleTestConnectivity(deps)
	handler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp testConnectivityResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "not found")
}

func TestHandleTestConnectivityCredentialBackfill(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		requestCredential string
		storedCredential  string
		requestURL        string
		storedURL         string
		expectBackfill    bool
		wantAuthHeader    string
	}{
		{
			name:              "backfill credential when empty and EE name provided",
			requestCredential: "",
			storedCredential:  "sk-stored-secret",
			requestURL:        "/v1",
			storedURL:         "/v1",
			expectBackfill:    true,
			wantAuthHeader:    "Bearer sk-stored-secret",
		},
		{
			name:              "no backfill when upstream URL does not match",
			requestCredential: "",
			storedCredential:  "sk-stored-secret",
			requestURL:        "/v1",
			storedURL:         "/v2",
			expectBackfill:    true,
			wantAuthHeader:    "",
		},
		{
			name:              "no backfill when new credential provided",
			requestCredential: "sk-new-key",
			storedCredential:  "sk-stored-secret",
			requestURL:        "/v1",
			storedURL:         "/v1",
			expectBackfill:    false,
			wantAuthHeader:    "Bearer sk-new-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedAuth string
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{"id": "test-model"},
					},
				})
			}))
			defer mockServer.Close()

			mockStorage := new(storageMocks.MockStorage)
			deps := &Dependencies{Storage: mockStorage}

			if tt.expectBackfill {
				mockStorage.On("ListExternalEndpoint", mock.MatchedBy(func(opt storage.ListOption) bool {
					return len(opt.Filters) == 2
				})).Return([]v1.ExternalEndpoint{
					{
						Spec: &v1.ExternalEndpointSpec{
							Upstreams: []v1.ExternalEndpointUpstreamEntry{
								{
									Upstream: &v1.ExternalEndpointUpstreamSpec{
										URL: mockServer.URL + tt.storedURL,
									},
									Auth: &v1.ExternalEndpointAuthSpec{
										Type:       "bearer",
										Credential: tt.storedCredential,
									},
									ModelMapping: map[string]string{"fast": "gpt-4o-mini"},
								},
							},
						},
					},
				}, nil)
			}

			eeName := "my-ee"
			ws := "default"
			reqObj := testConnectivityRequest{
				Upstream: &v1.ExternalEndpointUpstreamSpec{
					URL: mockServer.URL + tt.requestURL,
				},
				Auth: &v1.ExternalEndpointAuthSpec{
					Type:       "bearer",
					Credential: tt.requestCredential,
				},
				Name:      &eeName,
				Workspace: &ws,
			}

			b, _ := json.Marshal(reqObj)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/external_endpoints/test_connectivity", strings.NewReader(string(b)))
			c.Request.Header.Set("Content-Type", "application/json")

			handler := handleTestConnectivity(deps)
			handler(c)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tt.wantAuthHeader, receivedAuth)

			mockStorage.AssertExpectations(t)
		})
	}
}

func TestParseModelIDs(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		want       []string
		wantErr    bool
		errContain string
	}{
		{
			name: "valid response",
			body: `{"data":[{"id":"model-a"},{"id":"model-b"}]}`,
			want: []string{"model-a", "model-b"},
		},
		{
			name:       "empty data",
			body:       `{"data":[]}`,
			wantErr:    true,
			errContain: "no models found",
		},
		{
			name:       "invalid json",
			body:       `not json`,
			wantErr:    true,
			errContain: "invalid JSON",
		},
		{
			name:       "missing data field",
			body:       `{"object":"list"}`,
			wantErr:    true,
			errContain: "missing \"data\" field",
		},
		{
			name: "skip empty ids",
			body: `{"data":[{"id":"model-a"},{"id":""}]}`,
			want: []string{"model-a"},
		},
		{
			name:       "all empty ids",
			body:       `{"data":[{"id":""},{"id":""}]}`,
			wantErr:    true,
			errContain: "no models found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseModelIDs([]byte(tt.body))
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContain)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
