package metrics

import (
	"encoding/json"
	"maps"
	"sort"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/component"
)

// metricsNodeAgent is the single NodeAgent rendered for a cluster. Its runtime
// and scrape target come from the selected accelerator Profile; exporter
// runtime metadata is not inherited.
type metricsNodeAgent struct {
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

func selectedMetricsNodeAgent(
	clusterVersion string,
	plans []metricsAcceleratorPlan,
	targets []metricsScrapeTarget,
) (metricsNodeAgent, error) {
	nodeAgent := defaultMetricsNodeAgent()
	var selectedPlan *metricsAcceleratorPlan
	var selectedTarget *metricsScrapeTarget

	if len(plans) > 0 {
		selectedPlan = &plans[0]
		for index := range targets {
			if targets[index].AcceleratorType == selectedPlan.AcceleratorType {
				selectedTarget = &targets[index]

				break
			}
		}
	}

	var profile *v1.NodeAgentRuntimeProfile
	if selectedPlan != nil {
		profile = selectedPlan.NodeAgentRuntime
	}

	selection, err := component.SelectNodeAgent(clusterVersion, profile)

	if err != nil {
		return metricsNodeAgent{}, err
	}

	nodeAgent.Image = selection.Image
	nodeAgent.UseLegacyContract = selection.Contract == component.NodeAgentContractLegacy

	if selectedPlan == nil || selectedTarget == nil {
		return nodeAgent, nil
	}

	if !nodeAgent.UseLegacyContract {
		nodeAgent.AcceleratorType = selectedTarget.AcceleratorType
		nodeAgent.AcceleratorExporterPort = selectedTarget.Port
		nodeAgent.AcceleratorExporterMetricsPath = selectedTarget.MetricsPath
	}

	var runtimeEnv map[string]string
	if selectedPlan.NodeAgentRuntime != nil {
		runtimeEnv = selectedPlan.NodeAgentRuntime.Env
	}

	virtualizationMetricsTarget := selectedPlan.VirtualizationMetricsTarget
	acceleratorExporterPodSelector := selectedTarget.PodSelector
	acceleratorExporterNamespace := selectedTarget.Namespace

	if nodeAgent.UseLegacyContract {
		virtualizationMetricsTarget = nil
		acceleratorExporterPodSelector = nil
		acceleratorExporterNamespace = ""
	}

	env, err := nodeAgentRuntimeEnv(
		runtimeEnv,
		virtualizationMetricsTarget,
		acceleratorExporterNamespace,
		acceleratorExporterPodSelector,
	)
	if err != nil {
		return metricsNodeAgent{}, err
	}

	nodeAgent.Env = env

	runtime := selectedPlan.NodeAgentRuntime
	if runtime == nil {
		return nodeAgent, nil
	}

	nodeAgent.NodeSelector = maps.Clone(runtime.NodeSelector)
	nodeAgent.SecurityContext = nodeAgentRuntimeSecurityContext(runtime)

	runtimeMounts, runtimeVolumes := buildComponentVolumes(runtime.Volumes, runtime.VolumeMounts)
	nodeAgent.VolumeMounts = append(nodeAgent.VolumeMounts, runtimeMounts...)
	nodeAgent.Volumes = append(nodeAgent.Volumes, runtimeVolumes...)

	return nodeAgent, nil
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
