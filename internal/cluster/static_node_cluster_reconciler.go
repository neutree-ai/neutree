package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	staticNodeClusterLabelKey = "neutree.ai/static-node-cluster"
	staticNodeRoleLabelKey    = "neutree.ai/static-node-role"

	defaultRayDashboardPort = 8265
)

type StaticNodeClusterReconciler struct {
	RuntimeProfileProvider RuntimeProfileProvider
}

type RuntimeProfileProvider interface {
	RuntimeProfile(ctx context.Context, accelerator v1.StaticNodeAcceleratorStatus) (*v1.AcceleratorProfile, bool, error)
}

type StaticNodeClusterReconcilePlan struct {
	DesiredNodes []*v1.StaticNode
	Status       v1.StaticNodeClusterStatus
}

type staticNodeClusterUpgradeStep string

const (
	staticNodeClusterUpgradeStepWarming         staticNodeClusterUpgradeStep = "Warming"
	staticNodeClusterUpgradeStepStoppingWorkers staticNodeClusterUpgradeStep = "StoppingWorkers"
	staticNodeClusterUpgradeStepStartingHead    staticNodeClusterUpgradeStep = "StartingHead"
	staticNodeClusterUpgradeStepStartingWorkers staticNodeClusterUpgradeStep = "StartingWorkers"
	staticNodeClusterUpgradeStepVerifying       staticNodeClusterUpgradeStep = "Verifying"
)

type staticNodeDesiredPlan struct {
	Node                          *v1.StaticNode
	Accelerator                   *v1.StaticNodeAcceleratorStatus
	Profile                       *v1.AcceleratorProfile
	RuntimeProfileFallbackMessage string
}

func (r *StaticNodeClusterReconciler) Plan(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) (*StaticNodeClusterReconcilePlan, error) {
	plans, err := r.buildDesiredNodePlans(ctx, cluster, currentNodes)
	if err != nil {
		return nil, err
	}

	status := advanceStaticNodeClusterUpgradeStatus(cluster, currentNodes, r.AggregateStatus(cluster, currentNodes))
	status = withRuntimeProfileFallbackStatus(status, plans)

	return &StaticNodeClusterReconcilePlan{
		DesiredNodes: desiredNodesFromPlans(plans),
		Status:       status,
	}, nil
}

func (r *StaticNodeClusterReconciler) BuildDesiredNodes(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) ([]*v1.StaticNode, error) {
	plans, err := r.buildDesiredNodePlans(ctx, cluster, currentNodes)
	if err != nil {
		return nil, err
	}

	nodes := make([]*v1.StaticNode, 0, len(plans))
	for _, plan := range plans {
		nodes = append(nodes, plan.Node)
	}

	return nodes, nil
}

func desiredNodesFromPlans(plans []staticNodeDesiredPlan) []*v1.StaticNode {
	nodes := make([]*v1.StaticNode, 0, len(plans))
	for _, plan := range plans {
		nodes = append(nodes, plan.Node)
	}

	return nodes
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
			Node:                          desiredNode,
			Accelerator:                   acceleratorStatus,
			Profile:                       profile,
			RuntimeProfileFallbackMessage: fallbackMessage,
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

func withRuntimeProfileFallbackStatus(
	status v1.StaticNodeClusterStatus,
	plans []staticNodeDesiredPlan,
) v1.StaticNodeClusterStatus {
	messages := make([]string, 0, len(plans))

	for _, plan := range plans {
		if plan.RuntimeProfileFallbackMessage == "" {
			continue
		}

		nodeName := ""
		if plan.Node != nil && plan.Node.Metadata != nil {
			nodeName = plan.Node.Metadata.Name
		}

		if nodeName == "" {
			messages = append(messages, plan.RuntimeProfileFallbackMessage)
			continue
		}

		messages = append(messages, "static node "+nodeName+" "+plan.RuntimeProfileFallbackMessage)
	}

	if len(messages) == 0 {
		return status
	}

	status.ErrorMessage = joinStatusMessages(status.ErrorMessage, messages...)

	return status
}

func joinStatusMessages(current string, messages ...string) string {
	result := make([]string, 0, len(messages)+1)
	if current != "" {
		result = append(result, current)
	}

	result = append(result, messages...)

	return strings.Join(result, "; ")
}

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

func (r *StaticNodeClusterReconciler) AggregateStatus(
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
) v1.StaticNodeClusterStatus {
	desiredNodeNames, headName := staticNodeClusterDesiredNodeNames(cluster)

	status := v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseProvisioning,
		DesiredNodes: len(desiredNodeNames),
	}
	if upgrade := staticNodeClusterUpgrade(cluster); upgrade != nil {
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
	case status.Phase == v1.StaticNodeClusterPhaseUpgrading:
		status.Phase = v1.StaticNodeClusterPhaseUpgrading
	case anyNodeFailed:
		status.Phase = v1.StaticNodeClusterPhaseFailed
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

	return status
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
		if node == nil || node.Metadata == nil || node.Metadata.Name == "" {
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

func (r *StaticNodeClusterReconciler) runtimeProfile(
	ctx context.Context,
	accelerator v1.StaticNodeAcceleratorStatus,
) (*v1.AcceleratorProfile, string, error) {
	if accelerator.Type == "" || accelerator.Type == v1.StaticNodeAcceleratorTypeCPU {
		return nil, "", nil
	}

	if r == nil || r.RuntimeProfileProvider == nil {
		return nil, runtimeProfileFallbackMessage(accelerator), nil
	}

	profile, supported, err := r.RuntimeProfileProvider.RuntimeProfile(ctx, accelerator)
	if err != nil {
		return nil, "", err
	}

	if !supported {
		return nil, runtimeProfileFallbackMessage(accelerator), nil
	}

	return profile, "", nil
}

func runtimeProfileFallbackMessage(accelerator v1.StaticNodeAcceleratorStatus) string {
	profile := accelerator.ProductModel
	if profile == "" {
		profile = accelerator.Type
	}

	return "accelerator runtime profile " + strconv.Quote(profile) + " is not supported; fallback to CPU runtime"
}

func buildNodeComponents(
	cluster *v1.StaticNodeCluster,
	node *v1.StaticNode,
	profile *v1.AcceleratorProfile,
) []v1.NodeComponentSpec {
	role := v1.StaticNodeRoleWorker
	if node != nil && node.Spec != nil {
		role = node.Spec.Role
	}

	return []v1.NodeComponentSpec{buildRayComponent(cluster, role, profile)}
}

func withComponentConfigHashes(components []v1.NodeComponentSpec) []v1.NodeComponentSpec {
	result := make([]v1.NodeComponentSpec, len(components))
	for i, component := range components {
		result[i] = component
		result[i].ConfigHash = nodeComponentConfigHash(component)
	}

	return result
}

func nodeComponentConfigHash(component v1.NodeComponentSpec) string {
	component.ConfigHash = ""
	component.ConfigFiles = append([]v1.NodeComponentConfigFile{}, component.ConfigFiles...)

	for i := range component.ConfigFiles {
		if component.ConfigFiles[i].SkipRestartOnChange {
			component.ConfigFiles[i].Content = ""
		}
	}

	content, err := json.Marshal(component)
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}

func buildRayComponent(
	cluster *v1.StaticNodeCluster,
	role v1.StaticNodeRole,
	profile *v1.AcceleratorProfile,
) v1.NodeComponentSpec {
	image := buildRayRuntimeImage(cluster)
	env := rayRuntimeEnv(profile)
	dockerRunOptions := rayRuntimeDockerRunOptions(profile)
	command := []string{"/bin/bash", "-lc"}

	if role == v1.StaticNodeRoleHead {
		return v1.NodeComponentSpec{
			Name:             "ray-head",
			Type:             v1.NodeComponentTypeRayHead,
			Image:            image,
			Command:          command,
			Args:             []string{rayStartCommand(cluster, role)},
			Env:              env,
			DockerRunOptions: dockerRunOptions,
			HealthCheck: &v1.NodeComponentHealthCheck{
				Port:          defaultRayDashboardPort,
				RayNodeLabels: rayNodeLabels(cluster, role),
			},
		}
	}

	return v1.NodeComponentSpec{
		Name:             "ray-worker",
		Type:             v1.NodeComponentTypeRayWorker,
		Image:            image,
		Command:          command,
		Args:             []string{rayStartCommand(cluster, role)},
		Env:              env,
		DockerRunOptions: dockerRunOptions,
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPHost:      staticNodeClusterHeadIP(cluster),
			Port:          defaultRayDashboardPort,
			RayNodeLabels: rayNodeLabels(cluster, role),
		},
	}
}

func rayRuntimeEnv(profile *v1.AcceleratorProfile) map[string]string {
	env := map[string]string{
		"RAY_DEFAULT_OBJECT_STORE_MEMORY_PROPORTION":     "0.1",
		"RAY_enable_open_telemetry":                      "false",
		"RAY_EXPERIMENTAL_RUNTIME_ENV_CONTAINER_RUNTIME": "docker",
		"RAY_process_group_cleanup_enabled":              "true",
	}

	if profile == nil || profile.ClusterRuntime == nil {
		return env
	}

	for key, value := range profile.ClusterRuntime.Env {
		env[key] = value
	}

	return env
}

func rayRuntimeDockerRunOptions(profile *v1.AcceleratorProfile) []string {
	options := []string{
		"--net=host",
		"--ulimit nofile=65536:65536",
		"--volume /var/run/docker.sock:/var/run/docker.sock",
		"--volume /tmp:/tmp",
		"--pid=host",
		"--ipc=host",
	}

	if profile == nil || profile.ClusterRuntime == nil {
		return options
	}

	if profile.ClusterRuntime.Runtime != "" {
		options = append(options, "--runtime="+profile.ClusterRuntime.Runtime)
	}

	options = append(options, profile.ClusterRuntime.Options...)

	return options
}

func rayStartCommand(
	cluster *v1.StaticNodeCluster,
	role v1.StaticNodeRole,
) string {
	parts := []string{
		legacyRayContainerCleanupWatcherCommand(),
		"ray stop",
		"ulimit -n 65536",
	}

	commonArgs := strings.Join([]string{
		"--disable-usage-stats",
		"--node-manager-port=8077",
		"--dashboard-agent-listen-port=52365",
		"--min-worker-port=10002",
		"--max-worker-port=20000",
		"--runtime-env-agent-port=56999",
		fmt.Sprintf("--metrics-export-port=%d", v1.RayletMetricsPort),
	}, " ")

	if role == v1.StaticNodeRoleHead {
		parts = append(parts, strings.Join([]string{
			"python /home/ray/start.py --head --port=6379 --dashboard-host=0.0.0.0",
			commonArgs,
			"--dashboard-port=8265",
			"--ray-client-server-port=10001",
			rayNodeLabelArg(cluster, role),
		}, " "))
	} else {
		parts = append(parts, strings.Join([]string{
			"python /home/ray/start.py --address=" + staticNodeClusterHeadIP(cluster) + ":6379",
			commonArgs,
			rayNodeLabelArg(cluster, role),
		}, " "))
	}

	parts = append(parts, "tail -f /dev/null")

	return strings.Join(parts, " && ")
}

func legacyRayContainerCleanupCommand() string {
	return "docker rm -f ray_container >/dev/null 2>&1 || true"
}

func legacyRayContainerCleanupWatcherCommand() string {
	return "(while true; do " + legacyRayContainerCleanupCommand() + "; sleep 1; done) & true"
}

func rayNodeLabelArg(cluster *v1.StaticNodeCluster, role v1.StaticNodeRole) string {
	labels := rayNodeLabels(cluster, role)

	content, err := json.Marshal(labels)
	if err != nil || len(labels) == 0 {
		return ""
	}

	return "--labels='" + string(content) + "'"
}

func rayNodeLabels(cluster *v1.StaticNodeCluster, role v1.StaticNodeRole) map[string]string {
	labels := map[string]string{}
	if cluster != nil && cluster.Spec != nil && cluster.Spec.Version != "" {
		labels[v1.NeutreeServingVersionLabel] = cluster.Spec.Version
	}

	if role == v1.StaticNodeRoleWorker {
		labels[v1.NeutreeNodeProvisionTypeLabel] = v1.StaticNodeProvisionType
	}

	return labels
}

func staticNodeClusterHeadIP(cluster *v1.StaticNodeCluster) string {
	if cluster == nil || cluster.Spec == nil {
		return ""
	}

	for _, node := range cluster.Spec.Nodes {
		if normalizeStaticNodeRole(node.Role) == v1.StaticNodeRoleHead {
			return node.IP
		}
	}

	return ""
}

func buildNodeWarmSpec(components []v1.NodeComponentSpec) *v1.WarmSpec {
	if len(components) == 0 {
		return nil
	}

	images := make([]v1.WarmImageSpec, 0, len(components))
	seen := map[string]struct{}{}

	for _, component := range components {
		if component.Image == "" {
			continue
		}

		if _, exists := seen[component.Image]; exists {
			continue
		}

		seen[component.Image] = struct{}{}

		images = append(images, v1.WarmImageSpec{
			Name:     warmImageName(component),
			Ref:      component.Image,
			Required: true,
		})
	}

	if len(images) == 0 {
		return nil
	}

	return &v1.WarmSpec{
		Images: images,
	}
}

func buildRayRuntimeImage(cluster *v1.StaticNodeCluster) string {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Version == "" || cluster.Spec.ImageRegistry == "" {
		return ""
	}

	return util.BuildClusterImageRef(strings.TrimRight(cluster.Spec.ImageRegistry, "/"), cluster.Spec.Version, "")
}

func warmImageName(component v1.NodeComponentSpec) string {
	switch component.Type {
	case v1.NodeComponentTypeRayHead, v1.NodeComponentTypeRayWorker:
		return "ray-runtime"
	default:
		if component.Name != "" {
			return component.Name
		}

		return string(component.Type)
	}
}

func staticComponentImage(cluster *v1.StaticNodeCluster, image string) string {
	if image == "" {
		return ""
	}

	if cluster == nil || cluster.Spec == nil || cluster.Spec.ImageRegistry == "" {
		return image
	}

	imageRegistry := strings.TrimRight(cluster.Spec.ImageRegistry, "/")
	if strings.HasPrefix(image, imageRegistry+"/") {
		return image
	}

	return imageRegistry + "/" + stripSourceImageRegistry(image)
}

func stripSourceImageRegistry(image string) string {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) < 2 {
		return image
	}

	if isSourceImageRegistry(parts[0]) {
		return parts[1]
	}

	return image
}

func isSourceImageRegistry(segment string) bool {
	return segment == "localhost" || strings.Contains(segment, ".") || strings.Contains(segment, ":")
}

func copyStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}

	return copied
}

func copyAuth(auth *v1.Auth) *v1.Auth {
	if auth == nil {
		return nil
	}

	copied := *auth

	return &copied
}
