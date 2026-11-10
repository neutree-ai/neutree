// Package operator contains accelerator-specific Kubernetes node state owners.
package operator

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type OperatorContext struct {
	Cluster   *v1.Cluster
	Client    client.Client
	Namespace string
}

type OperatorResult struct{}

type AcceleratorOperator interface {
	Name() string
	AcceleratorType() string
	Reconcile(context.Context, *OperatorContext) (OperatorResult, error)
	Status(context.Context, *OperatorContext) (*v1.ComponentStatus, error)
	Delete(context.Context, *OperatorContext) error
}
