package cluster

import "context"

type ClusterReconcile interface {
	Reconcile(ctx context.Context) error
	ReconcileDelete(ctx context.Context) error
}
