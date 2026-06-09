package hami

import (
	"strings"

	"helm.sh/helm/v3/pkg/chartutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubeversion "k8s.io/apimachinery/pkg/version"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

var getKubernetesServerVersion = func(cluster *v1.Cluster) (*kubeversion.Info, error) {
	clientSet, err := util.GetClientSetFromCluster(cluster)
	if err != nil {
		return nil, err
	}

	return clientSet.Discovery().ServerVersion()
}

func (h *HAMiComponent) buildChartValues(scopePlan NodeScopePlan) map[string]interface{} {
	values := defaultChartValues(h.normalizedImagePrefix())
	values = mergeConfigPatch(values, scopePlan.ConfigPatch)

	if h.cluster.Spec != nil &&
		h.cluster.Spec.AcceleratorVirtualization != nil &&
		h.cluster.Spec.AcceleratorVirtualization.ConfigPatch != nil {
		values = mergeConfigPatch(values, h.cluster.Spec.AcceleratorVirtualization.ConfigPatch)
	}

	h.enforceProtectedChartValues(values, scopePlan)

	return values
}

func defaultChartValues(imageRegistry string) map[string]interface{} {
	return map[string]interface{}{
		"fullnameOverride": ChartReleaseName,
		"global": map[string]interface{}{
			"imageRegistry": imageRegistry,
			"imageTag":      Version,
		},
		"schedulerName": SchedulerName,
		"dra": map[string]interface{}{
			"enabled": false,
		},
		"mockDevicePlugin": map[string]interface{}{
			"enabled": false,
		},
		"prometheus": map[string]interface{}{
			"enabled": false,
		},
		"scheduler": map[string]interface{}{
			"admissionWebhook": map[string]interface{}{
				"enabled": true,
			},
			"certManager": map[string]interface{}{
				"enabled": false,
			},
			"patch": map[string]interface{}{
				"enabled": false,
			},
			"service": map[string]interface{}{
				"type": "ClusterIP",
			},
			"kubeScheduler": map[string]interface{}{
				"enabled": true,
				"image":   chartImageValues(KubeSchedulerImage, ""),
			},
			"extender": map[string]interface{}{
				"image": chartImageValues(HAMiImage, Version),
			},
		},
		"devicePlugin": map[string]interface{}{
			"enabled": true,
			"image":   chartImageValues(HAMiImage, Version),
			"monitor": map[string]interface{}{
				"image": chartImageValues(HAMiImage, Version),
			},
			"service": map[string]interface{}{
				"type": "ClusterIP",
			},
			"deviceSplitCount": NvidiaGPUDefaultDeviceSplitCount,
		},
	}
}

func (h *HAMiComponent) enforceProtectedChartValues(values map[string]interface{}, scopePlan NodeScopePlan) {
	setNestedChartValue(values, false, "dra", "enabled")
	setNestedChartValue(values, false, "scheduler", "patch", "enabled")
	setNestedChartValue(values, false, "scheduler", "certManager", "enabled")
	setNestedChartValue(values, "ClusterIP", "scheduler", "service", "type")
	setNestedChartValue(values, "ClusterIP", "devicePlugin", "service", "type")
	setNestedChartValue(values, shouldDeployDevicePlugin(scopePlan), "devicePlugin", "enabled")
	setNestedChartValue(values, "none", "devicePlugin", "migStrategy")
	setNestedChartValue(values, NvidiaGPUDefaultDeviceSplitCount, "devicePlugin", "deviceSplitCount")

	setNestedChartValue(values, chartImageValues(KubeSchedulerImage, h.resolveKubeSchedulerVersion()),
		"scheduler", "kubeScheduler", "image")
	setNestedChartValue(values, chartImageValues(HAMiImage, Version), "scheduler", "extender", "image")
	setNestedChartValue(values, chartImageValues(HAMiImage, Version), "devicePlugin", "image")
	setNestedChartValue(values, chartImageValues(HAMiImage, Version), "devicePlugin", "monitor", "image")

	if h.imagePullSecret != "" {
		setNestedChartValue(values, []string{h.imagePullSecret}, "global", "imagePullSecrets")
	}

	nodeScopeLabel := scopePlan.NodeScopeLabel
	if nodeScopeLabel.Key == "" {
		nodeScopeLabel = NvidiaNodeScopeLabel
	}
	setNestedChartValue(values, map[string]interface{}{
		nodeScopeLabel.Key: nodeScopeLabel.EnabledValue,
	}, "devicePlugin", "nvidiaNodeSelector")

	if root := h.resolveNvidiaDriverRoot(scopePlan.ConfigPatch); root != "" {
		setNestedChartValue(values, root, "devicePlugin", "nvidiaDriverRoot")
	}
}

func chartImageValues(repository, tag string) map[string]interface{} {
	values := map[string]interface{}{
		"repository": repository,
		"pullPolicy": "IfNotPresent",
	}
	if tag != "" {
		values["tag"] = tag
	}

	return values
}

func setNestedChartValue(values map[string]interface{}, value interface{}, path ...string) {
	current := values
	for _, key := range path[:len(path)-1] {
		next, ok := current[key].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			current[key] = next
		}
		current = next
	}

	current[path[len(path)-1]] = value
}

func (h *HAMiComponent) normalizedImagePrefix() string {
	return strings.TrimRight(strings.TrimSpace(h.imagePrefix), "/")
}

func DefaultKubeSchedulerVersion() string {
	return KubeSchedulerVersionsByMinor["1.32"]
}

func (h *HAMiComponent) resolveKubeSchedulerVersion() string {
	serverVersion, err := getKubernetesServerVersion(h.cluster)
	if err != nil {
		h.logger.Info("Failed to detect Kubernetes server version for HAMi scheduler image, using default",
			"error", err,
			"defaultVersion", DefaultKubeSchedulerVersion())
		return DefaultKubeSchedulerVersion()
	}

	minorVersion := kubernetesMajorMinor(serverVersion)
	schedulerVersion, ok := KubeSchedulerVersionsByMinor[minorVersion]
	if !ok {
		detectedVersion := kubeSchedulerVersionFromServerVersion(serverVersion)
		if detectedVersion != "" {
			h.logger.Info("Kubernetes minor version is not mapped for HAMi scheduler image, using detected version",
				"kubernetesVersion", serverVersion.GitVersion,
				"minorVersion", minorVersion,
				"detectedVersion", detectedVersion)
			return detectedVersion
		}

		h.logger.Info("Kubernetes minor version is not mapped for HAMi scheduler image and detected version is invalid, using default",
			"kubernetesVersion", serverVersion.GitVersion,
			"minorVersion", minorVersion,
			"defaultVersion", DefaultKubeSchedulerVersion())
		return DefaultKubeSchedulerVersion()
	}

	return schedulerVersion
}

func kubeSchedulerVersionFromServerVersion(serverVersion *kubeversion.Info) string {
	if serverVersion == nil {
		return ""
	}

	if version := kubeSchedulerVersionFromGitVersion(serverVersion.GitVersion); version != "" {
		return version
	}

	major := leadingVersionDigits(strings.TrimPrefix(serverVersion.Major, "v"))
	minor := leadingVersionDigits(serverVersion.Minor)
	if major == "" || minor == "" {
		return ""
	}

	return "v" + major + "." + minor + ".0"
}

func kubeSchedulerVersionFromGitVersion(gitVersion string) string {
	version := strings.TrimSpace(gitVersion)
	if version == "" {
		return ""
	}

	version = strings.SplitN(version, "+", 2)[0]
	normalized := strings.TrimPrefix(version, "v")
	parts := strings.Split(normalized, ".")
	if len(parts) < 2 {
		return ""
	}

	major := leadingVersionDigits(parts[0])
	minor := leadingVersionDigits(parts[1])
	if major == "" || minor == "" {
		return ""
	}

	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	return version
}

func (h *HAMiComponent) resolveChartKubeVersion() chartutil.KubeVersion {
	serverVersion, err := getKubernetesServerVersion(h.cluster)
	if err != nil {
		h.logger.Info("Failed to detect Kubernetes server version for HAMi chart render, using default",
			"error", err,
			"defaultVersion", DefaultKubeSchedulerVersion())
		return kubeVersionFromGitVersion(DefaultKubeSchedulerVersion())
	}

	return kubeVersionFromServerVersion(serverVersion)
}

func kubeVersionFromServerVersion(serverVersion *kubeversion.Info) chartutil.KubeVersion {
	if serverVersion == nil {
		return kubeVersionFromGitVersion(DefaultKubeSchedulerVersion())
	}

	major := leadingVersionDigits(strings.TrimPrefix(serverVersion.Major, "v"))
	minor := leadingVersionDigits(serverVersion.Minor)
	version := serverVersion.GitVersion
	if version == "" && major != "" && minor != "" {
		version = "v" + major + "." + minor + ".0"
	}

	return chartutil.KubeVersion{
		Version: version,
		Major:   major,
		Minor:   minor,
	}
}

func kubeVersionFromGitVersion(gitVersion string) chartutil.KubeVersion {
	trimmed := strings.TrimPrefix(gitVersion, "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) < 2 {
		return chartutil.KubeVersion{
			Version: gitVersion,
			Major:   "1",
			Minor:   "32",
		}
	}

	return chartutil.KubeVersion{
		Version: gitVersion,
		Major:   leadingVersionDigits(parts[0]),
		Minor:   leadingVersionDigits(parts[1]),
	}
}

func kubernetesMajorMinor(serverVersion *kubeversion.Info) string {
	if serverVersion == nil {
		return ""
	}

	major := leadingVersionDigits(strings.TrimPrefix(serverVersion.Major, "v"))
	minor := leadingVersionDigits(serverVersion.Minor)
	if major == "" || minor == "" {
		return ""
	}

	return major + "." + minor
}

func leadingVersionDigits(value string) string {
	for i, r := range value {
		if r < '0' || r > '9' {
			return value[:i]
		}
	}

	return value
}

func (h *HAMiComponent) resolveNvidiaDriverRoot(pluginConfigPatch map[string]interface{}) string {
	if h.cluster.Spec != nil &&
		h.cluster.Spec.AcceleratorVirtualization != nil &&
		h.cluster.Spec.AcceleratorVirtualization.ConfigPatch != nil {
		if root := nvidiaDriverRootFromPatch(h.cluster.Spec.AcceleratorVirtualization.ConfigPatch); root != "" {
			return root
		}
	}

	if root := nvidiaDriverRootFromPatch(pluginConfigPatch); root != "" {
		return root
	}

	return ""
}

func nvidiaDriverRootFromPatch(configPatch map[string]interface{}) string {
	if configPatch == nil {
		return ""
	}

	devicePlugin, ok := configPatch["devicePlugin"].(map[string]interface{})
	if !ok {
		return ""
	}

	root, ok := devicePlugin["nvidiaDriverRoot"].(string)
	if !ok {
		return ""
	}

	return root
}

const NvidiaGPUDefaultDeviceSplitCount = 100

func shouldDeployDevicePlugin(plan NodeScopePlan) bool {
	return len(plan.DisabledNodes) == 0 || len(plan.EnabledNodes) > 0 || len(plan.PatchedNodes) > 0
}

func (h *HAMiComponent) renderResources(scopePlan NodeScopePlan) (*unstructured.UnstructuredList, error) {
	return renderEmbeddedHAMiChart(h.buildChartValues(scopePlan), h.namespace, h.resolveChartKubeVersion())
}
