package hami

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestPlanNodeScopePatchesOnlyUnsetCandidateNodes(t *testing.T) {
	nodes := []corev1.Node{
		newScopeNode("gpu-unset", map[string]string{}),
		newScopeNode("gpu-enabled", map[string]string{plugin.NvidiaGPUVirtualizationLabelKey: "true"}),
		newScopeNode("gpu-disabled", map[string]string{plugin.NvidiaGPUVirtualizationLabelKey: "false"}),
		newScopeNode("stale-enabled", map[string]string{plugin.NvidiaGPUVirtualizationLabelKey: "true"}),
	}

	plan := PlanNodeScope(nodes, []string{"gpu-unset", "gpu-enabled", "gpu-disabled"}, defaultNodeScopeLabel(), true)

	assert.Equal(t, []string{"gpu-unset"}, plan.PatchedNodes)
	assert.Equal(t, []string{"gpu-enabled"}, plan.EnabledNodes)
	assert.Equal(t, []string{"gpu-disabled"}, plan.DisabledNodes)
	assert.Equal(t, []string{"stale-enabled"}, plan.StaleEnabledNodes)
	assert.Equal(t, map[string]string{plugin.NvidiaGPUVirtualizationLabelKey: "true"}, plan.Patches["gpu-unset"])
}

func TestPlanNodeScopeDoesNotPatchWhenVirtualizationDisabled(t *testing.T) {
	nodes := []corev1.Node{
		newScopeNode("gpu-unset", map[string]string{}),
	}

	plan := PlanNodeScope(nodes, []string{"gpu-unset"}, defaultNodeScopeLabel(), false)

	assert.Empty(t, plan.PatchedNodes)
	assert.Empty(t, plan.Patches)
}

func TestMergeVirtualizationConfigsSelectsOnlyEligibleOwner(t *testing.T) {
	template := &accelerator.DevicePluginTemplate{Manifest: "apiVersion: v1\nkind: ConfigMap\n"}
	owner := &accelerator.VirtualizationConfig{
		Supported:      true,
		CandidateNodes: []string{"owner-node"},
		NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
			Key:           "example.com/owner-enabled",
			EnabledValue:  "enabled",
			DisabledValue: "disabled",
		},
		DevicePluginTemplate: template,
		ConfigPatch: map[string]interface{}{
			"devices": map[string]interface{}{"owner": true},
		},
	}
	configs := []*accelerator.VirtualizationConfig{
		{
			Supported:       true,
			BlockingReasons: []string{"supported provider has no candidate nodes"},
			ConfigPatch:     map[string]interface{}{"devices": map[string]interface{}{"nonOwner": true}},
		},
		owner,
		{
			Supported:       false,
			BlockingReasons: []string{"provider does not support virtualization"},
		},
	}

	got, err := mergeVirtualizationConfigs(configs)

	require.NoError(t, err)
	assert.Same(t, owner, got)
	assert.Same(t, template, got.DevicePluginTemplate)
	assert.Equal(t, owner.ConfigPatch, got.ConfigPatch)
	require.NoError(t, virtualizationConfigBlocked(got))
}

func TestMergeVirtualizationConfigsRejectsInvalidOwnerCount(t *testing.T) {
	candidateOwner := func(node string) *accelerator.VirtualizationConfig {
		return &accelerator.VirtualizationConfig{
			Supported:      true,
			CandidateNodes: []string{node},
		}
	}
	tests := []struct {
		name            string
		configs         []*accelerator.VirtualizationConfig
		expected        string
		expectedReasons []string
	}{
		{
			name:     "no registered configs",
			configs:  nil,
			expected: "exactly one accelerator plugin must own HAMi virtualization, found 0",
		},
		{
			name: "zero owners retains provider reasons",
			configs: []*accelerator.VirtualizationConfig{
				{
					Supported:       true,
					BlockingReasons: []string{"provider one has no accelerator nodes"},
				},
				{
					Supported:       false,
					BlockingReasons: []string{"provider two does not support virtualization"},
				},
				nil,
			},
			expected: "exactly one accelerator plugin must own HAMi virtualization, found 0",
			expectedReasons: []string{
				"provider one has no accelerator nodes",
				"provider two does not support virtualization",
			},
		},
		{
			name: "multiple owners",
			configs: []*accelerator.VirtualizationConfig{
				candidateOwner("accelerator-node-one"),
				candidateOwner("accelerator-node-two"),
			},
			expected: "exactly one accelerator plugin must own HAMi virtualization, found 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mergeVirtualizationConfigs(tt.configs)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expected)
			for _, reason := range tt.expectedReasons {
				assert.Contains(t, err.Error(), reason)
			}
		})
	}
}

func TestHAMiPlanNodeScopePropagatesSelectedOwnerConfiguration(t *testing.T) {
	const scopeLabelKey = "example.com/owner-enabled"
	template := &accelerator.DevicePluginTemplate{Manifest: "apiVersion: v1\nkind: ConfigMap\n"}
	configPatch := map[string]interface{}{
		"devices": map[string]interface{}{"owner": true},
	}
	owner := fakeAcceleratorPlugin{
		acceleratorType: "owner",
		config: &accelerator.VirtualizationConfig{
			Supported:      true,
			CandidateNodes: []string{"owner-node"},
			NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
				Key:           scopeLabelKey,
				EnabledValue:  "enabled",
				DisabledValue: "disabled",
			},
			DevicePluginTemplate: template,
			ConfigPatch:          configPatch,
		},
	}
	nonOwner := fakeAcceleratorPlugin{
		acceleratorType: "non-owner",
		config: &accelerator.VirtualizationConfig{
			Supported:       true,
			BlockingReasons: []string{"no candidates for non-owner"},
			ConfigPatch:     map[string]interface{}{"devices": map[string]interface{}{"nonOwner": true}},
		},
	}
	provider := fakePluginProvider{
		plugins: map[string]plugin.AcceleratorPlugin{
			"non-owner": nonOwner,
			"owner":     owner,
		},
		supportedPlugins: []string{"non-owner", "owner"},
	}
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, nil, provider)

	plan, err := component.planNodeScope(context.Background(), []corev1.Node{
		*newHAMiNode("owner-node", map[string]string{}),
	}, true)

	require.NoError(t, err)
	assert.Equal(t, NodeScopeLabel{
		Key:           scopeLabelKey,
		EnabledValue:  "enabled",
		DisabledValue: "disabled",
	}, plan.NodeScopeLabel)
	assert.Equal(t, map[string]map[string]string{
		"owner-node": {scopeLabelKey: "enabled"},
	}, plan.Patches)
	assert.Equal(t, configPatch, plan.ConfigPatch)
	assert.Same(t, template, plan.DevicePluginTemplate)
}

func TestHAMiPlanNodeScopeCarriesVirtualizationMode(t *testing.T) {
	owner := fakeAcceleratorPlugin{
		acceleratorType: "owner",
		config: &accelerator.VirtualizationConfig{
			Supported:      true,
			CandidateNodes: []string{"owner-node"},
			NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
				Key:           "example.com/owner-enabled",
				EnabledValue:  "enabled",
				DisabledValue: "disabled",
			},
			Mode:               v1.AcceleratorVirtualizationModeTemplate,
			DefaultMode:        v1.AcceleratorVirtualizationModeTemplate,
			SupportedModes:     []v1.AcceleratorVirtualizationMode{v1.AcceleratorVirtualizationModeTemplate},
			SupportedResources: []string{v1.AcceleratorVirtualizationMemoryMiBKey},
		},
	}
	provider := fakePluginProvider{
		plugins:          map[string]plugin.AcceleratorPlugin{"owner": owner},
		supportedPlugins: []string{"owner"},
	}
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, nil, provider)

	plan, err := component.planNodeScope(context.Background(), []corev1.Node{
		*newHAMiNode("owner-node", map[string]string{}),
	}, true)

	require.NoError(t, err)
	assert.Equal(t, v1.AcceleratorVirtualizationModeTemplate, plan.Mode)
	assert.Equal(t, []string{v1.AcceleratorVirtualizationMemoryMiBKey}, plan.SupportedResources)
}

func TestHAMiDisableNodeScopeCleansAllSupportedProviderLabelsWithoutSelectingOwner(t *testing.T) {
	const (
		firstLabelKey       = "example.com/first-virtualization-enabled"
		secondLabelKey      = "example.com/second-virtualization-enabled"
		unsupportedLabelKey = "example.com/unsupported-enabled"
	)
	managed := newHAMiNode("managed", map[string]string{
		firstLabelKey:       "enabled",
		secondLabelKey:      "on",
		unsupportedLabelKey: "true",
		"example.com/keep":  "value",
	})
	managed.Annotations = map[string]string{
		hamiNodeLockAnnotation: "locked",
		"example.com/keep":     "value",
	}
	disabled := newHAMiNode("disabled", map[string]string{
		firstLabelKey:  "disabled",
		secondLabelKey: "off",
	})
	annotationOnly := newHAMiNode("annotation-only", map[string]string{})
	annotationOnly.Annotations = map[string]string{
		hamiNodeNVIDIARegisterAnnotation: "registered",
	}
	fakeClient := newHAMiFakeClient(t, managed, disabled, annotationOnly)
	provider := fakePluginProvider{
		plugins: map[string]plugin.AcceleratorPlugin{
			"first": fakeAcceleratorPlugin{
				acceleratorType: "first",
				config: &accelerator.VirtualizationConfig{
					Supported:      true,
					CandidateNodes: []string{"first-node"},
					NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
						Key:           firstLabelKey,
						EnabledValue:  "enabled",
						DisabledValue: "disabled",
					},
				},
			},
			"second": fakeAcceleratorPlugin{
				acceleratorType: "second",
				config: &accelerator.VirtualizationConfig{
					Supported:      true,
					CandidateNodes: []string{"second-node"},
					NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
						Key:           secondLabelKey,
						EnabledValue:  "on",
						DisabledValue: "off",
					},
				},
			},
			"unsupported": fakeAcceleratorPlugin{
				acceleratorType: "unsupported",
				config: &accelerator.VirtualizationConfig{
					Supported: false,
					NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
						Key:           unsupportedLabelKey,
						EnabledValue:  "true",
						DisabledValue: "false",
					},
				},
			},
		},
		supportedPlugins: []string{"first", "second", "unsupported"},
	}
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient, provider)

	err := component.DisableNodeScope(context.Background())

	require.NoError(t, err)
	gotManaged := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "managed"}, gotManaged))
	assert.NotContains(t, gotManaged.Labels, firstLabelKey)
	assert.NotContains(t, gotManaged.Labels, secondLabelKey)
	assert.Equal(t, "true", gotManaged.Labels[unsupportedLabelKey])
	assert.Equal(t, "value", gotManaged.Labels["example.com/keep"])
	assert.NotContains(t, gotManaged.Annotations, hamiNodeLockAnnotation)
	assert.Equal(t, "value", gotManaged.Annotations["example.com/keep"])

	gotDisabled := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "disabled"}, gotDisabled))
	assert.Equal(t, "disabled", gotDisabled.Labels[firstLabelKey])
	assert.Equal(t, "off", gotDisabled.Labels[secondLabelKey])

	gotAnnotationOnly := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "annotation-only"}, gotAnnotationOnly))
	assert.NotContains(t, gotAnnotationOnly.Annotations, hamiNodeNVIDIARegisterAnnotation)
}

func TestSupportedNodeScopeLabelsReturnsDistinctValidLabels(t *testing.T) {
	configs := []*accelerator.VirtualizationConfig{
		{
			Supported: true,
			NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
				Key:           "example.com/first",
				EnabledValue:  "on",
				DisabledValue: "off",
			},
		},
		nil,
		{
			Supported: false,
			NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
				Key: "example.com/unsupported",
			},
		},
		{Supported: true},
		{
			Supported: true,
			NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
				Key:           "example.com/first",
				EnabledValue:  "different",
				DisabledValue: "different",
			},
		},
		{
			Supported: true,
			NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
				Key: "example.com/defaulted",
			},
		},
	}

	labels := supportedNodeScopeLabels(configs)

	assert.Equal(t, []NodeScopeLabel{
		{Key: "example.com/first", EnabledValue: "on", DisabledValue: "off"},
		{Key: "example.com/defaulted", EnabledValue: "true", DisabledValue: "false"},
	}, labels)
}

func newScopeNode(name string, labels map[string]string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}
