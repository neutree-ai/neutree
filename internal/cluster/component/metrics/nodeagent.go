package metrics

import (
	"encoding/json"
	"sort"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// metricsNodeAgent is the single NodeAgent rendered for a cluster. Its
// runtime comes from the selected accelerator Profile, not exporter metadata.
type metricsNodeAgent struct {
	AcceleratorType string
	Env             []corev1.EnvVar
	SecurityContext *corev1.SecurityContext
	VolumeMounts    []corev1.VolumeMount
	Volumes         []corev1.Volume
}

func selectedMetricsNodeAgent(exporters []metricsAcceleratorExporter) (metricsNodeAgent, error) {
	nodeAgent := defaultMetricsNodeAgent()
	if len(exporters) == 0 {
		return nodeAgent, nil
	}

	selected := exporters[0]
	nodeAgent.AcceleratorType = selected.AcceleratorType

	var runtimeEnv map[string]string
	if selected.NodeAgent != nil {
		runtimeEnv = selected.NodeAgent.Env
	}

	env, err := nodeAgentRuntimeEnv(runtimeEnv, selected.VirtualizationMonitor)
	if err != nil {
		return metricsNodeAgent{}, err
	}

	nodeAgent.Env = env

	runtime := selected.NodeAgent
	if runtime == nil {
		return nodeAgent, nil
	}

	nodeAgent.SecurityContext = nodeAgentRuntimeSecurityContext(runtime)
	runtimeMounts, runtimeVolumes := buildComponentVolumes(runtime.Volumes, runtime.VolumeMounts)
	nodeAgent.VolumeMounts = append(nodeAgent.VolumeMounts, runtimeMounts...)
	nodeAgent.Volumes = append(nodeAgent.Volumes, runtimeVolumes...)

	return nodeAgent, nil
}

// nodeAgentRuntimeEnv keeps Profile runtime variables isolated from exporter
// variables and projects the complete selected monitor declaration as one
// reserved JSON document for the generic Kubernetes collector.
func nodeAgentRuntimeEnv(
	runtimeEnv map[string]string,
	virtualizationMonitor *v1.VirtualizationMonitorProfile,
) ([]corev1.EnvVar, error) {
	values := make(map[string]string, len(runtimeEnv)+1)
	for key, value := range runtimeEnv {
		values[key] = value
	}

	if virtualizationMonitor != nil {
		encoded, err := json.Marshal(virtualizationMonitor)
		if err != nil {
			return nil, err
		}

		values[v1.VirtualizationMonitorProfileEnvKey] = string(encoded)
	}

	if len(values) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	result := make([]corev1.EnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, corev1.EnvVar{Name: key, Value: values[key]})
	}

	return result, nil
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

func nodeAgentRuntimeSecurityContext(runtime *v1.NodeAgentProfile) *corev1.SecurityContext {
	if runtime == nil {
		return nil
	}

	var capabilities *corev1.Capabilities
	if runtime.Capabilities != nil {
		capabilities = runtime.Capabilities.DeepCopy()
	}

	if !runtime.Privileged && capabilities == nil {
		return nil
	}

	securityContext := &corev1.SecurityContext{}

	if runtime.Privileged {
		privileged := true
		securityContext.Privileged = &privileged
	}

	if capabilities != nil {
		securityContext.Capabilities = capabilities
	}

	return securityContext
}
