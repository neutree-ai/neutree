package plugin

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

const nvidiaValidationPath = accelerator.NvidiaValidationPath

func enrichNvidiaKubernetesProfile(ctx context.Context, reader client.Reader, profile *v1.AcceleratorProfile) error {
	policies, err := accelerator.ListGPUOperatorClusterPolicies(ctx, reader)
	if err != nil {
		return err
	}

	managed, runtimeClass, err := accelerator.NvidiaOperatorRuntime(policies)
	if err != nil {
		return err
	}

	// Kubernetes RuntimeClass comes from ClusterPolicy, not the StaticNode runtime default.
	profile.NodeAgentRuntime.Runtime = ""
	profile.MetricsExporter.Runtime.Runtime = ""
	agent := profile.NodeAgentRuntime
	agent.NodeSelector = map[string]string{
		accelerator.NvidiaGPUDiscoveryLabelKey: accelerator.NvidiaGPUDiscoveryLabelValue,
	}

	if !managed {
		return nil
	}

	agent.Runtime = runtimeClass
	agent.NodeSelector[accelerator.NvidiaGPUClientLabel] = accelerator.NvidiaGPUDiscoveryLabelValue
	appendToolkitValidation(&agent.InitContainers, &agent.Volumes, &agent.VolumeMounts, "")

	exporter := profile.MetricsExporter.Runtime
	exporter.Runtime = runtimeClass
	exporter.NodeSelector[accelerator.NvidiaGPUClientLabel] = accelerator.NvidiaGPUDiscoveryLabelValue
	appendToolkitValidation(&exporter.InitContainers, &exporter.Volumes, &exporter.VolumeMounts, profile.MetricsExporter.Image)

	return nil
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
