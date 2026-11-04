package bench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"

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

type fakeBenchRunner struct {
	mu       sync.Mutex
	commands []string
	result   string
}

func (r *fakeBenchRunner) record(command string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.commands = append(r.commands, command)
}

func (r *fakeBenchRunner) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.commands...)
}

func (r *fakeBenchRunner) Run(_ context.Context, command string, stdout, stderr io.Writer) error {
	r.record(command)

	switch {
	case command == benchmarkPreflightCommand():
		_, _ = io.WriteString(stdout, "usage\n")
	case strings.HasPrefix(command, "umask 077 && mkdir -- "):
		return nil
	case strings.HasPrefix(command, "cat -- "):
		result := r.result
		if result == "" {
			result = `{"completed":20,"failed":0,"duration":1.5,"request_throughput":2.5,"output_throughput":10.5,"p50_ttft_ms":12.5,"p99_ttft_ms":25.5}`
		}

		_, _ = io.WriteString(stdout, result)
	case strings.HasPrefix(command, "rm -rf -- "):
		return nil
	default:
		_, _ = io.WriteString(stdout, "benchmark output\n")
		_, _ = io.WriteString(stderr, "benchmark diagnostics\n")
	}

	return nil
}

func findBenchCommand(commands []string, match func(string) bool) string {
	for _, command := range commands {
		if match(command) {
			return command
		}
	}

	return ""
}

func countBenchCommands(commands []string, match func(string) bool) int {
	count := 0

	for _, command := range commands {
		if match(command) {
			count++
		}
	}

	return count
}

type outputRunner struct {
	stdout string
	stderr string
	err    error
}

func (r outputRunner) Run(_ context.Context, _ string, stdout, stderr io.Writer) error {
	_, _ = io.WriteString(stdout, r.stdout)
	_, _ = io.WriteString(stderr, r.stderr)

	return r.err
}

type caseFailureRunner struct{}

func (r *caseFailureRunner) Run(_ context.Context, command string, stdout, _ io.Writer) error {
	switch {
	case command == benchmarkPreflightCommand(),
		strings.HasPrefix(command, "umask 077 && mkdir -- "),
		strings.HasPrefix(command, "rm -rf -- "):
		return nil
	default:
		_, _ = io.WriteString(stdout, "benchmark output\n")

		return fmt.Errorf("case failed")
	}
}

type emptyResultRunner struct{}

func (r *emptyResultRunner) Run(_ context.Context, command string, stdout, _ io.Writer) error {
	if strings.HasPrefix(command, "cat -- ") {
		return nil
	}

	if strings.HasPrefix(command, "rm -rf -- ") {
		return nil
	}

	_, _ = io.WriteString(stdout, "benchmark output")

	return nil
}

func captureBenchmarkProgress(t *testing.T) *bytes.Buffer {
	t.Helper()

	state := klog.CaptureState()
	t.Cleanup(state.Restore)

	output := &bytes.Buffer{}
	klog.LogToStderr(false)
	klog.SetOutput(output)

	return output
}

// resolvedTarget returns the Endpoint and BenchTarget runBenchmark receives
// after resolution.
func resolvedTarget(runner remoteRunner) (v1.Endpoint, BenchTarget) {
	endpoint, _, _ := benchmarkFixtures()
	endpoint.Spec.Model.Name = "router-model"

	return endpoint, BenchTarget{
		Platform:  "kubernetes",
		RuntimeID: "pod/chat-a:vllm",
		BaseURL:   "http://router-service.ns.svc.cluster.local:8000/default/chat",
		Model:     "router-model",
		Tokenizer: "router-model",
		runner:    runner,
	}
}

func runResolvedBenchmark(t *testing.T, options Options, target BenchTarget) (string, error) {
	t.Helper()

	endpoint, _ := resolvedTarget(target.runner)
	output, err := createOutputDirectory(options.OutputDir)
	require.NoError(t, err)

	err = runBenchmark(
		context.Background(),
		&endpoint,
		target,
		options,
		expandCases(options.InputLens, options.OutputLens, options.Concurrencies),
		output,
	)

	return output, err
}

func TestRunBenchmarkCollectsCaseArtifacts(t *testing.T) {
	runner := &fakeBenchRunner{}
	progress := captureBenchmarkProgress(t)

	_, target := resolvedTarget(runner)
	output, err := runResolvedBenchmark(t, validOptions(t.TempDir()), target)
	require.NoError(t, err)
	require.DirExists(t, output)

	caseDir := filepath.Join(output, "input-16-output-4-concurrency-2")
	result, err := os.ReadFile(filepath.Join(caseDir, "result.json"))
	require.NoError(t, err)
	require.JSONEq(
		t,
		`{"completed":20,"failed":0,"duration":1.5,"request_throughput":2.5,"output_throughput":10.5,"p50_ttft_ms":12.5,"p99_ttft_ms":25.5}`,
		string(result),
	)

	stdout, err := os.ReadFile(filepath.Join(caseDir, "stdout.log"))
	require.NoError(t, err)
	require.Contains(t, string(stdout), "benchmark output")

	command, err := os.ReadFile(filepath.Join(caseDir, benchmarkCommandFileName))
	require.NoError(t, err)
	require.Contains(t, string(command), "vllm bench serve")
	require.Contains(t, string(command), "--model 'router-model'")
	require.Contains(t, string(command), "--tokenizer 'router-model'")

	manifest, err := os.ReadFile(filepath.Join(output, manifestFileName))
	require.NoError(t, err)
	require.Contains(t, string(manifest), `"measurement_scope": "endpoint-route"`)
	require.Contains(t, string(manifest), `"model_selection": "first-served-model"`)
	require.Contains(t, string(manifest), `"model": "router-model"`)
	require.Contains(t, string(manifest), `"status": "Succeeded"`)

	require.Equal(t, benchmarkPreflightCommand(), runner.commands[0])

	require.Equal(t, 2, countBenchCommands(runner.snapshot(), func(value string) bool {
		return strings.HasPrefix(value, "rm -rf -- ")
	}), "the case directory and the run root are removed on success")

	progressOutput := progress.String()
	for _, line := range strings.Split(strings.TrimSpace(progressOutput), "\n") {
		require.Regexp(t, `^I[0-9]{4} [0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}\s+[0-9]+ run\.go:[0-9]+\] `, line)
	}

	require.Contains(t, progressOutput, "benchmark preflight started")
	require.Contains(t, progressOutput, "benchmark preflight completed")
	require.Contains(t, progressOutput, "benchmark case 1/1 started: input_len=16 output_len=4 concurrency=2 num_prompts=20 request_rate=inf")
	require.Contains(t, progressOutput, "benchmark case 1/1 command: "+strings.TrimSpace(string(command)))
	require.Contains(t, progressOutput, "benchmark case 1/1 completed: result=")
	require.Contains(t, progressOutput, "completed=20 failed=0 duration_s=1.50 request_per_s=2.50 output_tokens_per_s=10.50 p50_ttft_ms=12.50 p99_ttft_ms=25.50")
	require.Contains(t, progressOutput, "benchmark completed: cases=1 result_dir="+output)
}

func TestRunBenchmarkKeepsSuccessfulCaseWhenResultSummaryIsUnavailable(t *testing.T) {
	runner := &fakeBenchRunner{
		result: `{"future_result_schema":true}`,
	}
	progress := captureBenchmarkProgress(t)

	_, target := resolvedTarget(runner)
	output, err := runResolvedBenchmark(t, validOptions(t.TempDir()), target)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(output, "input-16-output-4-concurrency-2", benchmarkResultFileName))
	require.Contains(t, progress.String(), "benchmark case 1/1 completed: result=")
	require.Contains(t, progress.String(), "summary=unavailable")
	require.Contains(t, progress.String(), "benchmark preflight completed")
}

func TestRunBenchmarkRecordsPreflightAndCaseFailures(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		progress := captureBenchmarkProgress(t)

		_, target := resolvedTarget(outputRunner{err: fmt.Errorf("preflight failed")})
		output, err := runResolvedBenchmark(t, validOptions(t.TempDir()), target)
		require.ErrorContains(t, err, "preflight")
		require.Contains(t, progress.String(), "benchmark preflight failed: vllm benchmark preflight failed: preflight failed")
		require.Regexp(t, `(?m)^E[0-9]{4} .*benchmark preflight failed:`, progress.String())

		manifest, readErr := os.ReadFile(filepath.Join(output, manifestFileName))
		require.NoError(t, readErr)
		require.Contains(t, string(manifest), "preflight failed")
	})

	t.Run("case", func(t *testing.T) {
		progress := captureBenchmarkProgress(t)

		_, target := resolvedTarget(&caseFailureRunner{})
		output, err := runResolvedBenchmark(t, validOptions(t.TempDir()), target)
		require.ErrorContains(t, err, "benchmark case")
		require.Contains(t, progress.String(), "benchmark case 1/1 failed: case failed")
		require.Regexp(t, `(?m)^E[0-9]{4} .*benchmark case 1/1 failed:`, progress.String())

		manifest, readErr := os.ReadFile(filepath.Join(output, manifestFileName))
		require.NoError(t, readErr)
		require.Contains(t, string(manifest), `"status": "Failed"`)
		require.Contains(t, string(manifest), "case failed")
	})
}

func TestRunBenchmarkFailsCaseWhenNoRequestCompletes(t *testing.T) {
	runner := &fakeBenchRunner{result: `{"completed":0,"failed":20,"duration":1.5}`}
	progress := captureBenchmarkProgress(t)

	_, target := resolvedTarget(runner)
	output, err := runResolvedBenchmark(t, validOptions(t.TempDir()), target)
	require.ErrorContains(t, err, "benchmark completed 0 requests")
	require.Contains(t, progress.String(), "benchmark case 1/1 failed: benchmark completed 0 requests")

	manifest, readErr := os.ReadFile(filepath.Join(output, manifestFileName))
	require.NoError(t, readErr)
	require.Contains(t, string(manifest), `"status": "Failed"`)
	require.FileExists(t, filepath.Join(output, "input-16-output-4-concurrency-2", benchmarkResultFileName))
}

func TestRunCaseRejectsEmptyCopiedResult(t *testing.T) {
	runner := &emptyResultRunner{}
	err := runCase(context.Background(), runner, BenchTarget{Model: "router-model", Tokenizer: "/models/chat"}, Options{
		NumPrompts:  1,
		RequestRate: "inf",
	}, Case{InputLen: 1, OutputLen: 1, Concurrency: 1}, t.TempDir(), "/tmp/neutree-bench-test")
	require.ErrorContains(t, err, "result is empty")
}

func TestBenchmarkCaseErrorStatusMarksDeadlinesCanceled(t *testing.T) {
	require.Equal(t, benchmarkCanceledStatus, benchmarkCaseErrorStatus(context.DeadlineExceeded))
	require.Equal(t, benchmarkFailedStatus, benchmarkCaseErrorStatus(errors.New("benchmark failed")))
}
