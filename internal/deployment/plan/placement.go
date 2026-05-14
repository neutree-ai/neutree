package plan

// PlacementSpec is the physical-placement constraint of a RoleGroup. All
// Roles inside one RoleGroup share this constraint.
//
// Notes on prior fields that were retired in the IR convergence:
//   - NodeLabels: topology design deferred; nodeSelector derives from
//     v1.ResourceSpec.Accelerator via the accelerator converter, not IR.
//   - per-Pool Placement: all known phases (PD same-host, PD split-host,
//     TP+PP, wide-EP) have role-uniform placement within one routing
//     domain, so it lives on RoleGroup. Per-role override added when a
//     real use case shows up (additive).
//   - CrossPoolAffinity: derivable from Strategy + Roles list; renderer
//     enumerates pairs and emits K8s podAffinity / antiAffinity directly.
type PlacementSpec struct {
	Strategy    PlacementStrategy
	Granularity string // "node" / "rack" / "fabric"
}

type PlacementStrategy int

const (
	UNCONSTRAINED PlacementStrategy = iota
	STRICT_PACK                     // PD same-host; wide-EP fabric
	PACK                            // soft pack hint
	SPREAD                          // soft spread hint
	STRICT_SPREAD                   // PD split-host; TP+PP cross-node
)
