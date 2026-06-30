package cluster

import (
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

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
	upgrade := staticNodeClusterUpgrade(cluster)
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

func staticNodeClusterUpgrade(cluster *v1.StaticNodeCluster) *staticNodeClusterUpgradeState {
	if cluster == nil || cluster.Spec == nil || cluster.Status == nil {
		return nil
	}

	observedVersion := cluster.Status.Version
	if observedVersion == "" || observedVersion == cluster.Spec.Version {
		return nil
	}

	step := staticNodeClusterUpgradeStepFromStatus(cluster.Status)
	if step == "" {
		step = staticNodeClusterUpgradeStepWarming
	}

	return &staticNodeClusterUpgradeState{
		ObservedVersion: observedVersion,
		Step:            step,
	}
}

func staticNodeClusterUpgradeStepFromStatus(status *v1.StaticNodeClusterStatus) staticNodeClusterUpgradeStep {
	if status == nil {
		return ""
	}

	for _, part := range strings.Split(status.ErrorMessage, ";") {
		step := staticNodeClusterUpgradeStep(strings.TrimSpace(part))
		if staticNodeClusterUpgradeStepValid(step) {
			return step
		}
	}

	return ""
}

func staticNodeClusterUpgradeStepValid(step staticNodeClusterUpgradeStep) bool {
	switch step {
	case staticNodeClusterUpgradeStepWarming,
		staticNodeClusterUpgradeStepStoppingWorkers,
		staticNodeClusterUpgradeStepStartingHead,
		staticNodeClusterUpgradeStepStartingWorkers,
		staticNodeClusterUpgradeStepVerifying:
		return true
	default:
		return false
	}
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

func advanceStaticNodeClusterUpgradeStatus(
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
	status v1.StaticNodeClusterStatus,
) v1.StaticNodeClusterStatus {
	step := staticNodeClusterUpgradeStepFromStatus(&status)
	if step == "" {
		return status
	}

	switch step {
	case staticNodeClusterUpgradeStepWarming:
		if status.WarmReady {
			step = staticNodeClusterUpgradeStepStoppingWorkers
		}
	case staticNodeClusterUpgradeStepStoppingWorkers:
		if staticNodeClusterWorkersStopped(cluster, currentNodes) {
			step = staticNodeClusterUpgradeStepStartingHead
		}
	case staticNodeClusterUpgradeStepStartingHead:
		if staticNodeClusterHeadRayRunningTarget(cluster, currentNodes) {
			step = staticNodeClusterUpgradeStepStartingWorkers
		}
	case staticNodeClusterUpgradeStepStartingWorkers:
		if staticNodeClusterRayRuntimeRunningTarget(cluster, currentNodes) {
			step = staticNodeClusterUpgradeStepVerifying
		}
	case staticNodeClusterUpgradeStepVerifying:
		if staticNodeClusterRayRuntimeRunningTarget(cluster, currentNodes) &&
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

func staticNodeClusterRayRuntimeRunningTarget(cluster *v1.StaticNodeCluster, nodes []*v1.StaticNode) bool {
	return staticNodeClusterHeadRayRunningTarget(cluster, nodes) && staticNodeClusterWorkersRayRunningTarget(cluster, nodes)
}

func staticNodeClusterHeadRayRunningTarget(cluster *v1.StaticNodeCluster, nodes []*v1.StaticNode) bool {
	headName := staticNodeClusterHeadName(cluster)
	if headName == "" {
		return false
	}

	node := staticNodeByName(nodes)[headName]
	if node == nil || node.Status == nil {
		return false
	}

	return rayComponentRunningTarget(node.Status.Components, v1.NodeComponentTypeRayHead, buildRayRuntimeImage(cluster))
}

func staticNodeClusterWorkersRayRunningTarget(cluster *v1.StaticNodeCluster, nodes []*v1.StaticNode) bool {
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

		if !rayComponentRunningTarget(node.Status.Components, v1.NodeComponentTypeRayWorker, buildRayRuntimeImage(cluster)) {
			return false
		}
	}

	return true
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
		if component.Type == v1.NodeComponentTypeRayWorker || component.Name == "ray-worker" {
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
