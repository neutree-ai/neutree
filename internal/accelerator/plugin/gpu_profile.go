package plugin

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

const nvidiaValidationPath = accelerator.NvidiaValidationPath

func enrichNvidiaKubernetesProfile(ctx context.Context, reader client.Reader, cluster *v1.Cluster, profile *v1.AcceleratorProfile) error {
	profile.NodeAgentRuntime.Runtime = ""
	profile.MetricsExporter.Runtime.Runtime = ""
	agent := profile.NodeAgentRuntime
	agent.NodeSelector = map[string]string{
		accelerator.NvidiaGPUDiscoveryLabelKey: accelerator.NvidiaGPUDiscoveryLabelValue,
	}

	nodes := &corev1.NodeList{}
	if err := reader.List(ctx, nodes, client.MatchingLabels{
		accelerator.NvidiaGPUDiscoveryLabelKey: accelerator.NvidiaGPUDiscoveryLabelValue,
	}); err != nil {
		return err
	}

	// Metrics enumerates every plugin before it selects the cluster accelerator.
	if len(nodes.Items) == 0 {
		return nil
	}

	policies, err := accelerator.ListGPUOperatorClusterPolicies(ctx, reader)
	if err != nil {
		return err
	}

	managed, _, err := accelerator.NvidiaOperatorRuntime(policies)
	if err != nil {
		return err
	}

	runtimeClass, err := accelerator.SelectRuntimeClass(ctx, reader, cluster, policies)
	if err != nil {
		return err
	}

	profile.NodeAgentRuntime.Runtime = runtimeClass
	profile.MetricsExporter.Runtime.Runtime = runtimeClass

	if runtimeClass == "" {
		return fmt.Errorf("NVIDIA RuntimeClass is unavailable; reconcile the NVIDIA accelerator operator before generating the profile")
	}

	if !managed {
		return nil
	}

	agent.NodeSelector[accelerator.NvidiaGPUClientLabel] = accelerator.NvidiaGPUDiscoveryLabelValue
	appendToolkitValidation(&agent.InitContainers, &agent.Volumes, &agent.VolumeMounts, "")

	exporter := profile.MetricsExporter.Runtime
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
