package manifests

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1 "github.com/neutree-ai/neutree/api/v1"
	shared "github.com/neutree-ai/neutree/internal/accelerator/operator/metrics"
	sharedmanifests "github.com/neutree-ai/neutree/internal/accelerator/operator/metrics/manifests"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	ClientDeployLabel    = "nvidia.com/gpu.deploy.client"
	ToolkitReadyHostPath = "/run/nvidia/validations"
	ToolkitReadyImage    = "busybox:1.36"
	daemonSetKind        = "DaemonSet"
	NodeAgentName        = "neutree-node-agent"
	ExporterName         = "nvidia-gpu-dcgm-exporter"
	ExporterPort         = 19400
	NodeAgentPort        = 19101
	OwnerLabel           = "neutree.ai/accelerator-operator"
	OwnerValue           = "nvidia"
	DeploymentModeLabel  = "neutree.ai/accelerator-deployment-mode"
	DeploymentModeValue  = "operator"
	MetricsTargetLabel   = "neutree.ai/metrics-target"
	AcceleratorTypeLabel = "neutree.ai/accelerator-type"
)

type NvidiaRenderOptions struct {
	Cluster          *v1.Cluster
	Namespace        string
	ImagePrefix      string
	ImagePullSecret  string
	Profile          *v1.AcceleratorProfile
	Managed          bool
	ExporterTarget   *v1.MetricsTargetProfile
	ExternalExporter bool
}

func RenderNvidia(_ context.Context, options NvidiaRenderOptions) (*unstructured.UnstructuredList, error) {
	if options.Cluster == nil || options.Cluster.Metadata == nil {
		return nil, fmt.Errorf("cluster metadata is required")
	}

	if options.Profile == nil {
		return nil, fmt.Errorf("NVIDIA accelerator profile is required")
	}

	if options.ExporterTarget == nil && !options.ExternalExporter {
		options.ExporterTarget = NvidiaManagedExporterTarget(options.Cluster, options.Namespace, options.Profile)
	}

	selection, err := clusterversion.SelectNodeAgent(options.Cluster.GetVersion())
	if err != nil {
		return nil, err
	}

	publicSelection := shared.NodeAgentSelection{Image: selection.Image, Legacy: selection.Contract == clusterversion.NodeAgentContractLegacy}

	agent, err := shared.NewNodeAgent(publicSelection, v1.AcceleratorTypeNVIDIAGPU.String(), options.ExporterTarget,
		NvidiaVirtualizationMetricsTarget(options.Namespace, options.Profile), options.Profile.NodeAgentRuntime)
	if err != nil {
		return nil, err
	}

	variables := shared.NewManifestVariables(options.Cluster, options.Namespace, options.ImagePrefix, options.ImagePullSecret, publicSelection)
	variables.EnableNeutreeNodeAgentMetrics = true
	variables.NodeAgent = agent

	if profile := options.Profile.MetricsExporter; profile != nil && !options.ExternalExporter {
		variables.AcceleratorExporters = []shared.AcceleratorExporter{*shared.BuildAcceleratorExporter(
			v1.AcceleratorTypeNVIDIAGPU.String(), profile, options.ImagePrefix)}
	}

	resources, err := util.RenderKubernetesManifest(sharedmanifests.Template, variables)
	if err != nil {
		return nil, err
	}

	if err := addToolkitValidation(resources, options.Managed); err != nil {
		return nil, err
	}

	return resources, nil
}

func objectLabels(cluster *v1.Cluster, app, target, acceleratorType string) map[string]string {
	labels := map[string]string{
		OwnerLabel: OwnerValue, DeploymentModeLabel: DeploymentModeValue,
		"app": app, MetricsTargetLabel: target,
		"cluster": cluster.Metadata.Name, "workspace": cluster.Metadata.Workspace,
	}
	if acceleratorType != "" {
		labels[AcceleratorTypeLabel] = acceleratorType
	}

	return labels
}
