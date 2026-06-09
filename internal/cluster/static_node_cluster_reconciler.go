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
	"github.com/neutree-ai/neutree/internal/componentversion"
)

const (
	staticNodeClusterLabelKey = "neutree.ai/static-node-cluster"
	staticNodeRoleLabelKey    = "neutree.ai/static-node-role"

	nodeExporterComponentName        = "node-exporter"
	vmagentComponentName             = "vmagent"
	acceleratorExporterComponentName = "accelerator-exporter"
	defaultVMAgentPort               = 8429
	defaultNodeExporterPort          = 9100
	defaultRayDashboardPort          = 8265
	defaultPrometheusHTTPPath        = "/metrics"
	defaultHealthHTTPPath            = "/health"
	vmagentConfigPath                = "/etc/neutree/vmagent/config.yaml"
	defaultNodeExporterImage         = "quay.io/prometheus/node-exporter:v1.8.2"
	defaultVMAgentImage              = "victoriametrics/vmagent:" + componentversion.VictoriaMetrics
)

type StaticNodeClusterReconciler struct{}

type StaticNodeClusterReconcilePlan struct {
	DesiredNodes []*v1.StaticNode
	Status       v1.StaticNodeClusterStatus
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

	if cluster.Spec.Version == "" {
		return nil, errors.New("static node cluster spec.version is required")
	}

	if cluster.Spec.ImageRegistry == "" {
		return nil, errors.New("static node cluster spec.image_registry is required")
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
				SSHAuth:         copyAuth(nodeSpec.SSHAuth),
				Warm:            buildNodeWarmSpec(cluster),
				Components:      buildNodeComponents(cluster, role, profile),
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

func buildNodeComponents(
	cluster *v1.StaticNodeCluster,
	role v1.StaticNodeRole,
	profile *v1.AcceleratorProfile,
) []v1.NodeComponentSpec {
	components := []v1.NodeComponentSpec{
		buildRayComponent(cluster, role, profile),
		{
			Name:  nodeExporterComponentName,
			Type:  v1.NodeComponentTypeNodeExporter,
			Image: defaultNodeExporterImage,
			Args:  []string{"--path.rootfs=/host"},
			DockerRunOptions: []string{
				"--net=host",
				"--pid=host",
			},
			Volumes: []v1.NodeComponentVolume{
				{
					Name:      "host-root",
					HostPath:  "/",
					MountPath: "/host",
					ReadOnly:  true,
				},
			},
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

	vmagentDependencies := []string{nodeExporterComponentName}

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

		vmagentDependencies = append(vmagentDependencies, acceleratorExporterComponentName)
	}

	if role == v1.StaticNodeRoleHead {
		vmagentArgs := []string{
			"-promscrape.config=" + vmagentConfigPath,
			fmt.Sprintf("-httpListenAddr=:%d", defaultVMAgentPort),
		}
		if cluster.Spec.MetricsRemoteWriteURL != "" {
			vmagentArgs = append(vmagentArgs, "-remoteWrite.url="+cluster.Spec.MetricsRemoteWriteURL)
		}

		components = append(components, v1.NodeComponentSpec{
			Name:             vmagentComponentName,
			Type:             v1.NodeComponentTypeMetricsAgent,
			Image:            defaultVMAgentImage,
			Args:             vmagentArgs,
			DockerRunOptions: []string{"--net=host"},
			Volumes: []v1.NodeComponentVolume{
				{
					Name:      "vmagent-config",
					HostPath:  vmagentConfigPath,
					MountPath: vmagentConfigPath,
					ReadOnly:  true,
				},
			},
			Dependencies:  vmagentDependencies,
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

		if node.Spec.Role != v1.StaticNodeRoleHead {
			continue
		}

		appendComponentConfigFile(node, vmagentComponentName, v1.NodeComponentConfigFile{
			Path:         vmagentConfigPath,
			Content:      renderVMAgentConfig(cluster, acceleratorProfiles),
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

func renderVMAgentConfig(
	cluster *v1.StaticNodeCluster,
	acceleratorProfiles map[string]*v1.AcceleratorProfile,
) string {
	var builder strings.Builder
	builder.WriteString("global:\n")
	builder.WriteString("  scrape_interval: 15s\n")
	builder.WriteString("scrape_configs:\n")

	nodes := append([]v1.StaticNodeClusterNodeSpec{}, cluster.Spec.Nodes...)
	sort.SliceStable(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	builder.WriteString("- job_name: static-node-node-exporter\n")
	builder.WriteString("  static_configs:\n")
	for _, node := range nodes {
		writeVMAgentStaticConfig(&builder, cluster, node, nodeExporterComponentName, defaultNodeExporterPort, nil)
	}

	if hasAcceleratorExporterTarget(nodes, acceleratorProfiles) {
		builder.WriteString("- job_name: static-node-accelerator-exporter\n")
		builder.WriteString("  static_configs:\n")

		for _, node := range nodes {
			profile := acceleratorProfiles[node.AcceleratorType]
			if profile == nil || profile.Metrics == nil || profile.Metrics.Exporter == nil {
				continue
			}

			extraLabels := map[string]string{
				"accelerator_type": node.AcceleratorType,
				"exporter_kind":    profile.Metrics.Exporter.Kind,
			}
			writeVMAgentStaticConfig(&builder, cluster, node, acceleratorExporterComponentName, profile.Metrics.Exporter.Port, extraLabels)
		}
	}

	return builder.String()
}

func hasAcceleratorExporterTarget(
	nodes []v1.StaticNodeClusterNodeSpec,
	acceleratorProfiles map[string]*v1.AcceleratorProfile,
) bool {
	for _, node := range nodes {
		profile := acceleratorProfiles[node.AcceleratorType]
		if profile != nil && profile.Metrics != nil && profile.Metrics.Exporter != nil {
			return true
		}
	}

	return false
}

func writeVMAgentStaticConfig(
	builder *strings.Builder,
	cluster *v1.StaticNodeCluster,
	node v1.StaticNodeClusterNodeSpec,
	source string,
	port int,
	extraLabels map[string]string,
) {
	builder.WriteString("  - targets:\n")
	builder.WriteString("    - ")
	builder.WriteString(strconv.Quote(fmt.Sprintf("%s:%d", node.IP, port)))
	builder.WriteString("\n")
	builder.WriteString("    labels:\n")
	builder.WriteString("      source: ")
	builder.WriteString(strconv.Quote(source))
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
	builder.WriteString("      node: ")
	builder.WriteString(strconv.Quote(node.Name))
	builder.WriteString("\n")
	builder.WriteString("      node_ip: ")
	builder.WriteString(strconv.Quote(node.IP))
	builder.WriteString("\n")
	builder.WriteString("      node_role: ")
	builder.WriteString(strconv.Quote(string(staticNodeClusterNodeRole(cluster, node))))
	builder.WriteString("\n")

	keys := make([]string, 0, len(extraLabels))
	for key := range extraLabels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		builder.WriteString("      ")
		builder.WriteString(key)
		builder.WriteString(": ")
		builder.WriteString(strconv.Quote(extraLabels[key]))
		builder.WriteString("\n")
	}
}

func staticNodeClusterNodeRole(
	cluster *v1.StaticNodeCluster,
	node v1.StaticNodeClusterNodeSpec,
) v1.StaticNodeRole {
	if cluster != nil && cluster.Spec != nil && node.Name == cluster.Spec.Head.NodeName {
		return v1.StaticNodeRoleHead
	}

	return normalizeStaticNodeRole(node.Role)
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

func buildRayComponent(
	cluster *v1.StaticNodeCluster,
	role v1.StaticNodeRole,
	profile *v1.AcceleratorProfile,
) v1.NodeComponentSpec {
	image := buildRayRuntimeImage(cluster)
	env := rayRuntimeEnv(profile)
	dockerRunOptions := rayRuntimeDockerRunOptions(profile)
	command := []string{"/bin/bash", "-lc"}

	if role == v1.StaticNodeRoleHead {
		return v1.NodeComponentSpec{
			Name:             "ray-head",
			Type:             v1.NodeComponentTypeRayHead,
			Image:            image,
			Command:          command,
			Args:             []string{rayStartCommand(cluster, role)},
			Env:              env,
			DockerRunOptions: dockerRunOptions,
			RestartPolicy:    v1.NodeComponentRestartPolicyAlways,
			HealthCheck: &v1.NodeComponentHealthCheck{
				HTTPPath: "/api/v0/cluster_metadata",
				Port:     defaultRayDashboardPort,
			},
		}
	}

	healthCommand := []string{
		"docker",
		"exec",
		sanitizeContainerName(componentContainerPrefix(cluster) + "-ray-worker"),
		"ray",
		"status",
		"--address=" + staticNodeClusterHeadIP(cluster) + ":6379",
	}

	return v1.NodeComponentSpec{
		Name:             "ray-worker",
		Type:             v1.NodeComponentTypeRayWorker,
		Image:            image,
		Command:          command,
		Args:             []string{rayStartCommand(cluster, role)},
		Env:              env,
		DockerRunOptions: dockerRunOptions,
		RestartPolicy:    v1.NodeComponentRestartPolicyAlways,
		HealthCheck: &v1.NodeComponentHealthCheck{
			Command: healthCommand,
		},
	}
}

func rayRuntimeEnv(profile *v1.AcceleratorProfile) map[string]string {
	env := map[string]string{
		"RAY_DEFAULT_OBJECT_STORE_MEMORY_PROPORTION":     "0.1",
		"RAY_enable_open_telemetry":                      "false",
		"RAY_EXPERIMENTAL_RUNTIME_ENV_CONTAINER_RUNTIME": "docker",
		"RAY_process_group_cleanup_enabled":              "true",
	}

	if profile == nil || profile.ClusterRuntime == nil {
		return env
	}

	for key, value := range profile.ClusterRuntime.Env {
		env[key] = value
	}

	return env
}

func rayRuntimeDockerRunOptions(profile *v1.AcceleratorProfile) []string {
	options := []string{
		"--net=host",
		"--ulimit nofile=65536:65536",
		"--volume /var/run/docker.sock:/var/run/docker.sock",
		"--volume /tmp:/tmp",
		"--pid=host",
		"--ipc=host",
	}

	if profile == nil || profile.ClusterRuntime == nil {
		return options
	}

	if profile.ClusterRuntime.Runtime != "" {
		options = append(options, "--runtime="+profile.ClusterRuntime.Runtime)
	}

	options = append(options, profile.ClusterRuntime.Options...)

	return options
}

func rayStartCommand(
	cluster *v1.StaticNodeCluster,
	role v1.StaticNodeRole,
) string {
	parts := []string{
		"ray stop",
		"ulimit -n 65536",
	}

	commonArgs := strings.Join([]string{
		"--disable-usage-stats",
		"--node-manager-port=8077",
		"--dashboard-agent-listen-port=52365",
		"--min-worker-port=10002",
		"--max-worker-port=20000",
		"--runtime-env-agent-port=56999",
		fmt.Sprintf("--metrics-export-port=%d", v1.RayletMetricsPort),
	}, " ")

	if role == v1.StaticNodeRoleHead {
		parts = append(parts, strings.Join([]string{
			"python /home/ray/start.py --head --port=6379 --dashboard-host=0.0.0.0",
			commonArgs,
			"--dashboard-port=8265",
			"--ray-client-server-port=10001",
			rayNodeLabelArg(cluster, role),
		}, " "))
	} else {
		parts = append(parts, strings.Join([]string{
			"python /home/ray/start.py --address=" + staticNodeClusterHeadIP(cluster) + ":6379",
			commonArgs,
			rayNodeLabelArg(cluster, role),
		}, " "))
	}

	parts = append(parts, "tail -f /dev/null")

	return strings.Join(parts, " && ")
}

func rayNodeLabelArg(cluster *v1.StaticNodeCluster, role v1.StaticNodeRole) string {
	labels := map[string]string{}
	if cluster != nil && cluster.Spec != nil && cluster.Spec.Version != "" {
		labels[v1.NeutreeServingVersionLabel] = cluster.Spec.Version
	}

	if role == v1.StaticNodeRoleWorker {
		labels[v1.NeutreeNodeProvisionTypeLabel] = v1.StaticNodeProvisionType
	}

	content, err := json.Marshal(labels)
	if err != nil || len(labels) == 0 {
		return ""
	}

	return "--labels='" + string(content) + "'"
}

func staticNodeClusterHeadIP(cluster *v1.StaticNodeCluster) string {
	if cluster == nil || cluster.Spec == nil {
		return ""
	}

	for _, node := range cluster.Spec.Nodes {
		if node.Name == cluster.Spec.Head.NodeName {
			return node.IP
		}
	}

	return ""
}

func componentContainerPrefix(cluster *v1.StaticNodeCluster) string {
	prefix := "neutree"
	if cluster != nil && cluster.Metadata != nil && cluster.Metadata.Name != "" {
		prefix += "-" + cluster.Metadata.Name
	}

	return prefix
}

func nodeMetricsReady(node *v1.StaticNode) bool {
	required := map[string]bool{
		nodeExporterComponentName: false,
	}

	if node.Spec != nil && node.Spec.Role == v1.StaticNodeRoleHead {
		required[vmagentComponentName] = false
	}

	if node.Spec != nil {
		for _, component := range node.Spec.Components {
			if component.Name == acceleratorExporterComponentName {
				required[acceleratorExporterComponentName] = false
			}
		}
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
	image := buildRayRuntimeImage(cluster)
	if image == "" {
		return nil
	}

	return &v1.WarmSpec{
		Images: []v1.WarmImageSpec{
			{
				Name:     "ray-runtime",
				Ref:      image,
				Required: true,
			},
		},
	}
}

func buildRayRuntimeImage(cluster *v1.StaticNodeCluster) string {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Version == "" || cluster.Spec.ImageRegistry == "" {
		return ""
	}

	return strings.TrimRight(cluster.Spec.ImageRegistry, "/") + "/neutree-serve:" + cluster.Spec.Version
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

func copyAuth(auth *v1.Auth) *v1.Auth {
	if auth == nil {
		return nil
	}

	copied := *auth

	return &copied
}
