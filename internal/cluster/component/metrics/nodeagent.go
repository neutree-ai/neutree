package metrics

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
)

// metricsNodeAgent projects the selected accelerator Profile; exporter runtime
// metadata is not inherited.
type metricsNodeAgent struct {
	Type                           string
	Image                          string
	UseLegacyContract              bool
	AcceleratorType                string
	AcceleratorExporterPort        int
	AcceleratorExporterMetricsPath string
	Env                            []corev1.EnvVar
	NodeSelector                   map[string]string
	Affinity                       *corev1.Affinity
	SecurityContext                *corev1.SecurityContext
	RuntimeClassName               string
	InitContainers                 []corev1.Container
	VolumeMounts                   []corev1.VolumeMount
	Volumes                        []corev1.Volume
}

type nodeAgentDeploymentPlan struct {
	Name      string
	NodeAgent metricsNodeAgent
}

func (m *MetricsComponent) planMetricsNodeAgents(plans []metricsAcceleratorPlan) ([]nodeAgentDeploymentPlan, error) {
	agent, err := selectedMetricsNodeAgent(m.cluster.GetVersion(), plans)
	if err != nil {
		return nil, err
	}

	// Legacy profiles retain their existing single agent and profile selector.
	deployments := []nodeAgentDeploymentPlan{{Name: neutreeNodeAgentMetricsName, NodeAgent: agent}}
	if len(plans) == 0 || !plans[0].OperatorManaged {
		return deployments, nil
	}

	agent.Type = nodeAgentTypeAccelerator
	cpuAgent := defaultMetricsNodeAgent()
	cpuAgent.Type = nodeAgentTypeCPU
	cpuAgent.Image = agent.Image
	cpuAgent.UseLegacyContract = agent.UseLegacyContract
	cpuAgent.Affinity = cpuNodeAgentAffinity(agent.NodeSelector)

	return []nodeAgentDeploymentPlan{
		{Name: cpuNodeAgentMetricsName, NodeAgent: cpuAgent},
		{Name: neutreeNodeAgentMetricsName, NodeAgent: agent},
	}, nil
}

func cpuNodeAgentAffinity(selector map[string]string) *corev1.Affinity {
	var expressions []corev1.NodeSelectorRequirement
	for _, key := range slices.Sorted(maps.Keys(selector)) {
		expressions = append(expressions, corev1.NodeSelectorRequirement{
			Key: key, Operator: corev1.NodeSelectorOpNotIn, Values: []string{selector[key]},
		})
	}

	// Exclude any node matching a profile selector entry, including readiness-only matches.
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: expressions,
		}}},
	}}
}

func selectedMetricsNodeAgent(
	clusterVersion string,
	plans []metricsAcceleratorPlan,
) (metricsNodeAgent, error) {
	nodeAgent := defaultMetricsNodeAgent()
	var selectedPlan *metricsAcceleratorPlan
	var selectedTarget *v1.MetricsTargetProfile

	if len(plans) > 0 {
		selectedPlan = &plans[0]
		selectedTarget = selectedPlan.ExporterTarget
	}

	selection, err := clusterversion.SelectNodeAgent(clusterVersion)

	if err != nil {
		return metricsNodeAgent{}, err
	}

	nodeAgent.Image = selection.Image
	nodeAgent.UseLegacyContract = selection.Contract == clusterversion.NodeAgentContractLegacy

	if selectedPlan != nil && selectedTarget != nil && !nodeAgent.UseLegacyContract {
		nodeAgent.AcceleratorType = selectedPlan.AcceleratorType
		nodeAgent.AcceleratorExporterPort = selectedTarget.Port
		nodeAgent.AcceleratorExporterMetricsPath = selectedTarget.MetricsPath
	}

	var runtimeEnv map[string]string
	if selectedPlan != nil && selectedPlan.NodeAgentRuntime != nil {
		runtimeEnv = selectedPlan.NodeAgentRuntime.Env
	}

	var virtualizationMetricsTarget *v1.MetricsTargetProfile
	var acceleratorExporterPodSelector map[string]string
	var acceleratorExporterNamespace string

	if selectedPlan != nil {
		virtualizationMetricsTarget = selectedPlan.VirtualizationMetricsTarget
	}

	if selectedTarget != nil {
		acceleratorExporterPodSelector = selectedTarget.PodSelector
		acceleratorExporterNamespace = selectedTarget.Namespace
	}

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
	if selectedPlan == nil {
		return nodeAgent, nil
	}

	runtime := selectedPlan.NodeAgentRuntime
	if runtime == nil {
		return nodeAgent, nil
	}

	nodeAgent.NodeSelector = maps.Clone(runtime.NodeSelector)
	nodeAgent.SecurityContext = nodeAgentRuntimeSecurityContext(runtime)
	nodeAgent.RuntimeClassName = runtime.Runtime
	nodeAgent.InitContainers = append([]corev1.Container(nil), runtime.InitContainers...)

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

func (m *MetricsComponent) nodeAgentScrapeTargets(name string) []metricsconfig.Target {
	if name == "" {
		return nil
	}

	selector := map[string]string{"neutree.ai/metrics-target": "node-agent"}

	rules := []promrelabel.RelabelConfig{{SourceLabels: []string{"__meta_kubernetes_pod_container_port_name"}, Action: "keep",
		Regex: &promrelabel.MultiLineRegex{S: "metrics"}}}
	rules = append(rules, m.podScrapeRelabelConfigs(neutreeNodeAgentMetricsPort, "namespace", "pod")...)

	return []metricsconfig.Target{{
		JobName: "neutree-node-agent", Namespace: m.namespace, Selectors: metricsconfig.PodSelectors(selector),
		Port: neutreeNodeAgentMetricsPort, MetricsPath: defaultMetricsPath, RelabelConfigs: rules,
	}}
}
