package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

// metricsAcceleratorExporter describes only the managed exporter workload.
// NodeAgent and scrape-target settings belong to metricsAcceleratorPlan.
type metricsAcceleratorExporter struct {
	Name            string
	AcceleratorType string
	ExporterName    string
	Image           string
	Command         []string
	Args            []string
	Env             []corev1.EnvVar
	Port            int
	MetricsPath     string

	SecurityContext *corev1.SecurityContext
	NodeSelector    map[string]string
	ConfigFileData  map[string]string
	ConfigChecksum  string
	VolumeMounts    []corev1.VolumeMount
	Volumes         []corev1.Volume
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
	return e.MetricsPath != defaultMetricsPath
}

func buildManagedAcceleratorExporter(
	acceleratorType string,
	profile *v1.AcceleratorExporterProfile,
	imagePrefix string,
) *metricsAcceleratorExporter {
	name := acceleratorExporterName(acceleratorType, profile.Name)
	runtime := profile.Runtime
	var runtimeVolumeMounts []corev1.VolumeMount
	var runtimeVolumes []corev1.Volume

	if runtime != nil {
		runtimeVolumeMounts, runtimeVolumes = buildComponentVolumes(runtime.Volumes, runtime.VolumeMounts)
	}

	configFileData, configVolumeMounts, configVolumes, configChecksum := buildExporterConfigVolumes(name, profile.ConfigFiles)
	volumeMounts := append(append([]corev1.VolumeMount{}, configVolumeMounts...), runtimeVolumeMounts...)
	volumes := append(append([]corev1.Volume{}, configVolumes...), runtimeVolumes...)

	// Kubernetes managed exporters intentionally do not project host network/PID
	// flags from the runtime profile; those flags are for static-node runtimes.
	return &metricsAcceleratorExporter{
		Name:            name,
		AcceleratorType: acceleratorType,
		ExporterName:    profile.Name,
		Image:           util.RewriteImageRef(imagePrefix, profile.Image),
		Command:         append([]string{}, profile.Command...),
		Args:            append([]string{}, profile.Args...),
		Env:             buildEnvironmentVariables(profile.Env),
		Port:            profile.Port,
		MetricsPath:     metricsTargetPath(profile.MetricsPath),
		SecurityContext: exporterRuntimeSecurityContext(runtime),
		NodeSelector:    exporterRuntimeNodeSelector(runtime),
		ConfigFileData:  configFileData,
		ConfigChecksum:  configChecksum,
		VolumeMounts:    volumeMounts,
		Volumes:         volumes,
	}
}

func managedAcceleratorExporters(plans []metricsAcceleratorPlan) []metricsAcceleratorExporter {
	exporters := make([]metricsAcceleratorExporter, 0, len(plans))

	for _, plan := range plans {
		if plan.Exporter != nil {
			exporter := *plan.Exporter
			if exporter.AcceleratorType == v1.AcceleratorTypeNVIDIAGPU.String() {
				// NVIDIA selection still uses the profile selector, but the
				// managed exporter remains compatible with CPU nodes. Clear only
				// the render copy so planning metadata stays intact.
				exporter.NodeSelector = nil
			}

			exporters = append(exporters, exporter)
		}
	}

	return exporters
}

func managedAcceleratorExporterSelector(acceleratorType string) map[string]string {
	return map[string]string{
		"neutree.ai/metrics-target":   "accelerator-exporter",
		"neutree.ai/accelerator-type": acceleratorType,
	}
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

func buildEnvironmentVariables(env map[string]string) []corev1.EnvVar {
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

func buildExporterConfigVolumes(
	exporterName string,
	configFiles []v1.AcceleratorExporterConfigFile,
) (map[string]string, []corev1.VolumeMount, []corev1.Volume, string) {
	if len(configFiles) == 0 {
		return nil, nil, nil, ""
	}

	volumeName := sanitizeKubernetesName(exporterName + "-config")

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
