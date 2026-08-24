package metrics

import (
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// metricsNodeAgent is the single NodeAgent rendered for a cluster. Its
// runtime comes from the selected accelerator Profile, not exporter metadata.
type metricsNodeAgent struct {
	AcceleratorType string
	SecurityContext *corev1.SecurityContext
	VolumeMounts    []corev1.VolumeMount
	Volumes         []corev1.Volume
}

func selectedMetricsNodeAgent(exporters []metricsAcceleratorExporter) metricsNodeAgent {
	nodeAgent := defaultMetricsNodeAgent()
	if len(exporters) == 0 || exporters[0].NodeAgentRuntime == nil {
		return nodeAgent
	}

	runtime := exporters[0].NodeAgentRuntime
	nodeAgent.AcceleratorType = exporters[0].AcceleratorType
	nodeAgent.SecurityContext = nodeAgentRuntimeSecurityContext(runtime)
	runtimeMounts, runtimeVolumes := buildComponentVolumes(runtime.Volumes, runtime.VolumeMounts)
	nodeAgent.VolumeMounts = append(nodeAgent.VolumeMounts, runtimeMounts...)
	nodeAgent.Volumes = append(nodeAgent.Volumes, runtimeVolumes...)

	return nodeAgent
}

func defaultMetricsNodeAgent() metricsNodeAgent {
	hostPathType := corev1.HostPathDirectoryOrCreate

	return metricsNodeAgent{
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "kubelet-pod-resources",
			MountPath: "/var/lib/kubelet/pod-resources",
		}},
		Volumes: []corev1.Volume{{
			Name: "kubelet-pod-resources",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: "/var/lib/kubelet/pod-resources",
				Type: &hostPathType,
			}},
		}},
	}
}

func nodeAgentRuntimeSecurityContext(runtime *v1.NodeAgentRuntimeProfile) *corev1.SecurityContext {
	if runtime == nil {
		return nil
	}

	capabilities := make([]corev1.Capability, 0)

	if runtime.Capabilities != nil {
		for _, capability := range runtime.Capabilities.Add {
			capabilities = append(capabilities, corev1.Capability(capability))
		}
	}

	if !runtime.Privileged && len(capabilities) == 0 {
		return nil
	}

	securityContext := &corev1.SecurityContext{}

	if runtime.Privileged {
		privileged := true
		securityContext.Privileged = &privileged
	}

	if len(capabilities) > 0 {
		securityContext.Capabilities = &corev1.Capabilities{Add: capabilities}
	}

	return securityContext
}
