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
	pocZCacheImageRepository = "lmcache/lmcache"
	pocZCacheImageTag        = "0.5.0"
	pocZCacheServicePort     = 7500
	pocZCacheMetricsPort     = 8000
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
	if cluster.Spec.ZCache.APIURL == "" {
		cluster.Status.ZCache = &v1.ZCacheStatus{Phase: "NotConfigured", Message: "zcache api_url is required for the PoC"}
		return nil
	}

	client := zcache.NewClient(cluster.Spec.ZCache.APIURL, http.DefaultClient)
	runtime, err := client.Runtime(ctx)
	if err == nil && runtime.Ready && runtime.Endpoint != nil {
		cluster.Status.ZCache = &v1.ZCacheStatus{
			Phase: "Ready", ReadyNodes: int32(len(runtime.Nodes)), DesiredNodes: int32(len(runtime.Nodes)),
			Endpoint: net.JoinHostPort(runtime.Endpoint.Address, strconv.Itoa(int(runtime.Endpoint.Port))),
		}
		return nil
	}

	nodes, err := client.ClusterNodes(ctx)
	if err != nil {
		cluster.Status.ZCache = &v1.ZCacheStatus{Phase: "NotReady", Message: err.Error()}
		return nil
	}
	targets := make([]string, 0, len(nodes.Nodes))
	for _, node := range nodes.Nodes {
		if node.Ready && node.Selectable {
			targets = append(targets, node.Name)
		}
	}
	l1Size := cluster.Spec.ZCache.L1SizeGiB
	if l1Size <= 0 {
		l1Size = 8
	}
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
		cluster.Status.ZCache = &v1.ZCacheStatus{Phase: "NotReady", ReadyNodes: 0, DesiredNodes: int32(len(targets)), Message: message}
		return nil
	}
	apply, err := client.Apply(ctx, request)
	if err != nil {
		cluster.Status.ZCache = &v1.ZCacheStatus{Phase: "NotReady", DesiredNodes: int32(len(targets)), Message: err.Error()}
		return nil
	}
	operation, err := client.Operation(ctx, apply.OperationID)
	message := fmt.Sprintf("operation %s submitted", apply.OperationID)
	phase := "Provisioning"
	if err == nil && operation.Summary != "" {
		message = operation.Summary
	}
	if err == nil && operation.Phase == "Failed" {
		phase = "NotReady"
		message = operation.Reason
	}
	cluster.Status.ZCache = &v1.ZCacheStatus{Phase: phase, DesiredNodes: int32(len(targets)), Message: message}
	return nil
}
