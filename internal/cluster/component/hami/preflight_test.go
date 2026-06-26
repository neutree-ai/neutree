package hami

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// newManagedHAMiWebhook creates a MutatingWebhookConfiguration with the HAMi managed
// label and optional cluster identity labels.
func newManagedHAMiWebhook(clusterName, workspace string) *admissionregistrationv1.MutatingWebhookConfiguration {
	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: WebhookName,
			Labels: map[string]string{
				ManagedComponentLabelKey:           ManagedComponentLabelValue,
				v1.NeutreeClusterLabelKey:          clusterName,
				v1.NeutreeClusterWorkspaceLabelKey: workspace,
			},
		},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: "hami-webhook.projecthami.io",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				CABundle: []byte("test"),
			},
		}},
	}
}

func TestPreflightRejectsWebhookManagedByAnotherCluster(t *testing.T) {
	// A HAMi webhook already exists and is labelled as managed by another
	// Neutree cluster (different workspace/name).
	cluster := newTestCluster()
	webhook := newManagedHAMiWebhook("other-cluster", "other-workspace")
	ctrlClient := newHAMiFakeClient(t, webhook)

	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, ctrlClient)

	err := component.Preflight(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "managed by another Neutree cluster")
}

func TestPreflightAllowsWebhookManagedByThisCluster(t *testing.T) {
	// Restart: the webhook already exists and is labelled as managed by
	// THIS Neutree cluster.
	cluster := newTestCluster()
	webhook := newManagedHAMiWebhook(cluster.Metadata.Name, cluster.Metadata.Workspace)
	ctrlClient := newHAMiFakeClient(t, webhook)

	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, ctrlClient)

	err := component.Preflight(context.Background())
	require.NoError(t, err)
}

func TestPreflightRejectsUnmanagedWebhook(t *testing.T) {
	// A HAMi webhook exists without the Neutree managed label (user-installed).
	cluster := newTestCluster()
	webhook := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: WebhookName,
		},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: "hami-webhook.projecthami.io",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				CABundle: []byte("test"),
			},
		}},
	}
	ctrlClient := newHAMiFakeClient(t, webhook)

	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, ctrlClient)

	err := component.Preflight(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "found existing unmanaged HAMi webhook")
}
