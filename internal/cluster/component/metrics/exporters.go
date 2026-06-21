package metrics

import (
	"context"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/componentversion"
)

const (
	nodeExporterDaemonSetName = "neutree-node-exporter"
	nodeExporterPort          = 9100
	neutreeMetricsName        = "neutree-node-agent"
	neutreeMetricsPort        = 9101

	defaultNodeExporterImage = "quay.io/prometheus/node-exporter:" + componentversion.NodeExporter
	defaultMetricsPath       = "/metrics"
)

type metricsAcceleratorExporter struct {
	Name          string
	AppLabel      string
	Kind          string
	ContainerName string
	JobName       string
	Image         string
	Args          []string
	Env           []corev1.EnvVar
	Port          int
	MetricsPath   string

	HostNetwork  bool
	HostPID      bool
	Capabilities []corev1.Capability

	NodeSelector   map[string]string
	ConfigMapName  string
	ConfigFileData map[string]string
	VolumeMounts   []corev1.VolumeMount
	Volumes        []corev1.Volume
}

func (e metricsAcceleratorExporter) HasCustomMetricsPath() bool {
	return strings.TrimSpace(e.MetricsPath) != "" && e.MetricsPath != defaultMetricsPath
}

func (m *MetricsComponent) planAcceleratorExporters(ctx context.Context) ([]metricsAcceleratorExporter, error) {
	if m.acceleratorMgr == nil {
		return nil, nil
	}

	acceleratorTypes := append([]string{}, m.acceleratorMgr.SupportPlugins()...)
	sort.Strings(acceleratorTypes)

	exporters := make([]metricsAcceleratorExporter, 0, len(acceleratorTypes))

	for _, acceleratorType := range acceleratorTypes {
		exporter, ok, err := m.buildAcceleratorExporter(ctx, acceleratorType)
		if err != nil {
			return nil, err
		}

		if ok {
			exporters = append(exporters, exporter)
		}
	}

	return exporters, nil
}

func (m *MetricsComponent) buildAcceleratorExporter(
	ctx context.Context,
	acceleratorType string,
) (metricsAcceleratorExporter, bool, error) {
	profile, supported, err := m.acceleratorMgr.GetAcceleratorProfile(ctx, acceleratorType)
	if err != nil {
		return metricsAcceleratorExporter{}, false, err
	}

	if !supported || profile == nil || profile.Metrics == nil || profile.Metrics.Exporter == nil {
		return metricsAcceleratorExporter{}, false, nil
	}

	exporterProfile := profile.Metrics.Exporter
	if strings.TrimSpace(exporterProfile.Image) == "" || exporterProfile.Port <= 0 {
		return metricsAcceleratorExporter{}, false, nil
	}

	name := acceleratorExporterName(acceleratorType, exporterProfile.Kind)
	configFileData, volumeMounts, volumes := buildExporterConfigVolumes(name, exporterProfile.ConfigFiles)
	runtime := exporterProfile.Runtime

	exporter := metricsAcceleratorExporter{
		Name:           name,
		AppLabel:       name,
		Kind:           exporterProfile.Kind,
		ContainerName:  acceleratorExporterContainerName(exporterProfile.Kind, name),
		JobName:        acceleratorExporterJobName(acceleratorType, exporterProfile.Kind),
		Image:          rewriteMetricsImage(m.imagePrefix, exporterProfile.Image),
		Args:           append([]string{}, exporterProfile.Args...),
		Env:            buildExporterEnv(exporterProfile.Env),
		Port:           exporterProfile.Port,
		MetricsPath:    exporterMetricsPath(exporterProfile.MetricsPath),
		HostNetwork:    exporterRuntimeHostNetwork(runtime),
		HostPID:        exporterRuntimeHostPID(runtime),
		Capabilities:   exporterRuntimeCapabilities(runtime),
		NodeSelector:   exporterRuntimeNodeSelector(runtime),
		ConfigMapName:  name + "-config",
		ConfigFileData: configFileData,
		VolumeMounts:   volumeMounts,
		Volumes:        volumes,
	}

	return exporter, true, nil
}

func acceleratorExporterName(acceleratorType string, kind string) string {
	if acceleratorType == v1.AcceleratorTypeNVIDIAGPU.String() && kind == "dcgm-exporter" {
		return "nvidia-dcgm-exporter"
	}

	if kind == "" {
		return sanitizeKubernetesName(acceleratorType + "-accelerator-exporter")
	}

	return sanitizeKubernetesName(acceleratorType + "-" + kind)
}

func acceleratorExporterContainerName(kind string, fallback string) string {
	if kind != "" {
		return sanitizeKubernetesName(kind)
	}

	return fallback
}

func acceleratorExporterJobName(acceleratorType string, kind string) string {
	if kind != "" {
		return sanitizeKubernetesName(kind)
	}

	return sanitizeKubernetesName(acceleratorType + "-accelerator-exporter")
}

func exporterMetricsPath(metricsPath string) string {
	metricsPath = strings.TrimSpace(metricsPath)
	if metricsPath == "" {
		return defaultMetricsPath
	}

	return metricsPath
}

func acceleratorExporterLocalMetricsURLs(exporters []metricsAcceleratorExporter) []string {
	urls := make([]string, 0, len(exporters))

	for _, exporter := range exporters {
		if !exporter.HostNetwork || exporter.Port <= 0 {
			continue
		}

		metricsPath := exporterMetricsPath(exporter.MetricsPath)
		if !strings.HasPrefix(metricsPath, "/") {
			metricsPath = "/" + metricsPath
		}

		urls = append(urls, "http://127.0.0.1:"+strconv.Itoa(exporter.Port)+metricsPath)
	}

	return urls
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

func exporterRuntimeHostNetwork(runtime *v1.AcceleratorExporterRuntimeProfile) bool {
	return runtime != nil && runtime.HostNetwork
}

func exporterRuntimeHostPID(runtime *v1.AcceleratorExporterRuntimeProfile) bool {
	return runtime != nil && runtime.HostPID
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
	configFiles []v1.NodeComponentConfigFile,
) (map[string]string, []corev1.VolumeMount, []corev1.Volume) {
	if len(configFiles) == 0 {
		return nil, nil, nil
	}

	configFileData := map[string]string{}
	volumeName := sanitizeKubernetesName(exporterName + "-config")
	volumeMounts := make([]corev1.VolumeMount, 0, len(configFiles))

	for _, configFile := range configFiles {
		if configFile.Path == "" {
			continue
		}

		key := configFileKey(configFile.Path)
		configFileData[key] = configFile.Content
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: configFile.Path,
			SubPath:   key,
			ReadOnly:  true,
		})
	}

	if len(configFileData) == 0 {
		return nil, nil, nil
	}

	volumes := []corev1.Volume{
		{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: exporterName + "-config"},
				},
			},
		},
	}

	return configFileData, volumeMounts, volumes
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

func rewriteMetricsImage(imagePrefix string, image string) string {
	if image == "" {
		return ""
	}

	imagePrefix = strings.TrimRight(strings.TrimSpace(imagePrefix), "/")
	if imagePrefix == "" || strings.HasPrefix(image, imagePrefix+"/") {
		return image
	}

	return imagePrefix + "/" + stripMetricsSourceImageRegistry(image)
}

func stripMetricsSourceImageRegistry(image string) string {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) < 2 {
		return image
	}

	if isMetricsSourceImageRegistry(parts[0]) {
		return parts[1]
	}

	return image
}

func isMetricsSourceImageRegistry(segment string) bool {
	return segment == "localhost" || strings.Contains(segment, ".") || strings.Contains(segment, ":")
}

func sanitizeKubernetesName(value string) string {
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

	if value == "" {
		return "accelerator-exporter"
	}

	return value
}
