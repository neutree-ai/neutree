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
	staticClusterLabelKey  = "neutree.ai/static-cluster"
	staticNodeRoleLabelKey = "neutree.ai/static-node-role"

	nodeExporterWorkerName    = "node-exporter"
	neutreeMetricsWorkerName  = "neutree-metrics"
	vmagentWorkerName         = "vmagent"
	acceleratorExporterName   = "accelerator-exporter"
	defaultNeutreeMetricsPort = 19090
	defaultVMAgentPort        = 8429
	defaultNodeExporterPort   = 9100
	defaultRayDashboardPort   = 8265
	defaultPrometheusHTTPPath = "/metrics"
	defaultHealthHTTPPath     = "/health"
	neutreeMetricsConfigPath  = "/etc/neutree/neutree-metrics/config.json"
	vmagentConfigPath         = "/etc/neutree/vmagent/config.yaml"
)

type StaticClusterReconciler struct{}

type StaticClusterReconcilePlan struct {
	DesiredNodes []*v1.StaticNode
	Status       v1.StaticClusterStatus
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

func (r *StaticClusterReconciler) Plan(
	cluster *v1.StaticCluster,
	currentNodes []*v1.StaticNode,
	acceleratorProfiles map[string]*v1.AcceleratorProfile,
) (*StaticClusterReconcilePlan, error) {
	desiredNodes, err := r.BuildDesiredNodes(cluster, acceleratorProfiles)
	if err != nil {
		return nil, err
	}

	return &StaticClusterReconcilePlan{
		DesiredNodes: desiredNodes,
		Status:       r.AggregateStatus(cluster, currentNodes),
	}, nil
}

func (r *StaticClusterReconciler) BuildDesiredNodes(
	cluster *v1.StaticCluster,
	acceleratorProfiles map[string]*v1.AcceleratorProfile,
) ([]*v1.StaticNode, error) {
	if cluster == nil {
		return nil, errors.New("static cluster is nil")
	}

	if cluster.Metadata == nil || cluster.Metadata.Name == "" {
		return nil, errors.New("static cluster metadata.name is required")
	}

	if cluster.Spec == nil {
		return nil, errors.New("static cluster spec is required")
	}

	if cluster.Spec.Head.NodeName == "" {
		return nil, errors.New("static cluster spec.head.node_name is required")
	}

	if len(cluster.Spec.Nodes) == 0 {
		return nil, errors.New("static cluster spec.nodes is required")
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
				Warm:            copyWarmSpec(cluster.Spec.Warm),
				Workers:         buildNodeWorkers(role, profile),
			},
		})
	}

	if !headSeen {
		return nil, fmt.Errorf("head node %s not found in static cluster nodes", cluster.Spec.Head.NodeName)
	}

	sort.SliceStable(desiredNodes, func(i, j int) bool {
		return desiredNodes[i].Metadata.Name < desiredNodes[j].Metadata.Name
	})

	if err := attachMetricsConfigFiles(cluster, desiredNodes, acceleratorProfiles); err != nil {
		return nil, err
	}

	for _, node := range desiredNodes {
		node.Spec.Workers = withWorkerConfigHashes(node.Spec.Workers)
	}

	return desiredNodes, nil
}

func (r *StaticClusterReconciler) AggregateStatus(
	cluster *v1.StaticCluster,
	nodes []*v1.StaticNode,
) v1.StaticClusterStatus {
	desiredNodes := 0
	headName := ""

	if cluster != nil && cluster.Spec != nil {
		desiredNodes = len(cluster.Spec.Nodes)
		headName = cluster.Spec.Head.NodeName
	}

	status := v1.StaticClusterStatus{
		Phase:        v1.StaticClusterPhaseProvisioning,
		DesiredNodes: desiredNodes,
	}

	if desiredNodes == 0 {
		status.Phase = v1.StaticClusterPhaseFailed
		status.ErrorMessage = "static cluster has no desired nodes"

		return status
	}

	warmKnown := len(nodes) > 0
	metricsKnown := len(nodes) > 0
	warmReady := warmKnown
	metricsReady := metricsKnown
	anyNodeFailed := false

	for _, node := range nodes {
		if node == nil || node.Metadata == nil || node.Status == nil {
			warmReady = false
			metricsReady = false

			continue
		}

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

	status.WarmReady = warmReady
	status.MetricsReady = metricsReady

	switch {
	case anyNodeFailed:
		status.Phase = v1.StaticClusterPhaseFailed
	case status.ReadyNodes == desiredNodes && status.HeadReady && status.WarmReady && status.MetricsReady:
		status.Phase = v1.StaticClusterPhaseReady
	case status.HeadReady && status.ReadyNodes > 0:
		status.Phase = v1.StaticClusterPhaseDegraded
	default:
		status.Phase = v1.StaticClusterPhaseProvisioning
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
		staticClusterLabelKey:  clusterName,
		staticNodeRoleLabelKey: string(role),
	}
}

func buildNodeWorkers(role v1.StaticNodeRole, profile *v1.AcceleratorProfile) []v1.NodeWorkerSpec {
	workers := []v1.NodeWorkerSpec{
		buildRayWorker(role),
		{
			Name:          nodeExporterWorkerName,
			Type:          v1.NodeWorkerTypeNodeExporter,
			RestartPolicy: v1.NodeWorkerRestartPolicyAlways,
			Ports: []v1.NodeWorkerPort{
				{Name: "metrics", Port: defaultNodeExporterPort, Protocol: "TCP"},
			},
			HealthCheck: &v1.NodeWorkerHealthCheck{
				HTTPPath: defaultPrometheusHTTPPath,
				Port:     defaultNodeExporterPort,
			},
		},
	}

	metricsDependencies := []string{nodeExporterWorkerName}

	if profile != nil && profile.Metrics != nil && profile.Metrics.Exporter != nil {
		exporter := profile.Metrics.Exporter

		workers = append(workers, v1.NodeWorkerSpec{
			Name:             acceleratorExporterName,
			Type:             exporter.WorkerType,
			Image:            exporter.Image,
			DockerRunOptions: append([]string{}, exporter.DockerRunOptions...),
			RestartPolicy:    v1.NodeWorkerRestartPolicyAlways,
			Ports: []v1.NodeWorkerPort{
				{Name: "metrics", Port: exporter.Port, Protocol: "TCP"},
			},
			HealthCheck: &v1.NodeWorkerHealthCheck{
				HTTPPath: defaultPrometheusHTTPPath,
				Port:     exporter.Port,
			},
		})

		metricsDependencies = append(metricsDependencies, acceleratorExporterName)
	}

	workers = append(workers, v1.NodeWorkerSpec{
		Name:          neutreeMetricsWorkerName,
		Type:          v1.NodeWorkerTypeMetricsNormalizer,
		Dependencies:  metricsDependencies,
		RestartPolicy: v1.NodeWorkerRestartPolicyAlways,
		Ports: []v1.NodeWorkerPort{
			{Name: "http", Port: defaultNeutreeMetricsPort, Protocol: "TCP"},
		},
		HealthCheck: &v1.NodeWorkerHealthCheck{
			HTTPPath: defaultHealthHTTPPath,
			Port:     defaultNeutreeMetricsPort,
		},
	})

	if role == v1.StaticNodeRoleHead {
		workers = append(workers, v1.NodeWorkerSpec{
			Name:          vmagentWorkerName,
			Type:          v1.NodeWorkerTypeMetricsAgent,
			Dependencies:  []string{neutreeMetricsWorkerName},
			RestartPolicy: v1.NodeWorkerRestartPolicyAlways,
			Ports: []v1.NodeWorkerPort{
				{Name: "http", Port: defaultVMAgentPort, Protocol: "TCP"},
			},
			HealthCheck: &v1.NodeWorkerHealthCheck{
				HTTPPath: defaultHealthHTTPPath,
				Port:     defaultVMAgentPort,
			},
		})
	}

	return workers
}

func attachMetricsConfigFiles(
	cluster *v1.StaticCluster,
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

		appendWorkerConfigFile(node, neutreeMetricsWorkerName, v1.NodeWorkerConfigFile{
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

		appendWorkerConfigFile(node, vmagentWorkerName, v1.NodeWorkerConfigFile{
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
	cluster *v1.StaticCluster,
	node *v1.StaticNode,
	profile *v1.AcceleratorProfile,
) (string, error) {
	config := metricsNormalizerConfig{
		Labels: map[string]string{
			"workspace":      node.Metadata.Workspace,
			"static_cluster": node.Spec.Cluster,
			"cluster_type":   "ray",
			"node":           node.Metadata.Name,
			"node_ip":        node.Spec.IP,
			"node_role":      string(node.Spec.Role),
		},
		Targets: []metricsNormalizerTarget{
			{
				Name:    nodeExporterWorkerName,
				URL:     fmt.Sprintf("http://127.0.0.1:%d%s", defaultNodeExporterPort, defaultPrometheusHTTPPath),
				Timeout: "5s",
			},
		},
	}

	if cluster != nil && cluster.Metadata != nil && config.Labels["static_cluster"] == "" {
		config.Labels["static_cluster"] = cluster.Metadata.Name
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
			Name:    acceleratorExporterName,
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

func renderVMAgentConfig(cluster *v1.StaticCluster) string {
	var builder strings.Builder
	builder.WriteString("global:\n")
	builder.WriteString("  scrape_interval: 15s\n")
	builder.WriteString("scrape_configs:\n")
	builder.WriteString("- job_name: neutree-metrics\n")
	builder.WriteString("  static_configs:\n")
	builder.WriteString("  - targets:\n")

	nodes := append([]v1.StaticClusterNodeSpec{}, cluster.Spec.Nodes...)
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
	builder.WriteString("      static_cluster: ")
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

func appendWorkerConfigFile(node *v1.StaticNode, workerName string, configFile v1.NodeWorkerConfigFile) {
	for i := range node.Spec.Workers {
		if node.Spec.Workers[i].Name != workerName {
			continue
		}

		worker := node.Spec.Workers[i]
		worker.ConfigFiles = append(append([]v1.NodeWorkerConfigFile{}, worker.ConfigFiles...), configFile)
		node.Spec.Workers[i] = worker

		return
	}
}

func withWorkerConfigHashes(workers []v1.NodeWorkerSpec) []v1.NodeWorkerSpec {
	result := make([]v1.NodeWorkerSpec, len(workers))
	for i, worker := range workers {
		result[i] = worker
		result[i].ConfigHash = nodeWorkerConfigHash(worker)
	}

	return result
}

func nodeWorkerConfigHash(worker v1.NodeWorkerSpec) string {
	worker.ConfigHash = ""

	content, err := json.Marshal(worker)
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}

func buildRayWorker(role v1.StaticNodeRole) v1.NodeWorkerSpec {
	if role == v1.StaticNodeRoleHead {
		return v1.NodeWorkerSpec{
			Name:          "ray-head",
			Type:          v1.NodeWorkerTypeRayHead,
			RestartPolicy: v1.NodeWorkerRestartPolicyAlways,
			HealthCheck: &v1.NodeWorkerHealthCheck{
				HTTPPath: defaultHealthHTTPPath,
				Port:     defaultRayDashboardPort,
			},
		}
	}

	return v1.NodeWorkerSpec{
		Name:          "ray-worker",
		Type:          v1.NodeWorkerTypeRayWorker,
		RestartPolicy: v1.NodeWorkerRestartPolicyAlways,
	}
}

func nodeMetricsReady(node *v1.StaticNode) bool {
	required := map[string]bool{
		nodeExporterWorkerName:   false,
		neutreeMetricsWorkerName: false,
	}

	if node.Spec != nil && node.Spec.Role == v1.StaticNodeRoleHead {
		required[vmagentWorkerName] = false
	}

	for _, worker := range node.Status.Workers {
		if _, ok := required[worker.Name]; ok && worker.Ready && worker.Phase == v1.NodeWorkerPhaseRunning {
			required[worker.Name] = true
		}
	}

	for _, ready := range required {
		if !ready {
			return false
		}
	}

	return true
}

func copyWarmSpec(warm *v1.WarmSpec) *v1.WarmSpec {
	if warm == nil {
		return nil
	}

	images := make([]v1.WarmImageSpec, len(warm.Images))
	copy(images, warm.Images)

	return &v1.WarmSpec{Images: images}
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
