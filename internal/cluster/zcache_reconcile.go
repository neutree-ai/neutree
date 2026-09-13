package cluster

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/zcache"
)

const (
	pocZCacheImageRepository = "registry.smtx.io/zcache/lmcache-standalone"
	pocZCacheImageTag        = "v0.5.0"
	pocZCacheServicePort     = 7500
	pocZCacheMetricsPort     = 8000
	pocZCacheAPIURL          = "http://zcache-api:8080"
)

// reconcileZCache drives only the PoC's single L1 runtime. It is deliberately
// best-effort: a missing ZCache API must not prevent unrelated cluster services
// from reconciling.
func (c *NativeKubernetesClusterReconciler) reconcileZCache(ctx context.Context, cluster *v1.Cluster) error {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.ZCache == nil || !cluster.Spec.ZCache.Enabled {
		if cluster != nil && cluster.Status != nil {
			cluster.Status.ZCache = nil
		}
		return nil
	}
	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}
	var existingOperation *v1.ZCacheOperationStatus
	if cluster.Status.ZCache != nil && cluster.Status.ZCache.Operation != nil {
		copy := *cluster.Status.ZCache.Operation
		existingOperation = &copy
	}
	apiURL := cluster.Spec.ZCache.APIURL
	if apiURL == "" {
		apiURL = pocZCacheAPIURL
	}
	client := zcache.NewClient(apiURL, http.DefaultClient)
	previousConfig := zcache.ConfigResponse{}
	if config, configErr := client.Config(ctx); configErr == nil {
		previousConfig = config
	}
	latestOperation, _ := client.LatestOperation(ctx)
	currentNodes, _ := client.Nodes(ctx)
	currentNodeStatuses := currentZCacheNodeStatuses(currentNodes.Nodes, l1SizeOrDefault(cluster))
	installation, _ := client.Installation(ctx)
	defer func() {
		if cluster.Status != nil && cluster.Status.ZCache != nil {
			cluster.Status.ZCache.Version = installation.Runtime.Version
		}
	}()
	runtime, err := client.Runtime(ctx)
	if err == nil && runtime.Ready && runtime.Endpoint != nil {
		nodeStatuses := make([]v1.ZCacheNodeStatus, 0, len(runtime.Nodes))
		for _, node := range runtime.Nodes {
			nodeStatuses = append(nodeStatuses, v1.ZCacheNodeStatus{Name: node, Phase: "Ready", CapacityGiB: l1SizeOrDefault(cluster)})
		}
		cluster.Status.ZCache = &v1.ZCacheStatus{
			Phase: "Ready", ReadyNodes: int32(len(runtime.Nodes)), DesiredNodes: int32(len(runtime.Nodes)),
			Endpoint:  net.JoinHostPort(runtime.Endpoint.Address, strconv.Itoa(int(runtime.Endpoint.Port))),
			Nodes:     nodeStatuses,
			Operation: operationStatus(latestOperation, configSnapshot(previousConfig), configSnapshotForCluster(cluster, runtime.Nodes), existingOperation),
		}
		return nil
	}

	nodes, err := client.ClusterNodes(ctx)
	if err != nil {
		cluster.Status.ZCache = &v1.ZCacheStatus{Phase: "NotReady", Message: err.Error(), Nodes: currentNodeStatuses,
			Operation: operationStatus(latestOperation, configSnapshot(previousConfig), configSnapshotForCluster(cluster, nil), existingOperation)}
		return nil
	}
	targets := make([]string, 0, len(nodes.Nodes))
	configuredTargets := map[string]bool{}
	hasConfiguredTargets := false
	if cluster.Spec.ZCache != nil {
		hasConfiguredTargets = cluster.Spec.ZCache.TargetNodes != nil
		for _, name := range cluster.Spec.ZCache.TargetNodes {
			configuredTargets[name] = true
		}
	}
	for _, node := range nodes.Nodes {
		if node.Ready && node.Selectable && (!hasConfiguredTargets || configuredTargets[node.Name]) {
			targets = append(targets, node.Name)
		}
	}
	l1Size := l1SizeOrDefault(cluster)
	request := zcache.ValidateRequest{LMCache: zcache.RuntimeRequest{
		Mode: "l1", TargetNodes: targets, DevicesByNode: map[string][]string{},
		Image:       zcache.RuntimeImage{Repository: pocZCacheImageRepository, Tag: pocZCacheImageTag, PullPolicy: "IfNotPresent"},
		Resources:   zcache.ResourceConfig{Requests: map[string]string{"cpu": "100m", "memory": "256Mi"}, Limits: map[string]string{"cpu": "500m", "memory": "512Mi"}},
		ServicePort: pocZCacheServicePort, MetricsPort: pocZCacheMetricsPort, L1SizeGB: l1Size,
	}}
	request.IdempotencyKey = "neutree-zcache-" + cluster.Key()
	validation, err := client.Validate(ctx, request)
	if err != nil || !validation.Valid {
		message := "runtime validation failed"
		if err != nil {
			message = err.Error()
		} else if len(validation.Nodes) > 0 && len(validation.Nodes[0].Reasons) > 0 {
			message = validation.Nodes[0].Reasons[0]
		}
		cluster.Status.ZCache = &v1.ZCacheStatus{Phase: "NotReady", ReadyNodes: countReadyNodes(currentNodeStatuses), DesiredNodes: int32(len(targets)), Message: message, Nodes: currentNodeStatuses,
			Operation: operationStatus(latestOperation, configSnapshot(previousConfig), configSnapshotForCluster(cluster, nil), existingOperation)}
		return nil
	}
	apply, err := client.Apply(ctx, request)
	if err != nil {
		cluster.Status.ZCache = &v1.ZCacheStatus{Phase: "NotReady", ReadyNodes: countReadyNodes(currentNodeStatuses), DesiredNodes: int32(len(targets)), Message: err.Error(), Nodes: currentNodeStatuses,
			Operation: operationStatus(latestOperation, configSnapshot(previousConfig), configSnapshotForCluster(cluster, nil), existingOperation)}
		return nil
	}
	operation, err := client.Operation(ctx, apply.OperationID)
	message := fmt.Sprintf("operation %s submitted", apply.OperationID)
	phase := "NotReady"
	if err == nil && operation.Summary != "" {
		message = operation.Summary
	}
	if err == nil && operation.Phase == "Failed" {
		phase = "NotReady"
		message = operation.Reason
	}
	readyNodes := int32(0)
	nodeStatuses := make([]v1.ZCacheNodeStatus, 0, len(operation.Nodes))
	for _, node := range operation.Nodes {
		if node.Phase == "Succeeded" || node.Phase == "Ready" {
			readyNodes++
		}
		nodeStatuses = append(nodeStatuses, v1.ZCacheNodeStatus{Name: node.NodeName, Phase: node.Phase, CapacityGiB: l1Size, Reason: node.Reason})
	}
	cluster.Status.ZCache = &v1.ZCacheStatus{Phase: phase, ReadyNodes: readyNodes, DesiredNodes: int32(len(targets)), Message: message, Nodes: currentNodeStatuses,
		Operation: operationStatus(operation, configSnapshot(previousConfig), configSnapshotForCluster(cluster, nil), existingOperation)}
	return nil
}

func configSnapshot(config zcache.ConfigResponse) v1.ZCacheConfigSnapshot {
	return v1.ZCacheConfigSnapshot{L1SizeGiB: config.LMCache.L1.SizeGiB, TargetNodes: append([]string(nil), config.LMCache.TargetNodes...)}
}

func configSnapshotForCluster(cluster *v1.Cluster, runtimeNodes []string) v1.ZCacheConfigSnapshot {
	targets := append([]string(nil), runtimeNodes...)
	if targets == nil && cluster != nil && cluster.Spec != nil && cluster.Spec.ZCache != nil {
		targets = append([]string(nil), cluster.Spec.ZCache.TargetNodes...)
	}
	return v1.ZCacheConfigSnapshot{L1SizeGiB: l1SizeOrDefault(cluster), TargetNodes: targets}
}

func operationStatus(operation zcache.OperationResponse, previous, desired v1.ZCacheConfigSnapshot, existing *v1.ZCacheOperationStatus) *v1.ZCacheOperationStatus {
	if operation.ID == "" {
		return nil
	}
	if existing != nil && existing.ID == operation.ID {
		previous = existing.PreviousConfig
		desired = existing.DesiredConfig
	}
	nodes := make([]v1.ZCacheOperationNodeStatus, 0, len(operation.Nodes))
	for _, node := range operation.Nodes {
		nodes = append(nodes, v1.ZCacheOperationNodeStatus{Name: node.NodeName, Phase: string(node.Phase), Reason: node.Reason})
	}
	return &v1.ZCacheOperationStatus{ID: operation.ID, Phase: string(operation.Phase), Kind: string(operation.Operation.Kind), Summary: operation.Summary, Reason: operation.Reason,
		PreviousConfig: previous, DesiredConfig: desired, Nodes: nodes, CanCancel: false}
}

func currentZCacheNodeStatuses(nodes []zcache.NodeResponse, capacity int32) []v1.ZCacheNodeStatus {
	result := make([]v1.ZCacheNodeStatus, 0, len(nodes))
	for _, node := range nodes {
		phase := "NotReady"
		if node.Runtime == "Ready" && node.Cache == "Available" {
			phase = "Ready"
		}
		result = append(result, v1.ZCacheNodeStatus{Name: node.Name, Phase: phase, CapacityGiB: capacity, Reason: node.Reason})
	}
	return result
}

func countReadyNodes(nodes []v1.ZCacheNodeStatus) int32 {
	var count int32
	for _, node := range nodes {
		if node.Phase == "Ready" || node.Phase == "Succeeded" {
			count++
		}
	}
	return count
}

func l1SizeOrDefault(cluster *v1.Cluster) int32 {
	if cluster != nil && cluster.Spec != nil && cluster.Spec.ZCache != nil && cluster.Spec.ZCache.L1SizeGiB > 0 {
		return cluster.Spec.ZCache.L1SizeGiB
	}
	return 8
}
