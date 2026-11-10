package plugin

import (
	"context"
	"maps"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

const nvidiaValidationPath = accelerator.NvidiaValidationPath

// ResolveKubernetesProfile applies the existing GPU Operator runtime gate to
// the base profile. StaticNode callers do not invoke this method.
func (p *GPUAcceleratorPlugin) ResolveKubernetesProfile(ctx context.Context, cluster *v1.Cluster, profile *v1.AcceleratorProfile) (*v1.AcceleratorProfile, error) {
	if cluster == nil || profile == nil {
		return profile, nil
	}

	ctrlClient, err := util.GetClientFromCluster(cluster)
	if err != nil {
		return nil, err
	}

	return resolveNvidiaKubernetesProfile(ctx, ctrlClient, profile, cluster.GetVersion())
}

func resolveNvidiaKubernetesProfile(ctx context.Context, reader client.Reader, profile *v1.AcceleratorProfile, clusterVersion string) (*v1.AcceleratorProfile, error) {
	policies, err := accelerator.ListGPUOperatorClusterPolicies(ctx, reader)
	if err != nil {
		return nil, err
	}

	managed, runtimeClass, err := accelerator.NvidiaOperatorRuntime(policies)
	if err != nil {
		return nil, err
	}

	containerd, err := nvidiaClusterUsesContainerd(ctx, reader)
	if err != nil {
		return nil, err
	}

	nodeAgentImage := ""

	if managed {
		selection, err := clusterversion.SelectNodeAgent(clusterVersion)
		if err != nil {
			return nil, err
		}

		nodeAgentImage = selection.Image
	}

	resolved := *profile
	if profile.NodeAgentRuntime != nil {
		resolved.NodeAgentRuntime = enrichNodeAgentRuntime(profile.NodeAgentRuntime, managed && containerd, runtimeClass, managed, nodeAgentImage)
	}

	if profile.MetricsExporter != nil && profile.MetricsExporter.Runtime != nil {
		exporter := *profile.MetricsExporter
		exporter.Runtime = enrichExporterRuntime(profile.MetricsExporter.Runtime, managed && containerd, runtimeClass, managed, exporter.Image)
		resolved.MetricsExporter = &exporter
	}

	return &resolved, nil
}

func nvidiaClusterUsesContainerd(ctx context.Context, reader client.Reader) (bool, error) {
	nodes := &corev1.NodeList{}
	if err := reader.List(ctx, nodes); err != nil {
		return false, err
	}

	return accelerator.NvidiaNodesUseContainerd(nodes.Items), nil
}

func enrichNodeAgentRuntime(base *v1.NodeAgentRuntimeProfile, useRuntimeClass bool, runtimeClass string, managed bool, image string) *v1.NodeAgentRuntimeProfile {
	resolved := *base
	resolved.NodeSelector = maps.Clone(base.NodeSelector)
	resolved.Env = maps.Clone(base.Env)
	resolved.Volumes = append([]v1.ComponentVolume(nil), base.Volumes...)
	resolved.VolumeMounts = append([]v1.ComponentVolumeMount(nil), base.VolumeMounts...)
	resolved.InitContainers = append([]corev1.Container(nil), base.InitContainers...)

	if useRuntimeClass {
		resolved.Runtime = runtimeClass
	} else {
		resolved.Runtime = ""
	}

	if managed {
		if resolved.NodeSelector == nil {
			resolved.NodeSelector = map[string]string{}
		}

		resolved.NodeSelector[accelerator.NvidiaGPUClientLabel] = accelerator.NvidiaGPUDiscoveryLabelValue
		resolved.NodeSelector[accelerator.NvidiaGPUDiscoveryLabelKey] = accelerator.NvidiaGPUDiscoveryLabelValue
		appendToolkitValidation(&resolved.InitContainers, &resolved.Volumes, &resolved.VolumeMounts, image)
	}

	return &resolved
}

func enrichExporterRuntime(
	base *v1.AcceleratorExporterRuntimeProfile, useRuntimeClass bool, runtimeClass string, managed bool, image string,
) *v1.AcceleratorExporterRuntimeProfile {
	resolved := *base
	resolved.NodeSelector = maps.Clone(base.NodeSelector)
	resolved.Volumes = append([]v1.ComponentVolume(nil), base.Volumes...)
	resolved.VolumeMounts = append([]v1.ComponentVolumeMount(nil), base.VolumeMounts...)
	resolved.InitContainers = append([]corev1.Container(nil), base.InitContainers...)

	if useRuntimeClass {
		resolved.Runtime = runtimeClass
	} else {
		resolved.Runtime = ""
	}

	if managed {
		if resolved.NodeSelector == nil {
			resolved.NodeSelector = map[string]string{}
		}

		resolved.NodeSelector[accelerator.NvidiaGPUClientLabel] = accelerator.NvidiaGPUDiscoveryLabelValue
		resolved.NodeSelector[accelerator.NvidiaGPUDiscoveryLabelKey] = accelerator.NvidiaGPUDiscoveryLabelValue
		appendToolkitValidation(&resolved.InitContainers, &resolved.Volumes, &resolved.VolumeMounts, image)
	}

	return &resolved
}

func appendToolkitValidation(initContainers *[]corev1.Container, volumes *[]v1.ComponentVolume, mounts *[]v1.ComponentVolumeMount, image string) {
	propagation := corev1.MountPropagationHostToContainer
	*initContainers = append(*initContainers, corev1.Container{
		Name: "toolkit-validation", Image: image,
		Command:      []string{"sh", "-c"},
		Args:         []string{"until [ -f " + nvidiaValidationPath + "/toolkit-ready ]; do sleep 5; done"},
		VolumeMounts: []corev1.VolumeMount{{Name: "nvidia-validations", MountPath: nvidiaValidationPath, ReadOnly: true, MountPropagation: &propagation}},
	})

	*volumes = append(*volumes, v1.ComponentVolume{
		Name:     "nvidia-validations",
		HostPath: &v1.ComponentHostPathVolumeSource{Path: nvidiaValidationPath, Type: v1.ComponentHostPathTypeDirectoryOrCreate},
	})
	readOnly := true
	*mounts = append(*mounts, v1.ComponentVolumeMount{Name: "nvidia-validations", MountPath: nvidiaValidationPath, ReadOnly: &readOnly})
}

var _ accelerator.KubernetesProfileResolver = (*GPUAcceleratorPlugin)(nil)
