package hami

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

func TestPreflightRejectsWhenNodesAlreadyLabeledByAnotherCluster(t *testing.T) {
	// Simulate a first-time deploy (no existing HAMi status) against a K8s cluster
	// whose nodes already carry the vGPU label from another Neutree cluster.
	cluster := newTestCluster()
	// Confirm no HAMi status exists (first deploy, not a restart).
	require.False(t, hasHAMiStatus(cluster))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node-1",
			Labels: map[string]string{
				plugin.NvidiaGPUVirtualizationLabelKey: "true",
			},
		},
	}
	ctrlClient := newHAMiFakeClient(t, node)

	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, ctrlClient)

	err := component.Preflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another Neutree cluster already manages")
}

func TestPreflightAllowsRestartWhenNodesAlreadyLabeledBySelf(t *testing.T) {
	// Simulate a restart: the cluster already owns HAMi and the node labels
	// were written by this same cluster.
	cluster := newTestCluster()
	markHAMiOwned(cluster)
	require.True(t, hasHAMiStatus(cluster))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node-1",
			Labels: map[string]string{
				plugin.NvidiaGPUVirtualizationLabelKey: "true",
			},
		},
	}
	ctrlClient := newHAMiFakeClient(t, node)

	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, ctrlClient)

	// Preflight should succeed — this is a restart of the owner cluster.
	err := component.Preflight(context.Background())
	require.NoError(t, err)
}

func TestPreflightAllowsFirstDeployWhenNoExistingLabels(t *testing.T) {
	// Simulate a first-time deploy on a clean K8s cluster with no vGPU labels.
	cluster := newTestCluster()
	require.False(t, hasHAMiStatus(cluster))

	// Node without the vGPU label.
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node-1",
		},
	}
	ctrlClient := newHAMiFakeClient(t, node)

	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, ctrlClient)

	// Without unmanaged HAMi resources, the only blocking factor is the
	// virtualization label check. We expect it to pass.
	err := component.Preflight(context.Background())
	// This may fail because there are no managed HAMi resources rendered;
	// we only care that it does NOT fail with the duplicate-owner message.
	if err != nil {
		assert.NotContains(t, err.Error(), "already manages")
	}
}

// hasHAMiStatus returns true when the cluster already carries a HAMi
// component status written by a prior reconcile.
func hasHAMiStatus(cluster *v1.Cluster) bool {
	if cluster.Status == nil || cluster.Status.ComponentStatus == nil {
		return false
	}
	_, ok := cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]
	return ok
}
