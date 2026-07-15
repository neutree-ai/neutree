package staticcluster

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	staticNodeClusterLabelKey = "neutree.ai/static-node-cluster"
	staticNodeRoleLabelKey    = "neutree.ai/static-node-role"
)

func normalizeStaticNodeRole(role v1.StaticNodeRole) v1.StaticNodeRole {
	if role == v1.StaticNodeRoleHead {
		return v1.StaticNodeRoleHead
	}

	return v1.StaticNodeRoleWorker
}

func staticNodeLabels(clusterName string, role v1.StaticNodeRole) map[string]string {
	return map[string]string{
		staticNodeClusterLabelKey: clusterName,
		staticNodeRoleLabelKey:    string(role),
	}
}

func staticNodeByName(nodes []*v1.StaticNode) map[string]*v1.StaticNode {
	result := make(map[string]*v1.StaticNode, len(nodes))

	for _, node := range nodes {
		if node == nil {
			continue
		}

		result[node.Metadata.Name] = node
	}

	return result
}

func currentStaticNodeAcceleratorStatus(node *v1.StaticNode) *v1.StaticNodeAcceleratorStatus {
	if node == nil || node.Status == nil || node.Status.Accelerator == nil {
		return nil
	}

	accelerator := *node.Status.Accelerator

	return &accelerator
}

func (r *Planner) runtimeProfile(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	accelerator v1.StaticNodeAcceleratorStatus,
) (*v1.AcceleratorProfile, error) {
	if accelerator.Type == "" || accelerator.Type == v1.StaticNodeAcceleratorTypeCPU {
		return nil, nil
	}

	if r == nil || r.AcceleratorProfileProvider == nil {
		return nil, errors.New("accelerator profile provider is required")
	}

	profile, err := r.AcceleratorProfileProvider.GetAcceleratorProfile(ctx, accelerator.Type)
	if err != nil {
		return nil, err
	}
	if cluster == nil || cluster.Spec == nil || cluster.Spec.RuntimeProfile == "" {
		return profile, nil
	}
	provider, ok := r.AcceleratorProfileProvider.(RuntimeProfileConfigProvider)
	if !ok {
		return nil, errors.New("accelerator runtime profile provider is required")
	}
	runtime, err := provider.GetRuntimeConfigForProfile(ctx, accelerator.Type, cluster.Spec.RuntimeProfile)
	if err != nil {
		return nil, err
	}
	copy := *profile
	copy.ClusterRuntime = &runtime
	return &copy, nil
}

func staticComponentImage(cluster *v1.StaticNodeCluster, image string) string {
	imageRegistry := ""
	if cluster != nil && cluster.Spec != nil {
		imageRegistry = cluster.Spec.ImageRegistry
	}

	return util.RewriteImageRef(imageRegistry, image)
}

func copyAuth(auth *v1.Auth) *v1.Auth {
	if auth == nil {
		return nil
	}

	copied := *auth

	return &copied
}
