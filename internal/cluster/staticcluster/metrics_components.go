package staticcluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/componentversion"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	nodeExporterComponentName           = "node-exporter"
	nodeAgentComponentName              = "neutree-node-agent"
	vmagentComponentName                = "vmagent"
	acceleratorExporterComponentName    = "accelerator-exporter"
	defaultVMAgentPort                  = 8429
	defaultNodeExporterPort             = 19100
	defaultNodeAgentPort                = 19101
	defaultPrometheusHTTPPath           = "/metrics"
	externalDCGMExporterPort            = 9400
	defaultHealthHTTPPath               = "/health"
	vmagentConfigPath                   = "/etc/neutree/vmagent/config.yaml"
	vmagentFileSDDir                    = "/etc/neutree/vmagent/file_sd"
	vmagentNodeExporterFileSDPath       = vmagentFileSDDir + "/node-exporter.json"
	vmagentNodeAgentFileSDPath          = vmagentFileSDDir + "/node-agent.json"
	vmagentRayFileSDPath                = vmagentFileSDDir + "/ray.json"
	managedAcceleratorExporterJobPrefix = "accelerator-exporter"
	defaultNodeExporterImage            = "quay.io/prometheus/node-exporter:" + componentversion.NodeExporter
	defaultVMAgentImage                 = "victoriametrics/vmagent:" + componentversion.VictoriaMetrics
)

const staticVMAgentConfigTemplateText = `global:
  scrape_interval: 30s
  scrape_timeout: 30s
scrape_configs:
{{ range .ScrapeConfigs }}- job_name: {{ .JobName }}
{{ if .MetricsPath }}  metrics_path: {{ .MetricsPath }}
{{ end }}  file_sd_configs:
  - files:
    - {{ .FileSDPath }}
{{ if .MetricRelabelConfigs }}  metric_relabel_configs:
{{ .MetricRelabelConfigs }}{{ end }}{{ end }}`

const staticVMAgentRayMetricRelabelConfigs = `    - source_labels: [application]
      target_label: application_original
      regex: '(.+)'
      replacement: '$1'
    - source_labels: [application]
      regex: '([^_]+)_(.+)'
      target_label: application
      replacement: '$2'
    - source_labels: [__name__]
      regex: 'ray_vllm[:_](.+)'
      target_label: __name__
      replacement: 'vllm:$1'
    - source_labels: [__name__]
      regex: 'ray_sglang[:_](.+)'
      target_label: __name__
      replacement: 'sglang:$1'
`

var staticVMAgentConfigTemplate = template.Must(template.New("static-vmagent-config").Parse(staticVMAgentConfigTemplateText))

type staticVMAgentConfigData struct {
	ScrapeConfigs []staticVMAgentScrapeConfig
}

type staticVMAgentScrapeConfig struct {
	JobName              string
	MetricsPath          string
	FileSDPath           string
	MetricRelabelConfigs string
}

func buildMetricsComponents(
	cluster *v1.StaticNodeCluster,
	node *v1.StaticNode,
	role v1.StaticNodeRole,
	profile *v1.AcceleratorProfile,
	metricsRemoteWriteURL string,
) []v1.NodeComponentSpec {
	if !util.IsHTTPOrHTTPSURL(metricsRemoteWriteURL) {
		return nil
	}

	components := []v1.NodeComponentSpec{buildNodeExporterComponent(cluster)}

	if acceleratorExporterMode(cluster) == v1.ClusterAcceleratorExporterModeManaged {
		if exporter := acceleratorExporterProfile(profile); validAcceleratorExporterProfile(exporter) {
			components = append(components, buildAcceleratorExporterComponent(cluster, exporter))
		}
	}

	components = append(components, buildNodeAgentComponent(cluster, node, profile))

	if role == v1.StaticNodeRoleHead {
		components = append(components, buildVMAgentComponent(cluster, metricsRemoteWriteURL))
	}

	return components
}

func buildNodeExporterComponent(cluster *v1.StaticNodeCluster) v1.NodeComponentSpec {
	return v1.NodeComponentSpec{
		Name:  nodeExporterComponentName,
		Image: staticComponentImage(cluster, defaultNodeExporterImage),
		Args: []string{
			"--path.rootfs=/host",
			fmt.Sprintf("--web.listen-address=:%d", defaultNodeExporterPort),
		},
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
		Ports: []v1.NodeComponentPort{
			{Name: "metrics", Port: defaultNodeExporterPort, Protocol: "TCP"},
		},
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPPath: defaultPrometheusHTTPPath,
			Port:     defaultNodeExporterPort,
		},
	}
}

func acceleratorExporterProfile(profile *v1.AcceleratorProfile) *v1.AcceleratorExporterProfile {
	if profile == nil {
		return nil
	}

	return profile.MetricsExporter
}

func validAcceleratorExporterProfile(exporter *v1.AcceleratorExporterProfile) bool {
	return exporter != nil &&
		strings.TrimSpace(exporter.Name) != "" &&
		strings.TrimSpace(exporter.Image) != "" &&
		exporter.Port > 0
}

func buildAcceleratorExporterComponent(
	cluster *v1.StaticNodeCluster,
	exporter *v1.AcceleratorExporterProfile,
) v1.NodeComponentSpec {
	return v1.NodeComponentSpec{
		Name:             acceleratorExporterComponentName,
		Image:            staticComponentImage(cluster, exporter.Image),
		Args:             append([]string{}, exporter.Args...),
		Env:              copyMetricsStringMap(exporter.Env),
		Volumes:          acceleratorExporterConfigVolumes(exporter.ConfigFiles),
		ConfigFiles:      acceleratorExporterComponentConfigFiles(exporter.ConfigFiles),
		DockerRunOptions: acceleratorExporterDockerRunOptions(exporter.Runtime),
		Ports: []v1.NodeComponentPort{
			{Name: "metrics", Port: exporter.Port, Protocol: "TCP"},
		},
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPPath: exporterMetricsPath(exporter),
			Port:     exporter.Port,
		},
	}
}

func buildNodeAgentComponent(
	cluster *v1.StaticNodeCluster,
	node *v1.StaticNode,
	profile *v1.AcceleratorProfile,
) v1.NodeComponentSpec {
	args := []string{
		fmt.Sprintf("--listen-address=:%d", defaultNodeAgentPort),
		"--cluster-type=ray",
		"--metrics-mode=" + string(acceleratorExporterMode(cluster)),
		fmt.Sprintf("--ray-dashboard-url=http://%s:%d", staticNodeClusterHeadIP(cluster), v1.RayDashboardPort),
	}

	if node != nil && node.Metadata != nil {
		args = append(args, "--node="+node.Metadata.Name)
	}

	if node != nil && node.Spec != nil {
		args = append(args, "--node-ip="+node.Spec.IP)
	}

	return v1.NodeComponentSpec{
		Name:             nodeAgentComponentName,
		Image:            staticComponentImage(cluster, defaultNodeAgentImage(cluster)),
		Args:             args,
		Env:              nodeAgentEnv(profile),
		DockerRunOptions: nodeAgentDockerRunOptions(profile),
		Ports: []v1.NodeComponentPort{
			{Name: "http", Port: defaultNodeAgentPort, Protocol: "TCP"},
		},
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPPath: defaultHealthHTTPPath,
			Port:     defaultNodeAgentPort,
		},
	}
}

func nodeAgentEnv(profile *v1.AcceleratorProfile) map[string]string {
	exporter := acceleratorExporterProfile(profile)
	if exporter == nil || len(exporter.Env) == 0 {
		return nil
	}

	allowed := map[string]struct{}{
		"NVIDIA_VISIBLE_DEVICES":     {},
		"NVIDIA_DRIVER_CAPABILITIES": {},
	}
	env := map[string]string{}

	for key, value := range exporter.Env {
		if _, ok := allowed[key]; !ok {
			continue
		}

		env[key] = value
	}

	return env
}

func defaultNodeAgentImage(cluster *v1.StaticNodeCluster) string {
	return "neutree/neutree-node-agent:" + componentversion.NeutreeNodeAgent
}

func staticNodeClusterVersion(cluster *v1.StaticNodeCluster) string {
	if cluster == nil || cluster.Spec == nil {
		return ""
	}

	return cluster.Spec.Version
}

func nodeAgentDockerRunOptions(profile *v1.AcceleratorProfile) []string {
	options := []string{"--net=host", "--pid=host", "--cgroupns=host"}
	exporter := acceleratorExporterProfile(profile)

	if exporter == nil || exporter.Runtime == nil {
		return options
	}

	// Until AcceleratorProfile exposes a dedicated NodeAgentRuntime, reuse the
	// metrics exporter runtime because both components need accelerator visibility.
	return appendDockerRunOptionsUnique(options, acceleratorExporterDockerRunOptions(exporter.Runtime)...)
}

func appendDockerRunOptionsUnique(options []string, values ...string) []string {
	seen := make(map[string]struct{}, len(options)+len(values))
	result := make([]string, 0, len(options)+len(values))
	merged := append(append([]string{}, options...), values...)

	for _, option := range merged {
		option = strings.TrimSpace(option)
		if option == "" {
			continue
		}

		if _, ok := seen[option]; ok {
			continue
		}

		seen[option] = struct{}{}

		result = append(result, option)
	}

	return result
}

func copyMetricsStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}

	return copied
}

func buildVMAgentComponent(cluster *v1.StaticNodeCluster, metricsRemoteWriteURL string) v1.NodeComponentSpec {
	vmagentArgs := []string{
		"-promscrape.config=" + vmagentConfigPath,
		fmt.Sprintf("-httpListenAddr=:%d", defaultVMAgentPort),
	}
	if metricsRemoteWriteURL != "" {
		vmagentArgs = append(vmagentArgs, "-remoteWrite.url="+metricsRemoteWriteURL)
	}

	return v1.NodeComponentSpec{
		Name:             vmagentComponentName,
		Image:            staticComponentImage(cluster, defaultVMAgentImage),
		Args:             vmagentArgs,
		DockerRunOptions: []string{"--net=host"},
		Volumes: []v1.NodeComponentVolume{
			{
				Name:      "vmagent-config-dir",
				HostPath:  "/etc/neutree/vmagent",
				MountPath: "/etc/neutree/vmagent",
				ReadOnly:  true,
			},
		},
		Ports: []v1.NodeComponentPort{
			{Name: "http", Port: defaultVMAgentPort, Protocol: "TCP"},
		},
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPPath: defaultHealthHTTPPath,
			Port:     defaultVMAgentPort,
		},
	}
}

func attachMetricsConfigFiles(cluster *v1.StaticNodeCluster, plans []DesiredNodePlan) {
	for _, plan := range plans {
		node := plan.Node
		if node == nil || node.Spec == nil || node.Spec.Role != v1.StaticNodeRoleHead {
			continue
		}

		appendComponentConfigFile(node, vmagentComponentName, v1.NodeComponentConfigFile{
			Path:         vmagentConfigPath,
			Content:      renderVMAgentConfig(cluster, plans),
			Mode:         "0644",
			Owner:        "root",
			Group:        "root",
			Sudo:         true,
			Atomic:       true,
			CreateParent: true,
		})

		for _, configFile := range renderVMAgentFileSDConfigFiles(cluster, plans) {
			appendComponentConfigFile(node, vmagentComponentName, configFile)
		}
	}
}

func renderVMAgentConfig(cluster *v1.StaticNodeCluster, plans []DesiredNodePlan) string {
	plans = append([]DesiredNodePlan{}, plans...)
	sort.SliceStable(plans, func(i, j int) bool {
		return plans[i].Node.Metadata.Name < plans[j].Node.Metadata.Name
	})

	scrapeConfigs := []staticVMAgentScrapeConfig{}
	if len(nodeExporterTargets(plans)) > 0 {
		scrapeConfigs = append(scrapeConfigs, staticVMAgentScrapeConfig{
			JobName:    "static-node-node-exporter",
			FileSDPath: strconv.Quote(vmagentNodeExporterFileSDPath),
		})
	}

	if len(nodeAgentTargets(plans)) > 0 {
		scrapeConfigs = append(scrapeConfigs, staticVMAgentScrapeConfig{
			JobName:    "static-node-node-agent",
			FileSDPath: strconv.Quote(vmagentNodeAgentFileSDPath),
		})
	}

	scrapeConfigs = append(scrapeConfigs, staticVMAgentScrapeConfig{
		JobName:              "static-node-ray",
		FileSDPath:           strconv.Quote(vmagentRayFileSDPath),
		MetricRelabelConfigs: staticVMAgentRayMetricRelabelConfigs,
	})

	groups := acceleratorExporterTargetGroups(cluster, plans)
	for i, group := range groups {
		scrapeConfig := staticVMAgentScrapeConfig{
			JobName:    acceleratorExporterTargetGroupJobName(group, len(groups), i),
			FileSDPath: strconv.Quote(acceleratorExporterTargetGroupFileSDPath(group)),
		}
		if group.MetricsPath != defaultPrometheusHTTPPath {
			scrapeConfig.MetricsPath = strconv.Quote(group.MetricsPath)
		}

		scrapeConfigs = append(scrapeConfigs, scrapeConfig)
	}

	return mustRenderStaticVMAgentConfig(staticVMAgentConfigData{ScrapeConfigs: scrapeConfigs})
}

func mustRenderStaticVMAgentConfig(data staticVMAgentConfigData) string {
	var output bytes.Buffer
	if err := staticVMAgentConfigTemplate.Execute(&output, data); err != nil {
		return ""
	}

	return output.String()
}

func renderVMAgentFileSDConfigFiles(
	cluster *v1.StaticNodeCluster,
	plans []DesiredNodePlan,
) []v1.NodeComponentConfigFile {
	configFiles := []v1.NodeComponentConfigFile{
		vmagentFileSDConfigFile(
			vmagentRayFileSDPath,
			renderVMAgentRayFileSDTargets(cluster, plans),
		),
	}

	if len(nodeExporterTargets(plans)) > 0 {
		configFiles = append(configFiles, vmagentFileSDConfigFile(
			vmagentNodeExporterFileSDPath,
			renderVMAgentNodeExporterFileSDTargets(cluster, plans),
		))
	}

	if len(nodeAgentTargets(plans)) > 0 {
		configFiles = append(configFiles, vmagentFileSDConfigFile(
			vmagentNodeAgentFileSDPath,
			renderVMAgentNodeAgentFileSDTargets(cluster, plans),
		))
	}

	for _, group := range acceleratorExporterTargetGroups(cluster, plans) {
		configFiles = append(configFiles, vmagentFileSDConfigFile(
			acceleratorExporterTargetGroupFileSDPath(group),
			renderVMAgentAcceleratorExporterFileSDTargets(cluster, group.Targets),
		))
	}

	return configFiles
}

func vmagentFileSDConfigFile(path string, content string) v1.NodeComponentConfigFile {
	return v1.NodeComponentConfigFile{
		Path:                path,
		Content:             content,
		Mode:                "0644",
		Owner:               "root",
		Group:               "root",
		Sudo:                true,
		Atomic:              true,
		CreateParent:        true,
		SkipRestartOnChange: true,
	}
}

type vmagentFileSDTarget struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels,omitempty"`
}

func renderVMAgentNodeExporterFileSDTargets(
	cluster *v1.StaticNodeCluster,
	plans []DesiredNodePlan,
) string {
	targets := make([]vmagentFileSDTarget, 0, len(plans))

	for _, plan := range nodeExporterTargets(plans) {
		targets = append(targets, vmagentFileSDTarget{
			Targets: []string{fmt.Sprintf("%s:%d", plan.Node.Spec.IP, defaultNodeExporterPort)},
			Labels:  vmagentTargetLabels(cluster, plan.Node, nodeExporterComponentName),
		})
	}

	return mustMarshalVMAgentFileSDTargets(targets)
}

func nodeExporterTargets(plans []DesiredNodePlan) []DesiredNodePlan {
	targets := make([]DesiredNodePlan, 0, len(plans))

	for _, plan := range plans {
		if plan.Node == nil || plan.Node.Spec == nil || !staticNodeHasComponent(plan.Node, nodeExporterComponentName) {
			continue
		}

		targets = append(targets, plan)
	}

	return targets
}

func renderVMAgentNodeAgentFileSDTargets(
	cluster *v1.StaticNodeCluster,
	plans []DesiredNodePlan,
) string {
	targets := make([]vmagentFileSDTarget, 0, len(plans))

	for _, plan := range nodeAgentTargets(plans) {
		targets = append(targets, vmagentFileSDTarget{
			Targets: []string{fmt.Sprintf("%s:%d", plan.Node.Spec.IP, defaultNodeAgentPort)},
			Labels:  vmagentTargetLabels(cluster, plan.Node, nodeAgentComponentName),
		})
	}

	return mustMarshalVMAgentFileSDTargets(targets)
}

func nodeAgentTargets(plans []DesiredNodePlan) []DesiredNodePlan {
	targets := make([]DesiredNodePlan, 0, len(plans))

	for _, plan := range plans {
		if plan.Node == nil || plan.Node.Spec == nil || !staticNodeHasComponent(plan.Node, nodeAgentComponentName) {
			continue
		}

		targets = append(targets, plan)
	}

	return targets
}

func renderVMAgentRayFileSDTargets(
	cluster *v1.StaticNodeCluster,
	plans []DesiredNodePlan,
) string {
	targets := make([]vmagentFileSDTarget, 0, len(plans))

	for _, plan := range plans {
		if plan.Node == nil || plan.Node.Spec == nil || !staticNodeHasRayComponent(plan.Node) {
			continue
		}

		targets = append(targets, vmagentFileSDTarget{
			Targets: []string{fmt.Sprintf("%s:%d", plan.Node.Spec.IP, v1.RayletMetricsPort)},
			Labels:  vmagentTargetLabels(cluster, plan.Node, "ray"),
		})
	}

	return mustMarshalVMAgentFileSDTargets(targets)
}

func staticNodeHasRayComponent(node *v1.StaticNode) bool {
	return staticNodeHasComponent(node, rayHeadComponentName) || staticNodeHasComponent(node, rayWorkerComponentName)
}

func staticNodeHasComponent(node *v1.StaticNode, name string) bool {
	if node == nil || node.Spec == nil {
		return false
	}

	for _, component := range node.Spec.Components {
		if component.Name == name {
			return true
		}
	}

	return false
}

func renderVMAgentAcceleratorExporterFileSDTargets(
	cluster *v1.StaticNodeCluster,
	targets []acceleratorExporterTarget,
) string {
	result := make([]vmagentFileSDTarget, 0, len(targets))

	for _, target := range targets {
		labels := vmagentTargetLabels(cluster, target.Node, acceleratorExporterComponentName)
		if target.AcceleratorType != "" {
			labels["accelerator_type"] = target.AcceleratorType
		}

		result = append(result, vmagentFileSDTarget{
			Targets: []string{fmt.Sprintf("%s:%d", target.Node.Spec.IP, target.Port)},
			Labels:  labels,
		})
	}

	return mustMarshalVMAgentFileSDTargets(result)
}

func mustMarshalVMAgentFileSDTargets(targets []vmagentFileSDTarget) string {
	content, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return "[]\n"
	}

	return string(content) + "\n"
}

type acceleratorExporterTargetGroup struct {
	AcceleratorType string
	MetricsPath     string
	JobName         string
	Targets         []acceleratorExporterTarget
}

type acceleratorExporterTarget struct {
	Node            *v1.StaticNode
	AcceleratorType string
	Component       v1.NodeComponentSpec
	Port            int
}

func acceleratorExporterTargetGroups(cluster *v1.StaticNodeCluster, plans []DesiredNodePlan) []acceleratorExporterTargetGroup {
	if acceleratorExporterMode(cluster) == v1.ClusterAcceleratorExporterModeExternal {
		return externalAcceleratorExporterTargetGroups(plans)
	}

	groupsByAcceleratorType := map[string]acceleratorExporterTargetGroup{}

	for _, plan := range plans {
		component, ok := desiredComponentByName(plan, acceleratorExporterComponentName)
		if !ok || len(component.Ports) == 0 || plan.Accelerator == nil || plan.Accelerator.Type == "" {
			continue
		}

		metricsPath := defaultPrometheusHTTPPath
		if component.HealthCheck != nil {
			metricsPath = normalizedMetricsPath(component.HealthCheck.HTTPPath)
		}

		acceleratorType := plan.Accelerator.Type
		group := groupsByAcceleratorType[acceleratorType]

		if group.AcceleratorType == "" {
			group.AcceleratorType = acceleratorType
			group.MetricsPath = metricsPath
			group.JobName = managedAcceleratorExporterJobName(acceleratorType)
		}

		group.Targets = append(group.Targets, acceleratorExporterTarget{
			Node:            plan.Node,
			AcceleratorType: acceleratorType,
			Component:       component,
			Port:            component.Ports[0].Port,
		})
		groupsByAcceleratorType[acceleratorType] = group
	}

	return sortedAcceleratorExporterTargetGroups(groupsByAcceleratorType)
}

func externalAcceleratorExporterTargetGroups(plans []DesiredNodePlan) []acceleratorExporterTargetGroup {
	targets := []acceleratorExporterTarget{}

	for _, plan := range plans {
		if plan.Node == nil || plan.Node.Spec == nil || plan.Accelerator == nil ||
			plan.Accelerator.Type != v1.AcceleratorTypeNVIDIAGPU.String() {
			continue
		}

		targets = append(targets, acceleratorExporterTarget{
			Node:            plan.Node,
			AcceleratorType: plan.Accelerator.Type,
			Port:            externalDCGMExporterPort,
		})
	}

	if len(targets) == 0 {
		return nil
	}

	return []acceleratorExporterTargetGroup{{
		MetricsPath: defaultPrometheusHTTPPath,
		Targets:     targets,
	}}
}

func sortedAcceleratorExporterTargetGroups(
	groupsByAcceleratorType map[string]acceleratorExporterTargetGroup,
) []acceleratorExporterTargetGroup {
	acceleratorTypes := make([]string, 0, len(groupsByAcceleratorType))
	for acceleratorType := range groupsByAcceleratorType {
		acceleratorTypes = append(acceleratorTypes, acceleratorType)
	}

	sort.Strings(acceleratorTypes)

	groups := make([]acceleratorExporterTargetGroup, 0, len(acceleratorTypes))
	for _, acceleratorType := range acceleratorTypes {
		groups = append(groups, groupsByAcceleratorType[acceleratorType])
	}

	return groups
}

func desiredComponentByName(plan DesiredNodePlan, name string) (v1.NodeComponentSpec, bool) {
	if plan.Node == nil || plan.Node.Spec == nil {
		return v1.NodeComponentSpec{}, false
	}

	for _, component := range plan.Node.Spec.Components {
		if component.Name == name {
			return component, true
		}
	}

	return v1.NodeComponentSpec{}, false
}

func acceleratorExporterJobName(metricsPath string, _ int, index int) string {
	if metricsPath == defaultPrometheusHTTPPath {
		return "static-node-accelerator-exporter"
	}

	name := strings.Trim(metricsPath, "/")
	name = strings.ReplaceAll(name, "/", "-")

	if name == "" {
		name = strconv.Itoa(index)
	}

	return "static-node-accelerator-exporter-" + name
}

func acceleratorExporterTargetGroupJobName(group acceleratorExporterTargetGroup, groupCount int, index int) string {
	if group.JobName != "" {
		return group.JobName
	}

	return acceleratorExporterJobName(group.MetricsPath, groupCount, index)
}

func managedAcceleratorExporterJobName(acceleratorType string) string {
	name := sanitizeStaticMetricsName(acceleratorType)
	if name == "" {
		return managedAcceleratorExporterJobPrefix
	}

	return managedAcceleratorExporterJobPrefix + "-" + name
}

func acceleratorExporterTargetGroupFileSDPath(group acceleratorExporterTargetGroup) string {
	if group.JobName != "" {
		return vmagentFileSDDir + "/" + group.JobName + ".json"
	}

	return acceleratorExporterFileSDPath(group.MetricsPath)
}

func acceleratorExporterFileSDPath(metricsPath string) string {
	return vmagentFileSDDir + "/" + strings.TrimPrefix(acceleratorExporterJobName(metricsPath, 2, 0), "static-node-") + ".json"
}

func sanitizeStaticMetricsName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastHyphen := false

	for _, char := range value {
		allowed := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
		if allowed {
			lastHyphen = false

			builder.WriteRune(char)

			continue
		}

		if builder.Len() > 0 && !lastHyphen {
			lastHyphen = true

			builder.WriteRune('-')
		}
	}

	return strings.Trim(builder.String(), "-")
}

func acceleratorExporterMode(cluster *v1.StaticNodeCluster) v1.ClusterAcceleratorExporterMode {
	if cluster == nil || cluster.Spec == nil {
		return v1.ClusterAcceleratorExporterModeManaged
	}

	config := &v1.ClusterConfig{Metrics: cluster.Spec.Metrics}

	return config.AcceleratorExporterMode()
}

func vmagentTargetLabels(
	cluster *v1.StaticNodeCluster,
	node *v1.StaticNode,
	source string,
) map[string]string {
	return map[string]string{
		"source":              source,
		"workspace":           cluster.Metadata.Workspace,
		"neutree_cluster":     cluster.Metadata.Name,
		"static_node_cluster": cluster.Metadata.Name,
		"cluster_type":        "ray",
		"node":                node.Metadata.Name,
		"node_ip":             node.Spec.IP,
		"node_role":           string(node.Spec.Role),
	}
}

func exporterMetricsPath(exporter *v1.AcceleratorExporterProfile) string {
	if exporter == nil {
		return defaultPrometheusHTTPPath
	}

	return normalizedMetricsPath(exporter.MetricsPath)
}

func normalizedMetricsPath(path string) string {
	if path == "" {
		return defaultPrometheusHTTPPath
	}

	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}

	return path
}

func acceleratorExporterDockerRunOptions(
	runtime *v1.AcceleratorExporterRuntimeProfile,
) []string {
	if runtime == nil {
		return nil
	}

	options := []string{}
	if runtime.HostNetwork {
		options = append(options, "--net=host")
	}

	if runtime.HostPID {
		options = append(options, "--pid=host")
	}

	if runtime.Capabilities != nil {
		for _, capability := range runtime.Capabilities.Add {
			capability = strings.TrimSpace(capability)
			if capability == "" {
				continue
			}

			options = append(options, "--cap-add="+capability)
		}
	}

	options = append(options, runtime.DockerRunOptions...)

	return options
}

func acceleratorExporterConfigVolumes(
	configFiles []v1.AcceleratorExporterConfigFile,
) []v1.NodeComponentVolume {
	volumes := make([]v1.NodeComponentVolume, 0, len(configFiles))

	for i, configFile := range configFiles {
		if configFile.Path == "" {
			continue
		}

		volumes = append(volumes, v1.NodeComponentVolume{
			Name:      "accelerator-exporter-config-" + strconv.Itoa(i),
			HostPath:  configFile.Path,
			MountPath: configFile.Path,
			ReadOnly:  true,
		})
	}

	return volumes
}

func acceleratorExporterComponentConfigFiles(
	configFiles []v1.AcceleratorExporterConfigFile,
) []v1.NodeComponentConfigFile {
	componentConfigFiles := make([]v1.NodeComponentConfigFile, 0, len(configFiles))

	for _, configFile := range configFiles {
		componentConfigFiles = append(componentConfigFiles, v1.NodeComponentConfigFile{
			Path:                configFile.Path,
			Content:             configFile.Content,
			Mode:                configFile.Mode,
			Owner:               configFile.Owner,
			Group:               configFile.Group,
			Sudo:                configFile.Sudo,
			Atomic:              configFile.Atomic,
			CreateParent:        configFile.CreateParent,
			SkipRestartOnChange: configFile.SkipRestartOnChange,
		})
	}

	return componentConfigFiles
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
