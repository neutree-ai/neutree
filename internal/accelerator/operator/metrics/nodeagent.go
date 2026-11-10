package metrics

import (
	"encoding/json"
	"maps"
	"sort"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type NodeAgentSelection struct {
	Image  string
	Legacy bool
}

// NodeAgent is the single NodeAgent rendered for a cluster. Its runtime
// and scrape target come from the selected accelerator Profile; exporter
// runtime metadata is not inherited.
type NodeAgent struct {
	Image                          string
	UseLegacyContract              bool
	AcceleratorType                string
	AcceleratorExporterPort        int
	AcceleratorExporterMetricsPath string
	Env                            []corev1.EnvVar
	NodeSelector                   map[string]string
	SecurityContext                *corev1.SecurityContext
	VolumeMounts                   []corev1.VolumeMount
	Volumes                        []corev1.Volume
}

// NewNodeAgent projects the selected runtime and discovery targets into shared manifest parameters.
func NewNodeAgent(selection NodeAgentSelection, acceleratorType string, target, virtualizationTarget *v1.MetricsTargetProfile,
	runtime *v1.NodeAgentRuntimeProfile,
) (NodeAgent, error) {
	agent := DefaultNodeAgent()
	var err error
	agent.Image = selection.Image
	agent.UseLegacyContract = selection.Legacy

	var runtimeEnv map[string]string
	if runtime != nil {
		runtimeEnv = runtime.Env
	}
	var exporterNamespace string
	var exporterSelector map[string]string

	if agent.UseLegacyContract {
		virtualizationTarget = nil
	} else if target != nil {
		agent.AcceleratorType = acceleratorType
		agent.AcceleratorExporterPort = target.Port
		agent.AcceleratorExporterMetricsPath = target.MetricsPath
		exporterNamespace, exporterSelector = target.Namespace, target.PodSelector
	}

	agent.Env, err = nodeAgentRuntimeEnv(runtimeEnv, virtualizationTarget, exporterNamespace, exporterSelector)
	if err != nil {
		return NodeAgent{}, err
	}

	if runtime != nil {
		agent.NodeSelector = maps.Clone(runtime.NodeSelector)
		agent.SecurityContext = nodeAgentRuntimeSecurityContext(runtime)
		mounts, volumes := buildComponentVolumes(runtime.Volumes, runtime.VolumeMounts)
		agent.VolumeMounts = append(agent.VolumeMounts, mounts...)
		agent.Volumes = append(agent.Volumes, volumes...)
	}

	return agent, nil
}

// nodeAgentRuntimeEnv keeps Profile runtime variables isolated from exporter
// variables and projects the selected monitor and exporter target. The target
// fields are rendered together by the control plane so NodeAgent and vmagent
// discover the same external workload.
func nodeAgentRuntimeEnv(
	runtimeEnv map[string]string,
	virtualizationMetricsTarget *v1.MetricsTargetProfile,
	acceleratorExporterNamespace string,
	acceleratorExporterPodSelector map[string]string,
) ([]corev1.EnvVar, error) {
	values := make(map[string]string, len(runtimeEnv)+3)
	for key, value := range runtimeEnv {
		values[key] = value
	}

	if virtualizationMetricsTarget != nil {
		encoded, err := json.Marshal(virtualizationMetricsTarget)
		if err != nil {
			return nil, err
		}

		values[v1.VirtualizationMetricsTargetEnvKey] = string(encoded)
	}

	if len(acceleratorExporterPodSelector) > 0 {
		encoded, err := json.Marshal(acceleratorExporterPodSelector)
		if err != nil {
			return nil, err
		}

		values[v1.AcceleratorExporterPodSelectorEnvKey] = string(encoded)
	}

	if acceleratorExporterNamespace != "" {
		values[v1.AcceleratorExporterNamespaceEnvKey] = acceleratorExporterNamespace
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

func DefaultNodeAgent() NodeAgent {
	hostPathType := corev1.HostPathDirectoryOrCreate

	return NodeAgent{
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
