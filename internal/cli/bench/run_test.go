package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/client"
)

type fakeResourceGetter struct {
	resources map[string]json.RawMessage
}

func (f fakeResourceGetter) Get(kind, workspace, name string) (json.RawMessage, error) {
	value, ok := f.resources[kind+"/"+workspace+"/"+name]
	if !ok {
		return nil, fmt.Errorf("not found")
	}

	return value, nil
}

type fakeClusterCredentials struct {
	cluster *v1.Cluster
	err     error
}

func (f fakeClusterCredentials) GetClusterCredentials(_, _ string) (*v1.Cluster, error) {
	return f.cluster, f.err
}

type fakeBenchRunner struct {
	commands []string
	model    string
	result   string
}

func (r *fakeBenchRunner) Run(_ context.Context, command string, stdout, stderr io.Writer) error {
	r.commands = append(r.commands, command)

	switch {
	case command == benchmarkPreflightCommand():
		_, _ = io.WriteString(stdout, "usage\n")
	case strings.HasPrefix(command, "python3 -c "):
		model := r.model
		if model == "" {
			model = "router-model"
		}

		_, _ = fmt.Fprintf(stdout, `{"data":[{"name":%q}]}`, model)
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

func TestRunCollectsLocalCaseArtifactsWithoutCredentials(t *testing.T) {
	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{Workspace: "default", Name: "chat"},
		Spec: &v1.EndpointSpec{
			Cluster: "cluster-a",
			Engine:  &v1.EndpointEngineSpec{Engine: v1.EngineNameVLLM},
			Model:   &v1.ModelSpec{Task: v1.TextGenerationModelTask},
		},
		Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
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

	runner := &fakeBenchRunner{}
	resolverCalled := false
	outputDir := t.TempDir()
	var progress bytes.Buffer

	output, err := run(context.Background(), dependencies{
		resources: fakeResourceGetter{resources: map[string]json.RawMessage{
			resourceKey("Endpoint", "default", "chat"):     mustJSON(t, endpoint),
			resourceKey("Cluster", "default", "cluster-a"): mustJSON(t, cluster),
		}},
		clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
		resolveTarget: func(_ context.Context, gotEndpoint *v1.Endpoint, gotCluster, gotCredential *v1.Cluster) (BenchTarget, error) {
			resolverCalled = true
			require.Equal(t, "chat", gotEndpoint.Metadata.Name)
			require.Equal(t, "cluster-a", gotCluster.Metadata.Name)
			require.Equal(t, "secret-kubeconfig", gotCredential.Spec.Config.KubernetesConfig.Kubeconfig)

			return BenchTarget{
				Platform:  "kubernetes",
				RuntimeID: "pod/chat-a:vllm",
				BaseURL:   "http://router-service.ns.svc.cluster.local:8000/default/chat",
				Tokenizer: "/models/chat",
				runner:    runner,
			}, nil
		},
	}, Options{
		Workspace:      "default",
		Endpoint:       "chat",
		InputLens:      []int{16},
		OutputLens:     []int{4},
		Concurrencies:  []int{2},
		NumPrompts:     20,
		RequestRate:    "inf",
		OutputDir:      outputDir,
		ProgressWriter: &progress,
	})
	require.NoError(t, err)
	require.True(t, resolverCalled)
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
	require.NotContains(t, string(command), "secret-kubeconfig")

	manifest, err := os.ReadFile(filepath.Join(output, "manifest.json"))
	require.NoError(t, err)
	require.Contains(t, string(manifest), `"measurement_scope": "endpoint-route"`)
	require.Contains(t, string(manifest), `"model_selection": "first-v1-model"`)
	require.Contains(t, string(manifest), `"model": "router-model"`)
	require.Contains(t, string(manifest), `"status": "Succeeded"`)
	require.NotContains(t, string(manifest), "secret-kubeconfig")

	require.Len(t, runner.commands, 6)
	require.Equal(t, benchmarkPreflightCommand(), runner.commands[0])
	require.Contains(t, runner.commands[1], "/v1/models")
	require.Contains(t, runner.commands[2], "vllm bench serve")
	require.Contains(t, runner.commands[2], "--model 'router-model'")
	require.True(t, strings.HasPrefix(runner.commands[3], "cat -- "))
	require.True(t, strings.HasPrefix(runner.commands[4], "rm -rf -- "))
	require.True(t, strings.HasPrefix(runner.commands[5], "rm -rf -- "))

	progressOutput := progress.String()
	require.Contains(t, progressOutput, "benchmark starting: endpoint=default/chat cases=1")
	require.Contains(t, progressOutput, "benchmark endpoint resolving")
	require.Contains(t, progressOutput, "benchmark endpoint resolved: cluster=cluster-a")
	require.Contains(t, progressOutput, "benchmark cluster resolving: cluster=cluster-a")
	require.Contains(t, progressOutput, "benchmark cluster resolved: type=kubernetes")
	require.Contains(t, progressOutput, "benchmark credentials resolving: cluster=cluster-a")
	require.Contains(t, progressOutput, "benchmark credentials resolved: cluster=cluster-a")
	require.Contains(t, progressOutput, "benchmark target resolving")
	require.Contains(t, progressOutput, "benchmark target resolved: platform=kubernetes runtime=pod/chat-a:vllm")
	require.Contains(t, progressOutput, "benchmark artifacts: "+output)
	require.Contains(t, progressOutput, "benchmark preflight started")
	require.Contains(t, progressOutput, "benchmark preflight completed: model=router-model")
	require.Contains(t, progressOutput, "benchmark case 1/1 started: input_len=16 output_len=4 concurrency=2 num_prompts=20 request_rate=inf")
	require.Contains(t, progressOutput, "benchmark case 1/1 command: umask 077 &&")
	require.Contains(t, progressOutput, "benchmark case 1/1 command: "+runner.commands[2])
	require.Contains(t, progressOutput, "benchmark case 1/1 completed: result=")
	require.Contains(t, progressOutput, "completed=20 failed=0 duration_s=1.50 request_per_s=2.50 output_tokens_per_s=10.50 p50_ttft_ms=12.50 p99_ttft_ms=25.50")
	require.Contains(t, progressOutput, "benchmark completed: cases=1 result_dir="+output)
	require.NotContains(t, progressOutput, "secret-kubeconfig")
}

func TestRunKeepsSuccessfulCaseWhenResultSummaryIsUnavailable(t *testing.T) {
	endpoint, cluster, credentialCluster := benchmarkFixtures()
	runner := &fakeBenchRunner{
		model:  "https://model-user:model-secret@models.example.test/qwen?token=model-token#fragment",
		result: `{"future_result_schema":true}`,
	}
	var progress bytes.Buffer

	output, err := run(context.Background(), dependencies{
		resources: fakeResourceGetter{resources: map[string]json.RawMessage{
			resourceKey("Endpoint", "default", "chat"):     mustJSON(t, endpoint),
			resourceKey("Cluster", "default", "cluster-a"): mustJSON(t, cluster),
		}},
		clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
		resolveTarget: func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error) {
			return BenchTarget{
				Platform:  "kubernetes",
				RuntimeID: "pod/chat-a:vllm",
				BaseURL:   "http://router-service.ns.svc.cluster.local:8000/default/chat",
				Tokenizer: "/models/chat",
				runner:    runner,
			}, nil
		},
	}, Options{
		Workspace:      "default",
		Endpoint:       "chat",
		InputLens:      []int{16},
		OutputLens:     []int{4},
		Concurrencies:  []int{2},
		NumPrompts:     20,
		RequestRate:    "inf",
		OutputDir:      t.TempDir(),
		ProgressWriter: &progress,
	})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(output, "input-16-output-4-concurrency-2", benchmarkResultFileName))
	require.Contains(t, progress.String(), "benchmark case 1/1 completed: result=")
	require.Contains(t, progress.String(), "summary=unavailable")
	require.Contains(t, progress.String(), "benchmark preflight completed: model=https://models.example.test/qwen")
	require.NotContains(t, progress.String(), "model-secret")
	require.NotContains(t, progress.String(), "model-token")
}

func TestRunRejectsMissingMatchingClusterCredentials(t *testing.T) {
	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{Workspace: "default", Name: "chat"},
		Spec: &v1.EndpointSpec{
			Cluster: "cluster-a",
			Engine:  &v1.EndpointEngineSpec{Engine: v1.EngineNameVLLM},
			Model:   &v1.ModelSpec{Task: v1.TextGenerationModelTask},
		},
		Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
	}
	cluster := v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "cluster-a"},
		Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType},
		Status:   &v1.ClusterStatus{Phase: v1.ClusterPhaseRunning},
	}

	_, err := run(context.Background(), dependencies{
		resources: fakeResourceGetter{resources: map[string]json.RawMessage{
			resourceKey("Endpoint", "default", "chat"):     mustJSON(t, endpoint),
			resourceKey("Cluster", "default", "cluster-a"): mustJSON(t, cluster),
		}},
		clusterCredentials: fakeClusterCredentials{err: fmt.Errorf("cluster credentials are unavailable")},
	}, Options{
		Workspace:     "default",
		Endpoint:      "chat",
		InputLens:     []int{16},
		OutputLens:    []int{4},
		Concurrencies: []int{2},
		NumPrompts:    20,
		RequestRate:   "inf",
		OutputDir:     t.TempDir(),
	})

	require.ErrorContains(t, err, "cluster credentials are unavailable")
}

func TestRunRejectsNilAPIAndInvalidResourceStates(t *testing.T) {
	_, err := Run(context.Background(), nil, Options{})
	require.ErrorContains(t, err, "API client")

	require.ErrorContains(t, validateEndpoint(nil), "metadata")
	require.ErrorContains(t, validateEndpoint(&v1.Endpoint{
		Metadata: &v1.Metadata{Workspace: "default", Name: "chat"},
		Spec:     &v1.EndpointSpec{},
		Status:   &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
	}), "Cluster")
	require.ErrorContains(t, validateEndpoint(&v1.Endpoint{
		Metadata: &v1.Metadata{Workspace: "default", Name: "chat"},
		Spec: &v1.EndpointSpec{
			Cluster: "cluster-a",
			Engine:  &v1.EndpointEngineSpec{Engine: v1.EngineNameSGLang},
			Model:   &v1.ModelSpec{Task: v1.TextGenerationModelTask},
		},
		Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
	}), "unsupported engine")

	require.ErrorContains(t, validateCluster(nil), "metadata")
	require.ErrorContains(t, validateCluster(&v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "cluster-a"},
		Spec:     &v1.ClusterSpec{Type: "unsupported"},
		Status:   &v1.ClusterStatus{Phase: v1.ClusterPhaseRunning},
	}), "unsupported type")
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

func TestRunRecordsPreflightAndCaseFailures(t *testing.T) {
	endpoint, cluster, credentialCluster := benchmarkFixtures()
	resources := fakeResourceGetter{resources: map[string]json.RawMessage{
		resourceKey("Endpoint", "default", "chat"):     mustJSON(t, endpoint),
		resourceKey("Cluster", "default", "cluster-a"): mustJSON(t, cluster),
	}}

	t.Run("preflight", func(t *testing.T) {
		var progress bytes.Buffer
		options := validOptions(t.TempDir())
		options.ProgressWriter = &progress

		output, err := run(context.Background(), dependencies{
			resources:          resources,
			clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
			resolveTarget: func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error) {
				return BenchTarget{runner: outputRunner{err: fmt.Errorf("preflight failed")}}, nil
			},
		}, options)
		require.ErrorContains(t, err, "preflight")
		require.Contains(t, progress.String(), "benchmark preflight failed: vllm benchmark preflight failed: preflight failed")

		manifest, readErr := os.ReadFile(filepath.Join(output, manifestFileName))
		require.NoError(t, readErr)
		require.Contains(t, string(manifest), "preflight failed")
	})

	t.Run("case", func(t *testing.T) {
		runner := &caseFailureRunner{}
		var progress bytes.Buffer
		options := validOptions(t.TempDir())
		options.ProgressWriter = &progress

		output, err := run(context.Background(), dependencies{
			resources:          resources,
			clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
			resolveTarget: func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error) {
				return BenchTarget{BaseURL: "http://router-service.ns.svc.cluster.local:8000/default/chat", runner: runner}, nil
			},
		}, options)
		require.ErrorContains(t, err, "benchmark case")
		require.Contains(t, progress.String(), "benchmark case 1/1 failed: case failed")

		manifest, readErr := os.ReadFile(filepath.Join(output, manifestFileName))
		require.NoError(t, readErr)
		require.Contains(t, string(manifest), `"status": "Failed"`)
		require.Contains(t, string(manifest), "case failed")
	})
}

func TestRuntimeFileHelpersHandleFailures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0600))
	_, err := createOutputDirectory(file)
	require.Error(t, err)
	_, err = createOutputFile(filepath.Join(file, "result.json"))
	require.Error(t, err)
	require.Error(t, writeManifest(file, manifest{}))

	_, cancel := benchmarkContext(context.Background(), 10)
	cancel()

	output := t.TempDir()
	_, err = runPreflight(context.Background(), outputRunner{err: fmt.Errorf("unavailable")}, "http://router-service.ns.svc.cluster.local:8000/default/chat", output)
	require.ErrorContains(t, err, "preflight")

	err = runCase(context.Background(), outputRunner{err: fmt.Errorf("run failed")}, BenchTarget{}, Options{
		NumPrompts:  1,
		RequestRate: "inf",
	}, Case{InputLen: 1, OutputLen: 1, Concurrency: 1}, output, "/tmp/neutree-bench-test")
	require.ErrorContains(t, err, "run failed")
}

func TestRunCaseRejectsEmptyCopiedResult(t *testing.T) {
	runner := &emptyResultRunner{}
	err := runCase(context.Background(), runner, BenchTarget{Model: "router-model", Tokenizer: "/models/chat"}, Options{
		NumPrompts:  1,
		RequestRate: "inf",
	}, Case{InputLen: 1, OutputLen: 1, Concurrency: 1}, t.TempDir(), "/tmp/neutree-bench-test")
	require.ErrorContains(t, err, "result is empty")
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

type caseFailureRunner struct{}

func (r *caseFailureRunner) Run(_ context.Context, command string, stdout, _ io.Writer) error {
	if command == benchmarkPreflightCommand() || strings.HasPrefix(command, "rm -rf -- ") {
		return nil
	}

	if strings.HasPrefix(command, "python3 -c ") {
		_, _ = io.WriteString(stdout, `{"data":[{"id":"router-model"}]}`)
		return nil
	}

	return fmt.Errorf("case failed")
}

func benchmarkFixtures() (v1.Endpoint, v1.Cluster, v1.Cluster) {
	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{Workspace: "default", Name: "chat"},
		Spec: &v1.EndpointSpec{
			Cluster: "cluster-a",
			Engine:  &v1.EndpointEngineSpec{Engine: v1.EngineNameVLLM},
			Model:   &v1.ModelSpec{Task: v1.TextGenerationModelTask},
		},
		Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
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

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return data
}

func resourceKey(kind, workspace, name string) string {
	return strings.Join([]string{kind, workspace, name}, "/")
}
