package cluster

import (
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	plugin "github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/deploy"
	"github.com/neutree-ai/neutree/internal/util"
)

func newNode(name string, schedulable bool, resources map[corev1.ResourceName]resource.Quantity, labels map[string]string) *corev1.Node {
	return newNodeWithAnnotations(name, schedulable, resources, labels, nil)
}

func newNodeWithAnnotations(
	name string,
	schedulable bool,
	resources map[corev1.ResourceName]resource.Quantity,
	labels map[string]string,
	annotations map[string]string,
) *corev1.Node {
	return &corev1.Node{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Node",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
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
	return newPodWithAnnotations(name, nodeName, resources, nil, phase)
}

func newPodWithAnnotations(
	name,
	nodeName string,
	resources map[corev1.ResourceName]resource.Quantity,
	annotations map[string]string,
	phase corev1.PodPhase,
) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
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
		name                             string
		acceleratorVirtualizationEnabled bool
		nodes                            []client.Object
		pods                             []client.Object
		expectedResources                v1.ClusterResources
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
				NodeResources: map[string]*v1.NodeResourceStatus{
					"node-1": {
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
				NodeResources: map[string]*v1.NodeResourceStatus{
					"node-1": {
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
		{
			name:                             "Single node with Neutree NVIDIA vGPU resources",
			acceleratorVirtualizationEnabled: true,
			nodes: []client.Object{
				newNodeWithAnnotations("node-1", true, map[corev1.ResourceName]resource.Quantity{
					plugin.NvidiaGPUKubernetesResource: resource.MustParse("20"),
					corev1.ResourceCPU:                 resource.MustParse("16"),
					corev1.ResourceMemory:              resource.MustParse("64Gi"),
				}, map[string]string{
					plugin.NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
					plugin.NvidiaGPUVirtualizationLabelKey:    "true",
					plugin.NvidiaGPUCountResource:             "2",
				}, map[string]string{
					resourceparser.NeutreeAcceleratorDevicesAnnotation: `[
						{"uuid":"GPU-1","product_model":"Tesla-T4","memory_mib":15360,"healthy":true},
						{"uuid":"GPU-2","product_model":"Tesla-T4","memory_mib":15360,"healthy":true}
					]`,
				}),
			},
			pods: []client.Object{
				newPodWithAnnotations("pod-1", "node-1", map[corev1.ResourceName]resource.Quantity{
					plugin.NvidiaGPUKubernetesResource:       resource.MustParse("1"),
					plugin.NvidiaGPUMemoryPercentageResource: resource.MustParse("100"),
					plugin.NvidiaGPUCoreResource:             resource.MustParse("100"),
					corev1.ResourceCPU:                       resource.MustParse("4"),
					corev1.ResourceMemory:                    resource.MustParse("16Gi"),
				}, map[string]string{
					resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
						{"uuid":"GPU-1","product":"Tesla-T4","memory_mib":15360,"core_units":100}
					]`,
				}, corev1.PodRunning),
			},
			expectedResources: v1.ClusterResources{
				AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
					v1.AcceleratorTypeNVIDIAGPU: {
						Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{
							"Tesla-T4": {
								MemoryTotalMiB: 15360,
							},
						},
					},
				},
				ResourceStatus: v1.ResourceStatus{
					Allocatable: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 2,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"Tesla-T4": 2,
								},
								Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
									"Tesla-T4": {
										Quantity: 2,
										Virtualization: &v1.AcceleratorVirtualizationResource{
											MemoryMiB: 30720,
											CoreUnits: 200,
										},
									},
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
									"Tesla-T4": 1,
								},
								Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
									"Tesla-T4": {
										Quantity: 1,
										Virtualization: &v1.AcceleratorVirtualizationResource{
											MemoryMiB: 15360,
											CoreUnits: 100,
										},
									},
								},
							},
						},
						CPU:    12,
						Memory: 48,
					},
				},
				NodeResources: map[string]*v1.NodeResourceStatus{
					"node-1": {
						ResourceStatus: v1.ResourceStatus{
							Allocatable: &v1.ResourceInfo{
								AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
									v1.AcceleratorTypeNVIDIAGPU: {
										Quantity: 2,
										ProductGroups: map[v1.AcceleratorProduct]float64{
											"Tesla-T4": 2,
										},
										Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
											"Tesla-T4": {
												Quantity: 2,
												Virtualization: &v1.AcceleratorVirtualizationResource{
													MemoryMiB: 30720,
													CoreUnits: 200,
												},
											},
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
											"Tesla-T4": 1,
										},
										Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
											"Tesla-T4": {
												Quantity: 1,
												Virtualization: &v1.AcceleratorVirtualizationResource{
													MemoryMiB: 15360,
													CoreUnits: 100,
												},
											},
										},
									},
								},
								CPU:    12,
								Memory: 48,
							},
						},
						Devices: []*v1.DeviceResource{
							{
								UUID:    "GPU-1",
								Product: "Tesla-T4",
								Health:  true,
								Allocatable: &v1.DeviceResourcePool{
									MemoryMiB: 15360,
									CoreUnits: 100,
								},
								Available: &v1.DeviceResourcePool{
									MemoryMiB: 0,
									CoreUnits: 0,
								},
							},
							{
								UUID:    "GPU-2",
								Product: "Tesla-T4",
								Health:  true,
								Allocatable: &v1.DeviceResourcePool{
									MemoryMiB: 15360,
									CoreUnits: 100,
								},
								Available: &v1.DeviceResourcePool{
									MemoryMiB: 15360,
									CoreUnits: 100,
								},
							},
						},
					},
				},
			},
		},
		{
			name:                             "NVIDIA node without Neutree annotations uses standard GPU resources",
			acceleratorVirtualizationEnabled: true,
			nodes: []client.Object{
				newNode("node-1", true, map[corev1.ResourceName]resource.Quantity{
					plugin.NvidiaGPUKubernetesResource: resource.MustParse("20"),
					corev1.ResourceCPU:                 resource.MustParse("16"),
					corev1.ResourceMemory:              resource.MustParse("64Gi"),
				}, map[string]string{
					plugin.NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
					plugin.NvidiaGPUVirtualizationLabelKey:    "true",
				}),
			},
			expectedResources: v1.ClusterResources{
				ResourceStatus: v1.ResourceStatus{
					Allocatable: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 20,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"Tesla-T4": 20,
								},
							},
						},
						CPU:    16,
						Memory: 64,
					},
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 20,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"Tesla-T4": 20,
								},
							},
						},
						CPU:    16,
						Memory: 64,
					},
				},
				NodeResources: map[string]*v1.NodeResourceStatus{
					"node-1": {
						ResourceStatus: v1.ResourceStatus{
							Allocatable: &v1.ResourceInfo{
								AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
									v1.AcceleratorTypeNVIDIAGPU: {
										Quantity: 20,
										ProductGroups: map[v1.AcceleratorProduct]float64{
											"Tesla-T4": 20,
										},
									},
								},
								CPU:    16,
								Memory: 64,
							},
							Available: &v1.ResourceInfo{
								AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
									v1.AcceleratorTypeNVIDIAGPU: {
										Quantity: 20,
										ProductGroups: map[v1.AcceleratorProduct]float64{
											"Tesla-T4": 20,
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
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &NativeKubernetesClusterReconciler{}
			acceleratorMgr := acceleratormocks.NewMockManager(t)
			acceleratorMgr.On("GetAllParsers").Return(map[string]resourceparser.ResourceParser{
				string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
			}).Maybe()
			cluster.acceleratorMgr = acceleratorMgr
			reconcileCtx := &ReconcileContext{
				Ctx: context.TODO(),
				Cluster: &v1.Cluster{
					Spec: &v1.ClusterSpec{
						AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
							Enabled: tt.acceleratorVirtualizationEnabled,
						},
					},
				},
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
			expectedDeleteCount:    1,
		},
		{
			name:                   "URL without HTTP/HTTPS scheme for metrics",
			metricsRemoteWriteURL:  "invalid-url",
			expectedReconcileCount: 0,
			expectedDeleteCount:    2,
		},
		{
			name:                   "Empty URL for metrics",
			metricsRemoteWriteURL:  "",
			expectedReconcileCount: 0,
			expectedDeleteCount:    2,
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

func TestComputeAdditionalComponents_HAMi(t *testing.T) {
	reconciler := &NativeKubernetesClusterReconciler{}
	reconcileCtx := &ReconcileContext{
		Cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "default",
			},
			Spec: &v1.ClusterSpec{
				Type: v1.KubernetesClusterType,
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
					Enabled: true,
				},
			},
		},
		kubernetesClusterConfig: &v1.KubernetesClusterConfig{},
	}

	reconcileComps, deleteComps := reconciler.ComputeAdditionalComponents(reconcileCtx, "test-prefix/")

	if len(reconcileComps) != 1 {
		t.Fatalf("expected accelerator virtualization component to be reconciled, got %d components", len(reconcileComps))
	}
	if len(deleteComps) != 1 {
		t.Fatalf("expected metrics component to be deleted when metrics URL is empty, got %d components", len(deleteComps))
	}
}

func TestKubernetesReconcileDeleteCleansAcceleratorVirtualizationNodeScope(t *testing.T) {
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "default"},
		Spec: &v1.ClusterSpec{
			Type: v1.KubernetesClusterType,
			AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
				Enabled: true,
			},
		},
	}
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: util.ClusterNamespace(cluster),
		},
	}
	gpuNode := newNodeWithAnnotations("gpu-node", true, nil, map[string]string{
		plugin.NvidiaGPUVirtualizationLabelKey: "true",
	}, map[string]string{
		"hami.io/node-nvidia-register":                     `[{"id":"GPU-delete-path"}]`,
		resourceparser.NeutreeAcceleratorDevicesAnnotation: `[{"uuid":"GPU-delete-path"}]`,
	})
	metricsClusterRole := newUnstructuredObject("rbac.authorization.k8s.io/v1", "ClusterRole",
		"", "vmagent-node-reader-test")
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(namespace, gpuNode, metricsClusterRole).
		Build()
	setLastAppliedConfig(t, fakeClient, namespace.Name, cluster.Metadata.Name, "metrics",
		[]unstructured.Unstructured{*metricsClusterRole})
	acceleratorMgr := acceleratormocks.NewMockManager(t)
	acceleratorMgr.On("SupportPlugins").Return([]string{string(v1.AcceleratorTypeNVIDIAGPU)})
	acceleratorMgr.On("GetPlugin", string(v1.AcceleratorTypeNVIDIAGPU)).
		Return(testVirtualizationPlugin{}, true)
	reconciler := &NativeKubernetesClusterReconciler{
		acceleratorMgr: acceleratorMgr,
	}
	reconcileCtx := &ReconcileContext{
		Ctx:                     context.TODO(),
		Cluster:                 cluster,
		clusterNamespace:        util.ClusterNamespace(cluster),
		kubernetesClusterConfig: &v1.KubernetesClusterConfig{},
		ctrClient:               fakeClient,
	}

	err := reconciler.reconcileDelete(reconcileCtx)

	require.ErrorContains(t, err, "metrics resources are not fully deleted")

	gotNode := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.TODO(), client.ObjectKey{Name: "gpu-node"}, gotNode))
	require.NotContains(t, gotNode.Labels, plugin.NvidiaGPUVirtualizationLabelKey)
	require.NotContains(t, gotNode.Annotations, "hami.io/node-nvidia-register")
	require.NotContains(t, gotNode.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)

	gotClusterRole := newUnstructuredObject("rbac.authorization.k8s.io/v1", "ClusterRole",
		"", "vmagent-node-reader-test")
	require.Error(t, fakeClient.Get(context.TODO(), client.ObjectKey{Name: "vmagent-node-reader-test"}, gotClusterRole))

	gotNamespace := &corev1.Namespace{}
	require.NoError(t, fakeClient.Get(context.TODO(), client.ObjectKey{Name: namespace.Name}, gotNamespace))

	err = reconciler.reconcileDelete(reconcileCtx)

	require.ErrorContains(t, err, "waiting for namespace deletion")
	require.Error(t, fakeClient.Get(context.TODO(), client.ObjectKey{Name: namespace.Name}, gotNamespace))
}

func TestKubernetesReconcileDeleteCleansAcceleratorVirtualizationNodeScopeFromStatus(t *testing.T) {
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "default"},
		Spec: &v1.ClusterSpec{
			Type: v1.KubernetesClusterType,
			AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
				Enabled: false,
			},
		},
		Status: &v1.ClusterStatus{
			ComponentStatus: map[string]*v1.ComponentStatus{
				v1.ComponentStatusAcceleratorVirtualizationKey: {
					Phase:   v1.ComponentPhaseReady,
					Managed: true,
				},
			},
		},
	}
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: util.ClusterNamespace(cluster),
		},
	}
	gpuNode := newNodeWithAnnotations("gpu-node", true, nil, map[string]string{
		plugin.NvidiaGPUVirtualizationLabelKey: "true",
	}, map[string]string{
		"hami.io/node-nvidia-register":                     `[{"id":"GPU-delete-status"}]`,
		resourceparser.NeutreeAcceleratorDevicesAnnotation: `[{"uuid":"GPU-delete-status"}]`,
	})
	metricsClusterRole := newUnstructuredObject("rbac.authorization.k8s.io/v1", "ClusterRole",
		"", "vmagent-node-reader-test")
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(namespace, gpuNode, metricsClusterRole).
		Build()
	setLastAppliedConfig(t, fakeClient, namespace.Name, cluster.Metadata.Name, "metrics",
		[]unstructured.Unstructured{*metricsClusterRole})
	acceleratorMgr := acceleratormocks.NewMockManager(t)
	acceleratorMgr.On("SupportPlugins").Return([]string{string(v1.AcceleratorTypeNVIDIAGPU)})
	acceleratorMgr.On("GetPlugin", string(v1.AcceleratorTypeNVIDIAGPU)).
		Return(testVirtualizationPlugin{}, true)
	reconciler := &NativeKubernetesClusterReconciler{
		acceleratorMgr: acceleratorMgr,
	}
	reconcileCtx := &ReconcileContext{
		Ctx:                     context.TODO(),
		Cluster:                 cluster,
		clusterNamespace:        util.ClusterNamespace(cluster),
		kubernetesClusterConfig: &v1.KubernetesClusterConfig{},
		ctrClient:               fakeClient,
	}

	err := reconciler.reconcileDelete(reconcileCtx)

	require.ErrorContains(t, err, "metrics resources are not fully deleted")

	gotNode := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.TODO(), client.ObjectKey{Name: "gpu-node"}, gotNode))
	require.NotContains(t, gotNode.Labels, plugin.NvidiaGPUVirtualizationLabelKey)
	require.NotContains(t, gotNode.Annotations, "hami.io/node-nvidia-register")
	require.NotContains(t, gotNode.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)
}

func newUnstructuredObject(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetNamespace(namespace)
	obj.SetName(name)

	return obj
}

func setLastAppliedConfig(
	t *testing.T,
	ctrlClient client.Client,
	namespace,
	resourceName,
	componentName string,
	objs []unstructured.Unstructured,
) {
	t.Helper()

	config, err := json.Marshal(objs)
	require.NoError(t, err)
	require.NoError(t, deploy.NewConfigStore(ctrlClient).Set(context.TODO(),
		namespace, resourceName, componentName, string(config), nil))
}

type testVirtualizationPlugin struct{}

func (testVirtualizationPlugin) Handle() plugin.AcceleratorPluginHandle {
	return nil
}

func (testVirtualizationPlugin) Resource() string {
	return string(v1.AcceleratorTypeNVIDIAGPU)
}

func (testVirtualizationPlugin) Type() string {
	return plugin.InternalPluginType
}

func (testVirtualizationPlugin) ResolveClusterVirtualizationConfig(
	context.Context,
	*v1.Cluster,
) (*plugin.VirtualizationConfig, error) {
	return &plugin.VirtualizationConfig{
		Supported: true,
		NodeScopeLabel: plugin.VirtualizationNodeScopeLabel{
			Key:           plugin.NvidiaGPUVirtualizationLabelKey,
			EnabledValue:  "true",
			DisabledValue: "false",
		},
	}, nil
}
