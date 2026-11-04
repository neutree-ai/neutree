package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/client"
)

// Run resolves an Endpoint and runs its benchmark cases. It does not perform
// role checks: the existing resource and credentials APIs remain the only
// authorization boundaries.
func Run(ctx context.Context, api *client.Client, options Options) (string, error) {
	return run(ctx, api, options)
}

func run(ctx context.Context, api *client.Client, options Options) (string, error) {
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

	if api == nil || api.Generic == nil {
		return "", fmt.Errorf("Neutree API client is required")
	}

	endpoint, target, err := resolveRunTarget(runCtx, api, options)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = target.Close()
	}()

	klog.Infof("benchmark endpoint ready: %s/%s", endpoint.Metadata.Workspace, endpoint.Metadata.Name)

	// Case execution and result collection are added separately; until then the
	// command can resolve and validate its target but cannot run one.
	return "", fmt.Errorf("benchmark execution is not implemented for %s targets", target.Platform)
}

func resolveRunTarget(ctx context.Context, api *client.Client, options Options) (*v1.Endpoint, BenchTarget, error) {
	klog.Info("benchmark endpoint resolving")

	endpoint, err := loadEndpoint(ctx, api, options.Workspace, options.Endpoint)
	if err := contextOrError(ctx, err); err != nil {
		return nil, BenchTarget{}, err
	}

	if err := validateEndpoint(endpoint); err != nil {
		return nil, BenchTarget{}, err
	}

	model, err := firstServedModel(endpoint)
	if err != nil {
		return nil, BenchTarget{}, err
	}

	klog.Infof("benchmark endpoint resolved: cluster=%s", endpoint.Spec.Cluster)
	klog.Infof("benchmark cluster resolving: cluster=%s", endpoint.Spec.Cluster)

	cluster, err := loadCluster(ctx, api, options.Workspace, endpoint.Spec.Cluster)
	if err := contextOrError(ctx, err); err != nil {
		return nil, BenchTarget{}, err
	}

	if err := validateCluster(cluster); err != nil {
		return nil, BenchTarget{}, err
	}

	klog.Infof("benchmark cluster resolved: type=%s", cluster.Spec.Type)
	klog.Infof("benchmark credentials resolving: cluster=%s", endpoint.Spec.Cluster)

	credentialCluster, err := api.GetClusterCredentialsContext(ctx, options.Workspace, endpoint.Spec.Cluster)
	if err := contextOrError(ctx, err); err != nil {
		return nil, BenchTarget{}, fmt.Errorf("get cluster credentials: %w", err)
	}

	klog.Infof("benchmark credentials resolved: cluster=%s", endpoint.Spec.Cluster)
	klog.Info("benchmark target resolving")

	tokenizer, err := resolveTokenizerPath(ctx, api, options.Workspace, endpoint, cluster)
	if err := contextOrError(ctx, err); err != nil {
		return nil, BenchTarget{}, err
	}

	target, err := resolveTarget(ctx, endpoint, cluster, credentialCluster)
	if err != nil {
		return nil, BenchTarget{}, contextOrError(ctx, err)
	}

	target.Tokenizer = tokenizer
	target.Model = model

	klog.Infof("benchmark model resolved: model=%s", model)

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

func loadEndpoint(ctx context.Context, api *client.Client, workspace, name string) (*v1.Endpoint, error) {
	raw, err := api.Generic.GetContext(ctx, "Endpoint", workspace, name)
	if err != nil {
		return nil, fmt.Errorf("get Endpoint %q: %w", name, err)
	}

	endpoint := &v1.Endpoint{}
	if err := json.Unmarshal(raw, endpoint); err != nil {
		return nil, fmt.Errorf("decode Endpoint %q: %w", name, err)
	}

	return endpoint, nil
}

func loadCluster(ctx context.Context, api *client.Client, workspace, name string) (*v1.Cluster, error) {
	raw, err := api.Generic.GetContext(ctx, "Cluster", workspace, name)
	if err != nil {
		return nil, fmt.Errorf("get Cluster %q: %w", name, err)
	}

	cluster := &v1.Cluster{}
	if err := json.Unmarshal(raw, cluster); err != nil {
		return nil, fmt.Errorf("decode Cluster %q: %w", name, err)
	}

	return cluster, nil
}

// resolveTokenizerPath computes the path the engine reads its model from inside
// its own container. The benchmark runs in that container and passes the path to
// the tokenizer, so prompt lengths are tokenized with the files the server
// actually serves.
func resolveTokenizerPath(ctx context.Context, api *client.Client, workspace string, endpoint *v1.Endpoint, cluster *v1.Cluster) (string, error) {
	mountRoot := v1.DefaultK8sClusterModelCacheMountPath
	if cluster.Spec.Type == v1.SSHClusterType {
		mountRoot = v1.DefaultSSHClusterModelCacheMountPath
	}

	modelRegistry, err := loadEndpointModelRegistry(ctx, api, workspace, endpoint)
	if err != nil {
		return "", err
	}

	version, err := resolveModelVersion(ctx, api, workspace, endpoint, modelRegistry)
	if err != nil {
		return "", err
	}

	modelPath, err := tokenizerPath(cluster, endpoint.Spec.Model, modelRegistry, version, mountRoot)
	if err != nil {
		return "", fmt.Errorf("resolve model path: %w", err)
	}

	return modelPath, nil
}

// tokenizerPath builds the path from an already resolved version, so the path
// rules can be exercised without a control plane.
func tokenizerPath(cluster *v1.Cluster, model *v1.ModelSpec, modelRegistry *v1.ModelRegistry, version, mountRoot string) (string, error) {
	if model == nil {
		return "", fmt.Errorf("Endpoint model is required")
	}

	// An Endpoint that names no registry loads the model by the name the spec
	// carries. Every registry-backed model is placed in the cluster's model
	// cache, which exists even when the cluster configures none of its own.
	if modelRegistry == nil || modelRegistry.Spec == nil {
		return model.Name, nil
	}

	return util.ModelCachePath(*cluster, model.Name, version, mountRoot)
}

// resolveModelVersion returns the version the model is placed under. A BentoML
// registry resolves "latest" by reading the registry's storage, which only the
// control plane mounts; everyone else takes the reference from the spec after
// the same normalization the deploy path applies.
func resolveModelVersion(
	ctx context.Context,
	api *client.Client,
	workspace string,
	endpoint *v1.Endpoint,
	modelRegistry *v1.ModelRegistry,
) (string, error) {
	if endpoint.Spec.Model == nil {
		return "", nil
	}

	version := endpoint.Spec.Model.Version
	if modelRegistry == nil || modelRegistry.Spec == nil {
		return version, nil
	}

	switch modelRegistry.Spec.Type {
	case v1.HuggingFaceModelRegistryType:
		return version, nil
	case v1.ModelScopeModelRegistryType:
		// Serving normalizes ModelScope's "latest" to the default branch, so
		// the path must not carry a literal "latest" directory.
		if version == v1.LatestVersion {
			return "", nil
		}

		return version, nil
	case v1.BentoMLModelRegistryType:
		if version != "" && version != v1.LatestVersion {
			return version, nil
		}
	default:
		return "", fmt.Errorf("unsupported model registry type: %s", modelRegistry.Spec.Type)
	}

	if api == nil || api.Models == nil {
		return "", fmt.Errorf("resolve version of model %q: Neutree API client is required", endpoint.Spec.Model.Name)
	}

	detail, err := api.Models.GetContext(ctx, workspace, endpoint.Spec.Model.Registry, endpoint.Spec.Model.Name, version)
	if err != nil {
		return "", fmt.Errorf("resolve version of model %q: %w", endpoint.Spec.Model.Name, err)
	}

	if detail == nil || detail.Version == nil || detail.Version.Name == "" {
		return "", fmt.Errorf("resolve version of model %q: control plane reported no version", endpoint.Spec.Model.Name)
	}

	return detail.Version.Name, nil
}

// loadEndpointModelRegistry returns the registry the Endpoint deploys from, or
// nil when it deploys from none.
func loadEndpointModelRegistry(ctx context.Context, api *client.Client, workspace string, endpoint *v1.Endpoint) (*v1.ModelRegistry, error) {
	if endpoint.Spec.Model == nil || endpoint.Spec.Model.Registry == "" {
		return nil, nil
	}

	raw, err := api.Generic.GetContext(ctx, "ModelRegistry", workspace, endpoint.Spec.Model.Registry)
	if err != nil {
		return nil, fmt.Errorf("get ModelRegistry %q: %w", endpoint.Spec.Model.Registry, err)
	}

	modelRegistry := &v1.ModelRegistry{}
	if err := json.Unmarshal(raw, modelRegistry); err != nil {
		return nil, fmt.Errorf("decode ModelRegistry %q: %w", endpoint.Spec.Model.Registry, err)
	}

	return modelRegistry, nil
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

// resolveTarget builds the transport for the Endpoint's Cluster. Each Cluster
// type gets its own resolver; a type no resolver handles is reported rather
// than silently skipped.
//
//nolint:unparam // the target is returned once cluster-specific resolvers land
func resolveTarget(_ context.Context, _ *v1.Endpoint, cluster, _ *v1.Cluster) (BenchTarget, error) {
	if cluster == nil || cluster.Spec == nil {
		return BenchTarget{}, fmt.Errorf("Cluster spec is required")
	}

	return BenchTarget{}, fmt.Errorf("benchmark target for Cluster type %q is not implemented", cluster.Spec.Type)
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
