package metrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/componentversion"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	nodeExporterDaemonSetName   = "neutree-node-exporter"
	nodeExporterPort            = 19100
	neutreeNodeAgentMetricsName = "neutree-node-agent"
	neutreeNodeAgentImageName   = "neutree/neutree-node-agent"
	neutreeNodeAgentMetricsPort = 19101
	externalDCGMExporterPort    = 9400

	defaultNodeExporterImage     = "quay.io/prometheus/node-exporter:" + componentversion.NodeExporter
	defaultMetricsPath           = "/metrics"
	acceleratorExporterJobPrefix = "accelerator-exporter"
)

type metricsAcceleratorExporter struct {
	Name            string
	AcceleratorType string
	ExporterName    string
	Image           string
	Args            []string
	Env             []corev1.EnvVar
	Port            int
	MetricsPath     string

	Capabilities []corev1.Capability

	NodeSelector   map[string]string
	ConfigFileData map[string]string
	ConfigChecksum string
	VolumeMounts   []corev1.VolumeMount
	Volumes        []corev1.Volume
}

func (e metricsAcceleratorExporter) AppLabel() string {
	return e.Name
}

func (e metricsAcceleratorExporter) ContainerName() string {
	if name := sanitizeKubernetesNameValue(e.ExporterName); name != "" {
		return name
	}

	return e.Name
}

func (e metricsAcceleratorExporter) JobName() string {
	return acceleratorExporterJobName(e.AcceleratorType)
}

func (e metricsAcceleratorExporter) ConfigMapName() string {
	return e.Name + "-config"
}

func (e metricsAcceleratorExporter) HasCustomMetricsPath() bool {
	return strings.TrimSpace(e.MetricsPath) != "" && e.MetricsPath != defaultMetricsPath
}

func (m *MetricsComponent) planAcceleratorExporters(ctx context.Context) ([]metricsAcceleratorExporter, error) {
	supported, err := m.supportsManagedMetricsExporters()
	if err != nil {
		return nil, err
	}

	if !supported {
		return nil, nil
	}

	if m.acceleratorMgr == nil {
		return nil, nil
	}

	acceleratorTypes := append([]string{}, m.acceleratorMgr.SupportPlugins()...)
	sort.Strings(acceleratorTypes)

	candidates := make([]metricsAcceleratorExporter, 0, len(acceleratorTypes))

	for _, acceleratorType := range acceleratorTypes {
		exporter, ok := m.buildAcceleratorExporter(ctx, acceleratorType)
		if ok {
			candidates = append(candidates, exporter)
		}
	}

	return m.selectClusterAcceleratorExporter(ctx, candidates)
}

func (m *MetricsComponent) acceleratorExporterMode() v1.ClusterAcceleratorExporterMode {
	if m.cluster == nil || m.cluster.Spec == nil {
		return v1.ClusterAcceleratorExporterModeManaged
	}

	return m.cluster.Spec.Config.AcceleratorExporterMode()
}

func (m *MetricsComponent) buildAcceleratorExporter(
	ctx context.Context,
	acceleratorType string,
) (metricsAcceleratorExporter, bool) {
	profile, err := m.acceleratorMgr.GetAcceleratorProfile(ctx, acceleratorType)
	if err != nil {
		klog.V(4).Infof("skip accelerator metrics exporter for %s: failed to get accelerator profile: %v", acceleratorType, err)
		return metricsAcceleratorExporter{}, false
	}

	if profile == nil || profile.MetricsExporter == nil {
		return metricsAcceleratorExporter{}, false
	}

	exporterProfile := profile.MetricsExporter
	if strings.TrimSpace(exporterProfile.Image) == "" ||
		exporterProfile.Port <= 0 ||
		!validAcceleratorExporterName(exporterProfile.Name) {
		return metricsAcceleratorExporter{}, false
	}

	name := acceleratorExporterName(acceleratorType, exporterProfile.Name)
	configFileData, volumeMounts, volumes, configChecksum := buildExporterConfigVolumes(name, exporterProfile.ConfigFiles)
	runtime := exporterProfile.Runtime

	// Kubernetes managed exporters intentionally do not project host network/PID
	// flags from the runtime profile; those flags are for static-node runtimes.
	exporter := metricsAcceleratorExporter{
		Name:            name,
		AcceleratorType: acceleratorType,
		ExporterName:    exporterProfile.Name,
		Image:           util.RewriteImageRef(m.imagePrefix, exporterProfile.Image),
		Args:            append([]string{}, exporterProfile.Args...),
		Env:             buildExporterEnv(exporterProfile.Env),
		Port:            exporterProfile.Port,
		MetricsPath:     exporterMetricsPath(exporterProfile.MetricsPath),
		Capabilities:    exporterRuntimeCapabilities(runtime),
		NodeSelector:    exporterRuntimeNodeSelector(runtime),
		ConfigFileData:  configFileData,
		ConfigChecksum:  configChecksum,
		VolumeMounts:    volumeMounts,
		Volumes:         volumes,
	}

	return exporter, true
}

func acceleratorExporterName(acceleratorType string, exporterName string) string {
	return sanitizeKubernetesNameValue(acceleratorType + "-" + exporterName)
}

func acceleratorExporterJobName(acceleratorType string) string {
	name := sanitizeKubernetesNameValue(acceleratorType)
	if name == "" {
		return acceleratorExporterJobPrefix
	}

	return acceleratorExporterJobPrefix + "-" + name
}

func validAcceleratorExporterName(exporterName string) bool {
	return sanitizeKubernetesNameValue(exporterName) != ""
}

func (m *MetricsComponent) selectClusterAcceleratorExporter(
	ctx context.Context,
	candidates []metricsAcceleratorExporter,
) ([]metricsAcceleratorExporter, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	nodes, err := m.clusterNodes(ctx)
	if err != nil {
		return nil, err
	}

	matchedExporters := make([]metricsAcceleratorExporter, 0, 1)

	for _, exporter := range candidates {
		if acceleratorExporterMatchesAnyNode(exporter, nodes) {
			matchedExporters = append(matchedExporters, exporter)
		}
	}

	return matchedExporters, nil
}

func (m *MetricsComponent) clusterNodes(ctx context.Context) ([]corev1.Node, error) {
	if m.ctrlClient == nil {
		return nil, fmt.Errorf("kubernetes client is required to match accelerator exporter node selectors")
	}

	nodeList := &corev1.NodeList{}
	if err := m.ctrlClient.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("list cluster nodes: %w", err)
	}

	return nodeList.Items, nil
}

func acceleratorExporterMatchesAnyNode(exporter metricsAcceleratorExporter, nodes []corev1.Node) bool {
	if len(exporter.NodeSelector) == 0 {
		return false
	}

	for _, node := range nodes {
		if nodeMatchesSelector(node, exporter.NodeSelector) {
			return true
		}
	}

	return false
}

func nodeMatchesSelector(node corev1.Node, selector map[string]string) bool {
	for key, value := range selector {
		if node.Labels[key] != value {
			return false
		}
	}

	return true
}

func exporterMetricsPath(metricsPath string) string {
	metricsPath = strings.TrimSpace(metricsPath)
	if metricsPath == "" {
		return defaultMetricsPath
	}

	if !strings.HasPrefix(metricsPath, "/") {
		return "/" + metricsPath
	}

	return metricsPath
}

func buildExporterEnv(env map[string]string) []corev1.EnvVar {
	if len(env) == 0 {
		return nil
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	envVars := make([]corev1.EnvVar, 0, len(keys))

	for _, key := range keys {
		envVars = append(envVars, corev1.EnvVar{Name: key, Value: env[key]})
	}

	return envVars
}

func nodeAgentEnvFromAcceleratorExporters(exporters []metricsAcceleratorExporter) []corev1.EnvVar {
	allowed := map[string]struct{}{
		"NVIDIA_VISIBLE_DEVICES":     {},
		"NVIDIA_DRIVER_CAPABILITIES": {},
	}
	env := map[string]string{}

	for _, exporter := range exporters {
		for _, item := range exporter.Env {
			if _, ok := allowed[item.Name]; !ok {
				continue
			}

			env[item.Name] = item.Value
		}
	}

	return buildExporterEnv(env)
}

func exporterRuntimeNodeSelector(runtime *v1.AcceleratorExporterRuntimeProfile) map[string]string {
	if runtime == nil {
		return nil
	}

	return copyStringMap(runtime.NodeSelector)
}

func exporterRuntimeCapabilities(
	runtime *v1.AcceleratorExporterRuntimeProfile,
) []corev1.Capability {
	if runtime == nil || runtime.Capabilities == nil {
		return nil
	}

	capabilities := make([]corev1.Capability, 0, len(runtime.Capabilities.Add))

	for _, capability := range runtime.Capabilities.Add {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}

		capabilities = append(capabilities, corev1.Capability(capability))
	}

	return capabilities
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}

	return copied
}

func buildExporterConfigVolumes(
	exporterName string,
	configFiles []v1.AcceleratorExporterConfigFile,
) (map[string]string, []corev1.VolumeMount, []corev1.Volume, string) {
	if len(configFiles) == 0 {
		return nil, nil, nil, ""
	}

	volumeName := sanitizeKubernetesName(exporterName + "-config")

	configFiles = validExporterConfigFiles(configFiles)
	if len(configFiles) == 0 {
		return nil, nil, nil, ""
	}

	baseNameCounts := map[string]int{}
	for _, configFile := range configFiles {
		baseNameCounts[configFileKey(configFile.Path)]++
	}

	configFileData := map[string]string{}
	volumeMounts := []corev1.VolumeMount{}
	dirItems := map[string][]corev1.KeyToPath{}
	dirVolumeNames := map[string]string{}
	dirOrder := []string{}
	checksum := newExporterConfigChecksum()

	for _, configFile := range configFiles {
		mountDir := path.Dir(configFile.Path)
		fileName := configFileKey(configFile.Path)
		key := fileName

		if baseNameCounts[fileName] > 1 {
			key = uniqueConfigFileKey(configFile.Path)
		}

		key = uniqueConfigMapKey(configFileData, key)
		configFileData[key] = configFile.Content

		dirItems[mountDir] = append(dirItems[mountDir], corev1.KeyToPath{Key: key, Path: fileName})

		checksum.write(configFile)

		if _, exists := dirVolumeNames[mountDir]; !exists {
			dirVolumeNames[mountDir] = configVolumeName(volumeName, len(dirOrder))
			dirOrder = append(dirOrder, mountDir)
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      dirVolumeNames[mountDir],
				MountPath: mountDir,
				ReadOnly:  true,
			})
		}
	}

	if len(configFileData) == 0 {
		return nil, nil, nil, ""
	}

	volumes := make([]corev1.Volume, 0, len(dirOrder))

	for _, mountDir := range dirOrder {
		items := dirItems[mountDir]
		sort.Slice(items, func(i, j int) bool {
			if items[i].Path == items[j].Path {
				return items[i].Key < items[j].Key
			}

			return items[i].Path < items[j].Path
		})

		volumes = append(volumes, corev1.Volume{
			Name: dirVolumeNames[mountDir],
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: exporterName + "-config"},
					Items:                items,
				},
			},
		})
	}

	return configFileData, volumeMounts, volumes, checksum.sum()
}

func validExporterConfigFiles(configFiles []v1.AcceleratorExporterConfigFile) []v1.AcceleratorExporterConfigFile {
	valid := make([]v1.AcceleratorExporterConfigFile, 0, len(configFiles))

	for _, configFile := range configFiles {
		if configFile.Path == "" {
			continue
		}

		mountDir := path.Dir(configFile.Path)
		if mountDir == "." || mountDir == "/" || mountDir == "" {
			continue
		}

		valid = append(valid, configFile)
	}

	return valid
}

type exporterConfigChecksum struct {
	hash    hashWriter
	hasData bool
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func newExporterConfigChecksum() exporterConfigChecksum {
	return exporterConfigChecksum{hash: sha256.New()}
}

func (c *exporterConfigChecksum) write(configFile v1.AcceleratorExporterConfigFile) {
	if configFile.SkipRestartOnChange {
		return
	}

	c.hasData = true
	_, _ = c.hash.Write([]byte(configFile.Path))
	_, _ = c.hash.Write([]byte{0})
	_, _ = c.hash.Write([]byte(configFile.Content))
	_, _ = c.hash.Write([]byte{0})
}

func (c exporterConfigChecksum) sum() string {
	if !c.hasData {
		return ""
	}

	return hex.EncodeToString(c.hash.Sum(nil))
}

func configFileKey(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) == 0 {
		return "config"
	}

	key := parts[len(parts)-1]
	if key == "" {
		return "config"
	}

	return key
}

func uniqueConfigFileKey(filePath string) string {
	value := strings.Trim(strings.TrimSpace(filePath), "/")
	if value == "" {
		return configFileKey(filePath)
	}

	var builder strings.Builder
	lastSeparator := false

	for _, char := range value {
		allowed := (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-' ||
			char == '_' ||
			char == '.'
		if allowed {
			builder.WriteRune(char)

			lastSeparator = false

			continue
		}

		if !lastSeparator {
			builder.WriteByte('.')

			lastSeparator = true
		}
	}

	key := strings.Trim(builder.String(), ".")
	if key == "" {
		return configFileKey(filePath)
	}

	return key
}

func uniqueConfigMapKey(existing map[string]string, key string) string {
	if _, ok := existing[key]; !ok {
		return key
	}

	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s.%d", key, index)
		if _, ok := existing[candidate]; !ok {
			return candidate
		}
	}
}

func configVolumeName(baseName string, index int) string {
	if index == 0 {
		return baseName
	}

	suffix := fmt.Sprintf("-%d", index+1)
	if len(baseName)+len(suffix) <= 63 {
		return baseName + suffix
	}

	trimmed := strings.Trim(baseName[:63-len(suffix)], "-")
	if trimmed == "" {
		return "accelerator-exporter-config" + suffix
	}

	return trimmed + suffix
}

func sanitizeKubernetesName(value string) string {
	value = sanitizeKubernetesNameValue(value)
	if value == "" {
		return "accelerator-exporter"
	}

	return value
}

func sanitizeKubernetesNameValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastHyphen := false

	for _, char := range value {
		allowed := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
		if allowed {
			builder.WriteRune(char)

			lastHyphen = false

			continue
		}

		if !lastHyphen {
			builder.WriteByte('-')

			lastHyphen = true
		}
	}

	value = builder.String()
	if len(value) > 63 {
		value = value[:63]
	}

	value = strings.Trim(value, "-")

	return value
}
