package cluster

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type StaticNodeClusterReconciler struct {
	AcceleratorProfileProvider AcceleratorProfileProvider
	RayVerifier                StaticNodeClusterRayVerifier
}

type AcceleratorProfileProvider interface {
	GetAcceleratorProfile(ctx context.Context, acceleratorType string) (*v1.AcceleratorProfile, bool, error)
}

type StaticNodeClusterReconcilePlan struct {
	DesiredNodes []*v1.StaticNode
	Status       v1.StaticNodeClusterStatus
}

func (r *StaticNodeClusterReconciler) Plan(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) (*StaticNodeClusterReconcilePlan, error) {
	plans, err := r.buildDesiredNodePlans(ctx, cluster, currentNodes)
	if err != nil {
		return nil, err
	}

	status := r.AggregateStatus(cluster, currentNodes, plans)

	return &StaticNodeClusterReconcilePlan{
		DesiredNodes: desiredNodesFromPlans(plans),
		Status:       status,
	}, nil
}

type StaticNodeClusterRayVerifier interface {
	VerifyRayCluster(ctx context.Context, cluster *v1.StaticNodeCluster) error
}

func (r *StaticNodeClusterReconciler) RequireRayClusterVerified(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
) v1.StaticNodeClusterStatus {
	if status.Phase != v1.StaticNodeClusterPhaseReady {
		return status
	}

	if r == nil || r.RayVerifier == nil {
		return status
	}

	if err := r.RayVerifier.VerifyRayCluster(ctx, cluster); err != nil {
		if upgrade := staticNodeClusterUpgrade(cluster); upgrade != nil {
			status.Phase = v1.StaticNodeClusterPhaseUpgrading
			status.Version = upgrade.ObservedVersion
		} else {
			status.Phase = v1.StaticNodeClusterPhaseProvisioning
			status.Version = ""

			if cluster != nil && cluster.Status != nil {
				status.Version = cluster.Status.Version
			}
		}

		status.ErrorMessage = "Ray cluster verification failed: " + err.Error()
	}

	return status
}

func (r *StaticNodeClusterReconciler) BuildDesiredNodes(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) ([]*v1.StaticNode, error) {
	plans, err := r.buildDesiredNodePlans(ctx, cluster, currentNodes)
	if err != nil {
		return nil, err
	}

	nodes := make([]*v1.StaticNode, 0, len(plans))
	for _, plan := range plans {
		nodes = append(nodes, plan.Node)
	}

	return nodes, nil
}

func desiredNodesFromPlans(plans []staticNodeDesiredPlan) []*v1.StaticNode {
	nodes := make([]*v1.StaticNode, 0, len(plans))
	for _, plan := range plans {
		nodes = append(nodes, plan.Node)
	}

	return nodes
}
