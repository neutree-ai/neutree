// Package portalloc is the cluster-level port allocator for Neutree.
//
// Responsibility:
//   - Allocate exactly N ports per (replica × role × rank × position) slot
//     where N is `Role.PortsPerRank` from the PD same-host config.
//   - Persist allocations in api.cluster_port_allocations.
//   - Hand the result back as cfg.Ports so renderers can inject env vars
//     deterministically.
//
// Allocation lifecycle is tied to the endpoint:
//   - Reconcile path: AllocateForPDSameHostConfig is called after deriving the
//     runtime config and before orchestrator.Apply. Idempotent — re-running on
//     an existing endpoint returns the same ports.
//   - Cleanup path: ReleaseAll on endpoint delete. ON DELETE CASCADE on the
//     PG FK also covers the case where the endpoint row is deleted directly.
package portalloc

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/pdconfig"
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

// Allocator is the public API. AllocateForPDSameHostConfig / ReleaseAll wrap
// Storage with config-aware allocation logic.
type Allocator interface {
	// AllocateForPDSameHostConfig reserves ports for every
	// (replica × role × rank × position) slot implied by the config, writes
	// them into cfg.Ports, and persists rows into Storage. Idempotent: if rows
	// already exist for endpointID, the existing assignments are returned (and
	// cfg.Ports is filled from them) — this keeps reconcile safe to retry
	// without leaking ports.
	AllocateForPDSameHostConfig(ctx context.Context, cluster *v1.Cluster, endpointID int, cfg *pdconfig.PDSameHostConfig) error

	// ReleaseAll removes every allocation for an endpoint. Idempotent.
	ReleaseAll(ctx context.Context, endpointID int) error
}
