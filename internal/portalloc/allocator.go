package portalloc

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/pdconfig"
)

type allocator struct {
	storage   Storage
	portRange v1.PortRangeSpec
}

// Option customizes an Allocator.
type Option func(*allocator)

// WithPortRange configures the global neutree-core port range used for new
// allocations. The range is validated when allocation runs.
func WithPortRange(pr v1.PortRangeSpec) Option {
	return func(a *allocator) {
		a.portRange = pr
	}
}

// New constructs an Allocator backed by the given Storage.
func New(storage Storage, opts ...Option) Allocator {
	a := &allocator{
		storage:   storage,
		portRange: v1.DefaultPortRange,
	}
	for _, opt := range opts {
		opt(a)
	}

	return a
}

// ValidatePortRange enforces the neutree-core global port-range contract.
func ValidatePortRange(pr v1.PortRangeSpec) error {
	switch {
	case pr.Start < 1024:
		return fmt.Errorf("port range requires Start >= 1024, got %d", pr.Start)
	case pr.Start > pr.End:
		return fmt.Errorf("port range requires Start <= End, got %d-%d", pr.Start, pr.End)
	case pr.End > 32767:
		return fmt.Errorf("port range requires End <= 32767, got %d", pr.End)
	case pr.End-pr.Start < 1000:
		return fmt.Errorf("port range requires End - Start >= 1000, got %d-%d", pr.Start, pr.End)
	default:
		return nil
	}
}

// AllocateForPDSameHostConfig: idempotent reservation of every port slot the
// PD same-host config asks
// for. Algorithm:
//  1. Look up existing allocations for endpointID. If a complete set is
//     already on disk, fill cfg.Ports from them and return early.
//  2. Otherwise, read the cluster-wide allocations to compute the in-use
//     port set, then walk the configured PortRange picking the smallest
//     unused port for each (RoleGroup × role × rank) slot the config still
//     needs.
//  3. Persist the newly-picked rows in a single InsertAllocations call so
//     a partial failure leaves no orphans.
//  4. Materialize cfg.Ports.
func (a *allocator) AllocateForPDSameHostConfig(
	ctx context.Context,
	cluster *v1.Cluster,
	endpointID int,
	cfg *pdconfig.PDSameHostConfig,
) error {
	if cfg == nil {
		return fmt.Errorf("portalloc: nil pd config")
	}

	if cluster == nil || cluster.ID == 0 {
		return fmt.Errorf("portalloc: cluster id is zero")
	}

	if endpointID <= 0 {
		return fmt.Errorf("portalloc: endpoint id must be positive, got %d", endpointID)
	}

	required, err := buildRequiredSlots(cfg)
	if err != nil {
		return err
	}

	if len(required) == 0 {
		// Nothing to allocate.
		cfg.Ports = materializePorts(cfg, nil)
		return nil
	}

	// (1) Idempotent fast path.
	existing, err := a.storage.ListAllocationsByEndpoint(ctx, endpointID)
	if err != nil {
		return fmt.Errorf("portalloc: list existing allocations: %w", err)
	}

	if isCompleteFor(required, existing) {
		cfg.Ports = materializePorts(cfg, existing)
		return nil
	}

	if len(existing) > 0 {
		// Partial / shape-mismatched allocations on disk. This is the
		// normal aftermath of an endpoint spec edit (role count, role
		// instances, or port requirement changed) — the prior port set is no
		// longer valid for the new config. Release the stale rows and
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

	pr := a.portRange
	if err := ValidatePortRange(pr); err != nil {
		return fmt.Errorf("portalloc: invalid port range: %w", err)
	}

	// `required` already walks role_group_index → Group.Roles (declaration
	// order) → rank. We keep that natural order so allocation maps to the
	// config top-to-bottom: group-0 prefill-0 first, then decode-0, etc.
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

		alloc := Allocation{
			ClusterID:      cluster.ID,
			Port:           port,
			EndpointID:     endpointID,
			RoleGroupIndex: slot.RoleGroupIndex,
			Role:           slot.Role,
			Rank:           slot.Rank,
		}

		picked = append(picked, alloc)
	}

	// (4) Persist atomically + materialize back to cfg.
	if err := a.storage.InsertAllocations(ctx, picked); err != nil {
		return fmt.Errorf("portalloc: persist allocations: %w", err)
	}

	cfg.Ports = materializePorts(cfg, picked)

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
	RoleGroupIndex int
	Role           string
	Rank           int
}

func buildRequiredSlots(cfg *pdconfig.PDSameHostConfig) ([]slotKey, error) {
	if cfg.Group == nil || cfg.NumReplicas <= 0 {
		return nil, nil
	}

	out := make([]slotKey, 0, 16)

	for replica := 0; replica < cfg.NumReplicas; replica++ {
		for _, role := range cfg.Group.Roles {
			if role.PortsPerRank <= 0 || role.Instances <= 0 {
				continue
			}

			if role.PortsPerRank > 1 {
				return nil, fmt.Errorf(
					"portalloc: role %s requests %d ports per rank; PD same-host currently supports one port per rank",
					role.Name, role.PortsPerRank,
				)
			}

			for rank := 0; rank < role.Instances; rank++ {
				out = append(out, slotKey{
					RoleGroupIndex: replica,
					Role:           role.Name,
					Rank:           rank,
				})
			}
		}
	}

	return out, nil
}

// isCompleteFor returns true when `existing` covers every slot in `required`.
// The relation is checked by set equality on slotKey.
func isCompleteFor(required []slotKey, existing []Allocation) bool {
	if len(existing) != len(required) {
		return false
	}

	have := make(map[slotKey]struct{}, len(existing))

	for _, a := range existing {
		have[slotKey{a.RoleGroupIndex, a.Role, a.Rank}] = struct{}{}
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

// materializePorts walks the allocations and fills cfg.Ports following the
// config shape: Ports[role_group_index][role][rank][0] = port.
//
// The config slice + slot order is preserved exactly, even when
// `allocations` is empty (each replica still gets an empty map so the
// renderer doesn't have to nil-check).
func materializePorts(cfg *pdconfig.PDSameHostConfig, allocations []Allocation) []pdconfig.ReplicaPortMap {
	if cfg == nil || cfg.NumReplicas <= 0 {
		return nil
	}

	ports := make([]pdconfig.ReplicaPortMap, cfg.NumReplicas)

	for i := range ports {
		ports[i] = pdconfig.ReplicaPortMap{}
	}

	if cfg.Group == nil {
		return ports
	}

	// Pre-size []int slices to (Instances × 1) so engine-side indexing works
	// even before any allocation row lands for that slot.
	for roleGroupIdx := 0; roleGroupIdx < cfg.NumReplicas; roleGroupIdx++ {
		for _, role := range cfg.Group.Roles {
			if role.PortsPerRank <= 0 || role.Instances <= 0 {
				continue
			}

			ranks := make([][]int, role.Instances)

			for rank := 0; rank < role.Instances; rank++ {
				ranks[rank] = make([]int, 1)
			}

			ports[roleGroupIdx][role.Name] = ranks
		}
	}

	for _, a := range allocations {
		if a.RoleGroupIndex < 0 || a.RoleGroupIndex >= len(ports) {
			continue
		}

		role, ok := ports[a.RoleGroupIndex][a.Role]
		if !ok {
			continue
		}

		if a.Rank < 0 || a.Rank >= len(role) {
			continue
		}

		role[a.Rank][0] = a.Port
	}

	return ports
}
