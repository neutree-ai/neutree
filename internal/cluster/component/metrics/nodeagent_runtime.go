package metrics

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
)

const (
	nodeAgentMetricsTargetLabel = "neutree.ai/metrics-target"
	nodeAgentMetricsTargetValue = "node-agent"
)

type metricsNodeAgent struct {
	Name              string
	AcceleratorType   string
	IncludedNodeNames []string
	ExcludedNodeNames []string
	Env               []corev1.EnvVar
	SecurityContext   *corev1.SecurityContext
	VolumeMounts      []corev1.VolumeMount
	Volumes           []corev1.Volume
}

type nodeAgentRuntimeCandidate struct {
	AcceleratorType string
	Runtime         *v1.NodeAgentRuntimeProfile
}

func (m *MetricsComponent) planNodeAgents(ctx context.Context) ([]metricsNodeAgent, error) {
	general := defaultMetricsNodeAgent(neutreeNodeAgentMetricsName)
	if m.acceleratorMgr == nil {
		return []metricsNodeAgent{general}, nil
	}

	candidates, err := m.nodeAgentRuntimeCandidates(ctx)
	if err != nil {
		return nil, err
	}

	if len(candidates) == 0 {
		return []metricsNodeAgent{general}, nil
	}

	nodes, err := m.clusterNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes for NodeAgent runtime profile: %w", err)
	}

	parsers := m.acceleratorMgr.GetAllParsers()
	matchedCandidates := make([]nodeAgentRuntimeCandidate, 0, 1)
	matchedNodeNamesByType := make(map[string][]string, len(candidates))

	for _, candidate := range candidates {
		parser, ok := parsers[candidate.AcceleratorType]
		if !ok || parser == nil {
			return nil, fmt.Errorf("NodeAgent runtime profile for accelerator %q requires a resource parser", candidate.AcceleratorType)
		}

		if err := validateNodeAgentRuntime(candidate); err != nil {
			return nil, err
		}

		matchedNodeNames, matchErr := nodeAgentRuntimeNodeNames(candidate, parser, nodes)
		if matchErr != nil {
			return nil, matchErr
		}

		if len(matchedNodeNames) == 0 {
			continue
		}

		matchedCandidates = append(matchedCandidates, candidate)
		matchedNodeNamesByType[candidate.AcceleratorType] = matchedNodeNames
	}

	if len(matchedCandidates) == 0 {
		return []metricsNodeAgent{general}, nil
	}

	if len(matchedCandidates) > 1 {
		return nil, fmt.Errorf("at most one explicit NodeAgent runtime profile may match a Kubernetes cluster, got %d", len(matchedCandidates))
	}

	candidate := matchedCandidates[0]
	matchedNodeNames := matchedNodeNamesByType[candidate.AcceleratorType]

	adapterNameSuffix := sanitizeKubernetesNameValue(candidate.AcceleratorType)
	if adapterNameSuffix == "" {
		return nil, fmt.Errorf("NodeAgent runtime accelerator type %q has no valid Kubernetes name", candidate.AcceleratorType)
	}

	adapter := defaultMetricsNodeAgent(neutreeNodeAgentMetricsName + "-" + adapterNameSuffix)
	adapter.AcceleratorType = candidate.AcceleratorType

	adapter.IncludedNodeNames = append([]string(nil), matchedNodeNames...)
	adapter.SecurityContext = nodeAgentRuntimeSecurityContext(candidate.Runtime)

	runtimeMounts, runtimeVolumes, err := buildComponentVolumes(candidate.Runtime.Volumes, candidate.Runtime.VolumeMounts)
	if err != nil {
		return nil, fmt.Errorf("validate NodeAgent runtime for accelerator %q: %w", candidate.AcceleratorType, err)
	}

	adapter.VolumeMounts = append(adapter.VolumeMounts, runtimeMounts...)
	adapter.Volumes = append(adapter.Volumes, runtimeVolumes...)
	general.ExcludedNodeNames = append([]string(nil), matchedNodeNames...)

	return []metricsNodeAgent{general, adapter}, nil
}

func (m *MetricsComponent) nodeAgentRuntimeCandidates(ctx context.Context) ([]nodeAgentRuntimeCandidate, error) {
	acceleratorTypes := append([]string(nil), m.acceleratorMgr.SupportPlugins()...)
	sort.Strings(acceleratorTypes)

	candidates := make([]nodeAgentRuntimeCandidate, 0, 1)

	for _, acceleratorType := range acceleratorTypes {
		profile, err := m.acceleratorMgr.GetAcceleratorProfile(ctx, acceleratorType)
		if err != nil {
			// A plugin without a profile cannot request a dedicated NodeAgent
			// runtime. Preserve the existing general NodeAgent behavior instead.
			continue
		}

		if profile == nil || profile.NodeAgentRuntime == nil {
			continue
		}

		if strings.TrimSpace(profile.AcceleratorType) != "" && profile.AcceleratorType != acceleratorType {
			return nil, fmt.Errorf("NodeAgent runtime profile type %q does not match registered accelerator %q", profile.AcceleratorType, acceleratorType)
		}

		if _, err := normalizedRuntimeProducts(profile.NodeAgentRuntime); err != nil {
			return nil, fmt.Errorf("validate NodeAgent runtime profile for accelerator %q: %w", acceleratorType, err)
		}

		candidates = append(candidates, nodeAgentRuntimeCandidate{
			AcceleratorType: acceleratorType,
			Runtime:         profile.NodeAgentRuntime,
		})
	}

	return candidates, nil
}

func defaultMetricsNodeAgent(name string) metricsNodeAgent {
	hostPathType := corev1.HostPathDirectoryOrCreate

	return metricsNodeAgent{
		Name: name,
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

func nodeAgentRuntimeNodeNames(
	candidate nodeAgentRuntimeCandidate,
	parser resourceparser.ResourceParser,
	nodes []corev1.Node,
) ([]string, error) {
	products, err := normalizedRuntimeProducts(candidate.Runtime)
	if err != nil {
		return nil, fmt.Errorf("validate NodeAgent runtime profile for accelerator %q: %w", candidate.AcceleratorType, err)
	}

	nodeNames := make([]string, 0, len(nodes))

	for _, node := range nodes {
		resourceInfo, err := parser.ParseFromKubernetes(node.Status.Allocatable, node.Labels)
		if err != nil {
			return nil, fmt.Errorf("parse NodeAgent runtime resources for node %q and accelerator %q: %w", node.Name, candidate.AcceleratorType, err)
		}

		if resourceInfo == nil {
			continue
		}

		group := resourceInfo.AcceleratorGroups[v1.AcceleratorType(candidate.AcceleratorType)]
		if group == nil || group.Quantity <= 0 {
			continue
		}

		if nodeAgentRuntimeSupportsProducts(group, products) {
			nodeNames = append(nodeNames, node.Name)
		}
	}

	sort.Strings(nodeNames)

	return nodeNames, nil
}

func normalizedRuntimeProducts(runtime *v1.NodeAgentRuntimeProfile) (map[v1.AcceleratorProduct]struct{}, error) {
	products := make(map[v1.AcceleratorProduct]struct{}, len(runtime.KubernetesProducts))

	for _, product := range runtime.KubernetesProducts {
		product = strings.TrimSpace(product)
		if product == "" {
			return nil, fmt.Errorf("kubernetes product must be non-empty")
		}

		key := v1.AcceleratorProduct(product)
		if _, exists := products[key]; exists {
			return nil, fmt.Errorf("kubernetes product %q is duplicated", product)
		}

		products[key] = struct{}{}
	}

	return products, nil
}

func nodeAgentRuntimeSupportsProducts(group *v1.AcceleratorGroup, products map[v1.AcceleratorProduct]struct{}) bool {
	if len(products) == 0 {
		return true
	}

	for product, quantity := range group.ProductGroups {
		if quantity <= 0 {
			continue
		}

		if _, ok := products[product]; ok {
			return true
		}
	}

	for product, resource := range group.Products {
		if resource == nil || resource.Quantity <= 0 {
			continue
		}

		if _, ok := products[product]; ok {
			return true
		}
	}

	return false
}

func validateNodeAgentRuntime(candidate nodeAgentRuntimeCandidate) error {
	runtimeMounts, runtimeVolumes, err := buildComponentVolumes(candidate.Runtime.Volumes, candidate.Runtime.VolumeMounts)
	if err != nil {
		return fmt.Errorf("validate NodeAgent runtime for accelerator %q: %w", candidate.AcceleratorType, err)
	}

	base := defaultMetricsNodeAgent(neutreeNodeAgentMetricsName)
	if err := validateNodeAgentRuntimeVolumeCollisions(base.VolumeMounts, base.Volumes, runtimeMounts, runtimeVolumes); err != nil {
		return fmt.Errorf("validate NodeAgent runtime for accelerator %q: %w", candidate.AcceleratorType, err)
	}

	return nil
}

func nodeAgentRuntimeSecurityContext(runtime *v1.NodeAgentRuntimeProfile) *corev1.SecurityContext {
	if runtime == nil {
		return nil
	}

	capabilities := make([]corev1.Capability, 0)

	if runtime.Capabilities != nil {
		for _, capability := range runtime.Capabilities.Add {
			capability = strings.TrimSpace(capability)
			if capability != "" {
				capabilities = append(capabilities, corev1.Capability(capability))
			}
		}
	}

	if !runtime.Privileged && len(capabilities) == 0 {
		return nil
	}

	context := &corev1.SecurityContext{}

	if runtime.Privileged {
		privileged := true
		context.Privileged = &privileged
	}

	if len(capabilities) > 0 {
		context.Capabilities = &corev1.Capabilities{Add: capabilities}
	}

	return context
}

func validateNodeAgentRuntimeVolumeCollisions(
	baseMounts []corev1.VolumeMount,
	baseVolumes []corev1.Volume,
	runtimeMounts []corev1.VolumeMount,
	runtimeVolumes []corev1.Volume,
) error {
	baseVolumeNames := make(map[string]struct{}, len(baseVolumes))
	for _, volume := range baseVolumes {
		baseVolumeNames[volume.Name] = struct{}{}
	}

	for _, volume := range runtimeVolumes {
		if _, exists := baseVolumeNames[volume.Name]; exists {
			return fmt.Errorf("component volume name %q conflicts with a NodeAgent host volume", volume.Name)
		}
	}

	baseMountNames := make(map[string]struct{}, len(baseMounts))
	baseMountPaths := make(map[string]struct{}, len(baseMounts))

	for _, mount := range baseMounts {
		baseMountNames[mount.Name] = struct{}{}
		baseMountPaths[mount.MountPath] = struct{}{}
	}

	for _, mount := range runtimeMounts {
		if _, exists := baseMountNames[mount.Name]; exists {
			return fmt.Errorf("component volume mount name %q conflicts with a NodeAgent host mount", mount.Name)
		}

		if _, exists := baseMountPaths[mount.MountPath]; exists {
			return fmt.Errorf("component volume mount path %q conflicts with a NodeAgent host mount", mount.MountPath)
		}
	}

	return nil
}
