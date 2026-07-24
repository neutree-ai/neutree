package staticcluster

import (
	"context"
	"fmt"
	"sort"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type Planner struct {
	AcceleratorProfileProvider AcceleratorProfileProvider
	MetricsRemoteWriteURL      string
}

type AcceleratorProfileProvider interface {
	GetAcceleratorProfile(ctx context.Context, acceleratorType string) (*v1.AcceleratorProfile, error)
}

type DesiredNodePlan struct {
	Node             *v1.StaticNode
	Accelerator      *v1.StaticNodeAcceleratorStatus
	TargetComponents []v1.NodeComponentSpec
}

func (r *Planner) Plan(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) ([]DesiredNodePlan, error) {
	plans, err := r.buildDesiredNodePlans(ctx, cluster, currentNodes)
	if err != nil {
		return nil, err
	}

	return plans, nil
}

func (r *Planner) buildDesiredNodePlans(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) ([]DesiredNodePlan, error) {
	if cluster == nil {
		return nil, errors.New("static node cluster is nil")
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
	plans := make([]DesiredNodePlan, 0, len(cluster.Spec.Nodes))

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
			Kind:       v1.StaticNodeKind,
			Metadata: &v1.Metadata{
				Workspace: cluster.Metadata.Workspace,
				Name:      nodeSpec.Name,
				Labels:    staticNodeLabels(cluster.Metadata.Name, role),
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
			plans = append(plans, DesiredNodePlan{Node: desiredNode})

			continue
		}

		profile, err := r.runtimeProfile(ctx, *acceleratorStatus)
		if err != nil {
			return nil, err
		}

		components := buildNodeComponents(cluster, desiredNode, profile, r.MetricsRemoteWriteURL)
		desiredNode.Spec.Warm = buildNodeWarmSpec(components)
		desiredNode.Spec.Components = components
		plans = append(plans, DesiredNodePlan{
			Accelerator: acceleratorStatus,
			Node:        desiredNode,
		})
	}

	if headCount != 1 {
		return nil, fmt.Errorf("static node cluster requires exactly one head node, got %d", headCount)
	}

	sort.SliceStable(plans, func(i, j int) bool {
		return plans[i].Node.Metadata.Name < plans[j].Node.Metadata.Name
	})

	attachMetricsConfigFiles(cluster, plans)

	// Snapshot the normalized target before upgrade staging can replace plan
	// components with the observed StaticNode Spec.
	for i := range plans {
		plan := &plans[i]
		if plan.Node == nil || plan.Node.Spec == nil {
			continue
		}

		plan.Node.Spec.Components = withComponentConfigHashes(plan.Node.Spec.Components)
		plan.TargetComponents = copyNodeComponents(plan.Node.Spec.Components)
	}

	applyRayRecreateUpgradePlan(cluster, currentByName, plans)

	// Upgrade staging may adopt observed components; normalize the final Spec
	// that will be written to the StaticNode.
	for _, plan := range plans {
		plan.Node.Spec.Components = withComponentConfigHashes(plan.Node.Spec.Components)
	}

	return plans, nil
}
