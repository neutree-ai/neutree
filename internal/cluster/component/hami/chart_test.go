package hami

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubeversion "k8s.io/apimachinery/pkg/version"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestEmbeddedHAMiChartVersion(t *testing.T) {
	hamiChart, err := loadEmbeddedHAMiChart()
	require.NoError(t, err)
	assert.Equal(t, "2.10.0", hamiChart.Metadata.Version)
	assert.Equal(t, "2.10.0", hamiChart.Metadata.AppVersion)
	assert.Empty(t, hamiChart.Metadata.Dependencies)
	assert.NotContains(t, hamiChart.Values, "dra")
	assert.NotContains(t, defaultChartValues(""), "dra")
	assert.NotContains(t, chartValuesFromYAML(protectedChartValuesYAML), "dra")
}

func TestHAMiChartProtectedImageVersions(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "docker.io",
		"", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))
	values := component.buildChartValues(NodeScopePlan{ConfigPatch: map[string]interface{}{
		"scheduler": map[string]interface{}{
			"extender": map[string]interface{}{
				"image": map[string]interface{}{"tag": "unexpected"},
			},
		},
		"devicePlugin": map[string]interface{}{
			"image": map[string]interface{}{"tag": "unexpected"},
			"monitor": map[string]interface{}{
				"image": map[string]interface{}{"tag": "unexpected"},
			},
		},
	}})

	assert.Equal(t, "v2.10.0", nestedMap(t, values, "scheduler", "extender", "image")["tag"])
	assert.Equal(t, "v2.10.0", nestedMap(t, values, "devicePlugin", "image")["tag"])
	assert.Equal(t, "v2.10.0", nestedMap(t, values, "devicePlugin", "monitor", "image")["tag"])
	assert.Equal(t, "v2.10.0", (HAMiStatus{Ready: true}).ComponentStatus().Version)
}

func TestHAMiChartKubernetesVersionCompatibility(t *testing.T) {
	originalGetKubernetesServerVersion := getKubernetesServerVersion
	t.Cleanup(func() {
		getKubernetesServerVersion = originalGetKubernetesServerVersion
	})

	for minor, schedulerVersion := range KubeSchedulerVersionsByMinor {
		t.Run(minor, func(t *testing.T) {
			getKubernetesServerVersion = func(*v1.Cluster) (*kubeversion.Info, error) {
				return &kubeversion.Info{GitVersion: schedulerVersion}, nil
			}
			component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
				"", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))
			resources, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())
			require.NoError(t, err)

			scheduler := findContainer(t, resources.Items, "Deployment", SchedulerName, "kube-scheduler")
			assert.Equal(t, "registry.example.com/neutree/kube-scheduler:"+schedulerVersion, scheduler["image"])
			assert.Contains(t, stringSlice(scheduler["command"]), "--config=/config/config.yaml")

			configMap := findObject(t, resources.Items, "ConfigMap", SchedulerName)
			config, found, err := unstructured.NestedString(configMap.Object, "data", "config.yaml")
			require.NoError(t, err)
			require.True(t, found)
			assert.Contains(t, config, "apiVersion: kubescheduler.config.k8s.io/v1\n")
			assert.Contains(t, config, "schedulerName: hami-scheduler")

			for _, resource := range resources.Items {
				assert.NotContains(t, strings.ToLower(resource.GetName()), "dra")
				assert.NotEqual(t, "ResourceClaim", resource.GetKind())
				assert.NotEqual(t, "ResourceSlice", resource.GetKind())
			}
		})
	}
}

func TestHAMiChartDeviceConfigContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "upstream defaults"},
		{name: "custom device config", content: "nvidia:\n  resourceCountName: nvidia.com/custom-gpu"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			values := defaultChartValues("")
			if testCase.content != "" {
				values = mergeChartValues(values, map[string]interface{}{
					"device-config": map[string]interface{}{"content": testCase.content},
				})
			}

			resources, err := renderEmbeddedHAMiChart(values, "neutree-system", kubeVersionFromBaseVersion("v1.32.13"))
			require.NoError(t, err)
			configMap := findObject(t, resources.Items, "ConfigMap", "hami-scheduler-device")
			content, found, err := unstructured.NestedString(configMap.Object, "data", "device-config.yaml")
			require.NoError(t, err)
			require.True(t, found)

			if testCase.content != "" {
				assert.Equal(t, testCase.content, strings.TrimSpace(content))
			} else {
				assert.Contains(t, content, "enableNumaTopology: false")
				assert.Contains(t, content, "migProfileAllowlist:")
				assert.NotContains(t, content, "knownMigGeometries:")
			}
		})
	}
}
