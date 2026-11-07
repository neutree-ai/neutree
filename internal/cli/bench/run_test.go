package bench

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/client"
)

func TestRunProgressUsesKlogStderr(t *testing.T) {
	const childEnvironment = "NEUTREE_BENCH_KLOG_TEST_CHILD"
	if os.Getenv(childEnvironment) == "1" {
		_, err := run(context.Background(), nil, validOptions(t.TempDir()))
		require.ErrorContains(t, err, "Neutree API client is required")
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestRunProgressUsesKlogStderr$")
	command.Env = append(os.Environ(), childEnvironment+"=1")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	require.NoError(t, command.Run())
	require.NotContains(t, stdout.String(), "benchmark starting")
	require.Regexp(t, `(?m)^I[0-9]{4} [0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}\s+[0-9]+ run\.go:[0-9]+\] benchmark starting: endpoint=default/chat cases=1$`, stderr.String())
}

func TestRunUsesConfiguredAPIClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/api/v1/endpoints", request.URL.Path)
		_, err := writer.Write([]byte("[]"))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	_, err := Run(context.Background(), client.NewClient(server.URL), validOptions(t.TempDir()))
	require.ErrorContains(t, err, "get Endpoint")
}

func TestRunTimeoutCancelsEndpointRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	options := validOptions(t.TempDir())
	options.Timeout = 200 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), client.NewClient(server.URL), options)
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Endpoint request did not start")
	}
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("Endpoint request did not stop on timeout")
	}
}

func TestValidateEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*v1.Endpoint)
		want   string
	}{
		{
			name:   "endpoint is not running",
			mutate: func(endpoint *v1.Endpoint) { endpoint.Status.Phase = "Pending" },
			want:   "is not Running",
		},
		{
			name:   "engine is not vllm",
			mutate: func(endpoint *v1.Endpoint) { endpoint.Spec.Engine.Engine = "sglang" },
			want:   `unsupported engine "sglang"`,
		},
		{
			name:   "task is not text generation",
			mutate: func(endpoint *v1.Endpoint) { endpoint.Spec.Model.Task = "embedding" },
			want:   `unsupported task "embedding"`,
		},
		{
			name:   "endpoint has no cluster",
			mutate: func(endpoint *v1.Endpoint) { endpoint.Spec.Cluster = "" },
			want:   "has no Cluster",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, _, _ := benchmarkFixtures()
			tc.mutate(&endpoint)

			require.ErrorContains(t, validateEndpoint(&endpoint), tc.want)
		})
	}
}

func TestValidateCluster(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*v1.Cluster)
		want   string
	}{
		{
			name:   "cluster is not running",
			mutate: func(cluster *v1.Cluster) { cluster.Status.Phase = "Pending" },
			want:   "is not Running",
		},
		{
			name:   "cluster type is unsupported",
			mutate: func(cluster *v1.Cluster) { cluster.Spec.Type = "local" },
			want:   `unsupported type "local"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cluster, _ := benchmarkFixtures()
			tc.mutate(&cluster)

			require.ErrorContains(t, validateCluster(&cluster), tc.want)
		})
	}
}

func TestTokenizerPath(t *testing.T) {
	huggingFace := &v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType}}
	bentoNFS := &v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{
		Type: v1.BentoMLModelRegistryType,
		Url:  "nfs://bentoml.example/models",
	}}
	bentoHTTP := &v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{
		Type: v1.BentoMLModelRegistryType,
		Url:  "http://bentoml.example",
	}}

	kubernetesCluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}}
	sshCluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType}}
	namedCacheCluster := &v1.Cluster{Spec: &v1.ClusterSpec{
		Type: v1.KubernetesClusterType,
		Config: &v1.ClusterConfig{
			ModelCaches: []v1.ModelCache{{Name: "shared"}},
		},
	}}

	for _, tc := range []struct {
		name      string
		cluster   *v1.Cluster
		model     *v1.ModelSpec
		registry  *v1.ModelRegistry
		version   string
		mountRoot string
		want      string
	}{
		{
			name:      "kubernetes mount root",
			cluster:   kubernetesCluster,
			model:     &v1.ModelSpec{Name: "Qwen/Qwen3"},
			registry:  huggingFace,
			version:   "v2",
			mountRoot: v1.DefaultK8sClusterModelCacheMountPath,
			want:      "/models-cache/default/Qwen/Qwen3/v2",
		},
		{
			name:      "ssh mount root",
			cluster:   sshCluster,
			model:     &v1.ModelSpec{Name: "Qwen/Qwen3"},
			registry:  huggingFace,
			version:   "v2",
			mountRoot: v1.DefaultSSHClusterModelCacheMountPath,
			want:      "/home/ray/.neutree/models-cache/default/Qwen/Qwen3/v2",
		},
		{
			name:      "named model cache",
			cluster:   namedCacheCluster,
			model:     &v1.ModelSpec{Name: "Qwen/Qwen3"},
			registry:  huggingFace,
			version:   "v2",
			mountRoot: v1.DefaultK8sClusterModelCacheMountPath,
			want:      "/models-cache/shared/Qwen/Qwen3/v2",
		},
		{
			name:      "no registry uses the model name",
			cluster:   kubernetesCluster,
			model:     &v1.ModelSpec{Name: "router-model"},
			mountRoot: v1.DefaultK8sClusterModelCacheMountPath,
			want:      "router-model",
		},
		{
			name:      "bentoml registry is placed in the model cache",
			cluster:   kubernetesCluster,
			model:     &v1.ModelSpec{Name: "iris"},
			registry:  bentoHTTP,
			version:   "v2",
			mountRoot: v1.DefaultK8sClusterModelCacheMountPath,
			want:      "/models-cache/default/iris/v2",
		},
		{
			name:      "modelscope latest uses the default revision",
			cluster:   kubernetesCluster,
			model:     &v1.ModelSpec{Name: "Qwen/Qwen3"},
			registry:  &v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{Type: v1.ModelScopeModelRegistryType}},
			version:   "",
			mountRoot: v1.DefaultK8sClusterModelCacheMountPath,
			want:      "/models-cache/default/Qwen/Qwen3",
		},
		{
			name:      "pinned bentoml version",
			cluster:   kubernetesCluster,
			model:     &v1.ModelSpec{Name: "iris"},
			registry:  bentoNFS,
			version:   "v3",
			mountRoot: v1.DefaultK8sClusterModelCacheMountPath,
			want:      "/models-cache/default/iris/v3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := tokenizerPath(tc.cluster, tc.model, tc.registry, tc.version, tc.mountRoot)
			require.NoError(t, err)
			require.Equal(t, tc.want, path)
		})
	}
}

func TestResolveModelVersion(t *testing.T) {
	huggingFace := &v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType}}
	modelScope := &v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{Type: v1.ModelScopeModelRegistryType}}
	bentoNFS := &v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{
		Type: v1.BentoMLModelRegistryType,
		Url:  "nfs://bentoml.example/models",
	}}
	bentoHTTP := &v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{
		Type: v1.BentoMLModelRegistryType,
		Url:  "http://bentoml.example",
	}}

	modelSpec := func(version string) *v1.Endpoint {
		return &v1.Endpoint{Spec: &v1.EndpointSpec{Model: &v1.ModelSpec{
			Name:     "iris",
			Registry: "bento",
			Version:  version,
		}}}
	}

	newServer := func(t *testing.T, handler http.HandlerFunc) *client.Client {
		t.Helper()

		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)

		return client.NewClient(server.URL)
	}

	modelResponse := func(body string) *client.Client {
		return newServer(t, func(writer http.ResponseWriter, request *http.Request) {
			require.Equal(t, "/api/v1/workspaces/default/model_registries/bento/models/iris", request.URL.Path)
			require.Empty(t, request.URL.Query().Get("version"))
			_, err := writer.Write([]byte(body))
			require.NoError(t, err)
		})
	}

	for _, tc := range []struct {
		name     string
		api      *client.Client
		endpoint *v1.Endpoint
		registry *v1.ModelRegistry
		want     string
		wantErr  string
	}{
		{
			name:     "huggingface keeps the revision",
			endpoint: modelSpec("main"),
			registry: huggingFace,
			want:     "main",
		},
		{
			name:     "modelscope normalizes latest",
			endpoint: modelSpec(v1.LatestVersion),
			registry: modelScope,
			want:     "",
		},
		{
			name:     "modelscope keeps a revision",
			endpoint: modelSpec("master"),
			registry: modelScope,
			want:     "master",
		},
		{
			name:     "pinned bentoml version needs no lookup",
			api:      newServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model lookup") }),
			endpoint: modelSpec("v3"),
			registry: bentoNFS,
			want:     "v3",
		},
		{
			name:     "no registry keeps the version",
			endpoint: modelSpec("main"),
			want:     "main",
		},
		{
			name:     "bentoml without nfs still resolves latest",
			api:      modelResponse(`{"name":"v2.0.0"}`),
			endpoint: modelSpec(v1.LatestVersion),
			registry: bentoHTTP,
			want:     "v2.0.0",
		},
		{
			name:     "bentoml latest resolved through the control plane",
			api:      modelResponse(`{"name":"v1.0.0"}`),
			endpoint: modelSpec(v1.LatestVersion),
			registry: bentoNFS,
			want:     "v1.0.0",
		},
		{
			name:     "no control plane client",
			endpoint: modelSpec(v1.LatestVersion),
			registry: bentoNFS,
			wantErr:  "Neutree API client is required",
		},
		{
			name: "control plane reports no version",
			api: newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
				_, err := writer.Write([]byte(`{}`))
				require.NoError(t, err)
			}),
			endpoint: modelSpec(v1.LatestVersion),
			registry: bentoNFS,
			wantErr:  "control plane reported no version",
		},
		{
			name: "control plane lookup fails",
			api: newServer(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusInternalServerError)
			}),
			endpoint: modelSpec(v1.LatestVersion),
			registry: bentoNFS,
			wantErr:  "resolve version of model \"iris\"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version, err := resolveModelVersion(context.Background(), tc.api, "default", tc.endpoint, tc.registry)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, version)
		})
	}
}

func benchmarkFixtures() (v1.Endpoint, v1.Cluster, v1.Cluster) {
	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{Workspace: "default", Name: "chat"},
		Spec: &v1.EndpointSpec{
			Cluster: "cluster-a",
			Engine:  &v1.EndpointEngineSpec{Engine: v1.EngineNameVLLM},
			Model:   &v1.ModelSpec{Task: v1.TextGenerationModelTask},
		},
		Status: &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseRUNNING,
			ServedModels: []string{"router-model"},
		},
	}
	cluster := v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "cluster-a"},
		Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType},
		Status:   &v1.ClusterStatus{Phase: v1.ClusterPhaseRunning},
	}
	credentialCluster := cluster
	credentialCluster.Spec.Config = &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{
		Kubeconfig: "secret-kubeconfig",
	}}

	return endpoint, cluster, credentialCluster
}

func validOptions(outputDir string) Options {
	return Options{
		Workspace:     "default",
		Endpoint:      "chat",
		InputLens:     []int{16},
		OutputLens:    []int{4},
		Concurrencies: []int{2},
		NumPrompts:    20,
		RequestRate:   "inf",
		OutputDir:     outputDir,
	}
}
