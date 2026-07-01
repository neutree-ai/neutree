package cluster

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func joinStatusMessages(current string, messages ...string) string {
	result := make([]string, 0, len(messages)+1)
	if current != "" {
		result = append(result, current)
	}

	result = append(result, messages...)

	return strings.Join(result, "; ")
}

type StaticNodeClusterStatusAggregator struct{}

func (a StaticNodeClusterStatusAggregator) Aggregate(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	plans []StaticNodeClusterDesiredNodePlan,
) v1.StaticNodeClusterStatus {
	desiredNodeNames, headName := staticNodeClusterDesiredNodeNames(cluster)

	status := v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseProvisioning,
		DesiredNodes: len(desiredNodeNames),
	}
	if upgrade := staticNodeClusterUpgrade(cluster, nodes, plans); upgrade != nil {
		status.Version = upgrade.ObservedVersion
		status.ErrorMessage = string(upgrade.Step)
		status.Phase = v1.StaticNodeClusterPhaseUpgrading
	}

	if status.DesiredNodes == 0 {
		status.Phase = v1.StaticNodeClusterPhaseFailed
		status.ErrorMessage = "static node cluster has no desired nodes"

		return status
	}

	seenDesiredNodes := map[string]struct{}{}
	warmReady := true
	anyNodeFailed := false

	for _, node := range nodes {
		if node == nil || node.Metadata == nil || node.Status == nil {
			warmReady = false

			continue
		}

		if _, desired := desiredNodeNames[node.Metadata.Name]; !desired {
			continue
		}

		seenDesiredNodes[node.Metadata.Name] = struct{}{}

		if node.Status.Phase == v1.StaticNodePhaseReady {
			status.ReadyNodes++
		}

		if node.Metadata.Name == headName {
			status.HeadReady = node.Status.Phase == v1.StaticNodePhaseReady
		}

		if node.Status.Phase == v1.StaticNodePhaseFailed {
			anyNodeFailed = true
		}

		if node.Status.Warm == nil || !node.Status.Warm.Ready {
			warmReady = false
		}
	}

	if len(seenDesiredNodes) < status.DesiredNodes {
		warmReady = false
	}

	status.WarmReady = warmReady

	switch {
	case anyNodeFailed:
		status.Phase = v1.StaticNodeClusterPhaseFailed
	case status.Phase == v1.StaticNodeClusterPhaseUpgrading:
		status.Phase = v1.StaticNodeClusterPhaseUpgrading
	case status.ReadyNodes == status.DesiredNodes && status.HeadReady && status.WarmReady:
		status.Phase = v1.StaticNodeClusterPhaseReady
	case status.HeadReady && status.ReadyNodes > 0:
		status.Phase = v1.StaticNodeClusterPhaseDegraded
	default:
		status.Phase = v1.StaticNodeClusterPhaseProvisioning
	}

	if status.Phase == v1.StaticNodeClusterPhaseReady && cluster != nil && cluster.Spec != nil {
		status.Version = cluster.Spec.Version
	}

	if status.Phase != v1.StaticNodeClusterPhaseReady {
		status.ErrorMessage = joinStatusMessages(
			status.ErrorMessage,
			staticNodeClusterNodeStatusMessages(cluster, staticNodeByName(nodes))...,
		)
	}

	// Status decisions intentionally happen in observed-state order:
	// 1. aggregate static-node readiness from current status,
	// 2. advance recreate-upgrade steps only from observed node/component state,
	// 3. require desired components to be written and observed before marking the desired version observed.
	//
	// status.version is the observed/converged version. It must not be advanced
	// to spec.version until desired components have reached static-node status.
	status = advanceStaticNodeClusterUpgradeStatus(cluster, nodes, status, plans)
	status = requireDesiredComponentsObserved(cluster, status, plans, staticNodeByName(nodes))

	return status
}

func requireDesiredComponentsObserved(
	cluster *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
	plans []StaticNodeClusterDesiredNodePlan,
	currentByName map[string]*v1.StaticNode,
) v1.StaticNodeClusterStatus {
	if status.Phase != v1.StaticNodeClusterPhaseReady {
		return status
	}

	messages := desiredComponentMismatchMessages(plans, currentByName)
	if len(messages) == 0 {
		return status
	}

	if upgrade := staticNodeClusterUpgrade(cluster, staticNodesFromByName(currentByName), plans); upgrade != nil {
		status.Phase = v1.StaticNodeClusterPhaseUpgrading
		status.Version = upgrade.ObservedVersion
		status.ErrorMessage = string(staticNodeClusterUpgradeStepVerifying)

		return status
	}

	status.Phase = v1.StaticNodeClusterPhaseProvisioning
	status.ErrorMessage = strings.Join(messages, "; ")

	return status
}

func desiredComponentMismatchMessages(
	plans []StaticNodeClusterDesiredNodePlan,
	currentByName map[string]*v1.StaticNode,
) []string {
	messages := []string{}

	for _, plan := range plans {
		if plan.Node == nil || plan.Node.Metadata == nil || plan.Node.Spec == nil || len(plan.Node.Spec.Components) == 0 {
			continue
		}

		current := currentByName[plan.Node.Metadata.Name]
		if current == nil || current.Status == nil {
			messages = append(messages, "static node "+plan.Node.Metadata.Name+" status is empty")

			continue
		}

		for _, component := range plan.Node.Spec.Components {
			if desiredComponentObserved(component, current.Status.Components) {
				continue
			}

			messages = append(messages, fmt.Sprintf(
				"static node %s component %s is not running desired config",
				plan.Node.Metadata.Name,
				component.Name,
			))
		}
	}

	return messages
}

func desiredComponentObserved(component v1.NodeComponentSpec, statuses []v1.NodeComponentStatus) bool {
	for _, status := range statuses {
		if status.Name != component.Name {
			continue
		}

		if !status.Ready || status.Phase != v1.NodeComponentPhaseRunning {
			return false
		}

		if component.ConfigHash != "" && status.ObservedHash != component.ConfigHash {
			return false
		}

		if component.Image != "" && status.ObservedImage != component.Image {
			return false
		}

		return true
	}

	return false
}

func staticNodeClusterNodeStatusMessages(
	cluster *v1.StaticNodeCluster,
	nodesByName map[string]*v1.StaticNode,
) []string {
	nodeNames := staticNodeClusterDesiredNodeNameList(cluster)
	messages := make([]string, 0, len(nodeNames))

	for _, nodeName := range nodeNames {
		node := nodesByName[nodeName]

		message := staticNodeStatusMessage(nodeName, node)
		if message == "" {
			continue
		}

		messages = append(messages, message)
	}

	return messages
}

func staticNodeStatusMessage(nodeName string, node *v1.StaticNode) string {
	if nodeName == "" {
		return ""
	}

	if node == nil {
		return "static node " + nodeName + " is missing"
	}

	if node.Status == nil {
		return "static node " + nodeName + " status is empty"
	}

	if node.Status.ErrorMessage != "" {
		return fmt.Sprintf("static node %s phase=%s: %s", nodeName, node.Status.Phase, node.Status.ErrorMessage)
	}

	if node.Status.Phase != v1.StaticNodePhaseReady {
		return fmt.Sprintf("static node %s phase=%s", nodeName, node.Status.Phase)
	}

	if node.Status.Warm == nil || !node.Status.Warm.Ready {
		return staticNodeWarmStatusMessage(nodeName, node.Status.Warm)
	}

	return ""
}

func staticNodeWarmStatusMessage(nodeName string, status *v1.WarmStatus) string {
	message := "static node " + nodeName + " warm not ready"
	if status == nil {
		return message
	}

	if status.Message != "" {
		return message + ": " + status.Message
	}

	if status.Reason != "" {
		return message + ": " + status.Reason
	}

	return message
}

func staticNodeClusterDesiredNodeNameList(cluster *v1.StaticNodeCluster) []string {
	if cluster == nil || cluster.Spec == nil {
		return nil
	}

	nodeNames := make([]string, 0, len(cluster.Spec.Nodes))
	seen := map[string]struct{}{}

	for _, node := range cluster.Spec.Nodes {
		if node.Name == "" {
			continue
		}

		if _, ok := seen[node.Name]; ok {
			continue
		}

		seen[node.Name] = struct{}{}

		nodeNames = append(nodeNames, node.Name)
	}

	sort.Strings(nodeNames)

	return nodeNames
}

func staticNodeClusterDesiredNodeNames(cluster *v1.StaticNodeCluster) (map[string]struct{}, string) {
	desiredNodeNames := map[string]struct{}{}
	if cluster == nil || cluster.Spec == nil {
		return desiredNodeNames, ""
	}

	for _, node := range cluster.Spec.Nodes {
		if node.Name != "" {
			desiredNodeNames[node.Name] = struct{}{}
		}
	}

	return desiredNodeNames, staticNodeClusterHeadName(cluster)
}

func staticNodeClusterHeadName(cluster *v1.StaticNodeCluster) string {
	if cluster == nil || cluster.Spec == nil {
		return ""
	}

	for _, node := range cluster.Spec.Nodes {
		if normalizeStaticNodeRole(node.Role) == v1.StaticNodeRoleHead {
			return node.Name
		}
	}

	return ""
}
