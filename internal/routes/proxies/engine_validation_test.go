package proxies

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestEngineValidationInput(test *testing.T) {
	cases := []struct {
		name          string
		query         string
		body          string
		wantLookup    bool
		wantSpec      bool
		wantMetadata  bool
		wantOperation engineValidationOperation
		wantVersions  []string
		wantName      string
		wantWorkspace string
	}{
		{
			name: "replace spec without changing metadata", body: `{"spec":{"versions":[{"version":"v2.0.0"}]}}`,
			wantLookup: true, wantSpec: true, wantOperation: engineValidationPatch,
			wantVersions: []string{"v2.0.0"}, wantName: "custom-engine", wantWorkspace: "workspace-a",
		},
		{
			name: "replace metadata without merging omitted workspace", body: `{"metadata":{"name":"renamed"}}`,
			wantLookup: true, wantMetadata: true, wantOperation: engineValidationPatch,
			wantVersions: []string{"v1.0.0", "v2.0.0"}, wantName: "renamed",
		},
		{
			name: "selected absent spec becomes null", query: "&columns=spec", body: `{}`,
			wantLookup: true, wantSpec: true, wantOperation: engineValidationPatch,
			wantName: "custom-engine", wantWorkspace: "workspace-a",
		},
		{
			name: "excluded spec does not change stored versions", query: "&columns=metadata",
			body:       `{"metadata":{"name":"custom-engine","workspace":"workspace-a"},"spec":1e400}`,
			wantLookup: true, wantMetadata: true, wantOperation: engineValidationPatch,
			wantVersions: []string{"v1.0.0", "v2.0.0"}, wantName: "custom-engine", wantWorkspace: "workspace-a",
		},
		{
			name: "soft delete still resolves the stored engine", body: `{"metadata":{"deletion_timestamp":"2026-10-20T00:00:00Z"}}`,
			wantLookup: true, wantMetadata: true, wantOperation: engineValidationSoftDelete,
			wantVersions: []string{"v1.0.0", "v2.0.0"},
		},
		{
			name: "status update needs no reference lookup", body: `{"status":{"phase":"Running"}}`,
			wantOperation: engineValidationPatch,
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			current := engineReferenceFixture()
			original, err := json.Marshal(current)
			require.NoError(test, err)
			store := storageMocks.NewMockStorage(test)
			if testCase.wantLookup {
				store.On("ListEngine", storage.ListOption{Filters: []storage.Filter{
					{Column: "id", Operator: "eq", Value: "7"},
				}}).Return([]v1.Engine{current}, nil).Once()
			}
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodPatch, "/engines?id=eq.7"+testCase.query, strings.NewReader(testCase.body))
			context.Request.ContentLength = 1
			context.Request.Header.Set("Content-Length", "1")

			input, validationErr := readEngineValidationInput(context)
			require.Nil(test, validationErr)
			assert.Equal(test, testCase.wantOperation, input.Operation)
			assert.Equal(test, testCase.wantSpec, input.HasSpec)
			assert.Equal(test, testCase.wantMetadata, input.HasMetadata)
			patch, err := json.Marshal(input.Patch)
			require.NoError(test, err)
			restored, err := io.ReadAll(context.Request.Body)
			require.NoError(test, err)
			assert.Equal(test, testCase.body, string(restored))
			assert.Equal(test, int64(len(testCase.body)), context.Request.ContentLength)
			assert.Equal(test, strconv.Itoa(len(testCase.body)), context.Request.Header.Get("Content-Length"))

			require.Nil(test, prepareEngineValidationInput(store, input))
			if !testCase.wantLookup {
				assert.Nil(test, input.Current)
				assert.Nil(test, input.New)
				return
			}
			require.Equal(test, current, *input.Current)
			require.NotNil(test, input.New)
			assert.NotSame(test, input.Current, input.New)
			assert.Equal(test, testCase.wantName, input.New.GetName())
			assert.Equal(test, testCase.wantWorkspace, input.New.GetWorkspace())
			var versions []string
			if input.New.Spec != nil {
				for _, version := range input.New.Spec.Versions {
					versions = append(versions, version.Version)
				}
			}
			assert.Equal(test, testCase.wantVersions, versions)
			if testCase.wantSpec {
				assert.Equal(test, input.Patch.Spec, input.New.Spec)
			} else {
				assert.Equal(test, current.Spec, input.New.Spec)
			}
			if testCase.wantMetadata {
				assert.Equal(test, input.Patch.Metadata, input.New.Metadata)
			} else {
				assert.Equal(test, current.Metadata, input.New.Metadata)
			}
			unchanged, err := json.Marshal(current)
			require.NoError(test, err)
			assert.Equal(test, original, unchanged)
			unchangedPatch, err := json.Marshal(input.Patch)
			require.NoError(test, err)
			assert.Equal(test, patch, unchangedPatch)
			store.AssertNotCalled(test, "Count", mock.Anything, mock.Anything)
		})
	}
}

func TestEngineValidationSkipsOtherMethods(test *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		test.Run(method, func(test *testing.T) {
			store := storageMocks.NewMockStorage(test)
			router := gin.New()
			router.Handle(method, "/engines", validateEngineRequest(store), func(context *gin.Context) {
				context.Status(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(method, "/engines", nil))
			assert.Equal(test, http.StatusNoContent, response.Code)
		})
	}
}

func TestEngineValidationIgnoresExcludedReferences(test *testing.T) {
	body := `{"status":{"phase":"Running"},"spec":1e400,"metadata":{"deletion_timestamp":"2026-10-20T00:00:00Z"}}`
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPatch, "/engines?columns=status", strings.NewReader(body))
	input, validationErr := readEngineValidationInput(context)
	require.Nil(test, validationErr)
	assert.Equal(test, engineValidationPatch, input.Operation)
	assert.False(test, input.HasSpec)
	assert.False(test, input.HasMetadata)
	require.Nil(test, prepareEngineValidationInput(nil, input))
	assert.Nil(test, input.Current)
	assert.Nil(test, input.New)
	require.Nil(test, validateEngineInput(nil, input))
	response, forwarded := serveEngineProxyRequest(test, nil, http.MethodPatch, "/engines?columns=status", body)
	assert.Equal(test, http.StatusNoContent, response.Code)
	assert.True(test, forwarded)
}

func TestEngineValidationCombinedReferenceChanges(test *testing.T) {
	current := engineReferenceFixture()
	original, err := json.Marshal(current)
	require.NoError(test, err)
	next := current
	next.Metadata = &v1.Metadata{Name: "renamed", Workspace: "workspace-b"}
	next.Spec = &v1.EngineSpec{Versions: []*v1.EngineVersion{{Version: "v2.0.0"}}}
	store := storageMocks.NewMockStorage(test)
	store.On("Count", storage.ENDPOINT_TABLE, []storage.Filter{
		{Column: "metadata->>workspace", Operator: "eq", Value: "workspace-a"},
		{Column: "spec->engine->>engine", Operator: "eq", Value: "custom-engine"},
	}).Return(1, nil).Once()
	validationErr := validateEngineInput(store, &engineValidationInput{
		Current: &current, New: &next, HasSpec: true, HasMetadata: true, Operation: engineValidationPatch,
	})
	require.NotNil(test, validationErr)
	assert.Equal(test, "10133", validationErr.Code)
	unchanged, err := json.Marshal(current)
	require.NoError(test, err)
	assert.Equal(test, original, unchanged)
}

func TestEngineReferenceProtectionInvalidPayload(test *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "empty body"},
		{name: "malformed JSON", body: `{"spec":`},
		{name: "array body", body: `[]`},
		{name: "null body", body: `null`},
		{name: "invalid spec", body: `{"spec":"invalid"}`},
		{name: "invalid version", body: `{"spec":{"versions":[false]}}`},
		{name: "invalid versions array", body: `{"spec":{"versions":false}}`},
		{name: "invalid metadata", body: `{"metadata":"invalid"}`},
		{name: "invalid metadata reference field", body: `{"metadata":{"name":17}}`},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			store := storageMocks.NewMockStorage(test)
			response, forwarded := serveEngineProxyRequest(test, store, http.MethodPatch, "/engines?id=eq.7", testCase.body)
			assert.Equal(test, http.StatusBadRequest, response.Code)
			assert.False(test, forwarded)
		})
	}
}

func TestEngineReferenceProtectionChecksEveryRemovedVersion(test *testing.T) {
	store := storageMocks.NewMockStorage(test)
	store.On("ListEngine", mock.Anything).Return([]v1.Engine{engineReferenceFixture()}, nil).Once()
	for _, version := range []string{"v1.0.0", "v2.0.0"} {
		count := 0
		if version == "v2.0.0" {
			count = 1
		}
		store.On("Count", storage.ENDPOINT_TABLE, []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: "workspace-a"},
			{Column: "spec->engine->>engine", Operator: "eq", Value: "custom-engine"},
			{Column: "spec->engine->>version", Operator: "eq", Value: version},
		}).Return(count, nil).Once()
	}

	response, forwarded := serveEngineProxyRequest(test, store, http.MethodPatch, "/engines?id=eq.7", `{"spec":{"versions":[]}}`)
	assert.Equal(test, http.StatusBadRequest, response.Code)
	assert.False(test, forwarded)
}

func TestEngineReferenceProtectionVersionEdgeCases(test *testing.T) {
	cases := []struct {
		name    string
		engine  v1.Engine
		body    string
		columns string
	}{
		{
			name:   "non-version query parameters do not alter target lookup",
			engine: engineReferenceFixture(),
			body:   `{"spec":{"versions":[{"version":"v1.0.0"},{"version":"v2.0.0"}]}}`,
		},
		{
			name: "missing stored spec",
			engine: v1.Engine{
				ID: 7, Metadata: &v1.Metadata{Name: "custom-engine", Workspace: "workspace-a"},
			},
			body: `{"spec":{"versions":[{"version":"v1.0.0"}]}}`,
		},
		{
			name: "nil versions do not panic or remove retained versions",
			engine: v1.Engine{
				ID: 7, Metadata: &v1.Metadata{Name: "custom-engine", Workspace: "workspace-a"},
				Spec: &v1.EngineSpec{Versions: []*v1.EngineVersion{nil, {Version: "v1.0.0"}}},
			},
			body: `{"spec":{"versions":[null,{"version":"v1.0.0"}]}}`,
		},
		{
			name:   "excluded invalid metadata does not affect spec validation",
			engine: engineReferenceFixture(),
			body:   `{"spec":{"versions":[{"version":"v1.0.0"},{"version":"v2.0.0"}]},"metadata":"ignored"}`,
		},
		{
			name:   "excluded deletion metadata does not delete the engine",
			engine: engineReferenceFixture(),
			body:   `{"spec":{"versions":[{"version":"v1.0.0"},{"version":"v2.0.0"}]},"metadata":{"deletion_timestamp":"2026-10-20T00:00:00Z"}}`,
		},
		{
			name:    "excluded invalid spec does not affect metadata validation",
			engine:  engineReferenceFixture(),
			body:    `{"metadata":{"name":"custom-engine","workspace":"workspace-a"},"spec":"ignored"}`,
			columns: "metadata",
		},
		{
			name:   "schema keys are not interpreted as Engine reference fields",
			engine: engineReferenceFixture(),
			body:   `{"spec":{"versions":[{"version":"v1.0.0","values_schema":{"Versions":{},"Version":{}}},{"version":"v2.0.0"}]}}`,
		},
		{
			name:    "annotation keys are not interpreted as metadata reference fields",
			engine:  engineReferenceFixture(),
			body:    `{"metadata":{"name":"custom-engine","workspace":"workspace-a","annotations":{"Name":"user-data","Workspace":"user-data"}}}`,
			columns: "metadata",
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			store := storageMocks.NewMockStorage(test)
			store.On("ListEngine", storage.ListOption{Filters: []storage.Filter{
				{Column: "id", Operator: "eq", Value: "7"},
			}}).Return([]v1.Engine{testCase.engine}, nil).Once()
			columns := testCase.columns
			if columns == "" {
				columns = "spec"
			}
			response, forwarded := serveEngineProxyRequest(test, store, http.MethodPatch,
				"/engines?id=eq.7&select=id&columns="+columns+"&order=id", testCase.body)
			assert.Equal(test, http.StatusNoContent, response.Code)
			assert.True(test, forwarded)
		})
	}
}

func TestEngineReferenceProtectionStorageUnavailable(test *testing.T) {
	response, forwarded := serveEngineProxyRequest(test, nil, http.MethodPatch, "/engines?id=eq.7", `{"spec":null}`)
	assert.Equal(test, http.StatusInternalServerError, response.Code)
	assert.False(test, forwarded)
}

func TestEngineReferenceProtectionExplicitColumns(test *testing.T) {
	cases := []struct {
		name       string
		query      string
		body       string
		wantCheck  bool
		wantStatus int
	}{
		{
			name: "explicit missing spec is not a harmless empty patch", query: "?id=eq.7&columns=spec", body: `{}`,
			wantCheck: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "quoted missing spec", query: "?id=eq.7&columns=%22spec%22", body: `{}`,
			wantCheck: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "explicit missing metadata", query: "?id=eq.7&columns=metadata", body: `{}`,
			wantCheck: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "columns excluding spec do not remove versions", query: "?id=eq.7&columns=status",
			body: `{"spec":null,"status":{"phase":"Created"}}`, wantStatus: http.StatusNoContent,
		},
		{
			name: "excluded large number is not decoded as float64", query: "?id=eq.7&columns=status",
			body: `{"spec":1e400,"status":{"phase":"Created"}}`, wantStatus: http.StatusNoContent,
		},
		{
			name: "repeated explicit columns rejected", query: "?id=eq.7&columns=status&columns=spec", body: `{}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "unsupported column syntax rejected", query: "?id=eq.7&columns=%22s%5Cpec%22", body: `{}`,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			store := storageMocks.NewMockStorage(test)
			if testCase.wantCheck {
				store.On("ListEngine", mock.Anything).Return([]v1.Engine{engineReferenceFixture()}, nil).Once()
				store.On("Count", storage.ENDPOINT_TABLE, []storage.Filter{
					{Column: "metadata->>workspace", Operator: "eq", Value: "workspace-a"},
					{Column: "spec->engine->>engine", Operator: "eq", Value: "custom-engine"},
				}).Return(1, nil).Once()
			}
			response, forwarded := serveEngineProxyRequest(test, store, http.MethodPatch, "/engines"+testCase.query, testCase.body)
			assert.Equal(test, testCase.wantStatus, response.Code)
			assert.Equal(test, testCase.wantStatus == http.StatusNoContent, forwarded)
		})
	}
}

func TestEngineReferenceProtectionRejectsCaseVariantFields(test *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "spec versions casing alias",
			body: `{"spec":{"versions":[{"version":"v2.0.0"}],"Versions":[{"version":"v1.0.0"},{"version":"v2.0.0"}]}}`,
		},
		{
			name: "nested version casing alias",
			body: `{"spec":{"versions":[{"version":"v2.0.0","Version":"v1.0.0"},{"version":"v2.0.0"}]}}`,
		},
		{
			name: "Unicode case folding alias",
			body: `{"spec":{"versions":[{"version":"v2.0.0"}],"ver\u017fions":[{"version":"v1.0.0"},{"version":"v2.0.0"}]}}`,
		},
		{
			name: "metadata name casing alias",
			body: `{"metadata":{"name":"renamed","Name":"custom-engine","workspace":"workspace-a"}}`,
		},
		{
			name: "metadata workspace casing alias",
			body: `{"metadata":{"name":"custom-engine","workspace":"workspace-b","Workspace":"workspace-a"}}`,
		},
		{
			name: "deletion timestamp casing alias",
			body: `{"metadata":{"name":"custom-engine","workspace":"workspace-a","deletion_timestamp":"2026-10-20T00:00:00Z","DELETION_TIMESTAMP":""}}`,
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			store := storageMocks.NewMockStorage(test)
			store.On("ListEngine", mock.Anything).Return([]v1.Engine{engineReferenceFixture()}, nil).Maybe()
			response, forwarded := serveEngineProxyRequest(test, store, http.MethodPatch, "/engines?id=eq.7", testCase.body)
			assert.Equal(test, http.StatusBadRequest, response.Code)
			assert.False(test, forwarded)
		})
	}
}

func TestEngineReferenceProtectionDuplicateFieldsUseLastValue(test *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "duplicate arrays do not merge previously decoded version objects",
			body: `{"spec":{"versions":[{"version":"v1.0.0"},{"version":"v2.0.0"}],"versions":[{},{"version":"v2.0.0"}]}}`,
		},
		{
			name: "duplicate version null clears the previous string",
			body: `{"spec":{"versions":[{"version":"v1.0.0","version":null},{"version":"v2.0.0"}]}}`,
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			store := storageMocks.NewMockStorage(test)
			store.On("ListEngine", mock.Anything).Return([]v1.Engine{engineReferenceFixture()}, nil).Once()
			store.On("Count", storage.ENDPOINT_TABLE, []storage.Filter{
				{Column: "metadata->>workspace", Operator: "eq", Value: "workspace-a"},
				{Column: "spec->engine->>engine", Operator: "eq", Value: "custom-engine"},
				{Column: "spec->engine->>version", Operator: "eq", Value: "v1.0.0"},
			}).Return(1, nil).Once()
			response, forwarded := serveEngineProxyRequest(test, store, http.MethodPatch, "/engines?id=eq.7", testCase.body)
			assert.Equal(test, http.StatusBadRequest, response.Code)
			assert.Contains(test, response.Body.String(), `"code":"10133"`)
			assert.False(test, forwarded)
		})
	}
}
