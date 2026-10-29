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

type fakeResourceGetter struct {
	resources map[string]json.RawMessage
}

func (f fakeResourceGetter) GetContext(_ context.Context, kind, workspace, name string) (json.RawMessage, error) {
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

func (f fakeClusterCredentials) GetClusterCredentialsContext(_ context.Context, _, _ string) (*v1.Cluster, error) {
	return f.cluster, f.err
}

type fakeBenchRunner struct {
	mu       sync.Mutex
	commands []string
	model    string
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

func TestRunProgressUsesKlogStderr(t *testing.T) {
	const childEnvironment = "NEUTREE_BENCH_KLOG_TEST_CHILD"
	if os.Getenv(childEnvironment) == "1" {
		_, err := run(context.Background(), dependencies{}, validOptions(t.TempDir()))
		require.ErrorContains(t, err, "resource client is required")
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

func captureBenchmarkProgress(t *testing.T) *bytes.Buffer {
	t.Helper()

	state := klog.CaptureState()
	t.Cleanup(state.Restore)

	output := &bytes.Buffer{}
	klog.LogToStderr(false)
	klog.SetOutput(output)

	return output
}

func (r *fakeBenchRunner) Run(_ context.Context, command string, stdout, stderr io.Writer) error {
	r.record(command)

	switch {
	case command == benchmarkPreflightCommand():
		_, _ = io.WriteString(stdout, "usage\n")
	case strings.Contains(command, "# neutree-bench-monitor"):
		_, _ = io.WriteString(stdout, "benchmark output\n")
		_, _ = io.WriteString(stderr, "benchmark diagnostics\n")
	case strings.HasPrefix(command, "tail -n +1 -F -- "):
		_, _ = io.WriteString(stdout, "benchmark log output\n")
	case strings.HasPrefix(command, "touch -- "), strings.HasPrefix(command, ": > "):
		return nil
	case strings.HasPrefix(command, "umask 077 && mkdir -- "):
		return nil
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
	progress := captureBenchmarkProgress(t)

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
				Tokenizer: tokenizerOptions{Path: "/models/chat"},
				runner:    runner,
			}, nil
		},
	}, Options{
		Workspace:     "default",
		Endpoint:      "chat",
		InputLens:     []int{16},
		OutputLens:    []int{4},
		Concurrencies: []int{2},
		NumPrompts:    20,
		RequestRate:   "inf",
		OutputDir:     outputDir,
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
	require.Contains(t, string(stdout), "benchmark log output")
	require.Contains(t, string(stdout), "benchmark diagnostics")

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

	require.Equal(t, benchmarkPreflightCommand(), runner.commands[0])
	require.Contains(t, runner.commands[1], "/v1/models")
	require.Contains(t, string(command), "--model 'router-model'")

	monitor := findBenchCommand(runner.commands, func(value string) bool {
		return strings.Contains(value, "# neutree-bench-monitor")
	})
	require.Contains(t, monitor, "vllm bench serve")
	require.Contains(t, monitor, ".neutree-bench-renew")
	require.Contains(t, monitor, ".neutree-bench-cancel")

	require.Contains(t, findBenchCommand(runner.commands, func(value string) bool {
		return strings.HasPrefix(value, "tail -n +1 -F -- ")
	}), "benchmark.log")

	require.Equal(t, 2, countBenchCommands(runner.commands, func(value string) bool {
		return strings.HasPrefix(value, "rm -rf -- ")
	}), "the case directory and the run root are removed on success")

	progressOutput := progress.String()
	for _, line := range strings.Split(strings.TrimSpace(progressOutput), "\n") {
		require.Regexp(t, `^I[0-9]{4} [0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}\s+[0-9]+ run\.go:[0-9]+\] `, line)
	}

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
	require.Contains(t, progressOutput, "benchmark case 1/1 command: "+strings.TrimSpace(string(command)))
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
	progress := captureBenchmarkProgress(t)

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
				Tokenizer: tokenizerOptions{Path: "/models/chat"},
				runner:    runner,
			}, nil
		},
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
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(output, "input-16-output-4-concurrency-2", benchmarkResultFileName))
	require.Contains(t, progress.String(), "benchmark case 1/1 completed: result=")
	require.Contains(t, progress.String(), "summary=unavailable")
	require.Contains(t, progress.String(), "benchmark preflight completed: model="+runner.model)
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

func TestRunRecordsPreflightAndCaseFailures(t *testing.T) {
	endpoint, cluster, credentialCluster := benchmarkFixtures()
	resources := fakeResourceGetter{resources: map[string]json.RawMessage{
		resourceKey("Endpoint", "default", "chat"):     mustJSON(t, endpoint),
		resourceKey("Cluster", "default", "cluster-a"): mustJSON(t, cluster),
	}}

	t.Run("preflight", func(t *testing.T) {
		progress := captureBenchmarkProgress(t)
		options := validOptions(t.TempDir())

		output, err := run(context.Background(), dependencies{
			resources:          resources,
			clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
			resolveTarget: func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error) {
				return BenchTarget{runner: outputRunner{err: fmt.Errorf("preflight failed")}}, nil
			},
		}, options)
		require.ErrorContains(t, err, "preflight")
		require.Contains(t, progress.String(), "benchmark preflight failed: vllm benchmark preflight failed: preflight failed")
		require.Regexp(t, `(?m)^E[0-9]{4} .*benchmark preflight failed:`, progress.String())

		manifest, readErr := os.ReadFile(filepath.Join(output, manifestFileName))
		require.NoError(t, readErr)
		require.Contains(t, string(manifest), "preflight failed")
	})

	t.Run("case", func(t *testing.T) {
		runner := &caseFailureRunner{}
		progress := captureBenchmarkProgress(t)
		options := validOptions(t.TempDir())

		output, err := run(context.Background(), dependencies{
			resources:          resources,
			clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
			resolveTarget: func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error) {
				return BenchTarget{BaseURL: "http://router-service.ns.svc.cluster.local:8000/default/chat", runner: runner}, nil
			},
		}, options)
		require.ErrorContains(t, err, "benchmark case")
		require.Contains(t, progress.String(), "benchmark case 1/1 failed: case failed")
		require.Regexp(t, `(?m)^E[0-9]{4} .*benchmark case 1/1 failed:`, progress.String())

		manifest, readErr := os.ReadFile(filepath.Join(output, manifestFileName))
		require.NoError(t, readErr)
		require.Contains(t, string(manifest), `"status": "Failed"`)
		require.Contains(t, string(manifest), "case failed")
	})
}

func TestRunFailsCaseWhenNoRequestCompletes(t *testing.T) {
	endpoint, cluster, credentialCluster := benchmarkFixtures()
	runner := &fakeBenchRunner{result: `{"completed":0,"failed":20,"duration":1.5}`}
	progress := captureBenchmarkProgress(t)

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
				runner:    runner,
			}, nil
		},
	}, validOptions(t.TempDir()))
	require.ErrorContains(t, err, "benchmark completed 0 requests")
	require.Contains(t, progress.String(), "benchmark case 1/1 failed: benchmark completed 0 requests")

	manifest, readErr := os.ReadFile(filepath.Join(output, manifestFileName))
	require.NoError(t, readErr)
	require.Contains(t, string(manifest), `"status": "Failed"`)
	require.FileExists(t, filepath.Join(output, "input-16-output-4-concurrency-2", benchmarkResultFileName))
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
	err := runCase(context.Background(), runner, BenchTarget{Model: "router-model", Tokenizer: tokenizerOptions{Path: "/models/chat"}}, Options{
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
	switch {
	case command == benchmarkPreflightCommand(),
		strings.HasPrefix(command, "umask 077 && mkdir -- "),
		strings.HasPrefix(command, "rm -rf -- "),
		strings.HasPrefix(command, "touch -- "),
		strings.HasPrefix(command, ": > "),
		strings.HasPrefix(command, "tail -n +1 -F -- "):
		return nil
	case strings.Contains(command, "# neutree-bench-monitor"):
		return fmt.Errorf("case failed")
	case strings.HasPrefix(command, "python3 -c "):
		_, _ = io.WriteString(stdout, `{"data":[{"id":"router-model"}]}`)

		return nil
	default:
		return fmt.Errorf("case failed")
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
