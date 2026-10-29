package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/client"
)

const (
	manifestFileName         = "manifest.json"
	preflightStdoutFileName  = "preflight.stdout.log"
	preflightStderrFileName  = "preflight.stderr.log"
	benchmarkResultFileName  = "result.json"
	benchmarkCommandFileName = "command.sh"
	benchmarkStdoutFileName  = "stdout.log"
	benchmarkLogFileName     = "benchmark.log"
	measurementScopeRoute    = "endpoint-route"
	benchmarkSucceededStatus = "Succeeded"
	benchmarkFailedStatus    = "Failed"
	benchmarkCanceledStatus  = "Canceled"
	remoteCleanupTimeout     = 30 * time.Second
)

type resourceGetter interface {
	GetContext(ctx context.Context, kind, workspace, name string) (json.RawMessage, error)
}

type clusterCredentialsGetter interface {
	GetClusterCredentialsContext(ctx context.Context, workspace, name string) (*v1.Cluster, error)
}

type targetResolver func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error)

type dependencies struct {
	resources          resourceGetter
	clusterCredentials clusterCredentialsGetter
	resolveTarget      targetResolver
}

type manifest struct {
	FormatVersion    string           `json:"format_version"`
	CreatedAt        time.Time        `json:"created_at"`
	MeasurementScope string           `json:"measurement_scope"`
	Endpoint         manifestEndpoint `json:"endpoint"`
	Target           manifestTarget   `json:"target"`
	Cases            []manifestCase   `json:"cases"`
	Error            string           `json:"error,omitempty"`
}

type manifestEndpoint struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
}

type manifestTarget struct {
	Platform        string `json:"platform"`
	RuntimeID       string `json:"runtime_id"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model,omitempty"`
	Tokenizer       string `json:"tokenizer"`
	TokenizerMode   string `json:"tokenizer_mode,omitempty"`
	TrustRemoteCode bool   `json:"trust_remote_code,omitempty"`
	ModelSelection  string `json:"model_selection"`
}

type manifestCase struct {
	Case        Case      `json:"case"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// Run resolves an Endpoint and runs its benchmark cases. It does not perform
// role checks: the existing resource and credentials APIs remain the only
// authorization boundaries.
func Run(ctx context.Context, api *client.Client, options Options) (string, error) {
	if api == nil || api.Generic == nil {
		return "", fmt.Errorf("Neutree API client is required")
	}

	return run(ctx, dependencies{
		resources:          api.Generic,
		clusterCredentials: api,
		resolveTarget:      resolveTarget,
	}, options)
}

func run(ctx context.Context, deps dependencies, options Options) (string, error) {
	if err := options.Validate(); err != nil {
		return "", err
	}

	runCtx, cancel := benchmarkContext(ctx, options.Timeout)
	defer cancel()

	if err := runCtx.Err(); err != nil {
		return "", err
	}

	benchmarkCases := expandCases(options.InputLens, options.OutputLens, options.Concurrencies)
	klog.Infof(
		"benchmark starting: endpoint=%s/%s cases=%d",
		options.Workspace,
		options.Endpoint,
		len(benchmarkCases),
	)

	if deps.resources == nil {
		return "", fmt.Errorf("resource client is required")
	}

	if deps.clusterCredentials == nil {
		return "", fmt.Errorf("cluster credential client is required")
	}

	endpoint, target, err := resolveRunTarget(runCtx, deps, options)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = target.Close()
	}()

	output, err := createOutputDirectory(options.OutputDir)
	if err != nil {
		return "", err
	}

	klog.Infof("benchmark artifacts: %s", output)

	result := manifest{
		FormatVersion:    "v1",
		CreatedAt:        time.Now().UTC(),
		MeasurementScope: measurementScopeRoute,
		Endpoint: manifestEndpoint{
			Workspace: endpoint.Metadata.Workspace,
			Name:      endpoint.Metadata.Name,
			Cluster:   endpoint.Spec.Cluster,
		},
		Target: manifestTarget{
			Platform:        target.Platform,
			RuntimeID:       target.RuntimeID,
			BaseURL:         target.BaseURL,
			Tokenizer:       target.Tokenizer.Path,
			TokenizerMode:   target.Tokenizer.Mode,
			TrustRemoteCode: target.Tokenizer.TrustRemoteCode,
			ModelSelection:  "first-v1-model",
		},
		Cases: make([]manifestCase, 0, len(benchmarkCases)),
	}
	if err := writeManifest(output, result); err != nil {
		return output, err
	}

	klog.Info("benchmark preflight started: checking vllm bench and discovering model")

	model, err := runPreflight(runCtx, target.runner, target.BaseURL, output)
	if err != nil {
		result.Error = err.Error()
		_ = writeManifest(output, result)

		klog.Errorf("benchmark preflight failed: %v", err)

		return output, err
	}

	klog.Infof("benchmark preflight completed: model=%s", model)

	target.Model = model
	result.Target.Model = model

	if err := writeManifest(output, result); err != nil {
		return output, err
	}

	remoteRoot := filepath.ToSlash(filepath.Join("/tmp", "neutree-bench-"+filepath.Base(output)))
	if err := prepareRemoteRoot(runCtx, target.runner, remoteRoot); err != nil {
		err = contextOrError(runCtx, err)
		result.Error = err.Error()
		_ = writeManifest(output, result)

		return output, err
	}

	preserveRoot := true

	defer func() {
		if preserveRoot {
			klog.Warningf("remote benchmark directory retained: %s on %s", remoteRoot, target.RuntimeID)
			return
		}

		cleanupRemoteDirectory(target.runner, remoteRoot)
	}()

	for caseIndex, benchmarkCase := range benchmarkCases {
		if err := runCtx.Err(); err != nil {
			result.Error = err.Error()
			_ = writeManifest(output, result)

			return output, err
		}

		caseNumber := caseIndex + 1
		command := benchmarkCaseCommand(target, options, benchmarkCase, remoteRoot)
		klog.Infof(
			"benchmark case %d/%d started: input_len=%d output_len=%d concurrency=%d num_prompts=%d request_rate=%s",
			caseNumber,
			len(benchmarkCases),
			benchmarkCase.InputLen,
			benchmarkCase.OutputLen,
			benchmarkCase.Concurrency,
			options.NumPrompts,
			options.RequestRate,
		)
		klog.Infof("benchmark case %d/%d command: %s", caseNumber, len(benchmarkCases), command)

		entry := manifestCase{Case: benchmarkCase, StartedAt: time.Now().UTC()}
		result.Cases = append(result.Cases, entry)

		if err := writeManifest(output, result); err != nil {
			return output, err
		}

		err := runCase(runCtx, target.runner, target, options, benchmarkCase, output, remoteRoot)
		completedAt := time.Now().UTC()
		manifestCaseIndex := len(result.Cases) - 1
		result.Cases[manifestCaseIndex].CompletedAt = completedAt

		if err != nil {
			result.Cases[manifestCaseIndex].Status = benchmarkCaseErrorStatus(err)
			result.Cases[manifestCaseIndex].Error = err.Error()
			result.Error = err.Error()
			_ = writeManifest(output, result)

			if errors.Is(err, context.Canceled) {
				klog.Infof("benchmark case %d/%d canceled: %v", caseNumber, len(benchmarkCases), err)
			} else {
				klog.Errorf("benchmark case %d/%d failed: %v", caseNumber, len(benchmarkCases), err)
			}

			return output, fmt.Errorf("benchmark case %s: %w", benchmarkCase.Name(), err)
		}

		result.Cases[manifestCaseIndex].Status = benchmarkSucceededStatus
		if err := writeManifest(output, result); err != nil {
			return output, err
		}

		resultPath := benchmarkCaseResultPath(output, benchmarkCase)

		summaryText := benchmarkResultSummaryText(resultPath)
		if summaryText == "" {
			klog.Infof(
				"benchmark case %d/%d completed: result=%s summary=unavailable",
				caseNumber,
				len(benchmarkCases),
				resultPath,
			)

			continue
		}

		klog.Infof(
			"benchmark case %d/%d completed: result=%s %s",
			caseNumber,
			len(benchmarkCases),
			resultPath,
			summaryText,
		)
	}

	preserveRoot = false

	klog.Infof("benchmark completed: cases=%d result_dir=%s", len(benchmarkCases), output)

	return output, nil
}

func resolveRunTarget(ctx context.Context, deps dependencies, options Options) (*v1.Endpoint, BenchTarget, error) {
	klog.Info("benchmark endpoint resolving")

	endpoint, err := loadEndpoint(ctx, deps.resources, options.Workspace, options.Endpoint)
	if err := contextOrError(ctx, err); err != nil {
		return nil, BenchTarget{}, err
	}

	if err := validateEndpoint(endpoint); err != nil {
		return nil, BenchTarget{}, err
	}

	klog.Infof("benchmark endpoint resolved: cluster=%s", endpoint.Spec.Cluster)
	klog.Infof("benchmark cluster resolving: cluster=%s", endpoint.Spec.Cluster)

	cluster, err := loadCluster(ctx, deps.resources, options.Workspace, endpoint.Spec.Cluster)
	if err := contextOrError(ctx, err); err != nil {
		return nil, BenchTarget{}, err
	}

	if err := validateCluster(cluster); err != nil {
		return nil, BenchTarget{}, err
	}

	klog.Infof("benchmark cluster resolved: type=%s", cluster.Spec.Type)
	klog.Infof("benchmark credentials resolving: cluster=%s", endpoint.Spec.Cluster)

	credentialCluster, err := deps.clusterCredentials.GetClusterCredentialsContext(ctx, options.Workspace, endpoint.Spec.Cluster)
	if err := contextOrError(ctx, err); err != nil {
		return nil, BenchTarget{}, fmt.Errorf("get cluster credentials: %w", err)
	}

	klog.Infof("benchmark credentials resolved: cluster=%s", endpoint.Spec.Cluster)

	if deps.resolveTarget == nil {
		return nil, BenchTarget{}, fmt.Errorf("benchmark target resolver is required")
	}

	klog.Info("benchmark target resolving")

	target, err := deps.resolveTarget(ctx, endpoint, cluster, credentialCluster)
	if err != nil {
		return nil, BenchTarget{}, contextOrError(ctx, err)
	}

	if err := ctx.Err(); err != nil {
		_ = target.Close()
		return nil, BenchTarget{}, err
	}

	if target.runner == nil {
		_ = target.Close()
		return nil, BenchTarget{}, fmt.Errorf("benchmark target runner is required")
	}

	klog.Infof("benchmark target resolved: platform=%s runtime=%s", target.Platform, target.RuntimeID)

	return endpoint, target, nil
}

func loadEndpoint(ctx context.Context, resources resourceGetter, workspace, name string) (*v1.Endpoint, error) {
	raw, err := resources.GetContext(ctx, "Endpoint", workspace, name)
	if err != nil {
		return nil, fmt.Errorf("get Endpoint %q: %w", name, err)
	}

	endpoint := &v1.Endpoint{}
	if err := json.Unmarshal(raw, endpoint); err != nil {
		return nil, fmt.Errorf("decode Endpoint %q: %w", name, err)
	}

	return endpoint, nil
}

func loadCluster(ctx context.Context, resources resourceGetter, workspace, name string) (*v1.Cluster, error) {
	raw, err := resources.GetContext(ctx, "Cluster", workspace, name)
	if err != nil {
		return nil, fmt.Errorf("get Cluster %q: %w", name, err)
	}

	cluster := &v1.Cluster{}
	if err := json.Unmarshal(raw, cluster); err != nil {
		return nil, fmt.Errorf("decode Cluster %q: %w", name, err)
	}

	return cluster, nil
}

func validateEndpoint(endpoint *v1.Endpoint) error {
	if endpoint == nil || endpoint.Metadata == nil || endpoint.Spec == nil || endpoint.Status == nil {
		return fmt.Errorf("Endpoint metadata, spec, and status are required")
	}

	if endpoint.Metadata.Workspace == "" || endpoint.Metadata.Name == "" {
		return fmt.Errorf("Endpoint workspace and name are required")
	}

	if endpoint.Status.Phase != v1.EndpointPhaseRUNNING {
		return fmt.Errorf("Endpoint %q is not Running (phase=%s)", endpoint.Metadata.Name, endpoint.Status.Phase)
	}

	if endpoint.Spec.Cluster == "" {
		return fmt.Errorf("Endpoint %q has no Cluster", endpoint.Metadata.Name)
	}

	if endpoint.Spec.Engine == nil || endpoint.Spec.Engine.Engine != v1.EngineNameVLLM {
		engine := ""
		if endpoint.Spec.Engine != nil {
			engine = endpoint.Spec.Engine.Engine
		}

		return fmt.Errorf("Endpoint %q uses unsupported engine %q; only vllm is supported", endpoint.Metadata.Name, engine)
	}

	if endpoint.Spec.Model == nil || endpoint.Spec.Model.Task != v1.TextGenerationModelTask {
		task := ""
		if endpoint.Spec.Model != nil {
			task = endpoint.Spec.Model.Task
		}

		return fmt.Errorf("Endpoint %q uses unsupported task %q; only text-generation is supported", endpoint.Metadata.Name, task)
	}

	return nil
}

func validateCluster(cluster *v1.Cluster) error {
	if cluster == nil || cluster.Metadata == nil || cluster.Spec == nil || cluster.Status == nil {
		return fmt.Errorf("Cluster metadata, spec, and status are required")
	}

	if cluster.Metadata.Workspace == "" || cluster.Metadata.Name == "" {
		return fmt.Errorf("Cluster workspace and name are required")
	}

	if cluster.Status.Phase != v1.ClusterPhaseRunning {
		return fmt.Errorf("Cluster %q is not Running (phase=%s)", cluster.Metadata.Name, cluster.Status.Phase)
	}

	if cluster.Spec.Type != v1.KubernetesClusterType && cluster.Spec.Type != v1.SSHClusterType {
		return fmt.Errorf("Cluster %q has unsupported type %q", cluster.Metadata.Name, cluster.Spec.Type)
	}

	return nil
}

func resolveTarget(ctx context.Context, endpoint *v1.Endpoint, cluster, credentialCluster *v1.Cluster) (BenchTarget, error) {
	if cluster == nil || cluster.Spec == nil {
		return BenchTarget{}, fmt.Errorf("Cluster spec is required")
	}

	switch cluster.Spec.Type {
	case v1.KubernetesClusterType:
		return resolveKubernetesTarget(ctx, cluster, credentialCluster, endpoint)
	case v1.SSHClusterType:
		return resolveStaticTarget(ctx, cluster, credentialCluster, endpoint)
	default:
		return BenchTarget{}, fmt.Errorf("unsupported Cluster type %q", cluster.Spec.Type)
	}
}

func createOutputDirectory(base string) (string, error) {
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", fmt.Errorf("create benchmark output directory: %w", err)
	}

	output, err := os.MkdirTemp(base, "neutree-bench-")
	if err != nil {
		return "", fmt.Errorf("create benchmark result directory: %w", err)
	}

	return output, nil
}

func benchmarkContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout == 0 {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, timeout)
}

func contextOrError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	return err
}

func benchmarkCaseErrorStatus(err error) string {
	if errors.Is(err, context.Canceled) {
		return benchmarkCanceledStatus
	}

	return benchmarkFailedStatus
}

func runPreflight(ctx context.Context, runner remoteRunner, baseURL, output string) (string, error) {
	stdout, err := createOutputFile(filepath.Join(output, preflightStdoutFileName))
	if err != nil {
		return "", err
	}
	defer stdout.Close() //nolint:errcheck

	stderr, err := createOutputFile(filepath.Join(output, preflightStderrFileName))
	if err != nil {
		return "", err
	}
	defer stderr.Close() //nolint:errcheck

	if err := runner.Run(ctx, benchmarkPreflightCommand(), stdout, stderr); err != nil {
		return "", fmt.Errorf("vllm benchmark preflight failed: %w", err)
	}

	var response bytes.Buffer
	if err := runner.Run(ctx, modelDiscoveryCommand(baseURL), io.MultiWriter(stdout, &response), stderr); err != nil {
		return "", fmt.Errorf("discover benchmark model: %w", err)
	}

	model, err := firstModelName(response.Bytes())
	if err != nil {
		return "", err
	}

	if _, err := fmt.Fprintf(stdout, "\nselected model: %s\n", model); err != nil {
		return "", fmt.Errorf("record benchmark model: %w", err)
	}

	return model, nil
}

// runCase runs one benchmark case. It removes the remote case directory only
// after the case succeeds, so a failed or canceled run stays inspectable.
func runCase(ctx context.Context, runner remoteRunner, target BenchTarget, options Options, benchmarkCase Case, output, remoteRoot string) (err error) {
	caseOutput := filepath.Join(output, benchmarkCase.Name())
	if err := os.MkdirAll(caseOutput, 0700); err != nil {
		return fmt.Errorf("create case output directory: %w", err)
	}

	stdout, err := createOutputFile(filepath.Join(caseOutput, benchmarkStdoutFileName))
	if err != nil {
		return err
	}
	defer stdout.Close() //nolint:errcheck

	remoteResultDir := benchmarkCaseRemoteResultDirectory(remoteRoot, benchmarkCase)

	defer func() {
		if err != nil {
			klog.Warningf("remote benchmark directory retained: %s", remoteResultDir)
			return
		}

		cleanupRemoteDirectory(runner, remoteResultDir)
	}()

	command := benchmarkCaseCommand(target, options, benchmarkCase, remoteRoot)
	if err := writeCommandFile(filepath.Join(caseOutput, benchmarkCommandFileName), command); err != nil {
		return err
	}

	if err := prepareBenchmarkControl(ctx, runner, remoteResultDir); err != nil {
		return err
	}

	// The monitor relays the benchmark output on the stream this CLI already
	// holds open, so the live log reaches stderr and stdout.log without a
	// second remote process to clean up on interrupt.
	logOutput := io.MultiWriter(stdout, os.Stderr)

	stopRenew := renewRemoteLease(ctx, runner, benchmarkRenewPath(remoteResultDir), remoteRenewInterval)
	defer stopRenew()

	if err := runControlledBenchmark(ctx, runner, remoteResultDir, command, logOutput); err != nil {
		return err
	}

	if err := collectBenchmarkResult(ctx, runner, remoteResultDir, caseOutput, stdout); err != nil {
		return err
	}

	return validateBenchmarkResult(caseOutput)
}

// validateBenchmarkResult rejects a result the serving route produced without
// completing any request. vLLM exits successfully and writes result.json in
// that case, so a non-empty file alone does not mean the case passed.
func validateBenchmarkResult(caseOutput string) error {
	summary, err := readBenchmarkResultSummary(filepath.Join(caseOutput, benchmarkResultFileName))
	if err != nil {
		return fmt.Errorf("read benchmark result summary: %w", err)
	}

	if summary.Completed != nil && *summary.Completed == 0 {
		return fmt.Errorf("benchmark completed 0 requests")
	}

	return nil
}

func collectBenchmarkResult(ctx context.Context, runner remoteRunner, remoteResultDir, caseOutput string, stderr io.Writer) error {
	resultPath := filepath.Join(caseOutput, benchmarkResultFileName)
	partialPath := resultPath + ".partial"

	result, err := createOutputFile(partialPath)
	if err != nil {
		return err
	}

	defer result.Close() //nolint:errcheck

	err = runner.Run(ctx, remoteResultCommand(remoteResultDir), result, stderr)
	if err := contextOrError(ctx, err); err != nil {
		return fmt.Errorf("copy benchmark result: %w", err)
	}

	info, err := result.Stat()
	if err != nil {
		return fmt.Errorf("inspect copied benchmark result: %w", err)
	}

	if info.Size() == 0 {
		return fmt.Errorf("copied benchmark result is empty")
	}

	if err := result.Close(); err != nil {
		return fmt.Errorf("close copied benchmark result: %w", err)
	}

	if err := os.Rename(partialPath, resultPath); err != nil {
		return fmt.Errorf("publish benchmark result: %w", err)
	}

	return nil
}

func benchmarkCaseRemoteResultDirectory(remoteRoot string, benchmarkCase Case) string {
	return filepath.ToSlash(filepath.Join(remoteRoot, benchmarkCase.Name()))
}

func benchmarkLogPath(remoteDir string) string {
	return filepath.ToSlash(filepath.Join(remoteDir, benchmarkLogFileName))
}

func benchmarkCaseResultPath(output string, benchmarkCase Case) string {
	return filepath.Join(output, benchmarkCase.Name(), benchmarkResultFileName)
}

func benchmarkCaseCommand(target BenchTarget, options Options, benchmarkCase Case, remoteRoot string) string {
	return buildBenchCommand(
		target,
		benchmarkCase,
		options.NumPrompts,
		options.RequestRate,
		benchmarkCaseRemoteResultDirectory(remoteRoot, benchmarkCase),
	)
}

func cleanupRemoteDirectory(runner remoteRunner, directory string) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteCleanupTimeout)
	defer cancel()

	if err := runner.Run(ctx, remoteCleanupCommand(directory), io.Discard, io.Discard); err != nil {
		klog.Warningf("failed to clean remote benchmark directory %s: %v", directory, err)
	}
}

func createOutputFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}

	return file, nil
}

func writeCommandFile(path, command string) error {
	if err := os.WriteFile(path, []byte(command+"\n"), 0600); err != nil {
		return fmt.Errorf("write benchmark command: %w", err)
	}

	return nil
}

func writeManifest(output string, value manifest) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode benchmark manifest: %w", err)
	}

	data = append(data, '\n')
	target := filepath.Join(output, manifestFileName)
	temporary := target + ".tmp"

	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return fmt.Errorf("write benchmark manifest: %w", err)
	}

	if err := os.Rename(temporary, target); err != nil {
		return fmt.Errorf("publish benchmark manifest: %w", err)
	}

	return nil
}
