package hami

import (
	"strings"

	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chartutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubeversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/klog/v2"

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
  defaultSchedulerPolicy:
    nodeSchedulerPolicy: binpack
    gpuSchedulerPolicy: topology-aware
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
  enabled: false
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
`

const protectedChartValuesYAML = `
dra:
  enabled: false
scheduler:
  admissionWebhook:
    enabled: true
  patch:
    enabled: false
  certManager:
    enabled: false
  service:
    type: ClusterIP
devicePlugin:
  service:
    type: ClusterIP
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
	// Merge order is intentional: chart defaults < plugin discovery patch <
	// supported user config_patch < Neutree protected values. Protected values
	// keep shared lifecycle, TLS, service, and image settings under Neutree control.
	values = mergeChartValues(values, scopePlan.ConfigPatch)

	if h.cluster.Spec != nil &&
		h.cluster.Spec.AcceleratorVirtualization != nil &&
		h.cluster.Spec.AcceleratorVirtualization.ConfigPatch != nil {
		configPatch := h.cluster.Spec.AcceleratorVirtualization.ConfigPatch
		if _, found := configPatch["devicePlugin"]; found {
			klog.Warningf("Ignoring legacy accelerator_virtualization.config_patch.devicePlugin for cluster %s",
				h.cluster.Metadata.WorkspaceName())
		}

		values = mergeChartValues(values, userVirtualizationConfigPatch(configPatch))
	}

	return mergeChartValues(values, h.protectedChartValues())
}

func userVirtualizationConfigPatch(configPatch map[string]interface{}) map[string]interface{} {
	if len(configPatch) == 0 {
		return nil
	}

	sanitized := make(map[string]interface{}, len(configPatch))

	for key, value := range configPatch {
		if key != "devicePlugin" {
			sanitized[key] = value
		}
	}

	return sanitized
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

	// chartutil.MergeTables keeps the first argument's scalar values and fills
	// missing keys from the second, so overrides must be passed first.
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

func (h *HAMiComponent) protectedChartValues() map[string]interface{} {
	values := mergeChartValues(chartValuesFromYAML(protectedChartValuesYAML), map[string]interface{}{
		"scheduler": map[string]interface{}{
			"kubeScheduler": map[string]interface{}{
				"image": chartImageValues(KubeSchedulerImageRegistry, KubeSchedulerImageRepository, h.resolveKubeSchedulerVersion()),
			},
			"extender": map[string]interface{}{
				"image": chartImageValues(HAMiImageRegistry, HAMiImageRepository, Version),
			},
			"admissionWebhook": map[string]interface{}{
				// Reject pod admission when the HAMi scheduler webhook is
				// unavailable instead of letting GPU pods fall through to the
				// default scheduler and bypass the HAMi global device view.
				// The webhook is scoped to this cluster's namespace so the Fail
				// policy cannot block pod creation in other namespaces.
				"failurePolicy": "Fail",
				"namespaceSelector": map[string]interface{}{
					"matchExpressions": []interface{}{
						map[string]interface{}{
							"key":      "kubernetes.io/metadata.name",
							"operator": "In",
							"values":   []interface{}{h.namespace},
						},
					},
				},
			},
		},
		"devicePlugin": map[string]interface{}{
			"image": chartImageValues(HAMiImageRegistry, HAMiImageRepository, Version),
			"monitor": map[string]interface{}{
				"image": chartImageValues(HAMiImageRegistry, HAMiImageRepository, Version),
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

	return values
}

func chartImageValues(registry, repository, tag string) map[string]interface{} {
	values := map[string]interface{}{
		"registry":   registry,
		"repository": repository,
		"pullPolicy": "IfNotPresent",
	}
	if tag != "" {
		values["tag"] = tag
	}

	return values
}

func (h *HAMiComponent) normalizedImagePrefix() string {
	imagePrefix := strings.TrimRight(strings.TrimSpace(h.imagePrefix), "/")
	if util.IsDockerHubImagePrefix(imagePrefix) {
		return ""
	}

	return imagePrefix
}

// DefaultKubeSchedulerVersion is used when the target cluster version cannot be
// detected or parsed.
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

	minorVersion := kubeServerMajorMinor(serverVersion)

	schedulerVersion, ok := KubeSchedulerVersionsByMinor[minorVersion]
	if !ok {
		// For an unmapped Kubernetes minor, use the exact detected scheduler
		// version and rely on the customer's offline image package to provide it.
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
	return kubeServerSemver(serverVersion)
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
	version := kubeServerSemver(serverVersion)
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

func kubeServerSemver(serverVersion *kubeversion.Info) string {
	if serverVersion == nil {
		return ""
	}

	if version, err := ntsemver.BaseVersion(serverVersion.GitVersion); err == nil && version != "" {
		return version
	}

	return kubeServerSemverFromMajorMinor(serverVersion.Major, serverVersion.Minor)
}

func kubeServerMajorMinor(serverVersion *kubeversion.Info) string {
	version := kubeServerSemver(serverVersion)
	if version == "" {
		return ""
	}

	majorMinor, err := ntsemver.MajorMinor(version)
	if err != nil {
		return ""
	}

	return majorMinor
}

func kubeServerSemverFromMajorMinor(major, minor string) string {
	major = kubeVersionNumericPrefix(strings.TrimPrefix(major, "v"))
	minor = kubeVersionNumericPrefix(minor)

	if major == "" || minor == "" {
		return ""
	}

	return "v" + major + "." + minor + ".0"
}

func kubeVersionNumericPrefix(value string) string {
	for i, r := range value {
		if r < '0' || r > '9' {
			return value[:i]
		}
	}

	return value
}

func (h *HAMiComponent) renderResources(scopePlan NodeScopePlan) (*unstructured.UnstructuredList, error) {
	resources, err := renderEmbeddedHAMiChart(h.buildChartValues(scopePlan), h.namespace, h.resolveChartKubeVersion())
	if err != nil {
		return resources, err
	}

	// The accelerator plugin does not require an additional device plugin.
	if scopePlan.DevicePluginTemplate == nil {
		return resources, nil
	}

	templateResources, err := util.RenderKubernetesManifest(scopePlan.DevicePluginTemplate.Manifest, struct {
		Namespace      string
		NodeScopeLabel NodeScopeLabel
		ImagePrefix    string
	}{
		Namespace:      h.namespace,
		NodeScopeLabel: scopePlan.NodeScopeLabel,
		ImagePrefix:    h.normalizedImagePrefix(),
	})
	if err != nil {
		return nil, err
	}

	resources.Items = append(resources.Items, templateResources.Items...)

	return resources, nil
}
