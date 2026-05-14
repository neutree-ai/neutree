package portalloc

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
)

type allocator struct {
	storage Storage
}

// New constructs an Allocator backed by the given Storage.
func New(storage Storage) Allocator { return &allocator{storage: storage} }

// AllocateForPlan: idempotent reservation of every port slot the plan asks
// for. Algorithm:
//  1. Look up existing allocations for endpointID. If a complete set is
//     already on disk, fill plan.Ports from them and return early.
//  2. Otherwise, read the cluster-wide allocations to compute the in-use
//     port set, then walk the cluster's PortRange picking the smallest
//     unused port for each (replica × role × rank × position) slot the
//     plan still needs.
//  3. Persist the newly-picked rows in a single InsertAllocations call so
//     a partial failure leaves no orphans.
//  4. Materialize plan.Ports.
func (a *allocator) AllocateForPlan(
	ctx context.Context,
	cluster *v1.Cluster,
	endpointID int,
	p *plan.DeploymentPlan,
) error {
	if p == nil {
		return fmt.Errorf("portalloc: nil plan")
	}
	if cluster == nil || cluster.ID == 0 {
		return fmt.Errorf("portalloc: cluster id is zero")
	}
	if endpointID <= 0 {
		return fmt.Errorf("portalloc: endpoint id must be positive, got %d", endpointID)
	}

	required := buildRequiredSlots(p)
	if len(required) == 0 {
		// Nothing to allocate (e.g., monolithic with PortsPerRank=0).
		p.Ports = materializePorts(p, nil)
		return nil
	}

	// (1) Idempotent fast path.
	existing, err := a.storage.ListAllocationsByEndpoint(ctx, endpointID)
	if err != nil {
		return fmt.Errorf("portalloc: list existing allocations: %w", err)
	}
	if isCompleteFor(required, existing) {
		p.Ports = materializePorts(p, existing)
		return nil
	}
	if len(existing) > 0 {
		// Partial / shape-mismatched allocations on disk. This is the
		// normal aftermath of an endpoint spec edit (role count, role
		// instances, or PortsPerRank changed) — the prior port set is no
		// longer valid for the new plan. Release the stale rows and
		// fall through to the fresh-allocation path so the orchestrator
		// reconcile loop is self-healing instead of demanding a manual
		// cleanup step.
		if err := a.storage.DeleteAllocationsByEndpoint(ctx, endpointID); err != nil {
			return fmt.Errorf(
				"portalloc: release %d stale allocations for endpoint %d before re-allocate: %w",
				len(existing), endpointID, err,
			)
		}
	}

	// (2) Cluster-wide in-use set.
	clusterAllocs, err := a.storage.ListAllocationsByCluster(ctx, cluster.ID)
	if err != nil {
		return fmt.Errorf("portalloc: list cluster allocations: %w", err)
	}
	inUse := make(map[int]struct{}, len(clusterAllocs))
	for _, alloc := range clusterAllocs {
		inUse[alloc.Port] = struct{}{}
	}

	// Pick port range — fall back to default if cluster didn't configure one.
	pr := v1.DefaultPortRange
	if cluster.Spec != nil && cluster.Spec.PortRange != nil {
		pr = *cluster.Spec.PortRange
	}
	if pr.Start <= 0 || pr.End < pr.Start {
		return fmt.Errorf("portalloc: invalid port range %d-%d", pr.Start, pr.End)
	}

	// `required` already walks replica → Group.Roles (declaration order) →
	// rank → position. We keep that natural order so allocation maps to the
	// IR top-to-bottom: replica-0 prefill-0 first, then decode-0, etc.
	// Reconcile retries replay the same order deterministically.

	// (3) Pick smallest unused port for each slot.
	picked := make([]Allocation, 0, len(required))
	cursor := pr.Start
	for _, slot := range required {
		port, next, ok := nextFreePort(cursor, pr.End, inUse)
		if !ok {
			return fmt.Errorf(
				"portalloc: cluster %d port range %d-%d exhausted while allocating endpoint %d (need %d more ports)",
				cluster.ID, pr.Start, pr.End, endpointID, len(required)-len(picked),
			)
		}
		inUse[port] = struct{}{}
		cursor = next
		picked = append(picked, Allocation{
			ClusterID:   cluster.ID,
			Port:        port,
			EndpointID:  endpointID,
			ReplicaIdx:  slot.ReplicaIdx,
			RoleName:    slot.RoleName,
			RankIdx:     slot.RankIdx,
			PositionIdx: slot.PositionIdx,
		})
	}

	// (4) Persist atomically + materialize back to plan.
	if err := a.storage.InsertAllocations(ctx, picked); err != nil {
		return fmt.Errorf("portalloc: persist allocations: %w", err)
	}
	p.Ports = materializePorts(p, picked)
	return nil
}

func (a *allocator) ReleaseAll(ctx context.Context, endpointID int) error {
	if endpointID <= 0 {
		return nil
	}
	if err := a.storage.DeleteAllocationsByEndpoint(ctx, endpointID); err != nil {
		return fmt.Errorf("portalloc: release endpoint %d: %w", endpointID, err)
	}
	return nil
}

// --------- internal helpers ---------

// slotKey identifies one port slot uniquely.
type slotKey struct {
	ReplicaIdx  int
	RoleName    string
	RankIdx     int
	PositionIdx int
}

func buildRequiredSlots(p *plan.DeploymentPlan) []slotKey {
	if p.Group == nil || p.NumReplicas <= 0 {
		return nil
	}
	out := make([]slotKey, 0, 16)
	for replica := 0; replica < p.NumReplicas; replica++ {
		for _, role := range p.Group.Roles {
			if role.PortsPerRank <= 0 || role.Instances <= 0 {
				continue
			}
			for rank := 0; rank < role.Instances; rank++ {
				for pos := 0; pos < role.PortsPerRank; pos++ {
					out = append(out, slotKey{
						ReplicaIdx:  replica,
						RoleName:    role.Name,
						RankIdx:     rank,
						PositionIdx: pos,
					})
				}
			}
		}
	}
	return out
}

// isCompleteFor returns true when `existing` covers every slot in `required`.
// The relation is checked by set equality on slotKey.
func isCompleteFor(required []slotKey, existing []Allocation) bool {
	if len(existing) < len(required) {
		return false
	}
	have := make(map[slotKey]struct{}, len(existing))
	for _, a := range existing {
		have[slotKey{a.ReplicaIdx, a.RoleName, a.RankIdx, a.PositionIdx}] = struct{}{}
	}
	for _, r := range required {
		if _, ok := have[r]; !ok {
			return false
		}
	}
	return true
}

// nextFreePort scans [cursor, end] and returns the first port not in inUse,
// along with the next cursor position. Returns ok=false when range is full.
func nextFreePort(cursor, end int, inUse map[int]struct{}) (int, int, bool) {
	for p := cursor; p <= end; p++ {
		if _, used := inUse[p]; used {
			continue
		}
		return p, p + 1, true
	}
	return 0, cursor, false
}

// materializePorts walks the allocations and fills plan.Ports following the
// IR shape: Ports[replica_idx][role_name][rank_idx][position_idx] = port.
//
// The IR slice + slot positional order is preserved exactly, even when
// `allocations` is empty (each replica still gets an empty map so the
// renderer doesn't have to nil-check).
func materializePorts(p *plan.DeploymentPlan, allocations []Allocation) []plan.ReplicaPortMap {
	if p == nil || p.NumReplicas <= 0 {
		return nil
	}
	ports := make([]plan.ReplicaPortMap, p.NumReplicas)
	for i := range ports {
		ports[i] = plan.ReplicaPortMap{}
	}
	if p.Group == nil {
		return ports
	}
	// Pre-size []int slices to (Instances × PortsPerRank) so positional
	// indexing works even before any allocation row lands for that slot.
	for replicaIdx := 0; replicaIdx < p.NumReplicas; replicaIdx++ {
		for _, role := range p.Group.Roles {
			if role.PortsPerRank <= 0 || role.Instances <= 0 {
				continue
			}
			ranks := make([][]int, role.Instances)
			for rank := 0; rank < role.Instances; rank++ {
				ranks[rank] = make([]int, role.PortsPerRank)
			}
			ports[replicaIdx][role.Name] = ranks
		}
	}
	for _, a := range allocations {
		if a.ReplicaIdx < 0 || a.ReplicaIdx >= len(ports) {
			continue
		}
		role, ok := ports[a.ReplicaIdx][a.RoleName]
		if !ok {
			continue
		}
		if a.RankIdx < 0 || a.RankIdx >= len(role) {
			continue
		}
		if a.PositionIdx < 0 || a.PositionIdx >= len(role[a.RankIdx]) {
			continue
		}
		role[a.RankIdx][a.PositionIdx] = a.Port
	}
	return ports
}
