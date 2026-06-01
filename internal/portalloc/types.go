// Package portalloc is the cluster-level port allocator for Neutree.
//
// Responsibility:
//   - Allocate one port per (RoleGroup x role x rank x purpose) slot.
//   - Persist allocations in api.cluster_port_allocations.
//   - Hand the result back as cfg.Ports so renderers can inject env vars
//     deterministically.
//
// Allocation lifecycle is tied to the endpoint:
//   - Reconcile path: AllocateForPDConfig is called after deriving the
//     PD runtime config and before orchestrator.Apply. Idempotent - re-running on
//     an existing endpoint returns the same ports.
//   - Cleanup path: ReleaseAll on endpoint delete. ON DELETE CASCADE on the
//     PG FK also covers the case where the endpoint row is deleted directly.
package portalloc

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/pdconfig"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Allocation is one persisted port row matching api.cluster_port_allocations.
type Allocation = storage.PortAllocation

// Storage is the persistence contract for portalloc.
//
// Atomicity: InsertAllocations should write all rows in a single transaction
// so a partially-failed allocation leaves no orphans. Existing row collision
// (same PK or unique key) must return an error so callers can detect races.
type Storage = storage.PortAllocationStorage

// Allocator is the public API. AllocateForPDConfig / ReleaseAll wrap Storage
// with PD config-aware allocation logic.
type Allocator interface {
	// AllocateForPDConfig reserves ports for every
	// (RoleGroup x role x rank x purpose) slot implied by the config, writes
	// them into cfg.Ports, and persists rows into Storage. Idempotent: if rows
	// already exist for endpointID, the existing assignments are returned (and
	// cfg.Ports is filled from them) - this keeps reconcile safe to retry
	// without leaking ports.
	AllocateForPDConfig(ctx context.Context, cluster *v1.Cluster, endpointID int, cfg *pdconfig.PDConfig) error

	// ReleaseAll removes every allocation for an endpoint. Idempotent.
	ReleaseAll(ctx context.Context, endpointID int) error
}
