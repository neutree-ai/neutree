package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
)

func TestMetricsResourcesSplitNodeAgentsForExplicitRuntime(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "npu-node"},
			Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
				"example.com/ascend": resource.MustParse("1"),
			}},
		},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "other-npu-node",
				Labels: map[string]string{"example.com/product": "Atlas-other"},
			},
			Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
				"example.com/ascend": resource.MustParse("1"),
			}},
		},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cpu-node"}},
	).Build()

	profile := &v1.AcceleratorProfile{
		AcceleratorType: "ascend_npu",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			KubernetesProducts: []string{"Atlas-310P"},
			Privileged:         true,
			Capabilities:       &v1.NodeAgentRuntimeCapabilities{Add: []string{"SYS_ADMIN"}},
			Volumes: []v1.ComponentVolume{{
				Name:     "ascend-driver",
				HostPath: &v1.ComponentHostPathVolumeSource{Path: "/opt/ascend/driver", Type: v1.ComponentHostPathTypeDirectory},
			}},
			VolumeMounts: []v1.ComponentVolumeMount{{Name: "ascend-driver", MountPath: "/opt/ascend/driver"}},
		},
		MetricsExporter: &v1.AcceleratorExporterProfile{Env: map[string]string{"EXPORTER_ONLY": "must-not-leak"}},
	}
	acceleratorMgr := acceleratormocks.NewMockManager(t)
	acceleratorMgr.EXPECT().SupportPlugins().Return([]string{"ascend_npu"}).Twice()
	acceleratorMgr.EXPECT().GetAcceleratorProfile(mock.Anything, "ascend_npu").Return(profile, nil).Twice()
	acceleratorMgr.EXPECT().GetAllParsers().Return(map[string]resourceparser.ResourceParser{"ascend_npu": nodeAgentRuntimeParser{}}).Once()

	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec:     &v1.ClusterSpec{Version: "v1.1.0"},
		},
		namespace:       "test-namespace",
		imagePullSecret: "pull-secret",
		ctrlClient:      client,
		acceleratorMgr:  acceleratorMgr,
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)

	general := findMetricsDaemonSet(t, objects, "neutree-node-agent")
	adapter := findMetricsDaemonSet(t, objects, "neutree-node-agent-ascend-npu")

	assert.Equal(t, "node-agent", general.Labels[nodeAgentMetricsTargetLabel])
	assert.Equal(t, "node-agent", adapter.Labels[nodeAgentMetricsTargetLabel])
	assert.Contains(t, adapter.Spec.Template.Spec.Containers[0].Args, "--accelerator-type=ascend_npu")
	assert.NotContains(t, general.Spec.Template.Spec.Containers[0].Args, "--accelerator-type=ascend_npu")
	require.NotNil(t, adapter.Spec.Template.Spec.Containers[0].SecurityContext)
	assert.True(t, *adapter.Spec.Template.Spec.Containers[0].SecurityContext.Privileged)
	assert.Contains(t, adapter.Spec.Template.Spec.Containers[0].SecurityContext.Capabilities.Add, corev1.Capability("SYS_ADMIN"))
	assert.NotContains(t, nodeAgentEnvNames(adapter.Spec.Template.Spec.Containers[0].Env), "EXPORTER_ONLY")
	assert.NotContains(t, nodeAgentEnvNames(general.Spec.Template.Spec.Containers[0].Env), "EXPORTER_ONLY")
	assert.Contains(t, daemonSetHostPathNames(adapter), "ascend-driver")
	assert.NotContains(t, daemonSetHostPathNames(general), "ascend-driver")

	require.NotNil(t, adapter.Spec.Template.Spec.Affinity)
	require.NotNil(t, adapter.Spec.Template.Spec.Affinity.NodeAffinity)
	require.NotNil(t, general.Spec.Template.Spec.Affinity)
	require.NotNil(t, general.Spec.Template.Spec.Affinity.NodeAffinity)
	assert.Contains(t, daemonSetMatchFieldValues(adapter, corev1.NodeSelectorOpIn), "npu-node")
	assert.Contains(t, daemonSetMatchFieldValues(general, corev1.NodeSelectorOpNotIn), "npu-node")
	assert.NotContains(t, daemonSetMatchFieldValues(adapter, corev1.NodeSelectorOpIn), "other-npu-node")
	assert.NotContains(t, daemonSetMatchFieldValues(general, corev1.NodeSelectorOpNotIn), "other-npu-node")
}

func TestPlanNodeAgentsRejectsMultipleMatchingRuntimeProfiles(t *testing.T) {
	client := fake.NewClientBuilder().WithObjects(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "accelerator-node"},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			"example.com/first":  resource.MustParse("1"),
			"example.com/second": resource.MustParse("1"),
		}},
	}).Build()
	acceleratorMgr := acceleratormocks.NewMockManager(t)
	acceleratorMgr.EXPECT().SupportPlugins().Return([]string{"first_accelerator", "second_accelerator"}).Once()
	acceleratorMgr.EXPECT().GetAcceleratorProfile(mock.Anything, "first_accelerator").Return(&v1.AcceleratorProfile{
		AcceleratorType:  "first_accelerator",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{},
	}, nil).Once()
	acceleratorMgr.EXPECT().GetAcceleratorProfile(mock.Anything, "second_accelerator").Return(&v1.AcceleratorProfile{
		AcceleratorType:  "second_accelerator",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{},
	}, nil).Once()
	acceleratorMgr.EXPECT().GetAllParsers().Return(map[string]resourceparser.ResourceParser{
		"first_accelerator":  namedNodeAgentRuntimeParser{resourceName: "example.com/first", acceleratorType: "first_accelerator"},
		"second_accelerator": namedNodeAgentRuntimeParser{resourceName: "example.com/second", acceleratorType: "second_accelerator"},
	}).Once()

	component := &MetricsComponent{ctrlClient: client, acceleratorMgr: acceleratorMgr}
	_, err := component.planNodeAgents(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most one explicit NodeAgent runtime profile")
}

func TestCheckResourcesStatusAggregatesExplicitNodeAgents(t *testing.T) {
	client := fake.NewClientBuilder().WithObjects(
		readyMetricsDeployment("neutree-kube-state-metrics"),
		readyMetricsDaemonSet(nodeExporterDaemonSetName, 2),
		readyMetricsDaemonSet(neutreeNodeAgentMetricsName, 2),
		readyMetricsDaemonSet(neutreeNodeAgentMetricsName+"-ascend-npu", 2),
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "npu-node"},
			Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
				"example.com/ascend": resource.MustParse("1"),
			}},
		},
	).Build()
	profile := &v1.AcceleratorProfile{
		AcceleratorType: "ascend_npu",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			KubernetesProducts: []string{"Atlas-310P"},
		},
	}
	acceleratorMgr := acceleratormocks.NewMockManager(t)
	acceleratorMgr.EXPECT().SupportPlugins().Return([]string{"ascend_npu"}).Twice()
	acceleratorMgr.EXPECT().GetAcceleratorProfile(mock.Anything, "ascend_npu").Return(profile, nil).Twice()
	acceleratorMgr.EXPECT().GetAllParsers().Return(map[string]resourceparser.ResourceParser{"ascend_npu": nodeAgentRuntimeParser{}}).Once()

	component := &MetricsComponent{
		ctrlClient:     client,
		namespace:      "default",
		acceleratorMgr: acceleratorMgr,
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.1.0"},
		},
	}

	status, err := component.CheckResourcesStatus(context.Background())

	require.NoError(t, err)
	assert.True(t, status.NeutreeNodeAgentMetricsRequired)
	assert.True(t, status.NeutreeNodeAgentMetricsDaemonSetReady)
	assert.Equal(t, 4, status.NeutreeNodeAgentMetricsPodsReady)
	assert.Equal(t, 4, status.NeutreeNodeAgentMetricsTotalPods)
}

type nodeAgentRuntimeParser struct{}

func (nodeAgentRuntimeParser) ParseFromKubernetes(resources map[corev1.ResourceName]resource.Quantity, labels map[string]string) (*v1.ResourceInfo, error) {
	quantity, ok := resources["example.com/ascend"]
	if !ok || quantity.Value() <= 0 {
		return nil, nil
	}
	product := labels["example.com/product"]
	if product == "" {
		product = "Atlas-310P"
	}

	return &v1.ResourceInfo{AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
		"ascend_npu": {
			Quantity: 1,
			ProductGroups: map[v1.AcceleratorProduct]float64{
				v1.AcceleratorProduct(product): 1,
			},
		},
	}}, nil
}

func (nodeAgentRuntimeParser) ParseFromRay(map[string]float64) (*v1.ResourceInfo, error) {
	return nil, nil
}

type namedNodeAgentRuntimeParser struct {
	resourceName    corev1.ResourceName
	acceleratorType v1.AcceleratorType
}

func (p namedNodeAgentRuntimeParser) ParseFromKubernetes(resources map[corev1.ResourceName]resource.Quantity, _ map[string]string) (*v1.ResourceInfo, error) {
	quantity, ok := resources[p.resourceName]
	if !ok || quantity.Value() <= 0 {
		return nil, nil
	}

	return &v1.ResourceInfo{AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
		p.acceleratorType: {Quantity: 1},
	}}, nil
}

func (namedNodeAgentRuntimeParser) ParseFromRay(map[string]float64) (*v1.ResourceInfo, error) {
	return nil, nil
}

func nodeAgentEnvNames(env []corev1.EnvVar) []string {
	names := make([]string, 0, len(env))
	for _, item := range env {
		names = append(names, item.Name)
	}

	return names
}

func daemonSetHostPathNames(daemonSet *appsv1.DaemonSet) []string {
	names := make([]string, 0, len(daemonSet.Spec.Template.Spec.Volumes))
	for _, volume := range daemonSet.Spec.Template.Spec.Volumes {
		if volume.HostPath != nil {
			names = append(names, volume.Name)
		}
	}

	return names
}

func daemonSetMatchFieldValues(daemonSet *appsv1.DaemonSet, operator corev1.NodeSelectorOperator) []string {
	values := []string{}
	if daemonSet == nil || daemonSet.Spec.Template.Spec.Affinity == nil || daemonSet.Spec.Template.Spec.Affinity.NodeAffinity == nil || daemonSet.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return values
	}

	for _, term := range daemonSet.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, requirement := range term.MatchFields {
			if requirement.Key == "metadata.name" && requirement.Operator == operator {
				values = append(values, requirement.Values...)
			}
		}
	}

	return values
}
