package operator

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/metricsconfig"
)

func TestBuildNvidiaComponentPlanOperatorAndLegacy(t *testing.T) {
	operatorPlan := BuildNvidiaComponentPlan(ProviderState{Managed: true})
	if operatorPlan.NodeSelector[ClientDeployLabel] != "true" {
		t.Fatalf("operator plan missing dependency selector: %#v", operatorPlan.NodeSelector)
	}
	if len(operatorPlan.Components) != 3 {
		t.Fatalf("components=%d, want 3", len(operatorPlan.Components))
	}
	for _, component := range operatorPlan.Components {
		if component.OwnerLabels[OperatorOwnerLabel] != OperatorOwnerValue {
			t.Fatalf("component %s missing owner labels", component.Name)
		}
		if component.NodeSelector[ClientDeployLabel] != "true" {
			t.Fatalf("component %s missing gate selector", component.Name)
		}
	}

	legacyPlan := BuildNvidiaComponentPlan(ProviderState{Managed: false})
	if len(legacyPlan.NodeSelector) != 0 {
		t.Fatalf("legacy plan unexpectedly has gate selector: %#v", legacyPlan.NodeSelector)
	}
}

func TestBasePodStartDecision(t *testing.T) {
	providerStart := time.Unix(100, 0)
	newPod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Unix(101, 0))}},
	}}}}
	if got := BasePodStartDecisionFor(newPod, providerStart); got != BasePodStartKeep {
		t.Fatalf("new pod decision=%s, want keep", got)
	}
	oldPod := newPod.DeepCopy()
	oldPod.Status.ContainerStatuses[0].State.Running.StartedAt = metav1.NewTime(providerStart)
	if got := BasePodStartDecisionFor(oldPod, providerStart); got != BasePodStartRebuild {
		t.Fatalf("old pod decision=%s, want rebuild", got)
	}
	unknownPod := newPod.DeepCopy()
	unknownPod.Status.ContainerStatuses[0].State.Running = nil
	if got := BasePodStartDecisionFor(unknownPod, providerStart); got != BasePodStartUnknown {
		t.Fatalf("unknown pod decision=%s, want unknown", got)
	}
}

func TestNvidiaProviderEvaluateReadyAndGateLabel(t *testing.T) {
	client := newNvidiaProviderClient(t, true)
	provider := NewNvidiaProvider(client)

	state, err := provider.Evaluate(context.Background())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !state.Managed || !state.Ready || !state.ProviderStartKnown {
		t.Fatalf("unexpected state: %#v", state)
	}

	node := &corev1.Node{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: "gpu-node"}, node); err != nil {
		t.Fatal(err)
	}
	if node.Labels[ClientDeployLabel] != "true" {
		t.Fatalf("client label=%q, want true", node.Labels[ClientDeployLabel])
	}
}

func TestNvidiaProviderUnknownVersionClaimsClientLabel(t *testing.T) {
	ctrlClient := newNvidiaProviderClient(t, true)
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(NVIDIAClusterPolicyGVK)
	if err := ctrlClient.Get(context.Background(), types.NamespacedName{Name: NVIDIAClusterPolicyName}, policy); err != nil {
		t.Fatal(err)
	}
	policy.SetLabels(nil)
	if err := ctrlClient.Update(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if _, err := NewNvidiaProvider(ctrlClient).Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{}
	if err := ctrlClient.Get(context.Background(), types.NamespacedName{Name: "gpu-node"}, node); err != nil {
		t.Fatal(err)
	}
	if node.Labels[ClientDeployLabel] != "true" || node.Annotations[ClientDeployManagedAnnotation] != "true" {
		t.Fatalf("unknown-version ownership not established: labels=%v annotations=%v", node.Labels, node.Annotations)
	}
}

func TestNvidiaProviderRequiresExplicitlyEnabledDependencies(t *testing.T) {
	ctrlClient := newNvidiaProviderClient(t, true)
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(NVIDIAClusterPolicyGVK)
	if err := ctrlClient.Get(context.Background(), types.NamespacedName{Name: NVIDIAClusterPolicyName}, policy); err != nil {
		t.Fatal(err)
	}
	policy.Object["spec"] = map[string]interface{}{"driver": map[string]interface{}{"enabled": true}}
	if err := ctrlClient.Update(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	state, err := NewNvidiaProvider(ctrlClient).Evaluate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Managed {
		t.Fatal("missing toolkit.enabled must not enter GPU Operator management mode")
	}
}

func TestProviderStatusMessageIncludesNodeDriverFailure(t *testing.T) {
	message := providerStatusMessage(ProviderState{
		Managed: true,
		Reason:  "NVIDIA provider is not ready",
		NodeStates: map[string]ProviderNodeState{
			"gpu-node-a": {Reason: "NVIDIA driver provider is not Ready"},
		},
	})
	if !strings.Contains(message, "gpu-node-a") || !strings.Contains(message, "driver provider") {
		t.Fatalf("status message lacks node driver details: %q", message)
	}
}

func TestNvidiaProviderDoesNotClaimExistingExternalClientLabel(t *testing.T) {
	ctrlClient := newNvidiaProviderClient(t, true)
	node := &corev1.Node{}
	if err := ctrlClient.Get(context.Background(), types.NamespacedName{Name: "gpu-node"}, node); err != nil {
		t.Fatal(err)
	}
	node.Labels[ClientDeployLabel] = "external-value"
	if err := ctrlClient.Update(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	if _, err := NewNvidiaProvider(ctrlClient).Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ctrlClient.Get(context.Background(), types.NamespacedName{Name: "gpu-node"}, node); err != nil {
		t.Fatal(err)
	}
	if node.Labels[ClientDeployLabel] != "external-value" || node.Annotations[ClientDeployManagedAnnotation] != "" {
		t.Fatalf("external client label was claimed: labels=%v annotations=%v", node.Labels, node.Annotations)
	}
}

func TestNvidiaProviderMissingRevisionKeepsGateFalse(t *testing.T) {
	client := newNvidiaProviderClient(t, false)
	state, err := NewNvidiaProvider(client).Evaluate(context.Background())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if state.Ready {
		t.Fatalf("expected provider not ready without current revision: %#v", state)
	}
	node := &corev1.Node{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: "gpu-node"}, node); err != nil {
		t.Fatal(err)
	}
	if node.Labels[ClientDeployLabel] != "false" {
		t.Fatalf("client label=%q, want false", node.Labels[ClientDeployLabel])
	}
}

func TestNvidiaProviderReleasesManagementForGPUOperatorPause(t *testing.T) {
	ctrlClient := newNvidiaProviderClient(t, false)
	node := &corev1.Node{}
	if err := ctrlClient.Get(context.Background(), types.NamespacedName{Name: "gpu-node"}, node); err != nil {
		t.Fatal(err)
	}
	node.Labels[ClientDeployLabel] = "paused-for-driver-upgrade"
	node.Annotations = map[string]string{ClientDeployManagedAnnotation: "true"}
	if err := ctrlClient.Update(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	if _, err := NewNvidiaProvider(ctrlClient).Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ctrlClient.Get(context.Background(), types.NamespacedName{Name: "gpu-node"}, node); err != nil {
		t.Fatal(err)
	}
	if _, found := node.Annotations[ClientDeployManagedAnnotation]; found {
		t.Fatal("Neutree management annotation still exists")
	}
	if got := node.Labels[ClientDeployLabel]; got != "paused-for-driver-upgrade" {
		t.Fatalf("client label=%q, want paused-for-driver-upgrade", got)
	}
}

func TestNvidiaProviderSkipsClientLabelForOperator26Point7(t *testing.T) {
	client := newNvidiaProviderClient(t, true)
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(NVIDIAClusterPolicyGVK)
	if err := client.Get(context.Background(), types.NamespacedName{Name: NVIDIAClusterPolicyName}, policy); err != nil {
		t.Fatal(err)
	}
	policy.SetLabels(map[string]string{"app.kubernetes.io/version": "v26.7.0"})
	if err := client.Update(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if _, err := NewNvidiaProvider(client).Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: "gpu-node"}, node); err != nil {
		t.Fatal(err)
	}
	if _, found := node.Labels[ClientDeployLabel]; found {
		t.Fatalf("client label should not be managed for GPU Operator 26.7+: %#v", node.Labels)
	}
}

func TestNvidiaOperatorRebuildsStaleOwnedPod(t *testing.T) {
	client := newNvidiaProviderClient(t, true)
	stale := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "stale-base", Namespace: "neutree-system", Labels: map[string]string{OperatorOwnerLabel: OperatorOwnerValue},
	}, Spec: corev1.PodSpec{NodeName: "gpu-node", Containers: []corev1.Container{{Name: "agent"}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "agent", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Unix(1, 0))}},
	}}}}
	if err := client.Create(context.Background(), stale); err != nil {
		t.Fatal(err)
	}

	result, err := NewNvidiaAcceleratorOperator().Reconcile(context.Background(), &OperatorContext{Client: client})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(result.RebuiltPods) != 1 || result.RebuiltPods[0] != "neutree-system/stale-base" {
		t.Fatalf("rebuilt pods=%v", result.RebuiltPods)
	}
	if err := client.Get(context.Background(), types.NamespacedName{Name: "stale-base", Namespace: "neutree-system"}, &corev1.Pod{}); err == nil {
		t.Fatal("stale base pod still exists")
	}
}

func newNvidiaProviderClient(t *testing.T, withRevision bool) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	scheme.AddKnownTypeWithName(NVIDIAClusterPolicyGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schemaForClusterPolicyList(), &unstructured.UnstructuredList{})

	policy := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvidia.com/v1", "kind": "ClusterPolicy", "metadata": map[string]interface{}{"name": NVIDIAClusterPolicyName, "labels": map[string]interface{}{"app.kubernetes.io/version": "v25.3.0"}},
		"spec": map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": true}},
	}}
	policy.SetGroupVersionKind(NVIDIAClusterPolicyGVK)

	now := metav1.NewTime(time.Unix(100, 0))
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-node", Labels: map[string]string{
		NVIDIAGPUNodeLabel:       NVIDIAGPUNodeValue,
		NVIDIADriverDeployLabel:  "true",
		NVIDIAToolkitDeployLabel: "true",
	}}}
	objects := []client.Object{policy, node}
	objects = append(objects, providerDaemonSet("driver", "driver-ds", NVIDIADriverDeployLabel), providerDaemonSet("toolkit", "toolkit-ds", NVIDIAToolkitDeployLabel))
	objects = append(objects,
		providerPodObject("driver-pod", "driver-ds", "driver-ds", "driver-rev", now),
		providerPodObject("toolkit-pod", "toolkit-ds", "toolkit-ds", "toolkit-rev", now),
	)
	if withRevision {
		objects = append(objects,
			controllerRevision("driver-ds", "driver-rev"), controllerRevision("toolkit-ds", "toolkit-rev"),
		)
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

func TestNvidiaOperatorRegistersMetricsTargets(t *testing.T) {
	registry := metricsconfig.NewRegistry()
	ctrlClient := newNvidiaProviderClient(t, true)
	_, err := NewNvidiaAcceleratorOperator().Reconcile(context.Background(), &OperatorContext{
		Client:         ctrlClient,
		MetricsTargets: registry,
		Cluster: &v1.Cluster{
			Spec: &v1.ClusterSpec{},
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	targets := registry.Snapshot().Targets
	if len(targets) != 2 {
		t.Fatalf("targets=%d, want 2 without virtualization: %#v", len(targets), targets)
	}
}
