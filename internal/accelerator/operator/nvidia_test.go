package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNvidiaOperatorReconcileNodeState(t *testing.T) {
	for _, test := range []struct {
		name       string
		managed    bool
		wantOwner  string
		wantClient string
	}{
		{name: "GPU Operator managed", managed: true, wantOwner: NVIDIAOperatorOwnerValue, wantClient: "false"},
		{name: "legacy driver", managed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))

			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "gpu-1",
				Labels: map[string]string{
					NVIDIAGPUNodeLabel:               NVIDIAGPUNodeValue,
					"neutree.ai/nvidia-vgpu-enabled": "false",
				},
				Annotations: map[string]string{"neutree.ai/accelerator-devices": "snapshot"},
			}}
			objects := []client.Object{node}
			if test.managed {
				objects = append(objects, nvidiaClusterPolicy())
			}
			ctrlClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

			_, err := NewNvidiaAcceleratorOperator().Reconcile(context.Background(), &OperatorContext{
				Cluster: &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"}},
				Client:  ctrlClient,
			})
			require.NoError(t, err)

			got := &corev1.Node{}
			require.NoError(t, ctrlClient.Get(context.Background(), client.ObjectKey{Name: node.Name}, got))
			require.Equal(t, "false", got.Labels["neutree.ai/nvidia-vgpu-enabled"])
			require.Equal(t, "snapshot", got.Annotations["neutree.ai/accelerator-devices"])
			require.Equal(t, test.wantOwner, got.Annotations[NVIDIAOperatorOwnerAnnotation])
			require.Equal(t, test.wantClient, got.Labels[NVIDIAGPUClientLabel])
		})
	}
}

func TestNvidiaOperatorStatusReportsDriverPodByNode(t *testing.T) {
	for _, test := range []struct {
		name       string
		driverPod  *corev1.Pod
		wantDetail string
	}{
		{
			name: "pending driver Pod",
			driverPod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "nvidia-driver-daemonset-abc", Labels: map[string]string{NVIDIADriverDeployLabel: "true"},
			}, Spec: corev1.PodSpec{NodeName: "gpu-1"}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
			wantDetail: "nvidia-driver-daemonset-abc",
		},
		{name: "missing driver Pod", wantDetail: "has no driver Pod"},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "gpu-1", Labels: map[string]string{NVIDIAGPUNodeLabel: NVIDIAGPUNodeValue},
			}}
			objects := []client.Object{node, nvidiaClusterPolicy()}
			if test.driverPod != nil {
				objects = append(objects, test.driverPod)
			}
			ctrlClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

			status, err := NewNvidiaAcceleratorOperator().Status(context.Background(), &OperatorContext{
				Cluster: &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"}},
				Client:  ctrlClient,
			})
			require.NoError(t, err)
			require.Equal(t, v1.ComponentPhaseNotReady, status.Phase)
			require.Equal(t, "DriverNotReady", status.Reason)
			require.Contains(t, status.Message, "gpu-1")
			require.Contains(t, status.Message, test.wantDetail)
			require.Contains(t, status.Message, "driver libraries may be unavailable")
		})
	}
}

func nvidiaClusterPolicy() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvidia.com/v1",
		"kind":       "ClusterPolicy",
		"metadata":   map[string]interface{}{"name": NVIDIAClusterPolicyName},
		"spec": map[string]interface{}{
			"driver": map[string]interface{}{"enabled": true},
		},
	}}
}
