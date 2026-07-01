package cluster

import (
	"context"
	"fmt"
	"sort"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type staticNodeDesiredPlan struct {
	Node                              *v1.StaticNode
	TargetComponents                  []v1.NodeComponentSpec
	Accelerator                       *v1.StaticNodeAcceleratorStatus
	Profile                           *v1.AcceleratorProfile
	AcceleratorProfileFallbackMessage string
}

func (r *StaticNodeClusterReconciler) buildDesiredNodePlans(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) ([]staticNodeDesiredPlan, error) {
	if cluster == nil {
		return nil, errors.New("static node cluster is nil")
	}

	if cluster.Metadata == nil || cluster.Metadata.Name == "" {
		return nil, errors.New("static node cluster metadata.name is required")
	}

	if cluster.Spec == nil {
		return nil, errors.New("static node cluster spec is required")
	}

	if cluster.Spec.Version == "" {
		return nil, errors.New("static node cluster spec.version is required")
	}

	if cluster.Spec.ImageRegistry == "" {
		return nil, errors.New("static node cluster spec.image_registry is required")
	}

	if len(cluster.Spec.Nodes) == 0 {
		return nil, errors.New("static node cluster spec.nodes is required")
	}

	nodeNames := make(map[string]struct{}, len(cluster.Spec.Nodes))
	headCount := 0
	currentByName := staticNodeByName(currentNodes)
	plans := make([]staticNodeDesiredPlan, 0, len(cluster.Spec.Nodes))

	for _, nodeSpec := range cluster.Spec.Nodes {
		if nodeSpec.Name == "" {
			return nil, errors.New("static node name is required")
		}

		if nodeSpec.IP == "" {
			return nil, fmt.Errorf("static node %s ip is required", nodeSpec.Name)
		}

		if _, exists := nodeNames[nodeSpec.Name]; exists {
			return nil, fmt.Errorf("duplicate static node %s", nodeSpec.Name)
		}

		nodeNames[nodeSpec.Name] = struct{}{}

		role := normalizeStaticNodeRole(nodeSpec.Role)
		if role == v1.StaticNodeRoleHead {
			headCount++
		}

		desiredNode := &v1.StaticNode{
			APIVersion: "v1",
			Kind:       "StaticNode",
			Metadata: &v1.Metadata{
				Workspace:   cluster.Metadata.Workspace,
				Name:        nodeSpec.Name,
				Labels:      staticNodeLabels(cluster.Metadata.Name, role),
				Annotations: copyStringMap(cluster.Metadata.Annotations),
			},
			Spec: &v1.StaticNodeSpec{
				Cluster: cluster.Metadata.Name,
				IP:      nodeSpec.IP,
				Role:    role,
				SSHAuth: copyAuth(nodeSpec.SSHAuth),
				Warm:    &v1.WarmSpec{},
			},
		}

		acceleratorStatus := currentStaticNodeAcceleratorStatus(currentByName[nodeSpec.Name])
		if acceleratorStatus == nil {
			plans = append(plans, staticNodeDesiredPlan{Node: desiredNode})

			continue
		}

		profile, fallbackMessage, err := r.runtimeProfile(ctx, *acceleratorStatus)
		if err != nil {
			return nil, err
		}

		components := buildNodeComponents(cluster, desiredNode, profile)
		desiredNode.Spec.Warm = buildNodeWarmSpec(components)
		desiredNode.Spec.Components = components
		plans = append(plans, staticNodeDesiredPlan{
			Node:                              desiredNode,
			TargetComponents:                  copyNodeComponents(components),
			Accelerator:                       acceleratorStatus,
			Profile:                           profile,
			AcceleratorProfileFallbackMessage: fallbackMessage,
		})
	}

	if headCount != 1 {
		return nil, fmt.Errorf("static node cluster requires exactly one head node, got %d", headCount)
	}

	sort.SliceStable(plans, func(i, j int) bool {
		return plans[i].Node.Metadata.Name < plans[j].Node.Metadata.Name
	})

	applyRayRecreateUpgradePlan(cluster, currentByName, plans)

	for _, plan := range plans {
		plan.Node.Spec.Components = withComponentConfigHashes(plan.Node.Spec.Components)
	}

	return plans, nil
}
