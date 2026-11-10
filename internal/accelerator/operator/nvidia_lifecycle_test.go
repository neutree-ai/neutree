package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/deploy"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

func lifecycleContext(ctrl client.Client) *OperatorContext {
	return &OperatorContext{Client: ctrl, Namespace: "tenant-a", Cluster: &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}, Spec: &v1.ClusterSpec{}}}
}

func lifecycleLabels() map[string]string {
	return map[string]string{OperatorOwnerLabel: OperatorOwnerValue, DeploymentModeLabel: DeploymentModeValue, v1.NeutreeClusterLabelKey: "cluster-a", v1.NeutreeClusterWorkspaceLabelKey: "workspace-a"}
}

func saveLifecycleResources(t *testing.T, opCtx *OperatorContext, objects ...client.Object) {
	t.Helper()
	var records []unstructured.Unstructured
	for _, object := range objects {
		require.NoError(t, opCtx.Client.Create(context.Background(), object))
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
		require.NoError(t, err)
		records = append(records, unstructured.Unstructured{Object: data})
	}
	data, err := json.Marshal(records)
	require.NoError(t, err)
	require.NoError(t, deploy.NewConfigStore(opCtx.Client).Set(context.Background(), opCtx.Namespace, opCtx.Cluster.GetName(), "accelerator-operator-"+opCtx.Cluster.GetName(), string(data), lifecycleLabels()))
}

func lifecycleDaemonSet(name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-a", UID: types.UID(name + "-uid"), Labels: lifecycleLabels(), Generation: 1}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 1, UpdatedNumberScheduled: 1, NumberReady: 1, NumberAvailable: 1}}
}

func TestNvidiaStatusReportsOwnedWorkloadFailure(t *testing.T) {
	for _, kind := range []string{"DaemonSet", "Deployment"} {
		t.Run(kind, func(t *testing.T) {
			ctrl := newNvidiaProviderClient(t, true)
			opCtx := lifecycleContext(ctrl)
			agent := lifecycleDaemonSet("neutree-node-agent")
			var unhealthy client.Object
			if kind == "DaemonSet" {
				ds := lifecycleDaemonSet("nvidia-gpu-dcgm-exporter")
				ds.Status.NumberReady = 0
				unhealthy = ds
			} else {
				replicas := int32(1)
				unhealthy = &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: "hami-scheduler", Namespace: opCtx.Namespace, Labels: lifecycleLabels(), Generation: 1}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1}}
			}
			saveLifecycleResources(t, opCtx, agent, unhealthy)
			status, err := NewNvidiaAcceleratorOperator(nil).Status(context.Background(), opCtx)
			require.NoError(t, err)
			require.Equal(t, v1.ComponentPhaseNotReady, status.Phase)
			require.Contains(t, status.Message, unhealthy.GetName())
		})
	}
}

func TestNvidiaDeleteCleansAllOwnedResourcesAndPreservesForeignScope(t *testing.T) {
	for _, test := range []struct {
		name      string
		inventory bool
		writeBack bool
	}{
		{name: "normal deletion", inventory: true},
		{name: "writer completes during deletion", inventory: true, writeBack: true},
		{name: "partial apply without inventory", writeBack: true},
	} {
		t.Run(test.name, func(t *testing.T) {

			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, appsv1.AddToScheme(scheme))
			require.NoError(t, rbacv1.AddToScheme(scheme))
			ctrl := fake.NewClientBuilder().WithScheme(scheme).Build()
			opCtx := lifecycleContext(ctrl)
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu", Labels: map[string]string{NVIDIAGPUNodeLabel: "true", ClientDeployLabel: "true"}, Annotations: map[string]string{ClientDeployManagedAnnotation: "true", ClientDeployOwnerAnnotation: "workspace-a/cluster-a", resourceparser.NeutreeAcceleratorDevicesAnnotation: "owned-device-snapshot"}}}
			foreignNode := node.DeepCopy()
			foreignNode.Name = "other-owner"
			foreignNode.Annotations = map[string]string{ClientDeployManagedAnnotation: "true", ClientDeployOwnerAnnotation: "workspace-b/cluster-b", resourceparser.NeutreeAcceleratorDevicesAnnotation: "foreign-device-snapshot"}
			require.NoError(t, ctrl.Create(context.Background(), node))
			require.NoError(t, ctrl.Create(context.Background(), foreignNode))
			owned := []client.Object{lifecycleDaemonSet("neutree-node-agent"), &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: opCtx.Namespace, Labels: lifecycleLabels()}}, &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: opCtx.Namespace, Labels: lifecycleLabels()}}, &rbacv1.ClusterRole{TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"}, ObjectMeta: metav1.ObjectMeta{Name: "agent", Labels: lifecycleLabels()}}}
			if test.inventory {
				saveLifecycleResources(t, opCtx, owned...)
			} else {
				for _, object := range owned {
					require.NoError(t, ctrl.Create(context.Background(), object))
				}
			}
			controller := true
			require.NoError(t, ctrl.Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "owned-agent", Namespace: opCtx.Namespace, Labels: lifecycleLabels(),
				OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "neutree-node-agent", UID: "neutree-node-agent-uid", Controller: &controller}},
			}, Spec: corev1.PodSpec{NodeName: node.Name}}))
			foreign := lifecycleDaemonSet("other-agent")
			foreign.Namespace = "tenant-b"
			require.NoError(t, ctrl.Create(context.Background(), foreign))
			foreignWorkspace := lifecycleDaemonSet("other-workspace")
			foreignWorkspace.Labels[v1.NeutreeClusterWorkspaceLabelKey] = "workspace-b"
			require.NoError(t, ctrl.Create(context.Background(), foreignWorkspace))
			require.Error(t, NewNvidiaAcceleratorOperator(nil).Delete(context.Background(), opCtx))
			// A running Pod can finish an annotation write while its controller is deleting.
			if test.writeBack {
				require.NoError(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(node), node))
				node.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation] = "last-live-snapshot"
				require.NoError(t, ctrl.Update(context.Background(), node))
			}
			require.Error(t, NewNvidiaAcceleratorOperator(nil).Delete(context.Background(), opCtx), "wait for the writer Pod to stop")
			require.NoError(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(node), node))
			require.Contains(t, node.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)
			require.NoError(t, ctrl.Delete(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "owned-agent", Namespace: opCtx.Namespace}}))
			// A new reconcile constructs a new operator instance and reads the saved cleanup scope.
			require.NoError(t, NewNvidiaAcceleratorOperator(nil).Delete(context.Background(), opCtx))
			for _, object := range owned {
				require.Error(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(object), object), object.GetName())
			}
			require.NoError(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(foreign), foreign))
			require.NoError(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(foreignWorkspace), foreignWorkspace))
			require.NoError(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(node), node))
			require.NotContains(t, node.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)
			require.NotContains(t, node.Annotations, ClientDeployManagedAnnotation)
			require.NotContains(t, node.Annotations, ClientDeployOwnerAnnotation)
			require.Equal(t, "true", node.Labels[ClientDeployLabel])
			require.NoError(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(foreignNode), foreignNode))
			require.Equal(t, "foreign-device-snapshot", foreignNode.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation])
			require.Equal(t, "true", foreignNode.Annotations[ClientDeployManagedAnnotation])
			config, err := deploy.NewConfigStore(ctrl).Get(context.Background(), opCtx.Namespace, opCtx.Cluster.GetName(), "accelerator-operator-"+opCtx.Cluster.GetName())
			require.NoError(t, err)
			require.Empty(t, config)
		})
	}
}

func TestNvidiaRebuildOnlyCurrentScopePods(t *testing.T) {
	ctrl := newNvidiaProviderClient(t, true)
	opCtx := lifecycleContext(ctrl)
	ds := lifecycleDaemonSet("neutree-node-agent")
	require.NoError(t, ctrl.Create(context.Background(), ds))
	own := NewNvidiaBasePod("old-agent", opCtx.Namespace, nil)
	own.Labels = lifecycleLabels()
	controller := true
	own.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", Name: ds.Name, UID: ds.UID, Controller: &controller}}
	own.Spec.NodeName = "gpu-node"
	own.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Unix(1, 0))}}}}
	require.NoError(t, ctrl.Create(context.Background(), own))
	foreign := own.DeepCopy()
	foreign.ResourceVersion = ""
	foreign.Name = "foreign-agent"
	foreign.Namespace = "tenant-b"
	require.NoError(t, ctrl.Create(context.Background(), foreign))
	orphan := own.DeepCopy()
	orphan.ResourceVersion = ""
	orphan.Name = "orphan-labelled-client"
	orphan.OwnerReferences = nil
	require.NoError(t, ctrl.Create(context.Background(), orphan))
	_, err := NewNvidiaAcceleratorOperator(nil).Reconcile(context.Background(), opCtx)
	require.NoError(t, err)
	require.Error(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(own), own))
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(foreign), foreign))
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(orphan), orphan))
}

func TestNvidiaPausedProviderIsNotReady(t *testing.T) {
	ctrl := newNvidiaProviderClient(t, false)
	node := &corev1.Node{}
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: "gpu-node"}, node))
	node.Labels[ClientDeployLabel] = "paused-for-driver-upgrade"
	require.NoError(t, ctrl.Update(context.Background(), node))
	state, err := NewNvidiaProvider(ctrl).Evaluate(context.Background())
	require.NoError(t, err)
	require.False(t, state.Ready)
	require.Contains(t, state.NodeStates, "gpu-node")
	require.Contains(t, state.NodeStates["gpu-node"].Reason, "paused")
}

func TestNvidiaClientLabelClaimRecordsClusterOwner(t *testing.T) {
	ctrl := newNvidiaProviderClient(t, true)
	opCtx := lifecycleContext(ctrl)
	_, err := NewNvidiaAcceleratorOperator(nil).Reconcile(context.Background(), opCtx)
	require.NoError(t, err)
	node := &corev1.Node{}
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: "gpu-node"}, node))
	require.Equal(t, "workspace-a/cluster-a", node.Annotations[ClientDeployOwnerAnnotation])
	node.Labels[ClientDeployLabel] = "false"
	node.Annotations[ClientDeployOwnerAnnotation] = "other/cluster"
	require.NoError(t, ctrl.Update(context.Background(), node))
	_, err = NewNvidiaAcceleratorOperator(nil).Reconcile(context.Background(), opCtx)
	require.NoError(t, err)
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: "gpu-node"}, node))
	require.Equal(t, "false", node.Labels[ClientDeployLabel])
	require.Equal(t, "other/cluster", node.Annotations[ClientDeployOwnerAnnotation])
}

func TestNvidiaClientLabelAdoptsLegacyClaimOnlyWithCurrentOwnedWorkload(t *testing.T) {
	ctrl := newNvidiaProviderClient(t, true)
	opCtx := lifecycleContext(ctrl)
	node := &corev1.Node{}
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: "gpu-node"}, node))
	node.Annotations = map[string]string{ClientDeployManagedAnnotation: "true"}
	node.Labels[ClientDeployLabel] = "false"
	require.NoError(t, ctrl.Update(context.Background(), node))
	_, err := NewNvidiaAcceleratorOperator(nil).Reconcile(context.Background(), opCtx)
	require.NoError(t, err)
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: "gpu-node"}, node))
	require.NotContains(t, node.Annotations, ClientDeployOwnerAnnotation)
	require.Equal(t, "false", node.Labels[ClientDeployLabel])
	require.NoError(t, ctrl.Create(context.Background(), lifecycleDaemonSet("neutree-node-agent")))
	_, err = NewNvidiaAcceleratorOperator(nil).Reconcile(context.Background(), opCtx)
	require.NoError(t, err)
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: "gpu-node"}, node))
	require.Equal(t, "workspace-a/cluster-a", node.Annotations[ClientDeployOwnerAnnotation])
	require.Equal(t, "true", node.Labels[ClientDeployLabel])
}

func TestNvidiaStatusReadyAndNodeFailure(t *testing.T) {
	ctrl := newNvidiaProviderClient(t, true)
	opCtx := lifecycleContext(ctrl)
	saveLifecycleResources(t, opCtx, lifecycleDaemonSet("neutree-node-agent"), lifecycleDaemonSet("nvidia-gpu-dcgm-exporter"))
	status, err := NewNvidiaAcceleratorOperator(nil).Status(context.Background(), opCtx)
	require.NoError(t, err)
	require.Equal(t, v1.ComponentPhaseReady, status.Phase)
	node := &corev1.Node{}
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: "gpu-node"}, node))
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
	require.NoError(t, ctrl.Status().Update(context.Background(), node))
	status, err = NewNvidiaAcceleratorOperator(nil).Status(context.Background(), opCtx)
	require.NoError(t, err)
	require.Equal(t, v1.ComponentPhaseNotReady, status.Phase)
	require.Contains(t, status.Message, "gpu-node")
	require.Contains(t, status.Message, "node is not Ready")
}

func TestNvidiaOperatorOwnsNodeScopeLifecycle(t *testing.T) {
	ctrl := newNvidiaProviderClient(t, true)
	opCtx := lifecycleContext(ctrl)
	config := &publicaccelerator.VirtualizationConfig{
		CandidateNodes: []string{"gpu-node"},
		NodeScopeLabel: publicaccelerator.VirtualizationNodeScopeLabel{Key: "neutree.ai/nvidia-vgpu-enabled", EnabledValue: "true", DisabledValue: "false"},
	}

	require.NoError(t, reconcileNvidiaNodeScope(context.Background(), opCtx, config))
	node := &corev1.Node{}
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: "gpu-node"}, node))
	require.Equal(t, "true", node.Labels[config.NodeScopeLabel.Key])

	require.NoError(t, cleanupNvidiaNodeScope(context.Background(), opCtx))
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: "gpu-node"}, node))
	require.NotContains(t, node.Labels, config.NodeScopeLabel.Key)
}

func TestNvidiaStatusRejectsZeroScheduledClientsWhenGPUExists(t *testing.T) {
	ctrl := newNvidiaProviderClient(t, true)
	opCtx := lifecycleContext(ctrl)
	ds := lifecycleDaemonSet("neutree-node-agent")
	ds.Status.DesiredNumberScheduled = 0
	ds.Status.NumberReady = 0
	ds.Status.NumberAvailable = 0
	ds.Status.UpdatedNumberScheduled = 0
	saveLifecycleResources(t, opCtx, ds)
	status, err := NewNvidiaAcceleratorOperator(nil).Status(context.Background(), opCtx)
	require.NoError(t, err)
	require.Equal(t, v1.ComponentPhaseNotReady, status.Phase)
}

type inventoryWriteFailureClient struct{ client.Client }

func (c inventoryWriteFailureClient) Update(ctx context.Context, object client.Object, opts ...client.UpdateOption) error {
	if object.GetName() == "neutree-cluster-a-accelerator-operator-cluster-a-config" {
		return fmt.Errorf("injected inventory write failure")
	}
	return c.Client.Update(ctx, object, opts...)
}

func TestNvidiaApplyRejectsUnsavedCurrentInventory(t *testing.T) {
	ctrl := newNvidiaProviderClient(t, true)
	opCtx := lifecycleContext(ctrl)
	agent := lifecycleDaemonSet("neutree-node-agent")
	saveLifecycleResources(t, opCtx, agent)
	exporter := lifecycleDaemonSet("nvidia-gpu-dcgm-exporter")
	exporter.Status.NumberReady = 0
	var desired []unstructured.Unstructured
	for _, object := range []client.Object{agent, exporter} {
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
		require.NoError(t, err)
		desired = append(desired, unstructured.Unstructured{Object: data})
	}
	opCtx.Client = inventoryWriteFailureClient{Client: ctrl}
	err := ApplyResources(context.Background(), opCtx, &unstructured.UnstructuredList{Items: desired})
	require.ErrorContains(t, err, "inventory")
	require.NoError(t, ctrl.Get(context.Background(), client.ObjectKeyFromObject(exporter), exporter), "new exporter creation really occurred before inventory persistence failed")
}

func TestWaitForLegacyResourcesEnforcesMigrationOwnership(t *testing.T) {
	for _, test := range []struct {
		name, want string
		labels     map[string]string
	}{
		{name: "legacy resource waits", want: "waiting for Legacy cleanup"},
		{name: "foreign resource blocks", labels: map[string]string{
			OperatorOwnerLabel: "other", v1.NeutreeClusterLabelKey: "other-cluster", v1.NeutreeClusterWorkspaceLabelKey: "other-workspace",
		}, want: "owned by another accelerator deployment"},
		{name: "owned resource proceeds", labels: lifecycleLabels()},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := newNvidiaProviderClient(t, true)
			opCtx := lifecycleContext(ctrl)
			live := lifecycleDaemonSet("hami-device-plugin")
			live.Labels = test.labels
			require.NoError(t, ctrl.Create(context.Background(), live))

			data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(live)
			require.NoError(t, err)
			err = waitForLegacyResources(context.Background(), opCtx, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{{Object: data}}})
			if test.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.want)
			}
		})
	}
}

func newNvidiaProviderClient(t *testing.T, withRevision bool) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	scheme.AddKnownTypeWithName(NVIDIAClusterPolicyGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schemaForClusterPolicyList(), &unstructured.UnstructuredList{})

	policy := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvidia.com/v1", "kind": "ClusterPolicy", "metadata": map[string]interface{}{"name": NVIDIAClusterPolicyName, "labels": map[string]interface{}{"app.kubernetes.io/version": "v25.3.0"}},
		"spec": map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": true}},
	}}
	policy.SetGroupVersionKind(NVIDIAClusterPolicyGVK)

	now := metav1.NewTime(time.Unix(100, 0))
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-node", Labels: map[string]string{
		NVIDIAGPUNodeLabel: NVIDIAGPUNodeValue, NVIDIADriverDeployLabel: "true", NVIDIAToolkitDeployLabel: "true",
	}}}
	objects := []client.Object{policy, node,
		providerDaemonSet("driver", "driver-ds", NVIDIADriverDeployLabel), providerDaemonSet("toolkit", "toolkit-ds", NVIDIAToolkitDeployLabel),
		providerPodObject("driver-pod", "driver-ds", "driver-ds", "driver-rev", now), providerPodObject("toolkit-pod", "toolkit-ds", "toolkit-ds", "toolkit-rev", now),
	}
	if withRevision {
		objects = append(objects, controllerRevision("driver-ds", "driver-rev"), controllerRevision("toolkit-ds", "toolkit-rev"))
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func schemaForClusterPolicyList() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicyList"}
}

func providerDaemonSet(role, name, label string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "gpu-operator", UID: types.UID(name + "-uid"), Generation: 1, Labels: map[string]string{"role": role}}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeSelector: map[string]string{NVIDIAGPUNodeLabel: NVIDIAGPUNodeValue, label: "true"}, Containers: []corev1.Container{{Name: role}}}}}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 1}}
}

func providerPodObject(name, dsName, dsUID, revision string, started metav1.Time) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "gpu-operator", Labels: map[string]string{controllerRevisionHashLabel: revision}, OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: dsName, UID: types.UID(dsUID + "-uid")}}}, Spec: corev1.PodSpec{NodeName: "gpu-node", Containers: []corev1.Container{{Name: "provider"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "provider", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}}}}}}
}

func controllerRevision(dsName, revision string) *appsv1.ControllerRevision {
	return &appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: dsName + "-" + revision, Namespace: "gpu-operator", Labels: map[string]string{controllerRevisionHashLabel: revision}, OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: dsName, UID: types.UID(dsName + "-uid")}}}, Revision: 1}
}

func (c inventoryWriteFailureClient) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if patch.Type() != types.ApplyPatchType {
		return c.Client.Patch(ctx, object, patch, opts...)
	}
	current, ok := object.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("invalid test object")
	}
	err := c.Client.Get(ctx, client.ObjectKeyFromObject(object), current)
	if apierrors.IsNotFound(err) {
		return c.Client.Create(ctx, object)
	}
	if err != nil {
		return err
	}
	object.SetResourceVersion(current.GetResourceVersion())
	return c.Client.Update(ctx, object)
}

func TestNvidiaProviderUnknownVersionWithRealOperatorPod(t *testing.T) {
	for _, test := range []struct {
		name, label, image string
		known, claim       bool
	}{
		{name: "devel tag", image: "nvcr.io/nvidia/gpu-operator:devel", claim: true},
		{name: "digest only", image: "nvcr.io/nvidia/gpu-operator@sha256:abcdef", claim: true},
		{name: "digest with registry port", image: "registry.local:5000/nvidia/gpu-operator@sha256:abcdef", claim: true},
		{name: "invalid label with valid old image", label: "devel", image: "nvcr.io/nvidia/gpu-operator:v25.3.4", known: true, claim: true},
		{name: "native version", image: "nvcr.io/nvidia/gpu-operator:v26.7.0", known: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := newNvidiaProviderClient(t, true)
			policy := &unstructured.Unstructured{}
			policy.SetGroupVersionKind(NVIDIAClusterPolicyGVK)
			require.NoError(t, ctrl.Get(context.Background(), client.ObjectKey{Name: NVIDIAClusterPolicyName}, policy))
			policy.SetLabels(map[string]string{"app.kubernetes.io/version": test.label})
			require.NoError(t, ctrl.Update(context.Background(), policy))
			require.NoError(t, ctrl.Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "gpu-operator", Labels: map[string]string{"app": "gpu-operator"}}, Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "init", Image: "busybox:1.36"}}, Containers: []corev1.Container{{Name: "gpu-operator", Image: test.image}}}}))
			state, err := NewNvidiaProvider(ctrl).Management(context.Background())
			require.NoError(t, err)
			require.Equal(t, test.known, state.OperatorVersionKnown)
			require.Equal(t, test.claim, state.ClientLabelManaged)
		})
	}
}

func TestNvidiaDeleteCleansOSNodeAgentBindingsWithoutClientClaim(t *testing.T) {
	ctx := context.Background()
	ctrl := newNvidiaProviderClient(t, true)
	opCtx := lifecycleContext(ctrl)
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(NVIDIAClusterPolicyGVK)
	require.NoError(t, ctrl.Get(ctx, client.ObjectKey{Name: NVIDIAClusterPolicyName}, policy))
	require.NoError(t, ctrl.Delete(ctx, policy))
	management, err := NewNvidiaProvider(ctrl).Management(ctx)
	require.NoError(t, err)
	require.False(t, management.Managed, "OS provider does not establish a client-label claim")

	agent := lifecycleDaemonSet("neutree-node-agent")
	exporter := lifecycleDaemonSet("nvidia-gpu-dcgm-exporter")
	saveLifecycleResources(t, opCtx, agent, exporter)
	controller := true
	cases := []struct {
		name, owner, podNamespace, dsName string
		uid                               types.UID
		bound, clean                      bool
	}{
		{name: "owned-os", podNamespace: opCtx.Namespace, dsName: agent.Name, uid: agent.UID, bound: true, clean: true},
		{name: "foreign-owner", owner: "workspace-b/cluster-b", podNamespace: opCtx.Namespace, dsName: agent.Name, uid: agent.UID, bound: true},
		{name: "unknown-no-pod"},
		{name: "stale-controller", podNamespace: opCtx.Namespace, dsName: agent.Name, uid: "previous-agent-uid", bound: true},
		{name: "foreign-namespace", podNamespace: "tenant-b", dsName: agent.Name, uid: agent.UID, bound: true},
		{name: "exporter-only", podNamespace: opCtx.Namespace, dsName: exporter.Name, uid: exporter.UID, bound: true},
	}
	for _, test := range cases {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: test.name, Annotations: map[string]string{resourceparser.NeutreeAcceleratorDevicesAnnotation: test.name}}}
		if test.owner != "" {
			node.Annotations[ClientDeployOwnerAnnotation] = test.owner
		}
		require.NoError(t, ctrl.Create(ctx, node))
		if test.bound {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: test.name + "-pod", Namespace: test.podNamespace, Labels: lifecycleLabels(), OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: test.dsName, UID: test.uid, Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: test.name}}
			require.NoError(t, ctrl.Create(ctx, pod))
		}
	}
	require.Error(t, NewNvidiaAcceleratorOperator(nil).Delete(ctx, opCtx))
	pods := &corev1.PodList{}
	require.NoError(t, ctrl.List(ctx, pods, client.InNamespace(opCtx.Namespace)))
	for i := range pods.Items {
		pod := &pods.Items[i]
		owner := metav1.GetControllerOf(pod)
		if owner != nil && owner.UID == agent.UID {
			require.NoError(t, ctrl.Delete(ctx, pod))
		}
	}
	require.NoError(t, NewNvidiaAcceleratorOperator(nil).Delete(ctx, opCtx))
	for _, test := range cases {
		node := &corev1.Node{}
		require.NoError(t, ctrl.Get(ctx, client.ObjectKey{Name: test.name}, node))
		if test.clean {
			require.NotContains(t, node.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation, test.name)
		} else {
			require.Equal(t, test.name, node.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation], test.name)
		}
		require.Equal(t, test.owner, node.Annotations[ClientDeployOwnerAnnotation], test.name)
		require.NotContains(t, node.Annotations, ClientDeployManagedAnnotation, test.name)
	}
}
