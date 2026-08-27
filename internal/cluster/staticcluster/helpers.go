package staticcluster

import (
	"context"
	"maps"

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
	accelerator v1.StaticNodeAcceleratorStatus,
) (*v1.AcceleratorProfile, error) {
	if accelerator.Type == "" || accelerator.Type == v1.StaticNodeAcceleratorTypeCPU {
		return nil, nil
	}

	if r == nil || r.AcceleratorProfileProvider == nil {
		return nil, errors.New("accelerator profile provider is required")
	}

	profile, err := r.AcceleratorProfileProvider.GetAcceleratorProfile(ctx, accelerator.Type)
	if err != nil || profile == nil {
		return nil, err
	}

	resolvedProfile := *profile
	if resolvedProfile.AcceleratorType == "" {
		resolvedProfile.AcceleratorType = accelerator.Type
	}

	provider, ok := r.AcceleratorProfileProvider.(staticNodeRuntimeConfigProvider)
	if !ok {
		return &resolvedProfile, nil
	}

	runtimeConfig, err := provider.GetStaticNodeRuntimeConfig(ctx, &accelerator)
	if err != nil || runtimeConfig == nil {
		return &resolvedProfile, err
	}

	resolvedProfile.ClusterRuntime = mergeRuntimeConfig(profile.ClusterRuntime, runtimeConfig)

	return &resolvedProfile, nil
}

func mergeRuntimeConfig(base, override *v1.RuntimeConfig) *v1.RuntimeConfig {
	if base == nil {
		return copyRuntimeConfig(override)
	}

	result := copyRuntimeConfig(base)
	if override.ImageSuffix != "" {
		result.ImageSuffix = override.ImageSuffix
	}

	if override.Runtime != "" {
		result.Runtime = override.Runtime
	}

	if override.Env != nil {
		if result.Env == nil {
			result.Env = map[string]string{}
		}

		for key, value := range override.Env {
			result.Env[key] = value
		}
	}

	if len(override.Options) > 0 {
		result.Options = append([]string(nil), override.Options...)
	}

	return result
}

func copyRuntimeConfig(config *v1.RuntimeConfig) *v1.RuntimeConfig {
	if config == nil {
		return nil
	}

	result := *config
	result.Env = maps.Clone(config.Env)
	result.Options = append([]string(nil), config.Options...)

	return &result
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
