package cluster

import v1 "github.com/neutree-ai/neutree/api/v1"

type staticNodeClusterUpgradeStep string

const (
	staticNodeClusterUpgradeStepWarming         staticNodeClusterUpgradeStep = "Warming"
	staticNodeClusterUpgradeStepStoppingWorkers staticNodeClusterUpgradeStep = "StoppingWorkers"
	staticNodeClusterUpgradeStepStartingHead    staticNodeClusterUpgradeStep = "StartingHead"
	staticNodeClusterUpgradeStepStartingWorkers staticNodeClusterUpgradeStep = "StartingWorkers"
	staticNodeClusterUpgradeStepVerifying       staticNodeClusterUpgradeStep = "Verifying"
)

func applyRayRecreateUpgradePlan(
	cluster *v1.StaticNodeCluster,
	currentByName map[string]*v1.StaticNode,
	plans []staticNodeDesiredPlan,
) {
	upgrade := staticNodeClusterUpgrade(cluster, staticNodesFromByName(currentByName), plans)
	if upgrade == nil {
		return
	}

	for i := range plans {
		plan := &plans[i]
		if plan.Node == nil || plan.Node.Metadata == nil || plan.Node.Spec == nil {
			continue
		}

		current := currentByName[plan.Node.Metadata.Name]

		switch upgrade.Step {
		case staticNodeClusterUpgradeStepWarming:
			useCurrentComponentsIfPresent(plan.Node, current)
		case staticNodeClusterUpgradeStepStoppingWorkers:
			useCurrentComponentsIfPresent(plan.Node, current)

			if plan.Node.Spec.Role == v1.StaticNodeRoleWorker {
				removeRayWorkerComponent(plan.Node)
			}
		case staticNodeClusterUpgradeStepStartingHead:
			if plan.Node.Spec.Role == v1.StaticNodeRoleWorker {
				useCurrentComponentsIfPresent(plan.Node, current)
				removeRayWorkerComponent(plan.Node)
			}
		}
	}
}

type staticNodeClusterUpgradeState struct {
	ObservedVersion string
	Step            staticNodeClusterUpgradeStep
}

func staticNodeClusterUpgrade(
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
	plans []staticNodeDesiredPlan,
) *staticNodeClusterUpgradeState {
	if cluster == nil || cluster.Spec == nil || cluster.Status == nil {
		return nil
	}

	observedVersion := cluster.Status.Version
	if observedVersion == "" || observedVersion == cluster.Spec.Version {
		return nil
	}

	return &staticNodeClusterUpgradeState{
		ObservedVersion: observedVersion,
		Step:            staticNodeClusterUpgradeStepFromObservedState(cluster, currentNodes, plans),
	}
}

func staticNodeClusterUpgradeStepFromObservedState(
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
	plans []staticNodeDesiredPlan,
) staticNodeClusterUpgradeStep {
	// Upgrade progress is derived from observed static-node/component state.
	// Status.error_message may display the current step, but it must not drive
	// the state machine; otherwise a stale or user-visible message can change
	// desired components on the next reconcile.
	switch {
	case staticNodeClusterRayRuntimeRunningTarget(cluster, currentNodes, plans):
		return staticNodeClusterUpgradeStepVerifying
	case staticNodeClusterWorkersStopped(cluster, currentNodes) &&
		staticNodeClusterHeadRayRunningTarget(cluster, currentNodes, plans):
		return staticNodeClusterUpgradeStepStartingWorkers
	case staticNodeClusterWorkersStopped(cluster, currentNodes):
		return staticNodeClusterUpgradeStepStartingHead
	case staticNodeClusterWarmReady(cluster, currentNodes):
		return staticNodeClusterUpgradeStepStoppingWorkers
	default:
		return staticNodeClusterUpgradeStepWarming
	}
}

func staticNodeClusterWarmReady(cluster *v1.StaticNodeCluster, nodes []*v1.StaticNode) bool {
	desiredNodeNames, _ := staticNodeClusterDesiredNodeNames(cluster)
	if len(desiredNodeNames) == 0 {
		return false
	}

	nodesByName := staticNodeByName(nodes)
	for name := range desiredNodeNames {
		node := nodesByName[name]
		if node == nil || node.Status == nil || node.Status.Warm == nil || !node.Status.Warm.Ready {
			return false
		}
	}

	return true
}

func useCurrentComponentsIfPresent(node *v1.StaticNode, current *v1.StaticNode) {
	if node == nil || node.Spec == nil || current == nil || current.Spec == nil || len(current.Spec.Components) == 0 {
		return
	}

	node.Spec.Components = copyNodeComponents(current.Spec.Components)
}

func removeRayWorkerComponent(node *v1.StaticNode) {
	if node == nil || node.Spec == nil {
		return
	}

	components := make([]v1.NodeComponentSpec, 0, len(node.Spec.Components))

	for i := range node.Spec.Components {
		if node.Spec.Components[i].Type != v1.NodeComponentTypeRayWorker {
			components = append(components, node.Spec.Components[i])
		}
	}

	node.Spec.Components = components
}

func copyNodeComponents(components []v1.NodeComponentSpec) []v1.NodeComponentSpec {
	result := make([]v1.NodeComponentSpec, len(components))
	copy(result, components)

	return result
}

func staticNodesFromByName(nodesByName map[string]*v1.StaticNode) []*v1.StaticNode {
	nodes := make([]*v1.StaticNode, 0, len(nodesByName))
	for _, node := range nodesByName {
		nodes = append(nodes, node)
	}

	return nodes
}

func advanceStaticNodeClusterUpgradeStatus(
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
	status v1.StaticNodeClusterStatus,
	plans []staticNodeDesiredPlan,
) v1.StaticNodeClusterStatus {
	if status.Phase == v1.StaticNodeClusterPhaseFailed {
		return status
	}

	upgrade := staticNodeClusterUpgrade(cluster, currentNodes, plans)
	if upgrade == nil {
		return status
	}

	step := upgrade.Step
	if step == staticNodeClusterUpgradeStepVerifying {
		if staticNodeClusterRayRuntimeRunningTarget(cluster, currentNodes, plans) &&
			status.ReadyNodes == status.DesiredNodes &&
			status.HeadReady &&
			status.WarmReady {
			status.Version = cluster.Spec.Version
			status.Phase = v1.StaticNodeClusterPhaseReady
			status.ErrorMessage = ""

			return status
		}
	}

	status.Phase = v1.StaticNodeClusterPhaseUpgrading
	status.Version = upgrade.ObservedVersion
	status.ErrorMessage = string(step)

	return status
}

func staticNodeClusterWorkersStopped(cluster *v1.StaticNodeCluster, nodes []*v1.StaticNode) bool {
	workerNames := staticNodeClusterWorkerNames(cluster)
	if len(workerNames) == 0 {
		return true
	}

	stopped := map[string]bool{}

	for _, node := range nodes {
		if node == nil || node.Metadata == nil || node.Status == nil {
			continue
		}

		if _, ok := workerNames[node.Metadata.Name]; !ok {
			continue
		}

		stopped[node.Metadata.Name] = rayComponentStopped(node.Status.Components)
	}

	for name := range workerNames {
		if !stopped[name] {
			return false
		}
	}

	return true
}

func staticNodeClusterRayRuntimeRunningTarget(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	plans []staticNodeDesiredPlan,
) bool {
	return staticNodeClusterHeadRayRunningTarget(cluster, nodes, plans) &&
		staticNodeClusterWorkersRayRunningTarget(cluster, nodes, plans)
}

func staticNodeClusterHeadRayRunningTarget(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	plans []staticNodeDesiredPlan,
) bool {
	headName := staticNodeClusterHeadName(cluster)
	if headName == "" {
		return false
	}

	node := staticNodeByName(nodes)[headName]
	if node == nil || node.Status == nil {
		return false
	}

	return rayComponentRunningTarget(
		node.Status.Components,
		v1.NodeComponentTypeRayHead,
		desiredRayComponentImage(plans, headName, v1.NodeComponentTypeRayHead),
	)
}

func staticNodeClusterWorkersRayRunningTarget(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	plans []staticNodeDesiredPlan,
) bool {
	workerNames := staticNodeClusterWorkerNames(cluster)
	if len(workerNames) == 0 {
		return true
	}

	nodesByName := staticNodeByName(nodes)
	for name := range workerNames {
		node := nodesByName[name]
		if node == nil || node.Status == nil {
			return false
		}

		if !rayComponentRunningTarget(
			node.Status.Components,
			v1.NodeComponentTypeRayWorker,
			desiredRayComponentImage(plans, name, v1.NodeComponentTypeRayWorker),
		) {
			return false
		}
	}

	return true
}

func desiredRayComponentImage(
	plans []staticNodeDesiredPlan,
	nodeName string,
	componentType v1.NodeComponentType,
) string {
	for _, plan := range plans {
		if plan.Node == nil || plan.Node.Metadata == nil || plan.Node.Spec == nil {
			continue
		}

		if plan.Node.Metadata.Name != nodeName {
			continue
		}

		components := plan.TargetComponents
		if len(components) == 0 {
			components = plan.Node.Spec.Components
		}

		for _, component := range components {
			if component.Type == componentType || rayComponentNameMatchesType(component.Name, componentType) {
				return component.Image
			}
		}
	}

	return ""
}

func staticNodeClusterWorkerNames(cluster *v1.StaticNodeCluster) map[string]struct{} {
	workerNames := map[string]struct{}{}
	if cluster == nil || cluster.Spec == nil {
		return workerNames
	}

	for _, nodeSpec := range cluster.Spec.Nodes {
		if normalizeStaticNodeRole(nodeSpec.Role) == v1.StaticNodeRoleWorker && nodeSpec.Name != "" {
			workerNames[nodeSpec.Name] = struct{}{}
		}
	}

	return workerNames
}

func rayComponentStopped(components []v1.NodeComponentStatus) bool {
	for _, component := range components {
		if component.Type == v1.NodeComponentTypeRayWorker ||
			rayComponentNameMatchesType(component.Name, v1.NodeComponentTypeRayWorker) {
			return component.Phase == v1.NodeComponentPhaseStopped
		}
	}

	return false
}

func rayComponentRunningTarget(components []v1.NodeComponentStatus, componentType v1.NodeComponentType, targetImage string) bool {
	for _, component := range components {
		if component.Type != componentType && !rayComponentNameMatchesType(component.Name, componentType) {
			continue
		}

		if !component.Ready || component.Phase != v1.NodeComponentPhaseRunning {
			return false
		}

		return targetImage == "" || component.ObservedImage == targetImage
	}

	return false
}

func rayComponentNameMatchesType(name string, componentType v1.NodeComponentType) bool {
	switch componentType {
	case v1.NodeComponentTypeRayHead:
		return name == "ray-head"
	case v1.NodeComponentTypeRayWorker:
		return name == "ray-worker"
	default:
		return false
	}
}
