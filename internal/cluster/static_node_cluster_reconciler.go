package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	staticNodeClusterLabelKey = "neutree.ai/static-node-cluster"
	staticNodeRoleLabelKey    = "neutree.ai/static-node-role"

	nodeExporterComponentName        = "node-exporter"
	neutreeMetricsComponentName      = "neutree-metrics"
	vmagentComponentName             = "vmagent"
	acceleratorExporterComponentName = "accelerator-exporter"
	defaultNeutreeMetricsPort        = 19090
	defaultVMAgentPort               = 8429
	defaultNodeExporterPort          = 9100
	defaultRayDashboardPort          = 8265
	defaultPrometheusHTTPPath        = "/metrics"
	defaultHealthHTTPPath            = "/health"
	neutreeMetricsConfigPath         = "/etc/neutree/neutree-metrics/config.json"
	vmagentConfigPath                = "/etc/neutree/vmagent/config.yaml"
)

type StaticNodeClusterReconciler struct{}

type StaticNodeClusterReconcilePlan struct {
	DesiredNodes []*v1.StaticNode
	Status       v1.StaticNodeClusterStatus
}

type metricsNormalizerConfig struct {
	Labels          map[string]string         `json:"labels"`
	Targets         []metricsNormalizerTarget `json:"targets"`
	AcceleratorType string                    `json:"accelerator_type,omitempty"`
	ExporterKind    string                    `json:"exporter_kind,omitempty"`
}

type metricsNormalizerTarget struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Kind    string `json:"kind,omitempty"`
	Timeout string `json:"timeout,omitempty"`
}

func (r *StaticNodeClusterReconciler) Plan(
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
	acceleratorProfiles map[string]*v1.AcceleratorProfile,
) (*StaticNodeClusterReconcilePlan, error) {
	desiredNodes, err := r.BuildDesiredNodes(cluster, acceleratorProfiles)
	if err != nil {
		return nil, err
	}

	return &StaticNodeClusterReconcilePlan{
		DesiredNodes: desiredNodes,
		Status:       r.AggregateStatus(cluster, currentNodes),
	}, nil
}

func (r *StaticNodeClusterReconciler) BuildDesiredNodes(
	cluster *v1.StaticNodeCluster,
	acceleratorProfiles map[string]*v1.AcceleratorProfile,
) ([]*v1.StaticNode, error) {
	if cluster == nil {
		return nil, errors.New("static node cluster is nil")
	}

	if cluster.Metadata == nil || cluster.Metadata.Name == "" {
		return nil, errors.New("static node cluster metadata.name is required")
	}

	if cluster.Spec == nil {
		return nil, errors.New("static node cluster spec is required")
	}

	if cluster.Spec.Head.NodeName == "" {
		return nil, errors.New("static node cluster spec.head.node_name is required")
	}

	if len(cluster.Spec.Nodes) == 0 {
		return nil, errors.New("static node cluster spec.nodes is required")
	}

	nodeNames := make(map[string]struct{}, len(cluster.Spec.Nodes))
	headSeen := false
	desiredNodes := make([]*v1.StaticNode, 0, len(cluster.Spec.Nodes))

	for _, nodeSpec := range cluster.Spec.Nodes {
		if nodeSpec.Name == "" {
			return nil, errors.New("static node name is required")
		}

		if nodeSpec.IP == "" {
			return nil, fmt.Errorf("static node %s ip is required", nodeSpec.Name)
		}

		if _, exists := nodeNames[nodeSpec.Name]; exists {
			return nil, fmt.Errorf("duplicate static node %s", nodeSpec.Name)
		}

		nodeNames[nodeSpec.Name] = struct{}{}

		role := normalizeStaticNodeRole(nodeSpec.Role)
		if nodeSpec.Name == cluster.Spec.Head.NodeName {
			role = v1.StaticNodeRoleHead
			headSeen = true
		}

		profile := acceleratorProfiles[nodeSpec.AcceleratorType]
		desiredNodes = append(desiredNodes, &v1.StaticNode{
			APIVersion: "v1",
			Kind:       "StaticNode",
			Metadata: &v1.Metadata{
				Workspace:   cluster.Metadata.Workspace,
				Name:        nodeSpec.Name,
				Labels:      staticNodeLabels(cluster.Metadata.Name, role),
				Annotations: copyStringMap(cluster.Metadata.Annotations),
			},
			Spec: &v1.StaticNodeSpec{
				Cluster:         cluster.Metadata.Name,
				IP:              nodeSpec.IP,
				Role:            role,
				AcceleratorType: nodeSpec.AcceleratorType,
				SSHAuthRef:      nodeSpec.SSHAuthRef,
				Warm:            buildNodeWarmSpec(cluster),
				Components:      buildNodeComponents(role, profile),
			},
		})
	}

	if !headSeen {
		return nil, fmt.Errorf("head node %s not found in static node cluster nodes", cluster.Spec.Head.NodeName)
	}

	sort.SliceStable(desiredNodes, func(i, j int) bool {
		return desiredNodes[i].Metadata.Name < desiredNodes[j].Metadata.Name
	})

	if err := attachMetricsConfigFiles(cluster, desiredNodes, acceleratorProfiles); err != nil {
		return nil, err
	}

	for _, node := range desiredNodes {
		node.Spec.Components = withComponentConfigHashes(node.Spec.Components)
	}

	return desiredNodes, nil
}

func (r *StaticNodeClusterReconciler) AggregateStatus(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
) v1.StaticNodeClusterStatus {
	desiredNodeNames := map[string]struct{}{}
	headName := ""

	if cluster != nil && cluster.Spec != nil {
		for _, node := range cluster.Spec.Nodes {
			if node.Name != "" {
				desiredNodeNames[node.Name] = struct{}{}
			}
		}
		headName = cluster.Spec.Head.NodeName
	}

	status := v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseProvisioning,
		DesiredNodes: len(desiredNodeNames),
	}

	if status.DesiredNodes == 0 {
		status.Phase = v1.StaticNodeClusterPhaseFailed
		status.ErrorMessage = "static node cluster has no desired nodes"

		return status
	}

	seenDesiredNodes := map[string]struct{}{}
	warmReady := true
	metricsReady := true
	anyNodeFailed := false

	for _, node := range nodes {
		if node == nil || node.Metadata == nil || node.Status == nil {
			warmReady = false
			metricsReady = false

			continue
		}

		if _, desired := desiredNodeNames[node.Metadata.Name]; !desired {
			continue
		}

		seenDesiredNodes[node.Metadata.Name] = struct{}{}

		if node.Status.Phase == v1.StaticNodePhaseReady {
			status.ReadyNodes++
		}

		if node.Metadata.Name == headName {
			status.HeadReady = node.Status.Phase == v1.StaticNodePhaseReady
		}

		if node.Status.Phase == v1.StaticNodePhaseFailed {
			anyNodeFailed = true
		}

		if node.Status.Warm == nil || !node.Status.Warm.Ready {
			warmReady = false
		}

		if !nodeMetricsReady(node) {
			metricsReady = false
		}
	}

	if len(seenDesiredNodes) < status.DesiredNodes {
		warmReady = false
		metricsReady = false
	}

	status.WarmReady = warmReady
	status.MetricsReady = metricsReady

	switch {
	case anyNodeFailed:
		status.Phase = v1.StaticNodeClusterPhaseFailed
	case status.ReadyNodes == status.DesiredNodes && status.HeadReady && status.WarmReady && status.MetricsReady:
		status.Phase = v1.StaticNodeClusterPhaseReady
	case status.HeadReady && status.ReadyNodes > 0:
		status.Phase = v1.StaticNodeClusterPhaseDegraded
	default:
		status.Phase = v1.StaticNodeClusterPhaseProvisioning
	}

	return status
}

func normalizeStaticNodeRole(role v1.StaticNodeRole) v1.StaticNodeRole {
	if role == v1.StaticNodeRoleHead {
		return v1.StaticNodeRoleHead
	}

	return v1.StaticNodeRoleWorker
}

func staticNodeLabels(clusterName string, role v1.StaticNodeRole) map[string]string {
	return map[string]string{
		staticNodeClusterLabelKey: clusterName,
		staticNodeRoleLabelKey:    string(role),
	}
}

func buildNodeComponents(role v1.StaticNodeRole, profile *v1.AcceleratorProfile) []v1.NodeComponentSpec {
	components := []v1.NodeComponentSpec{
		buildRayComponent(role),
		{
			Name:          nodeExporterComponentName,
			Type:          v1.NodeComponentTypeNodeExporter,
			RestartPolicy: v1.NodeComponentRestartPolicyAlways,
			Ports: []v1.NodeComponentPort{
				{Name: "metrics", Port: defaultNodeExporterPort, Protocol: "TCP"},
			},
			HealthCheck: &v1.NodeComponentHealthCheck{
				HTTPPath: defaultPrometheusHTTPPath,
				Port:     defaultNodeExporterPort,
			},
		},
	}

	metricsDependencies := []string{nodeExporterComponentName}

	if profile != nil && profile.Metrics != nil && profile.Metrics.Exporter != nil {
		exporter := profile.Metrics.Exporter

		components = append(components, v1.NodeComponentSpec{
			Name:             acceleratorExporterComponentName,
			Type:             exporter.ComponentType,
			Image:            exporter.Image,
			DockerRunOptions: append([]string{}, exporter.DockerRunOptions...),
			RestartPolicy:    v1.NodeComponentRestartPolicyAlways,
			Ports: []v1.NodeComponentPort{
				{Name: "metrics", Port: exporter.Port, Protocol: "TCP"},
			},
			HealthCheck: &v1.NodeComponentHealthCheck{
				HTTPPath: defaultPrometheusHTTPPath,
				Port:     exporter.Port,
			},
		})

		metricsDependencies = append(metricsDependencies, acceleratorExporterComponentName)
	}

	components = append(components, v1.NodeComponentSpec{
		Name:          neutreeMetricsComponentName,
		Type:          v1.NodeComponentTypeMetricsNormalizer,
		Dependencies:  metricsDependencies,
		RestartPolicy: v1.NodeComponentRestartPolicyAlways,
		Ports: []v1.NodeComponentPort{
			{Name: "http", Port: defaultNeutreeMetricsPort, Protocol: "TCP"},
		},
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPPath: defaultHealthHTTPPath,
			Port:     defaultNeutreeMetricsPort,
		},
	})

	if role == v1.StaticNodeRoleHead {
		components = append(components, v1.NodeComponentSpec{
			Name:          vmagentComponentName,
			Type:          v1.NodeComponentTypeMetricsAgent,
			Dependencies:  []string{neutreeMetricsComponentName},
			RestartPolicy: v1.NodeComponentRestartPolicyAlways,
			Ports: []v1.NodeComponentPort{
				{Name: "http", Port: defaultVMAgentPort, Protocol: "TCP"},
			},
			HealthCheck: &v1.NodeComponentHealthCheck{
				HTTPPath: defaultHealthHTTPPath,
				Port:     defaultVMAgentPort,
			},
		})
	}

	return components
}

func attachMetricsConfigFiles(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	acceleratorProfiles map[string]*v1.AcceleratorProfile,
) error {
	for _, node := range nodes {
		if node == nil || node.Metadata == nil || node.Spec == nil {
			continue
		}

		profile := acceleratorProfiles[node.Spec.AcceleratorType]

		content, err := renderNeutreeMetricsConfig(cluster, node, profile)
		if err != nil {
			return err
		}

		appendComponentConfigFile(node, neutreeMetricsComponentName, v1.NodeComponentConfigFile{
			Path:         neutreeMetricsConfigPath,
			Content:      content,
			Mode:         "0644",
			Owner:        "root",
			Group:        "root",
			Sudo:         true,
			Atomic:       true,
			CreateParent: true,
		})

		if node.Spec.Role != v1.StaticNodeRoleHead {
			continue
		}

		appendComponentConfigFile(node, vmagentComponentName, v1.NodeComponentConfigFile{
			Path:         vmagentConfigPath,
			Content:      renderVMAgentConfig(cluster),
			Mode:         "0644",
			Owner:        "root",
			Group:        "root",
			Sudo:         true,
			Atomic:       true,
			CreateParent: true,
		})
	}

	return nil
}

func renderNeutreeMetricsConfig(
	cluster *v1.StaticNodeCluster,
	node *v1.StaticNode,
	profile *v1.AcceleratorProfile,
) (string, error) {
	config := metricsNormalizerConfig{
		Labels: map[string]string{
			"workspace":           node.Metadata.Workspace,
			"static_node_cluster": node.Spec.Cluster,
			"cluster_type":        "ray",
			"node":                node.Metadata.Name,
			"node_ip":             node.Spec.IP,
			"node_role":           string(node.Spec.Role),
		},
		Targets: []metricsNormalizerTarget{
			{
				Name:    nodeExporterComponentName,
				URL:     fmt.Sprintf("http://127.0.0.1:%d%s", defaultNodeExporterPort, defaultPrometheusHTTPPath),
				Timeout: "5s",
			},
		},
	}

	if cluster != nil && cluster.Metadata != nil && config.Labels["static_node_cluster"] == "" {
		config.Labels["static_node_cluster"] = cluster.Metadata.Name
	}

	if profile != nil && profile.Metrics != nil && profile.Metrics.Exporter != nil {
		exporter := profile.Metrics.Exporter

		metricsPath := exporter.MetricsPath
		if metricsPath == "" {
			metricsPath = defaultPrometheusHTTPPath
		}

		config.AcceleratorType = node.Spec.AcceleratorType
		config.ExporterKind = exporter.Kind
		config.Targets = append(config.Targets, metricsNormalizerTarget{
			Name:    acceleratorExporterComponentName,
			URL:     fmt.Sprintf("http://127.0.0.1:%d%s", exporter.Port, metricsPath),
			Kind:    exporter.Kind,
			Timeout: "5s",
		})
	}

	content, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", errors.Wrap(err, "failed to render neutree metrics config")
	}

	return string(content) + "\n", nil
}

func renderVMAgentConfig(cluster *v1.StaticNodeCluster) string {
	var builder strings.Builder
	builder.WriteString("global:\n")
	builder.WriteString("  scrape_interval: 15s\n")
	builder.WriteString("scrape_configs:\n")
	builder.WriteString("- job_name: neutree-metrics\n")
	builder.WriteString("  static_configs:\n")
	builder.WriteString("  - targets:\n")

	nodes := append([]v1.StaticNodeClusterNodeSpec{}, cluster.Spec.Nodes...)
	sort.SliceStable(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	for _, node := range nodes {
		builder.WriteString("    - ")
		builder.WriteString(strconv.Quote(fmt.Sprintf("%s:%d", node.IP, defaultNeutreeMetricsPort)))
		builder.WriteString("\n")
	}

	builder.WriteString("    labels:\n")
	builder.WriteString("      source: ")
	builder.WriteString(strconv.Quote("neutree-metrics"))
	builder.WriteString("\n")
	builder.WriteString("      workspace: ")
	builder.WriteString(strconv.Quote(cluster.Metadata.Workspace))
	builder.WriteString("\n")
	builder.WriteString("      static_node_cluster: ")
	builder.WriteString(strconv.Quote(cluster.Metadata.Name))
	builder.WriteString("\n")
	builder.WriteString("      cluster_type: ")
	builder.WriteString(strconv.Quote("ray"))
	builder.WriteString("\n")

	if cluster.Spec.MetricsRemoteWriteURL != "" {
		builder.WriteString("remote_write:\n")
		builder.WriteString("- url: ")
		builder.WriteString(strconv.Quote(cluster.Spec.MetricsRemoteWriteURL))
		builder.WriteString("\n")
	}

	return builder.String()
}

func appendComponentConfigFile(node *v1.StaticNode, componentName string, configFile v1.NodeComponentConfigFile) {
	for i := range node.Spec.Components {
		if node.Spec.Components[i].Name != componentName {
			continue
		}

		component := node.Spec.Components[i]
		component.ConfigFiles = append(append([]v1.NodeComponentConfigFile{}, component.ConfigFiles...), configFile)
		node.Spec.Components[i] = component

		return
	}
}

func withComponentConfigHashes(components []v1.NodeComponentSpec) []v1.NodeComponentSpec {
	result := make([]v1.NodeComponentSpec, len(components))
	for i, component := range components {
		result[i] = component
		result[i].ConfigHash = nodeComponentConfigHash(component)
	}

	return result
}

func nodeComponentConfigHash(component v1.NodeComponentSpec) string {
	component.ConfigHash = ""

	content, err := json.Marshal(component)
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}

func buildRayComponent(role v1.StaticNodeRole) v1.NodeComponentSpec {
	if role == v1.StaticNodeRoleHead {
		return v1.NodeComponentSpec{
			Name:          "ray-head",
			Type:          v1.NodeComponentTypeRayHead,
			RestartPolicy: v1.NodeComponentRestartPolicyAlways,
			HealthCheck: &v1.NodeComponentHealthCheck{
				HTTPPath: defaultHealthHTTPPath,
				Port:     defaultRayDashboardPort,
			},
		}
	}

	return v1.NodeComponentSpec{
		Name:          "ray-worker",
		Type:          v1.NodeComponentTypeRayWorker,
		RestartPolicy: v1.NodeComponentRestartPolicyAlways,
	}
}

func nodeMetricsReady(node *v1.StaticNode) bool {
	required := map[string]bool{
		nodeExporterComponentName:   false,
		neutreeMetricsComponentName: false,
	}

	if node.Spec != nil && node.Spec.Role == v1.StaticNodeRoleHead {
		required[vmagentComponentName] = false
	}

	for _, component := range node.Status.Components {
		if _, ok := required[component.Name]; ok && component.Ready && component.Phase == v1.NodeComponentPhaseRunning {
			required[component.Name] = true
		}
	}

	for _, ready := range required {
		if !ready {
			return false
		}
	}

	return true
}

func buildNodeWarmSpec(cluster *v1.StaticNodeCluster) *v1.WarmSpec {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Version == "" || cluster.Spec.ImageRegistry == "" {
		return nil
	}

	return &v1.WarmSpec{
		Images: []v1.WarmImageSpec{
			{
				Name:     "ray-runtime",
				Ref:      strings.TrimRight(cluster.Spec.ImageRegistry, "/") + "/neutree-serve:" + cluster.Spec.Version,
				Required: true,
			},
		},
	}
}

func copyStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}

	return copied
}
