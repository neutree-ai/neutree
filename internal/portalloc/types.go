// Package portalloc is the cluster-level port allocator for Neutree.
//
// Responsibility:
//   - Allocate exactly N ports per (replica × role × rank × position) slot
//     where N is `Role.PortsPerRank` from the IR.
//   - Persist allocations in api.cluster_port_allocations.
//   - Hand the result back as plan.Ports so the renderer (Ray runtime_env /
//     K8s container.env) can inject env vars deterministically.
//
// Allocation lifecycle is tied to the endpoint:
//   - Reconcile path: AllocateForPlan is called after strategy.Compile and
//     before orchestrator.Apply. Idempotent — re-running on an existing
//     endpoint returns the same ports.
//   - Cleanup path: ReleaseAll on endpoint delete. ON DELETE CASCADE on the
//     PG FK also covers the case where the endpoint row is deleted directly.
package portalloc

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
)

// Allocation is one persisted port row matching the api.cluster_port_allocations
// schema 1:1.
type Allocation struct {
	ID          int    `json:"id,omitempty"`
	ClusterID   int    `json:"cluster_id"`
	Port        int    `json:"port"`
	EndpointID  int    `json:"endpoint_id"`
	ReplicaIdx  int    `json:"replica_idx"`
	RoleName    string `json:"role_name"`
	RankIdx     int    `json:"rank_idx"`
	PositionIdx int    `json:"position_idx"`
	AllocatedAt string `json:"allocated_at,omitempty"`
}

// Storage is the persistence contract for portalloc.
//
// Atomicity: InsertAllocations should write all rows in a single transaction
// so a partially-failed allocation leaves no orphans. Existing row collision
// (same PK or unique key) must return an error so callers can detect races.
type Storage interface {
	ListAllocationsByCluster(ctx context.Context, clusterID int) ([]Allocation, error)
	ListAllocationsByEndpoint(ctx context.Context, endpointID int) ([]Allocation, error)
	InsertAllocations(ctx context.Context, allocations []Allocation) error
	DeleteAllocationsByEndpoint(ctx context.Context, endpointID int) error
}

// Allocator is the public API. AllocateForPlan / ReleaseAll wrap Storage with
// the IR-aware logic.
type Allocator interface {
	// AllocateForPlan reserves ports for every (replica × role × rank × position)
	// slot implied by the plan, writes them into plan.Ports, and persists rows
	// into Storage. Idempotent: if rows already exist for endpointID, the
	// existing assignments are returned (and plan.Ports is filled from them)
	// — this keeps reconcile safe to retry without leaking ports.
	AllocateForPlan(ctx context.Context, cluster *v1.Cluster, endpointID int, p *plan.DeploymentPlan) error

	// ReleaseAll removes every allocation for an endpoint. Idempotent.
	ReleaseAll(ctx context.Context, endpointID int) error
}
