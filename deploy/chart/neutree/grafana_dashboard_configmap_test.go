package neutree

import (
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/neutree-ai/neutree/internal/util"
)

const (
	grafanaDashboardConfigMapSuffix  = "-grafana-dashboard"
	legacyGrafanaDashboardConfigMap  = "grafana-dashboards"
	grafanaDeploymentName            = "neutree-grafana"
	grafanaDashboardVolumeName       = "grafana-dashboards"
	grafanaDashboardMountPath        = "/var/lib/grafana/dashboards"
	longestGrafanaDashboardConfigMap = "neutree-endpoint-token-latency-embed-grafana-dashboard"
)

func TestGrafanaDashboardConfigMaps(t *testing.T) {
	resources := renderChart(t, nil)
	dashboardConfigMaps, grafana := renderedGrafanaDashboards(t, resources.Items)
	require.Contains(t, dashboardConfigMaps, longestGrafanaDashboardConfigMap)
	projectedConfigMaps := projectedDashboardConfigMaps(t, *grafana)
	require.Equal(t, dashboardConfigMaps, projectedConfigMaps)
	assertDashboardVolumeMount(t, *grafana)
}

func TestGrafanaDashboardRejectsLegacyMount(t *testing.T) {
	_, err := renderChartWithError(legacyGrafanaValues())
	require.Error(t, err)
	require.ErrorContains(t, err, "--reset-values")
}

func TestGrafanaDashboardRejectsIncompleteMigration(t *testing.T) {
	_, err := renderChartWithError(incompleteMigrationValues())
	require.Error(t, err)
	require.ErrorContains(t, err, "native dashboard volume")
}

func renderChart(t *testing.T, overrides map[string]interface{}) *unstructured.UnstructuredList {
	resources, err := renderChartWithError(overrides)
	require.NoError(t, err)

	return resources
}

func renderChartWithError(overrides map[string]interface{}) (*unstructured.UnstructuredList, error) {
	return renderChartForRelease(overrides, chartutil.ReleaseOptions{
		Name:      "neutree",
		Namespace: "default",
		Revision:  1,
		IsInstall: true,
	})
}

func renderChartForRelease(overrides map[string]interface{}, release chartutil.ReleaseOptions) (*unstructured.UnstructuredList, error) {
	valuesOverrides := map[string]interface{}{
		"jwtSecret":     "render-test-secret",
		"adminPassword": "render-test-password",
	}
	for key, value := range overrides {
		valuesOverrides[key] = value
	}

	chart, err := loader.Load(".")
	if err != nil {
		return nil, err
	}

	values, err := chartutil.ToRenderValuesWithSchemaValidation(
		chart,
		valuesOverrides,
		release,
		chartutil.DefaultCapabilities.Copy(),
		true,
	)
	if err != nil {
		return nil, err
	}

	renderedFiles, err := engine.Engine{}.Render(chart, values)
	if err != nil {
		return nil, err
	}

	resources, err := util.DecodeKubernetesManifest(joinRenderedManifests(renderedFiles))
	if err != nil {
		return nil, err
	}

	return resources, nil
}

func legacyGrafanaValues() map[string]interface{} {
	return map[string]interface{}{
		"grafana": map[string]interface{}{
			"extraConfigmapMounts": []interface{}{
				map[string]interface{}{
					"name":      "datasources",
					"mountPath": "/etc/grafana/provisioning/datasources",
					"configMap": "grafana-datasources",
					"readOnly":  true,
					"optional":  false,
				},
				map[string]interface{}{
					"name":      "dashboards",
					"mountPath": "/var/lib/grafana/dashboards",
					"configMap": legacyGrafanaDashboardConfigMap,
					"readOnly":  true,
				},
			},
			"extraContainerVolumes": []interface{}{},
			"extraVolumeMounts":     []interface{}{},
		},
	}
}

func incompleteMigrationValues() map[string]interface{} {
	return map[string]interface{}{
		"grafana": map[string]interface{}{
			"extraConfigmapMounts": []interface{}{
				map[string]interface{}{
					"name":      "datasources",
					"mountPath": "/etc/grafana/provisioning/datasources",
					"configMap": "grafana-datasources",
					"readOnly":  true,
					"optional":  false,
				},
			},
			"extraContainerVolumes": []interface{}{},
			"extraVolumeMounts":     []interface{}{},
		},
	}
}

func joinRenderedManifests(renderedFiles map[string]string) string {
	paths := make([]string, 0, len(renderedFiles))
	for filePath := range renderedFiles {
		extension := path.Ext(filePath)
		if extension == ".yaml" || extension == ".yml" {
			paths = append(paths, filePath)
		}
	}
	sort.Strings(paths)

	var manifest strings.Builder
	for _, filePath := range paths {
		content := strings.TrimSpace(renderedFiles[filePath])
		if content == "" {
			continue
		}

		manifest.WriteString("---\n")
		manifest.WriteString(content)
		manifest.WriteString("\n")
	}

	return manifest.String()
}

func renderedGrafanaDashboards(t *testing.T, resources []unstructured.Unstructured) (map[string]struct{}, *unstructured.Unstructured) {
	t.Helper()

	dashboardConfigMaps := make(map[string]struct{})
	var grafana *unstructured.Unstructured

	for i := range resources {
		resource := &resources[i]
		if resource.GetKind() == "ConfigMap" {
			require.NotEqual(t, legacyGrafanaDashboardConfigMap, resource.GetName())
			if strings.HasSuffix(resource.GetName(), grafanaDashboardConfigMapSuffix) {
				require.LessOrEqual(t, len(resource.GetName()), 63)
				require.Regexp(t, "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", resource.GetName())
				require.NotContains(t, dashboardConfigMaps, resource.GetName())
				data, found, err := unstructured.NestedStringMap(resource.Object, "data")
				require.NoError(t, err)
				require.True(t, found)
				require.Len(t, data, 1)
				dashboardConfigMaps[resource.GetName()] = struct{}{}
			}
		}

		if resource.GetKind() == "Deployment" && resource.GetName() == grafanaDeploymentName {
			grafana = resource
		}
	}

	require.NotEmpty(t, dashboardConfigMaps)
	require.NotNil(t, grafana)

	return dashboardConfigMaps, grafana
}

func projectedDashboardConfigMaps(t *testing.T, grafana unstructured.Unstructured) map[string]struct{} {
	t.Helper()

	volumes, found, err := unstructured.NestedSlice(grafana.Object, "spec", "template", "spec", "volumes")
	require.NoError(t, err)
	require.True(t, found)

	projectedConfigMaps := make(map[string]struct{})
	dashboardVolumes := 0
	for _, item := range volumes {
		volume, ok := item.(map[string]interface{})
		require.True(t, ok)
		legacyConfigMap, found, err := unstructured.NestedString(volume, "configMap", "name")
		require.NoError(t, err)
		if found {
			require.NotEqual(t, legacyGrafanaDashboardConfigMap, legacyConfigMap)
		}
		if volume["name"] != grafanaDashboardVolumeName {
			continue
		}
		dashboardVolumes++

		sources, found, err := unstructured.NestedSlice(volume, "projected", "sources")
		require.NoError(t, err)
		require.True(t, found)
		for _, item := range sources {
			source, ok := item.(map[string]interface{})
			require.True(t, ok)
			configMap, found, err := unstructured.NestedMap(source, "configMap")
			require.NoError(t, err)
			require.True(t, found)
			name, found, err := unstructured.NestedString(configMap, "name")
			require.NoError(t, err)
			require.True(t, found)
			projectedConfigMaps[name] = struct{}{}
		}
	}

	require.Equal(t, 1, dashboardVolumes)
	require.NotEmpty(t, projectedConfigMaps)

	return projectedConfigMaps
}

func assertDashboardVolumeMount(t *testing.T, grafana unstructured.Unstructured) {
	t.Helper()

	containers, found, err := unstructured.NestedSlice(grafana.Object, "spec", "template", "spec", "containers")
	require.NoError(t, err)
	require.True(t, found)

	dashboardMounts := 0
	for _, item := range containers {
		container, ok := item.(map[string]interface{})
		require.True(t, ok)
		if container["name"] != "grafana" {
			continue
		}

		mounts, found, err := unstructured.NestedSlice(container, "volumeMounts")
		require.NoError(t, err)
		require.True(t, found)
		for _, item := range mounts {
			mount, ok := item.(map[string]interface{})
			require.True(t, ok)
			if mount["mountPath"] == grafanaDashboardMountPath {
				dashboardMounts++
				require.Equal(t, grafanaDashboardVolumeName, mount["name"])
				readOnly, found, err := unstructured.NestedBool(mount, "readOnly")
				require.NoError(t, err)
				require.True(t, found)
				require.True(t, readOnly)
			}
		}
	}

	require.Equal(t, 1, dashboardMounts)
}
