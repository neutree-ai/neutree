package hami

import (
	"strings"

	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chartutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubeversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
	componentversion "github.com/neutree-ai/neutree/internal/component"
	"github.com/neutree-ai/neutree/internal/util"
)

const defaultChartValuesYAML = `
fullnameOverride: hami
global:
  imageRegistry: ""
schedulerName: hami-scheduler
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
devicePlugin:
  enabled: false
  image:
    repository: projecthami/hami
    pullPolicy: IfNotPresent
  monitor:
    image:
      repository: projecthami/hami
      pullPolicy: IfNotPresent
  service:
    type: ClusterIP
`

const protectedChartValuesYAML = `
scheduler:
  admissionWebhook:
    enabled: true
    manageNamespaceSelector: true
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

func (h *HAMiComponent) buildChartValues(scopePlan NodeScopePlan, hamiVersion string) map[string]interface{} {
	values := defaultChartValues(h.normalizedImagePrefix(), hamiVersion)
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

	return mergeChartValues(values, h.protectedChartValues(hamiVersion))
}

func userVirtualizationConfigPatch(configPatch map[string]interface{}) map[string]interface{} {
	if len(configPatch) == 0 {
		return nil
	}

	sanitized := deepCopyChartValues(configPatch)
	delete(sanitized, "devicePlugin")
	unstructured.RemoveNestedField(sanitized, "scheduler", "admissionWebhook", "namespaceSelector")

	return sanitized
}

func defaultChartValues(imageRegistry, hamiVersion string) map[string]interface{} {
	return mergeChartValues(chartValuesFromYAML(defaultChartValuesYAML), map[string]interface{}{
		"global": map[string]interface{}{
			"imageRegistry": imageRegistry,
			"imageTag":      hamiVersion,
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

func (h *HAMiComponent) protectedChartValues(hamiVersion string) map[string]interface{} {
	values := mergeChartValues(chartValuesFromYAML(protectedChartValuesYAML), map[string]interface{}{
		"scheduler": map[string]interface{}{
			"kubeScheduler": map[string]interface{}{
				"image": chartImageValues(KubeSchedulerImageRegistry, KubeSchedulerImageRepository, h.resolveKubeSchedulerVersion()),
			},
			"extender": map[string]interface{}{
				"image": chartImageValues(HAMiImageRegistry, HAMiImageRepository, hamiVersion),
			},
		},
		"devicePlugin": map[string]interface{}{
			"image": chartImageValues(HAMiImageRegistry, HAMiImageRepository, hamiVersion),
			"monitor": map[string]interface{}{
				"image": chartImageValues(HAMiImageRegistry, HAMiImageRepository, hamiVersion),
			},
		},
	})

	if hamiVersion == componentversion.HAMi {
		values = mergeChartValues(values, map[string]interface{}{
			"dra": map[string]interface{}{"enabled": false},
		})
	}

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

func (h *HAMiComponent) resolveKubeSchedulerVersion() string {
	serverVersion, err := getKubernetesServerVersion(h.cluster)
	if err != nil {
		h.logger.Info("Failed to detect Kubernetes server version for HAMi scheduler image, using default",
			"error", err,
			"defaultVersion", clusterversion.DefaultKubeSchedulerVersion)

		return clusterversion.DefaultKubeSchedulerVersion
	}

	schedulerVersion, source := clusterversion.KubeSchedulerVersionForClusterVersion(h.cluster.GetVersion(), serverVersion)

	switch source {
	case clusterversion.SchedulerVersionDetected:
		h.logger.Info("Kubernetes minor version is not mapped for HAMi scheduler image, using detected version",
			"kubernetesVersion", serverVersion.GitVersion,
			"detectedVersion", schedulerVersion)
	case clusterversion.SchedulerVersionDefault:
		h.logger.Info("Kubernetes minor version is not mapped for HAMi scheduler image and detected version is invalid, using default",
			"kubernetesVersion", clusterversion.KubernetesServerVersion(serverVersion),
			"defaultVersion", schedulerVersion)
	}

	h.logger.V(4).Info("Resolved HAMi Kubernetes scheduler image version",
		"schedulerVersion", schedulerVersion)

	return schedulerVersion
}

func (h *HAMiComponent) resolveChartKubeVersion() chartutil.KubeVersion {
	serverVersion, err := getKubernetesServerVersion(h.cluster)
	if err != nil {
		h.logger.Info("Failed to detect Kubernetes server version for HAMi chart render, using default",
			"error", err,
			"defaultVersion", clusterversion.DefaultKubeSchedulerVersion)

		return clusterversion.KubeVersionFromBaseVersion(clusterversion.DefaultKubeSchedulerVersion)
	}

	return clusterversion.KubeVersionFromServerVersion(serverVersion)
}

func (h *HAMiComponent) renderResources(scopePlan NodeScopePlan) (*unstructured.UnstructuredList, error) {
	hamiVersion, err := clusterversion.HAMiVersionForCluster(h.cluster.GetVersion())
	if err != nil {
		return nil, err
	}

	resources, err := renderEmbeddedHAMiChart(hamiVersion, h.buildChartValues(scopePlan, hamiVersion), h.namespace, h.resolveChartKubeVersion())
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
