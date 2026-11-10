package metrics

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/operator"
	"github.com/neutree-ai/neutree/internal/deploy"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
)

func operatorMetricsRegistry() metricsconfig.Registry {
	registry := metricsconfig.NewRegistry()
	if err := registry.Replace("operator/nvidia", []metricsconfig.Target{
		{
			JobName: "nvidia-node-agent", Namespace: "default", Port: 19101,
			Selectors: metricsconfig.PodSelectors(map[string]string{"neutree.ai/accelerator-operator": "nvidia", "neutree.ai/metrics-target": "node-agent"})},
		{
			JobName: "accelerator-exporter-nvidia-gpu", Namespace: "default", Port: 19400,
			Selectors: metricsconfig.PodSelectors(map[string]string{"neutree.ai/accelerator-operator": "nvidia", "neutree.ai/metrics-target": "accelerator-exporter"})},
	}); err != nil {
		panic(err)
	}
	return registry
}

func claimedOperatorLabels() map[string]string {
	return map[string]string{
		"neutree.ai/accelerator-operator": "nvidia", "neutree.ai/accelerator-deployment-mode": "operator",
		"neutree.ai/metrics-target": "node-agent",
		v1.NeutreeClusterLabelKey:   "test", v1.NeutreeClusterWorkspaceLabelKey: "default",
	}
}

func operatorMetricsComponent(ctrClient client.Client) *MetricsComponent {
	return (&MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.2.1"},
		},
		namespace: "default", ctrlClient: ctrClient,
		metricsRemoteWriteURL: "https://metrics.example.com/write",
	}).WithAcceleratorOperators(map[string]operator.AcceleratorOperator{"nvidia_gpu": operator.NewNvidiaAcceleratorOperator(nil)}).
		WithMetricsTargets(operatorMetricsRegistry())
}

type ownershipApplyClient struct{ client.Client }

func (c *ownershipApplyClient) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if patch.Type() != types.ApplyPatchType {
		return c.Client.Patch(ctx, object, patch, opts...)
	}
	current := object.DeepCopyObject().(client.Object)
	if err := c.Client.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
		if apierrors.IsNotFound(err) {
			return c.Client.Create(ctx, object)
		}
		return err
	}
	object.SetResourceVersion(current.GetResourceVersion())
	return c.Client.Update(ctx, object)
}

func TestMetricsApplyTransfersOnlyClaimedOperatorResources(t *testing.T) {
	ctx := context.Background()
	legacy := &appsv1.DaemonSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"},
		ObjectMeta: metav1.ObjectMeta{Name: neutreeNodeAgentMetricsName, Namespace: "default"},
	}
	claimed := legacy.DeepCopy()
	claimed.Labels = claimedOperatorLabels()
	claimed.UID = "operator-pod-template-owner"
	claimed.Spec.Template.Spec.Containers = []corev1.Container{{Name: "node-agent", Env: []corev1.EnvVar{{Name: "NVIDIA_VISIBLE_DEVICES", Value: "all"}}}}
	unclaimed := legacy.DeepCopy()
	unclaimed.Name = "unclaimed-exporter"
	wrongOwner := legacy.DeepCopy()
	wrongOwner.Name = "another-owner"
	wrongOwner.Labels = map[string]string{"neutree.ai/accelerator-operator": "other", "neutree.ai/metrics-target": "node-agent"}
	ctrClient := &ownershipApplyClient{Client: fake.NewClientBuilder().WithObjects(claimed, unclaimed, wrongOwner).Build()}
	component := operatorMetricsComponent(ctrClient)
	old := []unstructured.Unstructured{}
	for _, object := range []*appsv1.DaemonSet{legacy, unclaimed, wrongOwner} {
		data, err := json.Marshal(object)
		require.NoError(t, err)
		var converted unstructured.Unstructured
		require.NoError(t, converted.UnmarshalJSON(data))
		old = append(old, converted)
	}
	data, err := json.Marshal(old)
	require.NoError(t, err)
	store := deploy.NewConfigStore(ctrClient)
	require.NoError(t, store.Set(ctx, "default", "test", "metrics", string(data), nil))
	for range 2 {
		require.NoError(t, component.ApplyResources(ctx))
		got := &appsv1.DaemonSet{}
		require.NoError(t, ctrClient.Get(ctx, client.ObjectKeyFromObject(claimed), got))
		require.Equal(t, claimed.UID, got.UID)
		require.Equal(t, claimed.Spec.Template.Spec.Containers, got.Spec.Template.Spec.Containers)
		require.Equal(t, claimed.Labels, got.Labels)
	}
	for _, removed := range []*appsv1.DaemonSet{unclaimed, wrongOwner} {
		require.True(t, apierrors.IsNotFound(ctrClient.Get(ctx, client.ObjectKeyFromObject(removed), &appsv1.DaemonSet{})))
	}
	config, err := store.Get(ctx, "default", "test", "metrics")
	require.NoError(t, err)
	var saved []unstructured.Unstructured
	require.NoError(t, json.Unmarshal([]byte(config), &saved))
	for _, object := range saved {
		require.NotEqual(t, neutreeNodeAgentMetricsName, object.GetName(), "operator resource retained in Metrics last-applied")
	}
}

func TestMetricsDeletePreservesTransferredOperatorResources(t *testing.T) {
	ctx := context.Background()
	claimed := &appsv1.DaemonSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"},
		ObjectMeta: metav1.ObjectMeta{Name: neutreeNodeAgentMetricsName, Namespace: "default", Labels: claimedOperatorLabels()},
	}
	unclaimed := claimed.DeepCopy()
	unclaimed.Name = "old-legacy-client"
	unclaimed.Labels = nil
	component := operatorMetricsComponent(fake.NewClientBuilder().WithObjects(claimed, unclaimed).Build())
	old, err := json.Marshal([]*appsv1.DaemonSet{claimed, unclaimed})
	require.NoError(t, err)
	require.NoError(t, deploy.NewConfigStore(component.ctrlClient).Set(ctx, "default", "test", "metrics", string(old), nil))
	_, err = component.DeleteResources(ctx)
	require.NoError(t, err)
	require.NoError(t, component.ctrlClient.Get(ctx, client.ObjectKeyFromObject(claimed), &appsv1.DaemonSet{}))
	require.True(t, apierrors.IsNotFound(component.ctrlClient.Get(ctx, client.ObjectKeyFromObject(unclaimed), &appsv1.DaemonSet{})))
	deleted, err := component.DeleteResources(ctx)
	require.NoError(t, err)
	require.True(t, deleted)
}
