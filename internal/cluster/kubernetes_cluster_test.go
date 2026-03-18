package cluster

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	plugin "github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/util"
)

func newNode(name string, schedulable bool, resources map[corev1.ResourceName]resource.Quantity, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Node",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: corev1.NodeSpec{
			Unschedulable: !schedulable,
		},
		Status: corev1.NodeStatus{
			Allocatable: resources,
		},
	}
}

func newPod(name, nodeName string, resources map[corev1.ResourceName]resource.Quantity, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: resources,
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
}

func TestNativeKubernetesCluster_CalculateResource(t *testing.T) {
	tests := []struct {
		name              string
		nodes             []client.Object
		pods              []client.Object
		expectedResources v1.ClusterResources
	}{
		{
			name: "Single node with NVIDIA GPU",
			nodes: []client.Object{
				newNode("node-1", true, map[corev1.ResourceName]resource.Quantity{
					plugin.NvidiaGPUKubernetesResource: resource.MustParse("2"),
					corev1.ResourceCPU:                 resource.MustParse("16"),
					corev1.ResourceMemory:              resource.MustParse("64Gi"),
				}, map[string]string{
					plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_A100",
				}),
			},
			pods: []client.Object{
				newPod("pod-1", "node-1", map[corev1.ResourceName]resource.Quantity{
					plugin.NvidiaGPUKubernetesResource: resource.MustParse("1"),
					corev1.ResourceCPU:                 resource.MustParse("4"),
					corev1.ResourceMemory:              resource.MustParse("16Gi"),
				}, corev1.PodRunning),
			},
			expectedResources: v1.ClusterResources{
				ResourceStatus: v1.ResourceStatus{
					Allocatable: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 2,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"NVIDIA_A100": 2,
								},
							},
						},
						CPU:    16,
						Memory: 64,
					},
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 1,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"NVIDIA_A100": 1,
								},
							},
						},
						CPU:    12,
						Memory: 48,
					},
				},
				NodeResources: map[string]*v1.ResourceStatus{
					"node-1": {
						Allocatable: &v1.ResourceInfo{
							AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
								v1.AcceleratorTypeNVIDIAGPU: {
									Quantity: 2,
									ProductGroups: map[v1.AcceleratorProduct]float64{
										"NVIDIA_A100": 2,
									},
								},
							},
							CPU:    16,
							Memory: 64,
						},
						Available: &v1.ResourceInfo{
							AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
								v1.AcceleratorTypeNVIDIAGPU: {
									Quantity: 1,
									ProductGroups: map[v1.AcceleratorProduct]float64{
										"NVIDIA_A100": 1,
									},
								},
							},
							CPU:    12,
							Memory: 48,
						},
					},
				},
			},
		},
		{
			name: "Single node with NVIDIA GPU, Pod is Succeeded",
			nodes: []client.Object{
				newNode("node-1", true, map[corev1.ResourceName]resource.Quantity{
					plugin.NvidiaGPUKubernetesResource: resource.MustParse("2"),
					corev1.ResourceCPU:                 resource.MustParse("16"),
					corev1.ResourceMemory:              resource.MustParse("64Gi"),
				}, map[string]string{
					plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_A100",
				}),
			},
			pods: []client.Object{
				newPod("pod-1", "node-1", map[corev1.ResourceName]resource.Quantity{
					plugin.NvidiaGPUKubernetesResource: resource.MustParse("1"),
					corev1.ResourceCPU:                 resource.MustParse("4"),
					corev1.ResourceMemory:              resource.MustParse("16Gi"),
				}, corev1.PodSucceeded), // Succeeded pod should not count against used resources
			},
			expectedResources: v1.ClusterResources{
				ResourceStatus: v1.ResourceStatus{
					Allocatable: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 2,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"NVIDIA_A100": 2,
								},
							},
						},
						CPU:    16,
						Memory: 64,
					},
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 2,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"NVIDIA_A100": 2,
								},
							},
						},
						CPU:    16,
						Memory: 64,
					},
				},
				NodeResources: map[string]*v1.ResourceStatus{
					"node-1": {
						Allocatable: &v1.ResourceInfo{
							AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
								v1.AcceleratorTypeNVIDIAGPU: {
									Quantity: 2,
									ProductGroups: map[v1.AcceleratorProduct]float64{
										"NVIDIA_A100": 2,
									},
								},
							},
							CPU:    16,
							Memory: 64,
						},
						Available: &v1.ResourceInfo{
							AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
								v1.AcceleratorTypeNVIDIAGPU: {
									Quantity: 2,
									ProductGroups: map[v1.AcceleratorProduct]float64{
										"NVIDIA_A100": 2,
									},
								},
							},
							CPU:    16,
							Memory: 64,
						},
					},
				},
			},
		},
		{
			name: "Single node with NVIDIA GPU, Node is unschedulable",
			nodes: []client.Object{
				newNode("node-1", false, map[corev1.ResourceName]resource.Quantity{
					plugin.NvidiaGPUKubernetesResource: resource.MustParse("2"),
					corev1.ResourceCPU:                 resource.MustParse("16"),
					corev1.ResourceMemory:              resource.MustParse("64Gi"),
				}, map[string]string{
					plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_A100",
				}),
			},
			pods: []client.Object{
				newPod("pod-1", "node-1", map[corev1.ResourceName]resource.Quantity{
					plugin.NvidiaGPUKubernetesResource: resource.MustParse("1"),
					corev1.ResourceCPU:                 resource.MustParse("4"),
					corev1.ResourceMemory:              resource.MustParse("16Gi"),
				}, corev1.PodSucceeded), // Succeeded pod should not count against used resources
			},
			expectedResources: v1.ClusterResources{
				ResourceStatus: v1.ResourceStatus{
					Allocatable: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{},
						CPU:               0,
						Memory:            0,
					},
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{},
						CPU:               0,
						Memory:            0,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &NativeKubernetesClusterReconciler{}
			acceleratorMgr := acceleratormocks.NewMockManager(t)
			acceleratorMgr.On("GetAllParsers").Return(map[string]plugin.ResourceParser{
				string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
			}).Maybe()
			cluster.acceleratorMgr = acceleratorMgr
			reconcileCtx := &ReconcileContext{
				Ctx: context.TODO(),
				ctrClient: fake.NewClientBuilder().
					WithScheme(scheme.Scheme).
					WithObjects(tt.nodes...).
					WithObjects(tt.pods...).
					WithIndex(&corev1.Pod{}, "status.phase", func(obj client.Object) []string {
						pod := obj.(*corev1.Pod)
						return []string{string(pod.Status.Phase)}
					}).
					Build(),
			}

			resources, err := cluster.calculateResources(reconcileCtx)
			require.NoError(t, err)
			equal, diff, err := util.JsonEqual(resources, tt.expectedResources)
			require.NoError(t, err)
			require.True(t, equal, "expected resources do not match actual resources: %s", diff)
		})
	}
}

func TestComputeAdditionalComponents_Metrics(t *testing.T) {
	cluster := &NativeKubernetesClusterReconciler{}

	reconcileCtx := &ReconcileContext{
		Cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "default",
			},
		},
		kubernetesClusterConfig: &v1.KubernetesClusterConfig{},
	}

	imagePrefix := "test-prefix/"

	tests := []struct {
		name                   string
		metricsRemoteWriteURL  string
		expectedReconcileCount int
		expectedDeleteCount    int
	}{
		{
			name:                   "Valid HTTP URL for metrics",
			metricsRemoteWriteURL:  "http://example.com/metrics",
			expectedReconcileCount: 1,
			expectedDeleteCount:    0,
		},
		{
			name:                   "URL without HTTP/HTTPS scheme for metrics",
			metricsRemoteWriteURL:  "invalid-url",
			expectedReconcileCount: 0,
			expectedDeleteCount:    1,
		},
		{
			name:                   "Empty URL for metrics",
			metricsRemoteWriteURL:  "",
			expectedReconcileCount: 0,
			expectedDeleteCount:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster.metricsRemoteWriteURL = tt.metricsRemoteWriteURL

			reconcileComps, deleteComps := cluster.ComputeAdditionalComponents(reconcileCtx, imagePrefix)

			if len(reconcileComps) != tt.expectedReconcileCount {
				t.Errorf("expected %d reconcile components, got %d", tt.expectedReconcileCount, len(reconcileComps))
			}
			if len(deleteComps) != tt.expectedDeleteCount {
				t.Errorf("expected %d delete components, got %d", tt.expectedDeleteCount, len(deleteComps))
			}
		})
	}
}
