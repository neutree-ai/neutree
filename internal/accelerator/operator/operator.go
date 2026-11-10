// Package operator contains accelerator-specific Kubernetes lifecycle
// implementations. It is intentionally separate from the legacy Metrics and
// HAMi component renderers so an operator-owned accelerator cannot be
// accidentally rendered by both paths.
package operator

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
)

// AcceleratorOperator owns the complete Kubernetes lifecycle for one
// accelerator type. The cluster reconciler only supplies context and consumes
// the result; it does not interpret vendor-specific readiness.
type AcceleratorOperator interface {
	Name() string
	AcceleratorType() string
	Reconcile(context.Context, *OperatorContext) (OperatorResult, error)
	Status(context.Context, *OperatorContext) (*v1.ComponentStatus, error)
	Delete(context.Context, *OperatorContext) error
}

// OperatorContext contains cluster-scoped inputs shared by accelerator
// operators. Client points at the target Kubernetes API and is never nil in a
// normal reconcile.
type OperatorContext struct {
	Cluster         *v1.Cluster
	Client          client.Client
	Namespace       string
	ImagePrefix     string
	ImagePullSecret string
	Config          v1.KubernetesClusterConfig
	MetricsTargets  metricsconfig.Registry
}

// OperatorResult is the vendor-specific output of one reconcile. Resources
// are a plan rather than an already-applied object list; the owner integration
// can render/apply them with its existing deployer and attach cluster labels.
type OperatorResult struct {
	Provider    ProviderState
	Plan        ComponentPlan
	RebuiltPods []string
	DeletedPods []string
	Resources   *unstructured.UnstructuredList
}

// ComponentPlan describes the base workloads an operator must render. The
// plan carries only placement and ownership metadata so it can be merged into
// existing vendor-specific templates without duplicating Metrics/HAMi code.
type ComponentPlan struct {
	AcceleratorType string
	Managed         bool
	NodeSelector    map[string]string
	OwnerLabels     map[string]string
	Components      []ComponentSpec
}

type ComponentSpec struct {
	Name         string
	Kind         string
	NodeSelector map[string]string
	OwnerLabels  map[string]string
}

// ProviderState is the current provider and gate state for one target
// Kubernetes cluster. ProviderLastContainerStart is the latest current
// container start among all enabled provider DaemonSets.
type ProviderState struct {
	Managed                    bool
	Ready                      bool
	DriverManaged              bool
	ToolkitManaged             bool
	ProviderLastContainerStart time.Time
	ProviderStartKnown         bool
	PolicyGeneration           int64
	OperatorVersion            string
	OperatorVersionKnown       bool
	ClientLabelManaged         bool
	RuntimeClass               string
	ContainerRuntime           string
	Reason                     string
	NodeStates                 map[string]ProviderNodeState
}

// BasePodStartDecision is the result of comparing a base Pod with the
// provider's current container start boundary.
type BasePodStartDecision string

const (
	BasePodStartUnknown BasePodStartDecision = "unknown"
	BasePodStartKeep    BasePodStartDecision = "keep"
	BasePodStartRebuild BasePodStartDecision = "rebuild"
)

const (
	ClientDeployLabel                 = "nvidia.com/gpu.deploy.client"
	ClientDeployManagedAnnotation     = "neutree.ai/gpu.deploy.client.managed"
	OperatorOwnerLabel                = "neutree.ai/accelerator-operator"
	OperatorOwnerValue                = "nvidia"
	DeploymentModeLabel               = "neutree.ai/accelerator-deployment-mode"
	DeploymentModeValue               = "operator"
	NVIDIAGPUVirtualizationLabelKey   = "neutree.ai/nvidia-vgpu-enabled"
	NVIDIAGPUVirtualizationLabelValue = "true"
	NvidiaNodeAgentScope              = "node-agent"
	NvidiaExporterScope               = "exporter"
	NvidiaVirtualizationScope         = "virtualization"
)

func nvidiaScopeComponent(scope string) string {
	return "accelerator-operator-nvidia-" + scope
}

const (
	NvidiaOperatorOwnerLabel = OperatorOwnerLabel
)

// BasePodStartDecisionFor compares every currently running container in a
// base Pod. A Pod is stale when any container started at or before the provider
// boundary. Unknown container state is intentionally conservative.
func BasePodStartDecisionFor(pod *corev1.Pod, providerStart time.Time) BasePodStartDecision {
	if pod == nil || len(pod.Status.ContainerStatuses) == 0 || providerStart.IsZero() {
		return BasePodStartUnknown
	}

	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
			return BasePodStartUnknown
		}

		if !status.State.Running.StartedAt.Time.After(providerStart) {
			return BasePodStartRebuild
		}
	}

	return BasePodStartKeep
}
