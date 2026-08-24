package staticcluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
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
	components := []v1.NodeComponentSpec{buildNodeExporterComponent(cluster)}

	if acceleratorExporterMode(cluster) == v1.ClusterAcceleratorExporterModeManaged {
		if exporter := acceleratorExporterProfile(profile); validAcceleratorExporterProfile(exporter) {
			components = append(components, buildAcceleratorExporterComponent(cluster, exporter))
		}
	}

	components = append(components, buildNodeAgentComponent(cluster, node, profile))

	if role == v1.StaticNodeRoleHead && util.IsHTTPOrHTTPSURL(metricsRemoteWriteURL) {
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
	healthCheck := &v1.NodeComponentHealthCheck{
		HTTPPath: exporterMetricsPath(exporter),
		Port:     exporter.Port,
	}
	if exporter.Readiness != nil {
		if exporter.Readiness.HTTPPath != "" {
			healthCheck.HTTPPath = normalizedMetricsPath(exporter.Readiness.HTTPPath)
		}
		healthCheck.InitialDelaySec = exporter.Readiness.InitialDelaySeconds
		healthCheck.IntervalSec = exporter.Readiness.PeriodSeconds
		healthCheck.TimeoutSec = exporter.Readiness.TimeoutSeconds
	}

	volumes := acceleratorExporterConfigVolumes(exporter.ConfigFiles)
	volumes = append(volumes, runtimeAccessVolumes(exporter.Runtime)...)

	return v1.NodeComponentSpec{
		Name:             acceleratorExporterComponentName,
		Image:            staticComponentImage(cluster, exporter.Image),
		Command:          append([]string{}, exporter.Command...),
		Args:             append([]string{}, exporter.Args...),
		Env:              copyMetricsStringMap(exporter.Env),
		Volumes:          volumes,
		ConfigFiles:      acceleratorExporterComponentConfigFiles(exporter.ConfigFiles),
		DockerRunOptions: acceleratorExporterDockerRunOptions(exporter.Runtime),
		Ports: []v1.NodeComponentPort{
			{Name: "metrics", Port: exporter.Port, Protocol: "TCP"},
		},
		HealthCheck: healthCheck,
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
		"--procfs-root=/host/proc",
		"--cgroupfs-root=/host/sys/fs/cgroup",
	}

	if node != nil && node.Metadata != nil {
		args = append(args, "--node="+node.Metadata.Name)
	}

	if node != nil && node.Spec != nil {
		args = append(args, "--node-ip="+node.Spec.IP)
	}

	args = append(args, nodeAgentAdapterArgs(cluster, profile)...)

	volumes := []v1.NodeComponentVolume{
		{
			Name:      "host-proc",
			HostPath:  "/proc",
			MountPath: "/host/proc",
			ReadOnly:  true,
		},
		{
			Name:      "host-cgroup",
			HostPath:  "/sys/fs/cgroup",
			MountPath: "/host/sys/fs/cgroup",
			ReadOnly:  true,
		},
	}
	if usesNodeAgentAdapterProfile(profile) {
		volumes = append(volumes, runtimeAccessVolumes(acceleratorRuntime(profile))...)
	}

	return v1.NodeComponentSpec{
		Name:             nodeAgentComponentName,
		Image:            staticComponentImage(cluster, defaultNodeAgentImage(cluster)),
		Args:             args,
		Env:              nodeAgentEnv(profile),
		DockerRunOptions: nodeAgentDockerRunOptions(profile),
		Volumes:          volumes,
		Ports: []v1.NodeComponentPort{
			{Name: "http", Port: defaultNodeAgentPort, Protocol: "TCP"},
		},
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPPath: defaultHealthHTTPPath,
			Port:     defaultNodeAgentPort,
		},
	}
}

func nodeAgentAdapterArgs(
	cluster *v1.StaticNodeCluster,
	profile *v1.AcceleratorProfile,
) []string {
	if acceleratorExporterMode(cluster) != v1.ClusterAcceleratorExporterModeManaged ||
		!usesNodeAgentAdapterProfile(profile) {
		return nil
	}

	exporter := acceleratorExporterProfile(profile)
	if profile == nil || strings.TrimSpace(profile.AcceleratorType) == "" ||
		!validAcceleratorExporterProfile(exporter) {
		return nil
	}

	return []string{
		"--accelerator-type=" + profile.AcceleratorType,
		fmt.Sprintf("--accelerator-exporter-port=%d", exporter.Port),
		"--accelerator-exporter-metrics-path=" + exporterMetricsPath(exporter),
	}
}

func usesNodeAgentAdapterProfile(profile *v1.AcceleratorProfile) bool {
	exporter := acceleratorExporterProfile(profile)
	if exporter == nil {
		return false
	}

	return strings.EqualFold(
		strings.TrimSpace(exporter.Env[v1.NodeAgentAdapterProfileKey]),
		"true",
	)
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
		if key == v1.NodeAgentAdapterProfileKey {
			continue
		}

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

func acceleratorRuntime(profile *v1.AcceleratorProfile) *v1.AcceleratorExporterRuntimeProfile {
	exporter := acceleratorExporterProfile(profile)
	if exporter == nil {
		return nil
	}

	return exporter.Runtime
}

func validateStaticRuntimeAccess(profile *v1.AcceleratorProfile) error {
	runtime := acceleratorRuntime(profile)
	if runtime == nil {
		return nil
	}

	if err := validateStaticRuntimeVolumes(runtime); err != nil {
		return err
	}

	return validateStaticRuntimeDockerOptions(runtime)
}

func validateStaticRuntimeVolumes(runtime *v1.AcceleratorExporterRuntimeProfile) error {
	if len(runtime.Volumes) == 0 && len(runtime.VolumeMounts) == 0 {
		return nil
	}

	volumes := make(map[string]struct{}, len(runtime.Volumes))
	for _, volume := range runtime.Volumes {
		name := strings.TrimSpace(volume.Name)
		if name == "" {
			return fmt.Errorf("runtime volume name is required")
		}
		if _, exists := volumes[name]; exists {
			return fmt.Errorf("duplicate runtime volume name %q", name)
		}
		if volume.HostPath == nil {
			return fmt.Errorf("runtime volume %q host path is required", name)
		}
		if err := validateStaticRuntimePath(volume.HostPath.Path, "runtime volume host path", true); err != nil {
			return fmt.Errorf("runtime volume %q: %w", name, err)
		}
		switch volume.HostPath.Type {
		case v1.ComponentHostPathTypeDirectory, v1.ComponentHostPathTypeSocket:
		default:
			return fmt.Errorf("runtime volume %q has unsupported host path type %q", name, volume.HostPath.Type)
		}

		volumes[name] = struct{}{}
	}

	mounts := make(map[string]struct{}, len(runtime.VolumeMounts))
	mountPaths := make(map[string]struct{}, len(runtime.VolumeMounts))
	for _, mount := range runtime.VolumeMounts {
		name := strings.TrimSpace(mount.Name)
		if name == "" {
			return fmt.Errorf("runtime volume mount name is required")
		}
		if _, exists := volumes[name]; !exists {
			return fmt.Errorf("runtime volume mount %q does not reference a declared runtime volume", name)
		}
		if _, exists := mounts[name]; exists {
			return fmt.Errorf("duplicate runtime volume mount name %q", name)
		}
		if err := validateStaticRuntimePath(mount.MountPath, "runtime volume mount path", false); err != nil {
			return fmt.Errorf("runtime volume mount %q: %w", name, err)
		}
		if _, exists := mountPaths[mount.MountPath]; exists {
			return fmt.Errorf("duplicate runtime volume mount path %q", mount.MountPath)
		}

		mounts[name] = struct{}{}
		mountPaths[mount.MountPath] = struct{}{}
	}

	for name := range volumes {
		if _, exists := mounts[name]; !exists {
			return fmt.Errorf("runtime volume %q must have one mount", name)
		}
	}

	return nil
}

func validateStaticRuntimePath(value string, field string, allowRoot bool) error {
	if value == "" || strings.TrimSpace(value) != value || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("%s must be a non-empty absolute clean path", field)
	}
	if !allowRoot && value == "/" {
		return fmt.Errorf("%s must not be the container root", field)
	}

	return nil
}

func validateStaticRuntimeDockerOptions(runtime *v1.AcceleratorExporterRuntimeProfile) error {
	options := append([]string{}, runtime.DockerRunOptions...)
	if runtime.HostNetwork {
		options = append(options, "--net=host")
	}
	if runtime.HostPID {
		options = append(options, "--pid=host")
	}
	if runtime.Privileged {
		options = append(options, "--privileged")
	}
	if runtime.Runtime != "" {
		options = append(options, "--runtime="+runtime.Runtime)
	}
	if runtime.Capabilities != nil {
		for _, capability := range runtime.Capabilities.Add {
			options = append(options, "--cap-add="+strings.TrimSpace(capability))
		}
	}

	valuesByKey := map[string]string{}
	for _, raw := range options {
		option := strings.TrimSpace(raw)
		if option == "" {
			continue
		}
		if isUnstructuredRuntimeAccessOption(option) {
			return fmt.Errorf("runtime Docker option %q must use structured runtime volumes", option)
		}

		key, value := splitDockerRunOption(option)
		if key == "" || isRepeatableDockerRunOption(key) {
			continue
		}
		if previous, exists := valuesByKey[key]; exists && previous != value {
			return fmt.Errorf("conflicting Docker options for %q", key)
		}
		valuesByKey[key] = value
	}

	return nil
}

func isUnstructuredRuntimeAccessOption(option string) bool {
	fields := strings.Fields(option)
	if len(fields) == 0 {
		return false
	}

	name := fields[0]
	return name == "--device" || name == "--volume" || name == "--mount" || name == "-v" ||
		strings.HasPrefix(name, "--device=") || strings.HasPrefix(name, "--volume=") ||
		strings.HasPrefix(name, "--mount=") || strings.HasPrefix(name, "-v")
}

func splitDockerRunOption(option string) (string, string) {
	fields := strings.Fields(option)
	if len(fields) == 0 {
		return "", ""
	}

	if key, value, ok := strings.Cut(fields[0], "="); ok {
		return key, strings.TrimSpace(value)
	}

	return fields[0], strings.TrimSpace(strings.TrimPrefix(option, fields[0]))
}

func isRepeatableDockerRunOption(key string) bool {
	switch key {
	case "--add-host", "--cap-add", "--cap-drop", "--device", "--env", "--label", "--mount", "--security-opt", "--volume", "-e", "-v":
		return true
	default:
		return false
	}
}

func runtimeAccessVolumes(runtime *v1.AcceleratorExporterRuntimeProfile) []v1.NodeComponentVolume {
	if runtime == nil || len(runtime.Volumes) == 0 {
		return nil
	}

	mounts := make(map[string]v1.ComponentVolumeMount, len(runtime.VolumeMounts))
	for _, mount := range runtime.VolumeMounts {
		mounts[mount.Name] = mount
	}

	volumes := make([]v1.NodeComponentVolume, 0, len(runtime.Volumes))
	for _, volume := range runtime.Volumes {
		mount, ok := mounts[volume.Name]
		if !ok || volume.HostPath == nil {
			continue
		}
		readOnly := true
		if mount.ReadOnly != nil {
			readOnly = *mount.ReadOnly
		}
		volumes = append(volumes, v1.NodeComponentVolume{
			Name:      volume.Name,
			HostPath:  volume.HostPath.Path,
			MountPath: mount.MountPath,
			ReadOnly:  readOnly,
		})
	}

	return volumes
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
		if key == v1.NodeAgentAdapterProfileKey {
			continue
		}

		copied[key] = value
	}

	if len(copied) == 0 {
		return nil
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

		metricsPath := normalizedMetricsPath(plan.AcceleratorExporterMetricsPath)

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

	if runtime.Privileged {
		options = append(options, "--privileged")
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

	if runtime.Runtime != "" {
		options = append(options, "--runtime="+runtime.Runtime)
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
