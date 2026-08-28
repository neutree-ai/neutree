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
	"github.com/neutree-ai/neutree/internal/component"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	nodeExporterComponentName        = "node-exporter"
	nodeAgentComponentName           = "neutree-node-agent"
	vmagentComponentName             = "vmagent"
	acceleratorExporterComponentName = "accelerator-exporter"
	defaultVMAgentPort               = 8429
	defaultNodeExporterPort          = 19100
	defaultNodeAgentPort             = 19101
	defaultPrometheusHTTPPath        = "/metrics"
	defaultHealthHTTPPath            = "/health"
	vmagentConfigPath                = "/etc/neutree/vmagent/config.yaml"
	vmagentFileSDDir                 = "/etc/neutree/vmagent/file_sd"
	vmagentNodeExporterFileSDPath    = vmagentFileSDDir + "/node-exporter.json"
	vmagentNodeAgentFileSDPath       = vmagentFileSDDir + "/node-agent.json"
	vmagentRayFileSDPath             = vmagentFileSDDir + "/ray.json"
	acceleratorExporterJobPrefix     = "accelerator-exporter"
	defaultNodeExporterImage         = "quay.io/prometheus/node-exporter:" + component.NodeExporter
	defaultVMAgentImage              = "victoriametrics/vmagent:" + component.VictoriaMetrics
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
) ([]v1.NodeComponentSpec, error) {
	components := []v1.NodeComponentSpec{buildNodeExporterComponent(cluster)}

	if exporter := acceleratorExporterProfile(profile); exporter != nil &&
		acceleratorExporterMode(cluster) != v1.ClusterAcceleratorExporterModeExternal {
		components = append(components, buildAcceleratorExporterComponent(cluster, exporter))
	}

	nodeAgent, err := buildNodeAgentComponent(cluster, node, profile)
	if err != nil {
		return nil, err
	}

	components = append(components, nodeAgent)

	if role == v1.StaticNodeRoleHead && util.IsHTTPOrHTTPSURL(metricsRemoteWriteURL) {
		components = append(components, buildVMAgentComponent(cluster, metricsRemoteWriteURL))
	}

	return components, nil
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
		Volumes: []v1.NodeComponentVolume{{
			Name:      "host-root",
			HostPath:  "/",
			MountPath: "/host",
			ReadOnly:  true,
		}},
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

func acceleratorExporterMode(cluster *v1.StaticNodeCluster) v1.ClusterAcceleratorExporterMode {
	if cluster == nil || cluster.Spec == nil {
		return v1.ClusterAcceleratorExporterModeManaged
	}

	return (&v1.ClusterConfig{Metrics: cluster.Spec.Metrics}).AcceleratorExporterMode()
}

func buildAcceleratorExporterComponent(
	cluster *v1.StaticNodeCluster,
	exporter *v1.AcceleratorExporterProfile,
) v1.NodeComponentSpec {
	configVolumes, configVolumeMounts := acceleratorExporterConfigVolumes(exporter.ConfigFiles)
	var runtimeVolumes []v1.ComponentVolume
	var runtimeVolumeMounts []v1.ComponentVolumeMount

	if exporter.Runtime != nil {
		runtimeVolumes = append([]v1.ComponentVolume{}, exporter.Runtime.Volumes...)
		runtimeVolumeMounts = append([]v1.ComponentVolumeMount{}, exporter.Runtime.VolumeMounts...)
	}

	volumes := append(append([]v1.ComponentVolume{}, configVolumes...), runtimeVolumes...)
	volumeMounts := append(append([]v1.ComponentVolumeMount{}, configVolumeMounts...), runtimeVolumeMounts...)

	return v1.NodeComponentSpec{
		Name:             acceleratorExporterComponentName,
		Image:            staticComponentImage(cluster, exporter.Image),
		Command:          copyMetricsStringSlice(exporter.Command),
		Args:             copyMetricsStringSlice(exporter.Args),
		Env:              copyMetricsStringMap(exporter.Env),
		Volumes:          staticNodeComponentVolumes(volumes, volumeMounts),
		ConfigFiles:      acceleratorExporterComponentConfigFiles(exporter.ConfigFiles),
		DockerRunOptions: acceleratorExporterDockerRunOptions(exporter.Runtime),
		Ports: []v1.NodeComponentPort{
			{Name: "metrics", Port: exporter.Port, Protocol: "TCP"},
		},
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPPath: exporterMetricsPath(exporter.MetricsPath),
			Port:     exporter.Port,
		},
	}
}

func buildNodeAgentComponent(
	cluster *v1.StaticNodeCluster,
	node *v1.StaticNode,
	profile *v1.AcceleratorProfile,
) (v1.NodeComponentSpec, error) {
	volumes := nodeAgentComponentVolumes(profile)
	args := nodeAgentComponentArgs(cluster, profile)
	image, err := selectedNodeAgentImage(cluster.Spec.Version, profile)

	if err != nil {
		return v1.NodeComponentSpec{}, err
	}

	if node != nil && node.Metadata != nil {
		args = append(args, "--node="+node.Metadata.Name)
	}

	if node != nil && node.Spec != nil {
		args = append(args, "--node-ip="+node.Spec.IP)
	}

	return v1.NodeComponentSpec{
		Name:             nodeAgentComponentName,
		Image:            staticComponentImage(cluster, image),
		Args:             args,
		Env:              nodeAgentComponentEnv(cluster.Spec.Version, profile),
		DockerRunOptions: nodeAgentDockerRunOptions(profile),
		Volumes:          volumes,
		Ports: []v1.NodeComponentPort{
			{Name: "http", Port: defaultNodeAgentPort, Protocol: "TCP"},
		},
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPPath: defaultHealthHTTPPath,
			Port:     defaultNodeAgentPort,
		},
	}, nil
}

func nodeAgentComponentArgs(cluster *v1.StaticNodeCluster, profile *v1.AcceleratorProfile) []string {
	args := []string{
		fmt.Sprintf("--listen-address=:%d", defaultNodeAgentPort),
	}

	supportsExplicitProfileContract, err := component.SupportsNodeAgentProfileContract(cluster.Spec.Version)
	if err != nil || !supportsExplicitProfileContract || profile == nil || profile.AcceleratorType == "" {
		args = append(args,
			"--cluster-type=ray",
			"--metrics-mode="+string(acceleratorExporterMode(cluster)),
			fmt.Sprintf("--ray-dashboard-url=http://%s:%d", staticNodeClusterHeadIP(cluster), v1.RayDashboardPort),
			"--procfs-root=/host/proc",
			"--cgroupfs-root=/host/sys/fs/cgroup",
		)

		return args
	}

	args = append(args,
		"--cluster-type="+v1.SSHClusterType,
		fmt.Sprintf("--ray-dashboard-url=http://%s:%d", staticNodeClusterHeadIP(cluster), v1.RayDashboardPort),
		"--procfs-root=/host/proc",
		"--cgroupfs-root=/host/sys/fs/cgroup",
	)

	return append(args, nodeAgentProfileTargetArgs(profile)...)
}

func nodeAgentProfileTargetArgs(profile *v1.AcceleratorProfile) []string {
	if profile == nil || profile.AcceleratorType == "" {
		return nil
	}

	args := []string{"--accelerator-type=" + profile.AcceleratorType}
	exporter := acceleratorExporterProfile(profile)

	if exporter == nil {
		return args
	}

	return append(args,
		fmt.Sprintf("--accelerator-exporter-port=%d", exporter.Port),
		"--accelerator-exporter-metrics-path="+exporterMetricsPath(exporter.MetricsPath),
	)
}

func selectedNodeAgentImage(clusterVersion string, profile *v1.AcceleratorProfile) (string, error) {
	var nodeAgentProfile *v1.NodeAgentRuntimeProfile
	if profile != nil {
		nodeAgentProfile = profile.NodeAgentRuntime
	}

	selection, err := component.SelectNodeAgent(clusterVersion, nodeAgentProfile)
	if err != nil {
		return "", err
	}

	return selection.Image, nil
}

func defaultNodeAgentImage(cluster *v1.StaticNodeCluster) string {
	image, err := selectedNodeAgentImage(cluster.Spec.Version, nil)
	if err != nil || image == "" {
		return "neutree/neutree-node-agent:" + component.LegacyNeutreeNodeAgent
	}

	return image
}

func nodeAgentDockerRunOptions(profile *v1.AcceleratorProfile) []string {
	options := []string{"--net=host", "--pid=host", "--cgroupns=host"}

	runtime := nodeAgentRuntime(profile)
	if runtime == nil {
		return options
	}

	if runtime.Privileged {
		options = append(options, "--privileged")
	}

	if runtime.Capabilities != nil {
		for _, capability := range runtime.Capabilities.Add {
			options = append(options, "--cap-add="+string(capability))
		}
	}

	if runtime.Runtime != "" {
		options = append(options, "--runtime="+runtime.Runtime)
	}

	return append(options, runtime.DockerRunOptions...)
}

func nodeAgentRuntime(profile *v1.AcceleratorProfile) *v1.NodeAgentRuntimeProfile {
	if profile == nil {
		return nil
	}

	return profile.NodeAgentRuntime
}

func nodeAgentComponentVolumes(profile *v1.AcceleratorProfile) []v1.NodeComponentVolume {
	volumes := []v1.ComponentVolume{
		{Name: "host-proc", HostPath: &v1.ComponentHostPathVolumeSource{Path: "/proc", Type: v1.ComponentHostPathTypeDirectory}},
		{Name: "host-cgroup", HostPath: &v1.ComponentHostPathVolumeSource{Path: "/sys/fs/cgroup", Type: v1.ComponentHostPathTypeDirectory}},
	}
	mounts := []v1.ComponentVolumeMount{
		{Name: "host-proc", MountPath: "/host/proc", ReadOnly: staticComponentReadOnly(true)},
		{Name: "host-cgroup", MountPath: "/host/sys/fs/cgroup", ReadOnly: staticComponentReadOnly(true)},
	}

	runtime := nodeAgentRuntime(profile)
	if runtime == nil {
		return staticNodeComponentVolumes(volumes, mounts)
	}

	volumes = append(volumes, runtime.Volumes...)
	mounts = append(mounts, runtime.VolumeMounts...)

	return staticNodeComponentVolumes(volumes, mounts)
}

// staticNodeComponentVolumes lowers the backend-neutral Profile volume model
// to StaticNode's persisted inline mount layout. The conversion preserves the
// static runtime's read-only default and supports multiple mounts per source.
func staticNodeComponentVolumes(
	volumes []v1.ComponentVolume,
	mounts []v1.ComponentVolumeMount,
) []v1.NodeComponentVolume {
	volumeByName := make(map[string]v1.ComponentVolume, len(volumes))
	for _, volume := range volumes {
		volumeByName[volume.Name] = volume
	}

	result := make([]v1.NodeComponentVolume, 0, len(mounts))
	for _, mount := range mounts {
		volume, exists := volumeByName[mount.Name]
		if !exists || volume.HostPath == nil {
			continue
		}

		readOnly := true
		if mount.ReadOnly != nil {
			readOnly = *mount.ReadOnly
		}

		result = append(result, v1.NodeComponentVolume{
			Name:      mount.Name,
			HostPath:  volume.HostPath.Path,
			MountPath: mount.MountPath,
			ReadOnly:  readOnly,
		})
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

func nodeAgentComponentEnv(_ string, profile *v1.AcceleratorProfile) map[string]string {
	var runtimeEnv map[string]string
	if runtime := nodeAgentRuntime(profile); runtime != nil {
		runtimeEnv = runtime.Env
	}

	// Static NodeAgent has no Kubernetes Pod discovery path. The
	// VirtualizationMetricsTarget is therefore intentionally skipped here;
	// Kubernetes metrics rendering owns that monitor configuration.
	return copyMetricsStringMap(runtimeEnv)
}

func staticComponentReadOnly(value bool) *bool {
	return &value
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

func copyMetricsStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	return append([]string{}, values...)
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
		Volumes: []v1.NodeComponentVolume{{
			Name:      "vmagent-config-dir",
			HostPath:  "/etc/neutree/vmagent",
			MountPath: "/etc/neutree/vmagent",
			ReadOnly:  true,
		}},
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
			Content:      renderVMAgentConfig(plans),
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

func renderVMAgentConfig(plans []DesiredNodePlan) string {
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

	groups := acceleratorExporterTargetGroups(plans)
	for _, group := range groups {
		scrapeConfig := staticVMAgentScrapeConfig{
			JobName:    group.JobName,
			FileSDPath: strconv.Quote(vmagentFileSDDir + "/" + group.JobName + ".json"),
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

	for _, group := range acceleratorExporterTargetGroups(plans) {
		configFiles = append(configFiles, vmagentFileSDConfigFile(
			vmagentFileSDDir+"/"+group.JobName+".json",
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
	Port            int
}

func acceleratorExporterTargetGroups(plans []DesiredNodePlan) []acceleratorExporterTargetGroup {
	groupsByAcceleratorType := map[string]acceleratorExporterTargetGroup{}

	for _, plan := range plans {
		exporter := acceleratorExporterProfile(plan.AcceleratorProfile)
		if exporter == nil || plan.Node == nil || plan.Node.Spec == nil ||
			plan.Accelerator == nil || plan.Accelerator.Type == "" {
			continue
		}

		acceleratorType := plan.Accelerator.Type
		group := groupsByAcceleratorType[acceleratorType]

		if group.AcceleratorType == "" {
			group.AcceleratorType = acceleratorType
			group.MetricsPath = exporterMetricsPath(exporter.MetricsPath)
			group.JobName = acceleratorExporterMetricsJobName(acceleratorType)
		}

		group.Targets = append(group.Targets, acceleratorExporterTarget{
			Node:            plan.Node,
			AcceleratorType: acceleratorType,
			Port:            exporter.Port,
		})
		groupsByAcceleratorType[acceleratorType] = group
	}

	return sortedAcceleratorExporterTargetGroups(groupsByAcceleratorType)
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

func acceleratorExporterMetricsJobName(acceleratorType string) string {
	name := sanitizeStaticMetricsName(acceleratorType)
	if name == "" {
		return acceleratorExporterJobPrefix
	}

	return acceleratorExporterJobPrefix + "-" + name
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
		"cluster_type":        v1.SSHClusterType,
		"node":                node.Metadata.Name,
		"node_ip":             node.Spec.IP,
		"node_role":           string(node.Spec.Role),
	}
}

func exporterMetricsPath(metricsPath string) string {
	metricsPath = strings.TrimSpace(metricsPath)
	if metricsPath == "" {
		return defaultPrometheusHTTPPath
	}

	if !strings.HasPrefix(metricsPath, "/") {
		return "/" + metricsPath
	}

	return metricsPath
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

	if runtime.Privileged {
		options = append(options, "--privileged")
	}

	if runtime.Capabilities != nil {
		for _, capability := range runtime.Capabilities.Add {
			options = append(options, "--cap-add="+string(capability))
		}
	}

	// The exporter/node-agent previously received only --gpus all without an
	// explicit --runtime=. That left GPU injection to Docker's default
	// device-request hook rather than the nvidia runtime shim. Selecting the
	// runtime handler explicitly keeps device access in the runtime's OCI
	// rewrite, which modern NVIDIA toolkits emit into linux.resources.devices
	// (rebuildable by systemd), instead of a one-time prestart hook.
	if runtime.Runtime != "" {
		options = append(options, "--runtime="+runtime.Runtime)
	}

	options = append(options, runtime.DockerRunOptions...)

	return options
}

func acceleratorExporterConfigVolumes(
	configFiles []v1.AcceleratorExporterConfigFile,
) ([]v1.ComponentVolume, []v1.ComponentVolumeMount) {
	if len(configFiles) == 0 {
		return nil, nil
	}

	volumes := make([]v1.ComponentVolume, 0, len(configFiles))
	mounts := make([]v1.ComponentVolumeMount, 0, len(configFiles))

	for i, configFile := range configFiles {
		name := "accelerator-exporter-config-" + strconv.Itoa(i)
		volumes = append(volumes, v1.ComponentVolume{
			Name:     name,
			HostPath: &v1.ComponentHostPathVolumeSource{Path: configFile.Path, Type: v1.ComponentHostPathTypeFile},
		})

		mounts = append(mounts, v1.ComponentVolumeMount{
			Name:      name,
			MountPath: configFile.Path,
			ReadOnly:  staticComponentReadOnly(true),
		})
	}

	return volumes, mounts
}

func acceleratorExporterComponentConfigFiles(
	configFiles []v1.AcceleratorExporterConfigFile,
) []v1.NodeComponentConfigFile {
	if len(configFiles) == 0 {
		return nil
	}

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
