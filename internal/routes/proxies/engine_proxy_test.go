package proxies

import (
	"encoding/json"
	"errors"
	"io"
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

func TestEngineReferenceProtection(test *testing.T) {
	cases := []struct {
		name          string
		body          string
		version       string
		endpointCount int
		countError    error
		wantStatus    int
	}{
		{
			name:          "soft delete of referenced engine",
			body:          `{"metadata":{"deletion_timestamp":"2026-10-20T00:00:00Z"}}`,
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "force annotation cannot bypass references",
			body:          `{"metadata":{"deletion_timestamp":"2026-10-20T00:00:00Z","annotations":{"neutree.ai/force-delete":"true"}}}`,
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "stored identity overrides forged payload identity",
			body:          `{"metadata":{"name":"unreferenced","workspace":"other","deletion_timestamp":"2026-10-20T00:00:00Z"}}`,
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "renaming a referenced engine cannot bypass protection",
			body:          `{"metadata":{"name":"renamed","workspace":"workspace-a"}}`,
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "moving a referenced engine cannot bypass protection",
			body:          `{"metadata":{"name":"custom-engine","workspace":"workspace-b"}}`,
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "spec only removal of referenced version",
			body:          `{"spec":{"versions":[{"version":"v2.0.0"}]}}`,
			version:       "v1.0.0",
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "empty versions cannot bypass references",
			body:          `{"spec":{"versions":[]}}`,
			version:       "v1.0.0",
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "omitted versions replace the entire spec",
			body:          `{"spec":{"supported_tasks":["text-generation"]}}`,
			version:       "v1.0.0",
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "null versions cannot bypass references",
			body:          `{"spec":{"versions":null}}`,
			version:       "v1.0.0",
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "null spec cannot bypass references",
			body:          `{"spec":null}`,
			endpointCount: 1,
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:       "unused version can be removed while referenced version remains",
			body:       `{"spec":{"versions":[{"version":"v1.0.0"}]}}`,
			version:    "v2.0.0",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "deletion succeeds after references are released",
			body:       `{"metadata":{"deletion_timestamp":"2026-10-20T00:00:00Z"}}`,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "dependency lookup failure fails closed without leaking details",
			body:       `{"metadata":{"deletion_timestamp":"2026-10-20T00:00:00Z"}}`,
			countError: errors.New("secret endpoint customer-model at internal-database:5432"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			store := storageMocks.NewMockStorage(test)
			store.On("ListEngine", storage.ListOption{Filters: []storage.Filter{
				{Column: "id", Operator: "eq", Value: "7"},
			}}).Return([]v1.Engine{engineReferenceFixture()}, nil).Once()
			filters := []storage.Filter{
				{Column: "metadata->>workspace", Operator: "eq", Value: "workspace-a"},
				{Column: "spec->engine->>engine", Operator: "eq", Value: "custom-engine"},
			}
			if testCase.version != "" {
				filters = append(filters, storage.Filter{
					Column: "spec->engine->>version", Operator: "eq", Value: testCase.version,
				})
			}
			store.On("Count", storage.ENDPOINT_TABLE, filters).Return(testCase.endpointCount, testCase.countError).Once()

			response, forwarded := serveEngineProxyRequest(test, store, http.MethodPatch, "/engines?id=eq.7", testCase.body)
			assert.Equal(test, testCase.wantStatus, response.Code)
			assert.Equal(test, testCase.wantStatus == http.StatusNoContent, forwarded)
			if testCase.endpointCount > 0 {
				assert.Equal(test, "Neutree", response.Header().Get("X-Powered-By"))
				assert.Contains(test, response.Body.String(), `"code":"10133"`)
				assert.NotContains(test, response.Body.String(), "workspace-a")
				assert.NotContains(test, response.Body.String(), "custom-engine")
			}
			assert.NotContains(test, response.Body.String(), "customer-model")
			assert.NotContains(test, response.Body.String(), "internal-database")
			store.AssertNotCalled(test, "ListEndpoint", mock.Anything)
		})
	}
}

func TestEngineReferenceProtectionTargetResolution(test *testing.T) {
	cases := []struct {
		name       string
		query      string
		engines    []v1.Engine
		lookupErr  error
		wantLookup bool
		wantStatus int
	}{
		{name: "missing selectors", wantStatus: http.StatusBadRequest},
		{name: "duplicate selectors", query: "?id=eq.7&id=eq.8", wantStatus: http.StatusBadRequest},
		{name: "paginated update", query: "?id=eq.7&limit=1", wantStatus: http.StatusBadRequest},
		{name: "offset update", query: "?id=eq.7&offset=1", wantStatus: http.StatusBadRequest},
		{name: "logical selectors", query: "?and=(id.eq.7)", wantStatus: http.StatusBadRequest},
		{name: "missing target", query: "?id=eq.7", wantLookup: true, wantStatus: http.StatusBadRequest},
		{
			name: "ambiguous target", query: "?id=in.(7,8)",
			engines: []v1.Engine{engineReferenceFixture(), engineReferenceFixture()}, wantLookup: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "lookup failure", query: "?id=eq.7", lookupErr: errors.New("private storage details"),
			wantLookup: true, wantStatus: http.StatusInternalServerError,
		},
		{
			name: "malformed stored target", query: "?id=eq.7", engines: []v1.Engine{{ID: 7}},
			wantLookup: true, wantStatus: http.StatusInternalServerError,
		},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			store := storageMocks.NewMockStorage(test)
			if testCase.wantLookup {
				store.On("ListEngine", mock.Anything).Return(testCase.engines, testCase.lookupErr).Once()
			}

			response, forwarded := serveEngineProxyRequest(test, store, http.MethodPatch, "/engines"+testCase.query,
				`{"metadata":{"name":"custom-engine","workspace":"workspace-a","deletion_timestamp":"2026-10-20T00:00:00Z"}}`)
			assert.Equal(test, testCase.wantStatus, response.Code)
			assert.False(test, forwarded)
			assert.NotContains(test, response.Body.String(), "private storage details")
			store.AssertNotCalled(test, "Count", mock.Anything, mock.Anything)
		})
	}
}

func TestEngineReferenceProtectionPreservesOtherOperations(test *testing.T) {
	cases := []struct {
		name       string
		method     string
		body       string
		wantLookup bool
	}{
		{name: "read", method: http.MethodGet},
		{name: "create", method: http.MethodPost, body: `{"metadata":{"name":"new-engine"}}`},
		{name: "status update", method: http.MethodPatch, body: `{"status":{"phase":"Created"}}`},
		{
			name: "metadata update", method: http.MethodPatch,
			body: `{"metadata":{"name":"custom-engine","workspace":"workspace-a","display_name":"Engine"}}`, wantLookup: true,
		},
		{
			name: "version update preserves references", method: http.MethodPatch,
			body: `{"spec":{"versions":[{"version":"v1.0.0"},{"version":"v2.0.0"},{"version":"v3.0.0"}]}}`, wantLookup: true,
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			store := storageMocks.NewMockStorage(test)
			if testCase.wantLookup {
				store.On("ListEngine", mock.Anything).Return([]v1.Engine{engineReferenceFixture()}, nil).Once()
			}
			response, forwarded := serveEngineProxyRequest(test, store, testCase.method, "/engines?id=eq.7", testCase.body)
			assert.Equal(test, http.StatusNoContent, response.Code)
			assert.True(test, forwarded)
		})
	}
}

func engineReferenceFixture() v1.Engine {
	return v1.Engine{
		ID:       7,
		Metadata: &v1.Metadata{Name: "custom-engine", Workspace: "workspace-a"},
		Spec: &v1.EngineSpec{Versions: []*v1.EngineVersion{
			{Version: "v1.0.0"}, {Version: "v2.0.0"},
		}},
	}
}

func serveEngineProxyRequest(test *testing.T, store storage.Storage, method, path, body string) (*httptest.ResponseRecorder, bool) {
	test.Helper()
	gin.SetMode(gin.TestMode)
	forwarded := false
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, upstreamRequest *http.Request) {
		forwarded = true
		assert.Equal(test, "/engines", upstreamRequest.URL.Path)
		assert.Equal(test, "Bearer engine-editor-without-endpoint-read", upstreamRequest.Header.Get("Authorization"))
		if body != "" {
			forwardedBody, err := io.ReadAll(upstreamRequest.Body)
			require.NoError(test, err)
			expectedDecoder := json.NewDecoder(strings.NewReader(body))
			expectedDecoder.UseNumber()
			forwardedDecoder := json.NewDecoder(strings.NewReader(string(forwardedBody)))
			forwardedDecoder.UseNumber()

			var expectedPayload, forwardedPayload interface{}
			require.NoError(test, expectedDecoder.Decode(&expectedPayload))
			require.NoError(test, forwardedDecoder.Decode(&forwardedPayload))
			assert.Equal(test, expectedPayload, forwardedPayload)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	test.Cleanup(upstream.Close)

	router := gin.New()
	RegisterEngineRoutes(router.Group(""), []gin.HandlerFunc{func(context *gin.Context) {
		context.Set("postgrest_token", "engine-editor-without-endpoint-read")
		context.Next()
	}}, &Dependencies{Storage: store, StorageAccessURL: upstream.URL})
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := newCloseNotifyRecorder()
	router.ServeHTTP(response, request)

	return response.ResponseRecorder, forwarded
}
