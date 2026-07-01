package cluster

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type StaticNodeClusterPlanner struct {
	AcceleratorProfileProvider AcceleratorProfileProvider
}

type AcceleratorProfileProvider interface {
	GetAcceleratorProfile(ctx context.Context, acceleratorType string) (*v1.AcceleratorProfile, error)
}

func (r *StaticNodeClusterPlanner) Plan(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) ([]StaticNodeClusterDesiredNodePlan, error) {
	plans, err := r.buildDesiredNodePlans(ctx, cluster, currentNodes)
	if err != nil {
		return nil, err
	}

	return plans, nil
}

type StaticNodeClusterRayVerifier interface {
	VerifyRayCluster(ctx context.Context, cluster *v1.StaticNodeCluster) error
}

func RequireStaticNodeClusterRayVerified(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
	verifier StaticNodeClusterRayVerifier,
) v1.StaticNodeClusterStatus {
	if status.Phase != v1.StaticNodeClusterPhaseReady {
		return status
	}

	if verifier == nil {
		return status
	}

	if err := verifier.VerifyRayCluster(ctx, cluster); err != nil {
		if upgrade := staticNodeClusterUpgrade(cluster, nil, nil); upgrade != nil {
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
