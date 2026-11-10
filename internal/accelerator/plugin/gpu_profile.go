package plugin

import (
	"context"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

const (
	nvidiaValidationPath  = "/run/nvidia/validations"
	nvidiaValidationImage = "busybox:1.36"
)

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

	policies, err := listGPUOperatorClusterPolicies(ctx, ctrlClient)
	if err != nil {
		return nil, err
	}

	managed, runtimeClass := nvidiaKubernetesRuntime(policies)
	containerd, err := nvidiaClusterUsesContainerd(ctx, ctrlClient)
	if err != nil {
		return nil, err
	}

	resolved := *profile
	if profile.NodeAgentRuntime != nil {
		resolved.NodeAgentRuntime = enrichNodeAgentRuntime(profile.NodeAgentRuntime, managed && containerd, runtimeClass, managed)
	}
	if profile.MetricsExporter != nil && profile.MetricsExporter.Runtime != nil {
		exporter := *profile.MetricsExporter
		exporter.Runtime = enrichExporterRuntime(profile.MetricsExporter.Runtime, managed && containerd, runtimeClass, managed)
		resolved.MetricsExporter = &exporter
	}

	return &resolved, nil
}

func nvidiaKubernetesRuntime(policies []GPUOperatorClusterPolicy) (bool, string) {
	for _, policy := range policies {
		driver, foundDriver, _ := unstructured.NestedBool(policy.Spec, "driver", "enabled")
		toolkit, foundToolkit, _ := unstructured.NestedBool(policy.Spec, "toolkit", "enabled")
		if !foundDriver {
			driver = true
		}
		if !foundToolkit {
			toolkit = true
		}
		if !driver || !toolkit {
			continue
		}

		runtimeClass, _, _ := unstructured.NestedString(policy.Spec, "operator", "runtimeClass")
		if runtimeClass == "" {
			runtimeClass = "nvidia"
		}

		return true, runtimeClass
	}

	return false, ""
}

func nvidiaClusterUsesContainerd(ctx context.Context, reader client.Reader) (bool, error) {
	nodes := &corev1.NodeList{}
	if err := reader.List(ctx, nodes); err != nil {
		return false, err
	}
	if len(nodes.Items) == 0 {
		return false, nil
	}

	foundGPU := false
	for _, node := range nodes.Items {
		if node.Labels[NvidiaGPUDiscoveryLabelKey] != NvidiaGPUDiscoveryLabelValue {
			continue
		}
		foundGPU = true

		name, _, found := strings.Cut(node.Status.NodeInfo.ContainerRuntimeVersion, "://")
		if !found || name != "containerd" {
			return false, nil
		}
	}

	return foundGPU, nil
}

func enrichNodeAgentRuntime(base *v1.NodeAgentRuntimeProfile, useRuntimeClass bool, runtimeClass string, managed bool) *v1.NodeAgentRuntimeProfile {
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
		appendToolkitValidation(&resolved.InitContainers, &resolved.Volumes, &resolved.VolumeMounts)
	}

	return &resolved
}

func enrichExporterRuntime(base *v1.AcceleratorExporterRuntimeProfile, useRuntimeClass bool, runtimeClass string, managed bool) *v1.AcceleratorExporterRuntimeProfile {
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
		appendToolkitValidation(&resolved.InitContainers, &resolved.Volumes, &resolved.VolumeMounts)
	}

	return &resolved
}

func appendToolkitValidation(initContainers *[]corev1.Container, volumes *[]v1.ComponentVolume, mounts *[]v1.ComponentVolumeMount) {
	propagation := corev1.MountPropagationHostToContainer
	*initContainers = append(*initContainers, corev1.Container{
		Name: "toolkit-validation", Image: nvidiaValidationImage,
		Command:      []string{"sh", "-c"},
		Args:         []string{"until [ -f " + nvidiaValidationPath + "/toolkit-ready ] && { grep -q '^nvidia ' /proc/modules || [ -e /dev/dxg ]; }; do sleep 5; done"},
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
