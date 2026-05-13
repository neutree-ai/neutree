package plan

// PlacementSpec is the physical-placement constraint of a single Pool.
type PlacementSpec struct {
	Strategy    PlacementStrategy
	Granularity string // Phase 0 always "node"
}

type PlacementStrategy int

const (
	UNCONSTRAINED PlacementStrategy = iota
	STRICT_PACK                     // PD same-host
	PACK
	SPREAD
	STRICT_SPREAD
)

// CrossPoolAffinity is the cross-Pool topology relationship inside a Replica.
// Phase 0 PD same-host uses Type="co-locate" Granularity="node".
type CrossPoolAffinity struct {
	FromPool    string
	ToPool      string
	Type        string // "co-locate" | "anti-affine"
	Granularity string
}
