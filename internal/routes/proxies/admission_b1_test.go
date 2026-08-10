package proxies

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestB1RoutesRegisterCreateAndUpdateValidationHooks(t *testing.T) {
	testCases := []struct {
		name       string
		register   func(*gin.RouterGroup, []gin.HandlerFunc, *Dependencies) error
		resource   any
		createHook string
		updateHook string
	}{
		{
			name:       "endpoints",
			register:   RegisterEndpointRoutes,
			resource:   endpointAdmissionResource,
			createHook: "community.endpoint.vgpu.create",
			updateHook: "community.endpoint.vgpu.update",
		},
		{
			name:       "clusters",
			register:   RegisterClusterRoutes,
			resource:   clusterAdmissionResource,
			createHook: "community.cluster.accelerator-virtualization.create",
			updateHook: "community.cluster.version-and-accelerator-virtualization.update",
		},
		{
			name:       "model catalogs",
			register:   RegisterModelCatalogRoutes,
			resource:   modelCatalogAdmissionResource,
			createHook: "community.model-catalog.recipe.create",
			updateHook: "community.model-catalog.recipe.update",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			registry := admission.NewRegistry()
			require.NoError(t, testCase.register(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry}))
			require.NoError(t, registry.Seal())

			createChain, err := registry.Chain(testCase.resource, admission.Create)
			require.NoError(t, err)
			require.Equal(t, testCase.createHook, createChain.Hooks()[0].Name)

			updateChain, err := registry.Chain(testCase.resource, admission.Update)
			require.NoError(t, err)
			require.Equal(t, testCase.updateHook, updateChain.Hooks()[0].Name)
		})
	}
}

func TestB1AdmissionHooksPreserveValidationErrors(t *testing.T) {
	t.Run("endpoint create and update use candidate vGPU resources", func(t *testing.T) {
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, nil)
		markClusterVGPUReady(cluster, "cluster", "default")
		registry := admission.NewRegistry()
		require.NoError(t, RegisterEndpointRoutes(gin.New().Group("/resource"), nil, &Dependencies{
			Admission: registry,
			Storage:   &fakeClusterStorage{clusters: []v1.Cluster{*cluster}},
		}))
		require.NoError(t, registry.Seal())

		candidate := v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default"}, Spec: &v1.EndpointSpec{Cluster: "cluster", Resources: &v1.ResourceSpec{
			Accelerator: map[string]string{
				v1.AcceleratorTypeKey:                    string(v1.AcceleratorTypeNVIDIAGPU),
				v1.AcceleratorVirtualizationMemoryMiBKey: "4096",
			},
		}}}
		for _, operation := range []admission.Operation{admission.Create, admission.Update} {
			chain, err := registry.Chain(endpointAdmissionResource, operation)
			require.NoError(t, err)
			_, err = chain.Run(admission.RequestContext{Context: context.Background()}, v1.Endpoint{}, candidate)
			requireAdmissionError(t, err, 10218,
				"endpoint accelerator virtualization requires accelerator product",
				"Set spec.resources.accelerator.product to the target GPU product",
			)
		}
	})

	t.Run("cluster virtualization and version errors retain 10208 through 10212 semantics", func(t *testing.T) {
		store := &fakeClusterStorage{endpoints: []v1.Endpoint{{
			Spec: &v1.EndpointSpec{Resources: &v1.ResourceSpec{Accelerator: map[string]string{
				v1.AcceleratorVirtualizationMemoryMiBKey: "4096",
			}}},
		}}}
		registry := admission.NewRegistry()
		require.NoError(t, RegisterClusterRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
		require.NoError(t, registry.Seal())

		createChain, err := registry.Chain(clusterAdmissionResource, admission.Create)
		require.NoError(t, err)
		_, err = createChain.Run(admission.RequestContext{Context: context.Background()}, nil, v1.Cluster{
			Spec: &v1.ClusterSpec{
				Type:                      v1.SSHClusterType,
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: true},
			},
		})
		requireAdmissionError(t, err, 10208,
			"spec.accelerator_virtualization is only supported for Kubernetes clusters",
			"Use a Kubernetes cluster when enabling accelerator virtualization",
		)

		updateChain, err := registry.Chain(clusterAdmissionResource, admission.Update)
		require.NoError(t, err)
		old := v1.Cluster{
			Metadata: &v1.Metadata{Name: "cluster", Workspace: "default"},
			Spec: &v1.ClusterSpec{
				Type:                      v1.KubernetesClusterType,
				Version:                   "v1.1.0",
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: true},
			},
		}
		candidate := old
		candidate.Spec = &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.0.1"}
		_, err = updateChain.Run(admission.RequestContext{Context: context.Background()}, old, candidate)
		requireAdmissionError(t, err, 10212,
			"invalid cluster version update",
			"cluster version downgrade is not supported: current version is v1.1.0, desired version is v1.0.1",
		)

		_, err = createChain.Run(admission.RequestContext{Context: context.Background()}, nil, v1.Cluster{
			Spec: &v1.ClusterSpec{
				Type: v1.KubernetesClusterType, Version: "nightly",
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: true},
			},
		})
		requireAdmissionError(t, err, 10209,
			"invalid cluster version",
			"failed to parse spec.version \"nightly\": failed to parse version \"nightly\": Invalid Semantic Version",
		)

		_, err = createChain.Run(admission.RequestContext{Context: context.Background()}, nil, v1.Cluster{
			Spec: &v1.ClusterSpec{
				Type: v1.KubernetesClusterType, Version: "v1.1.0",
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
					Enabled: true, ConfigPatch: map[string]interface{}{"dra": map[string]interface{}{}},
				},
			},
		})
		requireAdmissionError(t, err, 10210,
			"unsupported accelerator_virtualization.config_patch key \"dra\"",
			"Only devicePlugin, scheduler, and global config_patch keys are supported",
		)

		candidate = old
		candidate.Spec = &v1.ClusterSpec{
			Type: v1.KubernetesClusterType, Version: "v1.1.0",
			AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: false},
		}
		_, err = updateChain.Run(admission.RequestContext{Context: context.Background()}, old, candidate)
		requireAdmissionError(t, err, 10211,
			"cannot disable accelerator virtualization for cluster 'default/cluster'",
			"1 vGPU endpoint(s) still reference this cluster; delete the vGPU endpoints before disabling accelerator virtualization",
		)
	})

	t.Run("model catalog recipe errors retain 10224", func(t *testing.T) {
		registry := admission.NewRegistry()
		require.NoError(t, RegisterModelCatalogRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry}))
		require.NoError(t, registry.Seal())
		chain, err := registry.Chain(modelCatalogAdmissionResource, admission.Create)
		require.NoError(t, err)
		_, err = chain.Run(admission.RequestContext{Context: context.Background()}, nil, v1.ModelCatalog{
			Spec: &v1.ModelCatalogSpec{Features: []v1.RecipeFeature{{
				Name: "a", ConflictsWith: []string{"a"},
			}}},
		})
		requireAdmissionError(t, err, 10224,
			"model_catalog: feature \"a\" lists itself in conflicts_with",
			"Fix the recipe definition and retry",
		)
	})
}

func TestModelCatalogCreateAdmissionRunnerMapsInvalidPayloadLocally(t *testing.T) {
	registry := admission.NewRegistry()
	require.NoError(t, registry.RegisterResource(modelCatalogAdmissionResource))
	require.NoError(t, registry.Seal())

	defaultStatus, defaultBody := runAdmissionRunner(
		t,
		CreateAdmissionRunner(registry, modelCatalogAdmissionResource),
		http.MethodPost,
		`{"spec":{"resources":{"cpu":1}}}`,
	)
	require.Equal(t, http.StatusBadRequest, defaultStatus)
	require.JSONEq(t, `{"code":10301,"message":"invalid admission request"}`, defaultBody)

	status, body := runAdmissionRunner(
		t,
		CreateAdmissionRunnerWithOptions(registry, modelCatalogAdmissionResource, modelCatalogCreateAdmissionRunnerOptions),
		http.MethodPost,
		`{"spec":{"resources":{"cpu":1}}}`,
	)
	require.Equal(t, http.StatusBadRequest, status)
	require.JSONEq(t, `{"code":10223,"message":"invalid model_catalog payload: json: cannot unmarshal number into Go struct field ResourceSpec.spec.resources.cpu of type string","hint":"Check the model catalog spec fields and types"}`, body)
}

func TestB1PatchAdmissionRunnerMapsResourcePayloadErrors(t *testing.T) {
	testCases := []struct {
		name          string
		old           string
		body          string
		want          string
		defaultRunner func(*fakePatchAdmissionReader) gin.HandlerFunc
		mappedRunner  func(*fakePatchAdmissionReader) gin.HandlerFunc
	}{
		{
			name: "endpoint vGPU",
			old:  `{"id":1,"metadata":{"name":"endpoint","workspace":"default"},"spec":{}}`,
			body: `{"spec":{"resources":{"gpu":1}}}`,
			want: `{"code":10214,"message":"invalid endpoint payload","hint":"json: cannot unmarshal number into Go struct field ResourceSpec.spec.resources.gpu of type string"}`,
			defaultRunner: func(reader *fakePatchAdmissionReader) gin.HandlerFunc {
				return newPatchAdmissionRunner(fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, endpointAdmissionResource, storage.ENDPOINT_TABLE)
			},
			mappedRunner: func(reader *fakePatchAdmissionReader) gin.HandlerFunc {
				return newPatchAdmissionRunnerWithOptions(fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, endpointAdmissionResource, storage.ENDPOINT_TABLE, endpointPatchAdmissionRunnerOptions)
			},
		},
		{
			name: "cluster virtualization",
			old:  `{"id":1,"metadata":{"name":"cluster","workspace":"default"},"spec":{}}`,
			body: `{"spec":{"version":1}}`,
			want: `{"code":10209,"message":"invalid cluster payload","hint":"json: cannot unmarshal number into Go value of type string"}`,
			defaultRunner: func(reader *fakePatchAdmissionReader) gin.HandlerFunc {
				return newPatchAdmissionRunner(fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, clusterAdmissionResource, storage.CLUSTERS_TABLE)
			},
			mappedRunner: func(reader *fakePatchAdmissionReader) gin.HandlerFunc {
				return newPatchAdmissionRunnerWithOptions(fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, clusterAdmissionResource, storage.CLUSTERS_TABLE, clusterPatchAdmissionRunnerOptions)
			},
		},
		{
			name: "model catalog recipe",
			old:  `{"id":1,"metadata":{"name":"catalog","workspace":"default"},"spec":{}}`,
			body: `{"spec":{"resources":{"cpu":1}}}`,
			want: `{"code":10223,"message":"invalid model_catalog payload: json: cannot unmarshal number into Go struct field ResourceSpec.spec.resources.cpu of type string","hint":"Check the model catalog spec fields and types"}`,
			defaultRunner: func(reader *fakePatchAdmissionReader) gin.HandlerFunc {
				return newPatchAdmissionRunner(fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, modelCatalogAdmissionResource, storage.MODEL_CATALOG_TABLE)
			},
			mappedRunner: func(reader *fakePatchAdmissionReader) gin.HandlerFunc {
				return newPatchAdmissionRunnerWithOptions(fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, modelCatalogAdmissionResource, storage.MODEL_CATALOG_TABLE, modelCatalogPatchAdmissionRunnerOptions)
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(testCase.old)}}
			defaultStatus, defaultBody := runPatchAdmissionRunner(
				t,
				testCase.defaultRunner(reader),
				"/?id=eq.1", testCase.body, nil,
			)
			require.Equal(t, http.StatusBadRequest, defaultStatus)
			require.JSONEq(t, `{"code":10301,"message":"invalid admission request"}`, defaultBody)

			reader = &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(testCase.old)}}
			status, body := runPatchAdmissionRunner(
				t,
				testCase.mappedRunner(reader),
				"/?id=eq.1", testCase.body, nil,
			)
			require.Equal(t, http.StatusBadRequest, status)
			require.JSONEq(t, testCase.want, body)
		})
	}
}

func TestB1CreateAdmissionPreservesLegacyBodyShapes(t *testing.T) {
	t.Run("endpoint and cluster forward empty bodies and reject arrays", func(t *testing.T) {
		testCases := []struct {
			name   string
			array  string
			want   string
			runner func(*admission.Registry) gin.HandlerFunc
		}{
			{
				name:  "endpoint",
				array: `[]`,
				want:  `{"code":10214,"message":"invalid endpoint payload","hint":"json: cannot unmarshal array into Go value of type v1.Endpoint"}`,
				runner: func(registry *admission.Registry) gin.HandlerFunc {
					return CreateAdmissionRunnerWithOptions(registry, endpointAdmissionResource, endpointCreateAdmissionRunnerOptions)
				},
			},
			{
				name:  "cluster",
				array: `[]`,
				want:  `{"code":10209,"message":"invalid cluster payload","hint":"json: cannot unmarshal array into Go value of type v1.Cluster"}`,
				runner: func(registry *admission.Registry) gin.HandlerFunc {
					return CreateAdmissionRunnerWithOptions(registry, clusterAdmissionResource, clusterCreateAdmissionRunnerOptions)
				},
			},
		}

		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				registry := admission.NewRegistry()
				if testCase.name == "endpoint" {
					require.NoError(t, registry.RegisterResource(endpointAdmissionResource))
				} else {
					require.NoError(t, registry.RegisterResource(clusterAdmissionResource))
				}
				require.NoError(t, registry.Seal())
				runner := testCase.runner(registry)

				emptyStatus, _ := runAdmissionRunner(t, runner, http.MethodPost, ``)
				require.Equal(t, http.StatusNoContent, emptyStatus)

				arrayStatus, arrayBody := runAdmissionRunner(t, runner, http.MethodPost, testCase.array)
				require.Equal(t, http.StatusBadRequest, arrayStatus)
				require.JSONEq(t, testCase.want, arrayBody)
			})
		}
	})

	t.Run("model catalog forwards empty bodies and admits arrays", func(t *testing.T) {
		registry := admission.NewRegistry()
		require.NoError(t, registry.RegisterResource(modelCatalogAdmissionResource))
		require.NoError(t, registry.Seal())
		runner := CreateAdmissionRunnerWithOptions(registry, modelCatalogAdmissionResource, modelCatalogCreateAdmissionRunnerOptions)

		emptyStatus, _ := runAdmissionRunner(t, runner, http.MethodPost, ``)
		require.Equal(t, http.StatusNoContent, emptyStatus)

		arrayStatus, arrayBody := runAdmissionRunner(t, runner, http.MethodPost, `[{"spec":{}}]`)
		require.Equal(t, http.StatusNoContent, arrayStatus)
		require.JSONEq(t, `[{"spec":{}}]`, arrayBody)
	})
}

func TestB1AdmissionPermitsAndForwardsUnknownNestedFields(t *testing.T) {
	registry := admission.NewRegistry()
	require.NoError(t, RegisterEndpointRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry}))
	require.NoError(t, registry.Seal())

	createRunner := CreateAdmissionRunnerWithOptions(registry, endpointAdmissionResource, endpointCreateAdmissionRunnerOptions)
	createStatus, createBody := runAdmissionRunner(t, createRunner, http.MethodPost, `{"spec":{"future_setting":{"enabled":true}}}`)
	require.Equal(t, http.StatusNoContent, createStatus)
	require.JSONEq(t, `{"spec":{"future_setting":{"enabled":true}}}`, createBody)

	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":1,"metadata":{"name":"endpoint","workspace":"default"},"spec":{}}`)}}
	patchRunner := newPatchAdmissionRunnerWithOptions(
		registryPatchAdmissionChainResolver{registry: registry}, reader, endpointAdmissionResource, storage.ENDPOINT_TABLE, endpointPatchAdmissionRunnerOptions,
	)
	var forwarded string
	patchStatus, _ := runPatchAdmissionRunner(t, patchRunner, "/?id=eq.1", `{"spec":{"future_setting":{"enabled":true}}}`, func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		forwarded = string(body)
		c.Status(http.StatusNoContent)
	})
	require.Equal(t, http.StatusNoContent, patchStatus)
	require.JSONEq(t, `{"spec":{"future_setting":{"enabled":true}}}`, forwarded)
}

func TestB1PatchAdmissionMapsBodyReadAndCloseErrors(t *testing.T) {
	testCases := []struct {
		name   string
		want   string
		runner func(*fakePatchAdmissionReader) gin.HandlerFunc
	}{
		{
			name: "endpoint",
			want: `{"code":10214,"message":"invalid endpoint payload","hint":"body failed"}`,
			runner: func(reader *fakePatchAdmissionReader) gin.HandlerFunc {
				return newPatchAdmissionRunnerWithOptions(fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, endpointAdmissionResource, storage.ENDPOINT_TABLE, endpointPatchAdmissionRunnerOptions)
			},
		},
		{
			name: "cluster",
			want: `{"code":10209,"message":"invalid cluster payload","hint":"body failed"}`,
			runner: func(reader *fakePatchAdmissionReader) gin.HandlerFunc {
				return newPatchAdmissionRunnerWithOptions(fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, clusterAdmissionResource, storage.CLUSTERS_TABLE, clusterPatchAdmissionRunnerOptions)
			},
		},
		{
			name: "model catalog",
			want: `{"code":10223,"message":"failed to read request body: body failed","hint":"Retry the request"}`,
			runner: func(reader *fakePatchAdmissionReader) gin.HandlerFunc {
				return newPatchAdmissionRunnerWithOptions(fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, modelCatalogAdmissionResource, storage.MODEL_CATALOG_TABLE, modelCatalogPatchAdmissionRunnerOptions)
			},
		},
	}

	for _, testCase := range testCases {
		for _, failure := range []string{"read", "close"} {
			t.Run(testCase.name+" "+failure, func(t *testing.T) {
				reader := &fakePatchAdmissionReader{}
				body := &failingPatchBody{reader: strings.NewReader(`{}`)}
				if failure == "read" {
					body.readErr = errors.New("body failed")
				} else {
					body.closeErr = errors.New("body failed")
				}

				forwarded := false
				status, response := runPatchAdmissionRunnerWithBody(t, testCase.runner(reader), body, func(*gin.Context) {
					forwarded = true
				})
				require.Equal(t, http.StatusBadRequest, status)
				require.JSONEq(t, testCase.want, response)
				require.Zero(t, reader.calls)
				require.False(t, forwarded)
			})
		}
	}
}

type failingPatchBody struct {
	reader   io.Reader
	readErr  error
	closeErr error
}

func (b *failingPatchBody) Read(buffer []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.reader.Read(buffer)
}

func (b *failingPatchBody) Close() error {
	return b.closeErr
}

func runPatchAdmissionRunnerWithBody(t *testing.T, runner gin.HandlerFunc, body io.ReadCloser, next gin.HandlerFunc) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PATCH("/", func(c *gin.Context) { c.Set("postgrest_token", "postgrest-jwt") }, runner, next)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/?id=eq.1", nil)
	request.Body = body
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

func requireAdmissionError(t *testing.T, err error, code int, message, hint string) {
	t.Helper()
	var admissionErr *admission.Error
	require.ErrorAs(t, err, &admissionErr)
	require.Equal(t, code, admissionErr.Code)
	require.Equal(t, message, admissionErr.Message)
	require.Equal(t, hint, admissionErr.Hint)
}
