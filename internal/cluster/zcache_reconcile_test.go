package cluster

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/zcache"
)

func TestCurrentZCacheNodeStatusesMapsRuntimeAndCacheTogether(t *testing.T) {
	statuses := currentZCacheNodeStatuses([]zcache.NodeResponse{
		{Name: "gpu-1", Runtime: "Ready", Cache: "Available"},
		{Name: "gpu-2", Runtime: "Ready", Cache: "Unavailable", Reason: "cache pod is not ready"},
	}, 8)

	if statuses[0].Phase != "Ready" || statuses[1].Phase != "NotReady" {
		t.Fatalf("unexpected node phases: %+v", statuses)
	}
	if statuses[1].Reason != "cache pod is not ready" {
		t.Fatalf("unexpected node reason: %+v", statuses[1])
	}
}

func TestOperationStatusPreservesCapturedConfigAcrossPolling(t *testing.T) {
	existing := &v1.ZCacheOperationStatus{
		ID:             "op-1",
		PreviousConfig: v1.ZCacheConfigSnapshot{L1SizeGiB: 8, TargetNodes: []string{"gpu-1", "gpu-2"}},
		DesiredConfig:  v1.ZCacheConfigSnapshot{L1SizeGiB: 4, TargetNodes: []string{"gpu-1"}},
	}
	status := operationStatus(
		zcache.OperationResponse{ID: "op-1", Phase: "Failed"},
		v1.ZCacheConfigSnapshot{L1SizeGiB: 4, TargetNodes: []string{"gpu-1"}},
		v1.ZCacheConfigSnapshot{L1SizeGiB: 1, TargetNodes: []string{"gpu-2"}},
		existing,
	)

	if status.PreviousConfig.L1SizeGiB != 8 || status.DesiredConfig.L1SizeGiB != 4 {
		t.Fatalf("polling overwrote operation configs: %+v", status)
	}
}
