package cluster

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func joinStatusMessages(current string, messages ...string) string {
	result := make([]string, 0, len(messages)+1)
	appendMessage := func(message string) {
		if message == "" {
			return
		}

		if stringSliceContains(result, message) {
			return
		}

		result = append(result, message)
	}

	if current != "" {
		for _, message := range strings.Split(current, "; ") {
			appendMessage(message)
		}
	}

	for _, message := range messages {
		appendMessage(message)
	}

	return strings.Join(result, "; ")
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

type StaticNodeClusterStatusAggregator struct{}

type staticNodeClusterStatusContext struct {
	DesiredNodes int
	ReadyNodes   int
	HeadReady    bool
	WarmReady    bool

	AnyNodeFailed  bool
	HasMissing     bool
	HasStale       bool
	HasSpecDrift   bool
	HasStatusDrift bool

	Upgrade  *staticNodeClusterUpgradeState
	Messages []string
}

func (a StaticNodeClusterStatusAggregator) Aggregate(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	plans []StaticNodeClusterDesiredNodePlan,
) v1.StaticNodeClusterStatus {
	ctx := buildStaticNodeClusterStatusContext(cluster, nodes, plans)
	phase := determineStaticNodeClusterPhase(ctx)

	status := v1.StaticNodeClusterStatus{
		Phase:        phase,
		DesiredNodes: ctx.DesiredNodes,
		ReadyNodes:   ctx.ReadyNodes,
		HeadReady:    ctx.HeadReady,
		WarmReady:    ctx.WarmReady,
	}

	if phase == v1.StaticNodeClusterPhaseReady && cluster != nil && cluster.Spec != nil {
		status.Version = cluster.Spec.Version
		return status
	}

	if ctx.Upgrade != nil {
		status.Version = ctx.Upgrade.ObservedVersion
	}

	status.ErrorMessage = joinStatusMessages("", ctx.Messages...)

	return status
}

func buildStaticNodeClusterStatusContext(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	plans []StaticNodeClusterDesiredNodePlan,
) staticNodeClusterStatusContext {
	desiredNodeNames, headName := staticNodeClusterDesiredNodeNames(cluster)
	currentByName := staticNodeByName(nodes)
	plansByName := staticNodeClusterPlanByName(plans)
	ctx := staticNodeClusterStatusContext{
		DesiredNodes: len(desiredNodeNames),
		WarmReady:    true,
		Upgrade:      staticNodeClusterUpgrade(cluster, nodes, plans),
	}

	if ctx.Upgrade != nil {
		ctx.Messages = append(ctx.Messages, string(ctx.Upgrade.Step))
	}

	if ctx.DesiredNodes == 0 {
		ctx.AnyNodeFailed = true
		ctx.Messages = append(ctx.Messages, "static node cluster has no desired nodes")

		return ctx
	}

	for _, node := range nodes {
		if node == nil || node.Metadata == nil {
			continue
		}

		if _, desired := desiredNodeNames[node.Metadata.Name]; desired {
			continue
		}

		ctx.HasStale = true
		ctx.Messages = append(ctx.Messages, staticNodeClusterStaleNodeMessage(node))
	}

	for _, nodeName := range staticNodeClusterDesiredNodeNameList(cluster) {
		node := currentByName[nodeName]
		plan := plansByName[nodeName]

		if node == nil {
			ctx.HasMissing = true
			ctx.WarmReady = false
			ctx.Messages = append(ctx.Messages, "static node "+nodeName+" is missing")

			continue
		}

		if staticNodeSpecDrifted(plan.Node, node) {
			ctx.HasSpecDrift = true
			ctx.Messages = append(ctx.Messages, "static node "+nodeName+" spec is not observed")
		}

		if node.Status == nil {
			ctx.HasStatusDrift = true
			ctx.WarmReady = false
			ctx.Messages = append(ctx.Messages, "static node "+nodeName+" status is empty")

			continue
		}

		if node.Status.Phase == v1.StaticNodePhaseReady {
			ctx.ReadyNodes++
		}

		if nodeName == headName {
			ctx.HeadReady = node.Status.Phase == v1.StaticNodePhaseReady
		}

		if node.Status.Phase == v1.StaticNodePhaseFailed {
			ctx.AnyNodeFailed = true
		}

		if node.Status.Warm == nil || !node.Status.Warm.Ready {
			ctx.WarmReady = false
		}

		if message := staticNodeStatusMessage(nodeName, node); message != "" {
			ctx.Messages = append(ctx.Messages, message)
		}
	}

	statusDriftMessages := desiredComponentMismatchMessages(plans, currentByName)
	if len(statusDriftMessages) > 0 {
		ctx.HasStatusDrift = true
		ctx.Messages = append(ctx.Messages, statusDriftMessages...)
	}

	return ctx
}

func desiredComponentMismatchMessages(
	plans []StaticNodeClusterDesiredNodePlan,
	currentByName map[string]*v1.StaticNode,
) []string {
	messages := []string{}

	for _, plan := range plans {
		if plan.Node == nil || plan.Node.Spec == nil || len(plan.Node.Spec.Components) == 0 {
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

func determineStaticNodeClusterPhase(ctx staticNodeClusterStatusContext) v1.StaticNodeClusterPhase {
	if ctx.DesiredNodes == 0 || ctx.AnyNodeFailed {
		return v1.StaticNodeClusterPhaseFailed
	}

	if ctx.Upgrade != nil {
		if staticNodeClusterStatusConverged(ctx) && ctx.Upgrade.Step == staticNodeClusterUpgradeStepVerifying {
			return v1.StaticNodeClusterPhaseReady
		}

		return v1.StaticNodeClusterPhaseUpgrading
	}

	if staticNodeClusterStatusConverged(ctx) {
		return v1.StaticNodeClusterPhaseReady
	}

	return v1.StaticNodeClusterPhaseProvisioning
}

func staticNodeClusterStatusConverged(ctx staticNodeClusterStatusContext) bool {
	return !ctx.HasMissing &&
		!ctx.HasStale &&
		!ctx.HasSpecDrift &&
		!ctx.HasStatusDrift &&
		ctx.ReadyNodes == ctx.DesiredNodes &&
		ctx.HeadReady &&
		ctx.WarmReady
}

func staticNodeClusterPlanByName(plans []StaticNodeClusterDesiredNodePlan) map[string]StaticNodeClusterDesiredNodePlan {
	result := make(map[string]StaticNodeClusterDesiredNodePlan, len(plans))

	for _, plan := range plans {
		if plan.Node == nil || plan.Node.Metadata == nil {
			continue
		}

		result[plan.Node.Metadata.Name] = plan
	}

	return result
}

func staticNodeSpecDrifted(desired *v1.StaticNode, current *v1.StaticNode) bool {
	if desired == nil || current == nil {
		return false
	}

	return !reflect.DeepEqual(current.Spec, desired.Spec)
}

func staticNodeClusterStaleNodeMessage(node *v1.StaticNode) string {
	if node == nil || node.Metadata == nil {
		return ""
	}

	if node.Metadata.DeletionTimestamp != "" {
		return "stale static node " + node.Metadata.Name + " is deleting"
	}

	return "stale static node " + node.Metadata.Name + " exists"
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
