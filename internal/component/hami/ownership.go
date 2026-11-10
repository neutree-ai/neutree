// Package hami contains shared HAMi ownership checks.
package hami

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	WebhookName                = "hami-webhook"
	ManagedComponentLabelKey   = "neutree.ai/component"
	ManagedComponentLabelValue = "hami"
)

// ValidateWebhookOwnership protects the shared HAMi installation before either owner applies resources.
func ValidateWebhookOwnership(ctx context.Context, reader client.Reader, cluster *v1.Cluster) error {
	webhook := &unstructured.Unstructured{}
	webhook.SetAPIVersion("admissionregistration.k8s.io/v1")
	webhook.SetKind("MutatingWebhookConfiguration")

	if err := reader.Get(ctx, client.ObjectKey{Name: WebhookName}, webhook); err != nil {
		return client.IgnoreNotFound(err)
	}

	labels := webhook.GetLabels()
	if labels[ManagedComponentLabelKey] != ManagedComponentLabelValue {
		return errors.New("found existing unmanaged HAMi webhook hami-webhook")
	}

	if labels[v1.NeutreeClusterWorkspaceLabelKey] != cluster.Metadata.Workspace ||
		labels[v1.NeutreeClusterLabelKey] != cluster.Metadata.Name {
		return errors.New("found existing HAMi webhook managed by another Neutree cluster")
	}

	return nil
}
