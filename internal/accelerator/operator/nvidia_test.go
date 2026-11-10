package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestNvidiaOperatorReconcileNodeState(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		label, annotation         *string
		version                   string
		running                   bool
		wantLabel, wantAnnotation *string
	}{
		{name: "initialize pending driver", wantLabel: ptr.To("false"), wantAnnotation: ptr.To("true")},
		{name: "Running driver does not require Ready", running: true, wantLabel: ptr.To("true"), wantAnnotation: ptr.To("true")},
		{name: "external label is not adopted", label: ptr.To("false"), running: true, wantLabel: ptr.To("false")},
		{name: "removed annotation stays removed", label: ptr.To("true"), wantLabel: ptr.To("true")},
		{name: "annotation without label is not repaired", annotation: ptr.To("true"), running: true, wantAnnotation: ptr.To("true")},
		{name: "managed driver stops", label: ptr.To("true"), annotation: ptr.To("true"), wantLabel: ptr.To("false"), wantAnnotation: ptr.To("true")},
		{name: "managed driver recovers", label: ptr.To("false"), annotation: ptr.To("true"), running: true, wantLabel: ptr.To("true"), wantAnnotation: ptr.To("true")},
		{name: "26.7 preserves label and annotation", version: "v26.7.0", label: ptr.To("false"), annotation: ptr.To("true"), running: true, wantLabel: ptr.To("false"), wantAnnotation: ptr.To("true")},
		{name: "26.7 does not initialize", version: "v26.7.1", running: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := driverTestNode()
			if tc.label != nil {
				node.Labels[accelerator.NvidiaGPUClientLabel] = *tc.label
			}
			if tc.annotation != nil {
				node.Annotations[accelerator.NeutreeOwnerAnnotation] = *tc.annotation
			}
			policy := driverTestPolicy()
			if tc.version != "" {
				policy.SetLabels(map[string]string{"app.kubernetes.io/version": tc.version})
			}
			pod := driverTestPod(corev1.PodPending)
			if tc.running {
				pod.Status.Phase = corev1.PodRunning
			}
			ctrlClient := driverTestClient(t, node, policy, driverTestDaemonSet(), pod)
			_, err := NewNvidiaAcceleratorOperator().Reconcile(context.Background(), driverTestContext(ctrlClient))
			require.NoError(t, err)
			got := &corev1.Node{}
			require.NoError(t, ctrlClient.Get(context.Background(), client.ObjectKeyFromObject(node), got))
			label, labelled := got.Labels[accelerator.NvidiaGPUClientLabel]
			require.Equal(t, tc.wantLabel != nil, labelled)
			if tc.wantLabel != nil {
				require.Equal(t, *tc.wantLabel, label)
			}
			annotation, annotated := got.Annotations[accelerator.NeutreeOwnerAnnotation]
			require.Equal(t, tc.wantAnnotation != nil, annotated)
			if tc.wantAnnotation != nil {
				require.Equal(t, *tc.wantAnnotation, annotation)
			}
			require.Equal(t, "false", got.Labels["neutree.ai/nvidia-vgpu-enabled"])
			require.Equal(t, "snapshot", got.Annotations["neutree.ai/accelerator-devices"])
		})
	}
}

func TestNvidiaOperatorDeletePreservesClientLabel(t *testing.T) {
	for _, tc := range []struct {
		name                string
		labelled, annotated bool
	}{
		{name: "managed client", labelled: true, annotated: true},
		{name: "annotation only", annotated: true},
		{name: "external client", labelled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := driverTestNode()
			if tc.labelled {
				node.Labels[accelerator.NvidiaGPUClientLabel] = "false"
			}
			if tc.annotated {
				node.Annotations[accelerator.NeutreeOwnerAnnotation] = "true"
			}
			ctrlClient := driverTestClient(t, node)
			require.NoError(t, NewNvidiaAcceleratorOperator().Delete(context.Background(), driverTestContext(ctrlClient)))
			got := &corev1.Node{}
			require.NoError(t, ctrlClient.Get(context.Background(), client.ObjectKeyFromObject(node), got))
			_, exists := got.Labels[accelerator.NvidiaGPUClientLabel]
			require.Equal(t, tc.labelled, exists)
			if tc.labelled {
				require.Equal(t, "false", got.Labels[accelerator.NvidiaGPUClientLabel])
			}
			require.NotContains(t, got.Annotations, accelerator.NeutreeOwnerAnnotation)
			require.Equal(t, "snapshot", got.Annotations["neutree.ai/accelerator-devices"])
			require.Equal(t, "false", got.Labels["neutree.ai/nvidia-vgpu-enabled"])
		})
	}
}

func TestNvidiaOperatorStatusReportsDriverPodByNode(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		phase                     corev1.PodPhase
		ready, missing, unmanaged bool
		wantDetail                string
	}{
		{name: "pending driver", phase: corev1.PodPending, wantDetail: "Init:CrashLoopBackOff"},
		{name: "missing driver", missing: true, wantDetail: "has no driver Pod"},
		{name: "driver status not yet reported", wantDetail: "Unknown"},
		{name: "Running but not Ready", phase: corev1.PodRunning, wantDetail: "Pod is not Ready"},
		{name: "ready driver", phase: corev1.PodRunning, ready: true},
		{name: "OS driver", unmanaged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []client.Object{driverTestNode(), driverTestDaemonSet()}
			if !tc.unmanaged {
				objects = append(objects, driverTestPolicy())
			}
			if !tc.missing {
				pod := driverTestPod(tc.phase)
				if tc.phase == corev1.PodPending {
					pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "driver-manager", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
				}
				if tc.ready {
					pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
				}
				objects = append(objects, pod)
			}
			status, err := NewNvidiaAcceleratorOperator().Status(context.Background(), driverTestContext(driverTestClient(t, objects...)))
			require.NoError(t, err)
			if tc.wantDetail == "" {
				require.Equal(t, v1.ComponentPhaseReady, status.Phase)
				return
			}
			require.Equal(t, v1.ComponentPhaseNotReady, status.Phase)
			require.Equal(t, "DriverNotReady", status.Reason)
			require.Contains(t, status.Message, "gpu-1")
			require.Contains(t, status.Message, tc.wantDetail)
			require.Contains(t, status.Message, "driver libraries may be unavailable")
			if !tc.missing {
				require.Contains(t, status.Message, "nvidia-driver-abc")
			}
		})
	}
}

func TestNvidiaOperatorVersionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		wantNative    bool
	}{
		{name: "old release", version: "v25.3.4"},
		{name: "threshold", version: "26.7.0", wantNative: true},
		{name: "unknown version", version: "latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := accelerator.GPUOperatorClusterPolicy{Labels: map[string]string{}}
			policy.Labels["app.kubernetes.io/version"] = tc.version
			native := nativeNvidiaClientManagement([]accelerator.GPUOperatorClusterPolicy{policy})
			require.Equal(t, tc.wantNative, native)
		})
	}
}

func driverTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicy"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicyList"}, &unstructured.UnstructuredList{})
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func driverTestContext(ctrlClient client.Client) *OperatorContext {
	return &OperatorContext{Cluster: &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"}}, Client: ctrlClient}
}

func driverTestNode() *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-1", Labels: map[string]string{
		accelerator.NvidiaGPUDiscoveryLabelKey: "true", "neutree.ai/nvidia-vgpu-enabled": "false",
	}, Annotations: map[string]string{"neutree.ai/accelerator-devices": "snapshot"}}}
}

func driverTestPolicy() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvidia.com/v1", "kind": "ClusterPolicy", "metadata": map[string]interface{}{"name": "custom-policy"},
		"spec": map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": true}},
	}}
}

func driverTestDaemonSet() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "nvidia-driver", Namespace: "gpu-operator", UID: "driver-ds"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeSelector: map[string]string{accelerator.NvidiaDriverDeployLabel: "true"}}}}}
}

func driverTestPod(phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nvidia-driver-abc", Namespace: "gpu-operator", UID: "driver-pod", Labels: map[string]string{accelerator.NvidiaDriverAppLabel: accelerator.NvidiaDriverAppValue, accelerator.NvidiaDriverComponentLabel: accelerator.NvidiaDriverComponentValue}, OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "nvidia-driver", UID: "driver-ds", Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{NodeName: "gpu-1"}, Status: corev1.PodStatus{Phase: phase}}
}
