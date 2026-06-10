package hami

import (
	"strings"

	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chartutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubeversion "k8s.io/apimachinery/pkg/version"

	v1 "github.com/neutree-ai/neutree/api/v1"
	ntsemver "github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
)

const defaultChartValuesYAML = `
fullnameOverride: hami
global:
  imageRegistry: ""
  imageTag: v2.9.0
schedulerName: hami-scheduler
dra:
  enabled: false
mockDevicePlugin:
  enabled: false
prometheus:
  enabled: false
scheduler:
  admissionWebhook:
    enabled: true
  certManager:
    enabled: false
  patch:
    enabled: false
  service:
    type: ClusterIP
  kubeScheduler:
    enabled: true
    image:
      repository: kube-scheduler
      pullPolicy: IfNotPresent
  extender:
    image:
      repository: projecthami/hami
      pullPolicy: IfNotPresent
      tag: v2.9.0
devicePlugin:
  enabled: true
  image:
    repository: projecthami/hami
    pullPolicy: IfNotPresent
    tag: v2.9.0
  monitor:
    image:
      repository: projecthami/hami
      pullPolicy: IfNotPresent
      tag: v2.9.0
  service:
    type: ClusterIP
  deviceSplitCount: 100
`

const protectedChartValuesYAML = `
dra:
  enabled: false
scheduler:
  patch:
    enabled: false
  certManager:
    enabled: false
  service:
    type: ClusterIP
devicePlugin:
  service:
    type: ClusterIP
  migStrategy: none
  deviceSplitCount: 100
`

var getKubernetesServerVersion = func(cluster *v1.Cluster) (*kubeversion.Info, error) {
	clientSet, err := util.GetClientSetFromCluster(cluster)
	if err != nil {
		return nil, err
	}

	return clientSet.Discovery().ServerVersion()
}

func (h *HAMiComponent) buildChartValues(scopePlan NodeScopePlan) map[string]interface{} {
	values := defaultChartValues(h.normalizedImagePrefix())
	values = mergeChartValues(values, scopePlan.ConfigPatch)

	if h.cluster.Spec != nil &&
		h.cluster.Spec.AcceleratorVirtualization != nil &&
		h.cluster.Spec.AcceleratorVirtualization.ConfigPatch != nil {
		values = mergeChartValues(values, h.cluster.Spec.AcceleratorVirtualization.ConfigPatch)
	}

	return mergeChartValues(values, h.protectedChartValues(scopePlan))
}

func defaultChartValues(imageRegistry string) map[string]interface{} {
	return mergeChartValues(chartValuesFromYAML(defaultChartValuesYAML), map[string]interface{}{
		"global": map[string]interface{}{
			"imageRegistry": imageRegistry,
			"imageTag":      Version,
		},
	})
}

func mergeChartValues(base map[string]interface{}, overrides map[string]interface{}) map[string]interface{} {
	if len(overrides) == 0 {
		return base
	}

	return chartutil.MergeTables(deepCopyChartValues(overrides), base)
}

func deepCopyChartValues(values map[string]interface{}) map[string]interface{} {
	copied := map[string]interface{}{}
	data, err := yaml.Marshal(values)
	if err != nil {
		return copied
	}
	if err := yaml.Unmarshal(data, &copied); err != nil {
		return map[string]interface{}{}
	}

	return copied
}

func chartValuesFromYAML(valuesYAML string) map[string]interface{} {
	values := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(valuesYAML), &values); err != nil {
		return map[string]interface{}{}
	}

	return values
}

func (h *HAMiComponent) protectedChartValues(scopePlan NodeScopePlan) map[string]interface{} {
	values := mergeChartValues(chartValuesFromYAML(protectedChartValuesYAML), map[string]interface{}{
		"scheduler": map[string]interface{}{
			"kubeScheduler": map[string]interface{}{
				"image": chartImageValues(KubeSchedulerImage, h.resolveKubeSchedulerVersion()),
			},
			"extender": map[string]interface{}{
				"image": chartImageValues(HAMiImage, Version),
			},
		},
		"devicePlugin": map[string]interface{}{
			"enabled": shouldDeployDevicePlugin(scopePlan),
			"image":   chartImageValues(HAMiImage, Version),
			"monitor": map[string]interface{}{
				"image": chartImageValues(HAMiImage, Version),
			},
		},
	})

	if h.imagePullSecret != "" {
		values = mergeChartValues(values, map[string]interface{}{
			"global": map[string]interface{}{
				"imagePullSecrets": []string{h.imagePullSecret},
			},
		})
	}

	nodeScopeLabel := scopePlan.NodeScopeLabel
	if nodeScopeLabel.Key == "" {
		nodeScopeLabel = NvidiaNodeScopeLabel
	}
	values = mergeChartValues(values, map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"nvidiaNodeSelector": map[string]interface{}{
				nodeScopeLabel.Key: nodeScopeLabel.EnabledValue,
			},
		},
	})

	if root := h.resolveNvidiaDriverRoot(scopePlan.ConfigPatch); root != "" {
		values = mergeChartValues(values, map[string]interface{}{
			"devicePlugin": map[string]interface{}{
				"nvidiaDriverRoot": root,
			},
		})
	}

	return values
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
	return kubernetesBaseVersion(serverVersion)
}

func (h *HAMiComponent) resolveChartKubeVersion() chartutil.KubeVersion {
	serverVersion, err := getKubernetesServerVersion(h.cluster)
	if err != nil {
		h.logger.Info("Failed to detect Kubernetes server version for HAMi chart render, using default",
			"error", err,
			"defaultVersion", DefaultKubeSchedulerVersion())
		return kubeVersionFromBaseVersion(DefaultKubeSchedulerVersion())
	}

	return kubeVersionFromServerVersion(serverVersion)
}

func kubeVersionFromServerVersion(serverVersion *kubeversion.Info) chartutil.KubeVersion {
	version := kubernetesBaseVersion(serverVersion)
	if version == "" {
		return kubeVersionFromBaseVersion(DefaultKubeSchedulerVersion())
	}

	return kubeVersionFromBaseVersion(version)
}

func kubeVersionFromBaseVersion(version string) chartutil.KubeVersion {
	majorMinor, err := ntsemver.MajorMinor(version)
	if err != nil {
		return chartutil.KubeVersion{
			Version: version,
			Major:   "1",
			Minor:   "32",
		}
	}

	parts := strings.SplitN(majorMinor, ".", 2)
	return chartutil.KubeVersion{
		Version: version,
		Major:   parts[0],
		Minor:   parts[1],
	}
}

func kubernetesBaseVersion(serverVersion *kubeversion.Info) string {
	if serverVersion == nil {
		return ""
	}

	if version, err := ntsemver.BaseVersion(serverVersion.GitVersion); err == nil && version != "" {
		return version
	}

	return kubernetesBaseVersionFromMajorMinor(serverVersion.Major, serverVersion.Minor)
}

func kubernetesMajorMinor(serverVersion *kubeversion.Info) string {
	version := kubernetesBaseVersion(serverVersion)
	if version == "" {
		return ""
	}

	majorMinor, err := ntsemver.MajorMinor(version)
	if err != nil {
		return ""
	}

	return majorMinor
}

func kubernetesBaseVersionFromMajorMinor(major, minor string) string {
	major = kubernetesVersionNumber(strings.TrimPrefix(major, "v"))
	minor = kubernetesVersionNumber(minor)
	if major == "" || minor == "" {
		return ""
	}

	return "v" + major + "." + minor + ".0"
}

func kubernetesVersionNumber(value string) string {
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
