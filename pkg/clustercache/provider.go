// Package clustercache defines the optional cluster cache integration contract.
// Implementations and their installation assets are supplied by the distribution.
package clustercache

import (
	"context"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Context contains the cluster infrastructure already prepared by the controller.
// SaveStatus persists operation intent before a provider sends an external request.
type Context struct {
	Cluster         *v1.Cluster
	Client          client.Client
	RESTConfig      *rest.Config
	Namespace       string
	ImagePrefix     string
	ImagePullSecret string
	SaveStatus      func(*v1.ClusterStatus) error
}

// Provider manages cache independently from ordinary cluster health. Delete must
// finish before the cluster namespace is removed; errors block normal deletion.
type Provider interface {
	Reconcile(context.Context, *Context) error
	Delete(context.Context, *Context) error
}
