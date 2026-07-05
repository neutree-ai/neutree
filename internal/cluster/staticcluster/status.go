package staticcluster

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
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
		for _, message := range splitStatusMessages(current) {
			appendMessage(message)
		}
	}

	for _, message := range messages {
		appendMessage(message)
	}

	return strings.Join(result, "\n")
}

func splitStatusMessages(message string) []string {
	parts := strings.FieldsFunc(message, func(r rune) bool {
		return r == '\n' || r == ';'
	})
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}

	return result
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func DashboardURL(cluster *v1.StaticNodeCluster) string {
	return fmt.Sprintf("http://%s:%d", staticNodeClusterHeadIP(cluster), v1.RayDashboardPort)
}

type StatusAggregator struct{}

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

	Upgrade          *staticNodeClusterUpgradeState
	StaleMessages    []string
	BlockingMessages []string
}

func (a StatusAggregator) Aggregate(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	plans []DesiredNodePlan,
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

	status.ErrorMessage = staticNodeClusterErrorMessage(ctx, phase)

	return status
}

func (a StatusAggregator) RequireRayClusterReady(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
) v1.StaticNodeClusterStatus {
	if status.Phase != v1.StaticNodeClusterPhaseReady {
		return status
	}

	if err := verifyRayCluster(ctx, cluster); err == nil {
		return status
	} else {
		status.ErrorMessage = joinStatusMessages(status.ErrorMessage, "ray cluster verification failed: "+err.Error())
	}

	if cluster != nil && cluster.Status != nil && cluster.Status.Phase == v1.StaticNodeClusterPhaseReady {
		status.Phase = v1.StaticNodeClusterPhaseFailed
	} else {
		status.Phase = v1.StaticNodeClusterPhaseProvisioning
	}

	return status
}

func verifyRayCluster(_ context.Context, cluster *v1.StaticNodeCluster) error {
	nodes, err := dashboard.NewDashboardService(DashboardURL(cluster)).ListNodes()
	if err != nil {
		return errors.Wrap(err, "failed to list ray nodes")
	}

	aliveByIP := map[string]struct{}{}

	for _, node := range nodes {
		if node.Raylet.State != v1.AliveNodeState {
			continue
		}

		aliveByIP[node.IP] = struct{}{}
	}

	missing := missingDesiredRayNodeIPs(cluster, aliveByIP)
	if len(missing) > 0 {
		return errors.Errorf("ray nodes are not alive: %v", missing)
	}

	return nil
}

func missingDesiredRayNodeIPs(cluster *v1.StaticNodeCluster, aliveByIP map[string]struct{}) []string {
	if cluster == nil || cluster.Spec == nil {
		return nil
	}

	missing := make([]string, 0)

	for _, node := range cluster.Spec.Nodes {
		if node.IP == "" {
			continue
		}

		if _, ok := aliveByIP[node.IP]; ok {
			continue
		}

		missing = append(missing, node.IP)
	}

	sort.Strings(missing)

	return missing
}

func buildStaticNodeClusterStatusContext(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	plans []DesiredNodePlan,
) staticNodeClusterStatusContext {
	desiredNodeNames, headName := staticNodeClusterDesiredNodeNames(cluster)
	currentByName := staticNodeByName(nodes)
	plansByName := staticNodeClusterPlanByName(plans)
	ctx := staticNodeClusterStatusContext{
		DesiredNodes: len(desiredNodeNames),
		WarmReady:    true,
		Upgrade:      staticNodeClusterUpgrade(cluster, nodes, plans),
	}

	if ctx.DesiredNodes == 0 {
		ctx.AnyNodeFailed = true
		ctx.BlockingMessages = append(ctx.BlockingMessages, "static node cluster has no desired nodes")

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
		ctx.StaleMessages = append(ctx.StaleMessages, staticNodeClusterStaleNodeMessage(node))
	}

	for _, nodeName := range staticNodeClusterDesiredNodeNameList(cluster) {
		aggregateDesiredStaticNodeStatus(
			&ctx,
			nodeName,
			nodeName == headName,
			currentByName[nodeName],
			plansByName[nodeName],
		)
	}

	return ctx
}

func aggregateDesiredStaticNodeStatus(
	ctx *staticNodeClusterStatusContext,
	nodeName string,
	isHead bool,
	current *v1.StaticNode,
	plan DesiredNodePlan,
) {
	if current == nil {
		ctx.HasMissing = true
		ctx.WarmReady = false
		ctx.BlockingMessages = append(ctx.BlockingMessages, "static node "+nodeName+" is missing")

		return
	}

	if staticNodeSpecDrifted(plan.Node, current) {
		ctx.HasSpecDrift = true
		ctx.BlockingMessages = append(ctx.BlockingMessages, "static node "+nodeName+" spec is not observed")
	}

	if current.Status == nil {
		ctx.HasStatusDrift = true
		ctx.WarmReady = false
		ctx.BlockingMessages = append(ctx.BlockingMessages, "static node "+nodeName+" status is empty")

		return
	}

	if current.Status.Phase == v1.StaticNodePhaseReady {
		ctx.ReadyNodes++
	}

	if isHead {
		ctx.HeadReady = current.Status.Phase == v1.StaticNodePhaseReady
	}

	if current.Status.Phase == v1.StaticNodePhaseFailed {
		ctx.AnyNodeFailed = true
	}

	if current.Status.Warm == nil || !current.Status.Warm.Ready {
		ctx.WarmReady = false
	}

	if message := staticNodeStatusMessage(nodeName, current.Status); message != "" {
		ctx.BlockingMessages = append(ctx.BlockingMessages, message)
	}

	aggregateDesiredStaticNodeComponentStatus(ctx, nodeName, plan, current.Status.Components)
}

func aggregateDesiredStaticNodeComponentStatus(
	ctx *staticNodeClusterStatusContext,
	nodeName string,
	plan DesiredNodePlan,
	statuses []v1.NodeComponentStatus,
) {
	if plan.Node == nil || plan.Node.Spec == nil || len(plan.Node.Spec.Components) == 0 {
		return
	}

	for _, component := range plan.Node.Spec.Components {
		if desiredComponentObserved(component, statuses) {
			continue
		}

		ctx.HasStatusDrift = true
		ctx.BlockingMessages = append(ctx.BlockingMessages, fmt.Sprintf(
			"static node %s component %s is not running desired config",
			nodeName,
			component.Name,
		))
	}
}

func determineStaticNodeClusterPhase(ctx staticNodeClusterStatusContext) v1.StaticNodeClusterPhase {
	if ctx.DesiredNodes == 0 || ctx.AnyNodeFailed {
		return v1.StaticNodeClusterPhaseFailed
	}

	if ctx.Upgrade != nil {
		if staticNodeClusterUpgradeReady(ctx) {
			return v1.StaticNodeClusterPhaseReady
		}

		return v1.StaticNodeClusterPhaseUpgrading
	}

	if staticNodeClusterStatusConverged(ctx) {
		return v1.StaticNodeClusterPhaseReady
	}

	return v1.StaticNodeClusterPhaseProvisioning
}

func staticNodeClusterErrorMessage(
	ctx staticNodeClusterStatusContext,
	phase v1.StaticNodeClusterPhase,
) string {
	if phase == v1.StaticNodeClusterPhaseReady {
		return ""
	}

	messages := make([]string, 0, len(ctx.StaleMessages)+len(ctx.BlockingMessages)+1)
	if ctx.Upgrade != nil {
		messages = append(messages, string(ctx.Upgrade.Step))
	}

	messages = append(messages, ctx.StaleMessages...)
	messages = append(messages, ctx.BlockingMessages...)

	return joinStatusMessages("", messages...)
}

func staticNodeClusterUpgradeReady(ctx staticNodeClusterStatusContext) bool {
	return ctx.Upgrade != nil &&
		ctx.Upgrade.TargetRuntimeObserved &&
		staticNodeClusterStatusConverged(ctx)
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

func staticNodeClusterPlanByName(plans []DesiredNodePlan) map[string]DesiredNodePlan {
	result := make(map[string]DesiredNodePlan, len(plans))

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

func staticNodeStatusMessage(nodeName string, status *v1.StaticNodeStatus) string {
	if nodeName == "" {
		return ""
	}

	if status.Phase != v1.StaticNodePhaseReady && status.ErrorMessage != "" {
		return fmt.Sprintf("static node %s phase=%s:\n%s", nodeName, status.Phase, status.ErrorMessage)
	}

	if status.Phase != v1.StaticNodePhaseReady {
		return fmt.Sprintf("static node %s phase=%s", nodeName, status.Phase)
	}

	if status.Warm == nil || !status.Warm.Ready {
		return staticNodeWarmStatusMessage(nodeName, status.Warm)
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
