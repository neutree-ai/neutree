package v1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

func TestClusterProfileSchemeRegistration(t *testing.T) {
	s := scheme.NewScheme()
	require.NoError(t, AddToScheme(s))

	obj, err := s.New(ClusterProfileKind)
	require.NoError(t, err)
	assert.IsType(t, &ClusterProfile{}, obj)
	assert.Equal(t, ClusterProfileKind, obj.GetKind())
	assert.Empty(t, obj.GetWorkspace())

	list, err := s.NewList(ClusterProfileListKind)
	require.NoError(t, err)
	assert.IsType(t, &ClusterProfileList{}, list)

	tableObj, err := s.New("cluster_profiles")
	require.NoError(t, err)
	assert.IsType(t, &ClusterProfile{}, tableObj)
}

func TestClusterProfileJSONRoundTripPreservesCompleteComponents(t *testing.T) {
	input := &ClusterProfile{
		ID:         1,
		APIVersion: "v1",
		Kind:       ClusterProfileKind,
		Metadata:   &Metadata{Name: "v1.2.0-rc.1"},
		Spec: &ClusterProfileSpec{
			Components: map[string]ClusterProfileComponents{
				SSHClusterType: {
					RayRuntime:   ImageRef{Image: "neutree/neutree-serve", Tag: "v1.2.0-rc.1"},
					NodeAgent:    ImageRef{Image: "neutree/node-agent", Tag: "v1.2.0-rc.1"},
					NodeExporter: ImageRef{Image: "prom/node-exporter", Tag: "v1.8.2"},
					VMAgent:      ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.102.1"},
				},
				KubernetesClusterType: {
					KubernetesRuntime: ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.2.0-rc.1"},
					Router:            ImageRef{Image: "neutree/router", Tag: "v1.2.0-rc.1"},
					NodeAgent:         ImageRef{Image: "neutree/node-agent", Tag: "v1.2.0-rc.1"},
					NodeExporter:      ImageRef{Image: "prom/node-exporter", Tag: "v1.8.2"},
					VMAgent:           ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.102.1"},
					KubeStateMetrics:  ImageRef{Image: "kube-state-metrics/kube-state-metrics", Tag: "v2.13.0"},
				},
			},
		},
	}

	payload, err := json.Marshal(input)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"id": 1,
		"api_version": "v1",
		"kind": "ClusterProfile",
		"metadata": {"name": "v1.2.0-rc.1"},
		"spec": {
			"components": {
				"ssh": {
					"ray_runtime": {"image": "neutree/neutree-serve", "tag": "v1.2.0-rc.1"},
					"kubernetes_runtime": {},
					"router": {},
					"node_agent": {"image": "neutree/node-agent", "tag": "v1.2.0-rc.1"},
					"node_exporter": {"image": "prom/node-exporter", "tag": "v1.8.2"},
					"vmagent": {"image": "victoriametrics/vmagent", "tag": "v1.102.1"},
					"kube_state_metrics": {}
				},
				"kubernetes": {
					"ray_runtime": {},
					"kubernetes_runtime": {"image": "neutree/neutree-runtime", "tag": "v1.2.0-rc.1"},
					"router": {"image": "neutree/router", "tag": "v1.2.0-rc.1"},
					"node_agent": {"image": "neutree/node-agent", "tag": "v1.2.0-rc.1"},
					"node_exporter": {"image": "prom/node-exporter", "tag": "v1.8.2"},
					"vmagent": {"image": "victoriametrics/vmagent", "tag": "v1.102.1"},
					"kube_state_metrics": {"image": "kube-state-metrics/kube-state-metrics", "tag": "v2.13.0"}
				}
			}
		}
	}`, string(payload))

	var output ClusterProfile
	require.NoError(t, json.Unmarshal(payload, &output))
	require.NotNil(t, output.Spec)
	assert.Equal(t, "v1.2.0-rc.1", output.GetName())
	ssh, found := output.Spec.ComponentsFor(SSHClusterType)
	require.True(t, found)
	assert.Equal(t, "neutree/neutree-serve", ssh.RayRuntime.Image)
	kubernetes, found := output.Spec.ComponentsFor(KubernetesClusterType)
	require.True(t, found)
	assert.Equal(t, "neutree/neutree-runtime", kubernetes.KubernetesRuntime.Image)
	assert.Equal(t, "v2.13.0", kubernetes.KubeStateMetrics.Tag)
	_, found = output.Spec.ComponentsFor("docker")
	assert.False(t, found)
}

func TestClusterProfileAPIShapeExcludesLegacyReleaseInfoFields(t *testing.T) {
	profileType := reflect.TypeOf(ClusterProfile{})
	for _, field := range []string{"Status", "Workspace", "Revision", "Channel", "BuildIdentity", "UpgradeTo", "AcceleratorComponents"} {
		_, found := profileType.FieldByName(field)
		assert.False(t, found, "ClusterProfile must not expose %s", field)
	}

	specType := reflect.TypeOf(ClusterProfileSpec{})
	require.Equal(t, 1, specType.NumField())
	_, found := specType.FieldByName("ClusterType")
	assert.False(t, found, "ClusterProfile identity must be the exact version only")
	_, found = specType.FieldByName("Components")
	assert.True(t, found, "ClusterProfileSpec must expose typed components")

	imageRefType := reflect.TypeOf(ImageRef{})
	require.Equal(t, 2, imageRefType.NumField())
	for _, field := range []string{"Image", "Tag"} {
		_, found := imageRefType.FieldByName(field)
		assert.True(t, found, "ImageRef must expose %s", field)
	}
}

func TestClusterProfileSupportedTypes(t *testing.T) {
	assert.True(t, IsSupportedClusterType(SSHClusterType))
	assert.True(t, IsSupportedClusterType(KubernetesClusterType))
	assert.False(t, IsSupportedClusterType("docker"))
	assert.False(t, IsSupportedClusterType(""))
	assert.False(t, IsSupportedClusterType(" ssh "))
	assert.False(t, IsSupportedClusterType("kubernetes\n"))
}

func TestClusterProfileListSetItems(t *testing.T) {
	list := &ClusterProfileList{}
	list.SetItems([]scheme.Object{
		&ClusterProfile{ID: 1, Metadata: &Metadata{Name: "v1.2.0-rc.1"}},
		&ClusterProfile{ID: 2, Metadata: &Metadata{Name: "v1.2.0-rc.2"}},
	})

	require.Len(t, list.Items, 2)
	assert.Equal(t, "v1.2.0-rc.1", list.Items[0].GetName())
	assert.Equal(t, "v1.2.0-rc.2", list.Items[1].GetName())
}
