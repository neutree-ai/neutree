package portalloc

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
)

func newCluster(id int, start, end int) *v1.Cluster {
	pr := &v1.PortRangeSpec{Start: start, End: end}
	return &v1.Cluster{
		ID: id,
		Spec: &v1.ClusterSpec{
			PortRange: pr,
		},
	}
}

func pdPlan(numReplicas, prefillInstances, decodeInstances, portsPerRank int) *plan.DeploymentPlan {
	return &plan.DeploymentPlan{
		NumReplicas: numReplicas,
		Group: &plan.RoleGroup{
			Placement: &plan.PlacementSpec{Strategy: plan.STRICT_PACK, Granularity: "node"},
			Roles: []*plan.Role{
				{Name: "prefill", Instances: prefillInstances, PortsPerRank: portsPerRank},
				{Name: "decode", Instances: decodeInstances, PortsPerRank: portsPerRank},
			},
		},
		Transfer: &plan.KVTransferConfig{Connector: "nixl"},
	}
}

func TestAllocateForPlan_PDSameHost_1P1D_OneReplica(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := newCluster(1, 20000, 20100)
	p := pdPlan(1, 1, 1, 1)

	if err := alloc.AllocateForPlan(context.Background(), cluster, 7, p); err != nil {
		t.Fatalf("AllocateForPlan: %v", err)
	}
	if mem.Count() != 2 {
		t.Errorf("expected 2 allocations (1 prefill + 1 decode), got %d", mem.Count())
	}
	if len(p.Ports) != 1 {
		t.Fatalf("plan.Ports len: got %d want 1", len(p.Ports))
	}
	if got := p.Ports[0]["prefill"][0][0]; got != 20000 {
		t.Errorf("prefill rank-0 pos-0: got %d want 20000", got)
	}
	if got := p.Ports[0]["decode"][0][0]; got != 20001 {
		t.Errorf("decode rank-0 pos-0: got %d want 20001", got)
	}
}

func TestAllocateForPlan_PortsAreUnique_AcrossReplicas(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := newCluster(1, 20000, 20100)
	p := pdPlan(3, 1, 1, 1)

	if err := alloc.AllocateForPlan(context.Background(), cluster, 11, p); err != nil {
		t.Fatalf("AllocateForPlan: %v", err)
	}
	seen := map[int]struct{}{}
	for replicaIdx, rmap := range p.Ports {
		for role, ranks := range rmap {
			for rankIdx, ports := range ranks {
				for _, port := range ports {
					if _, dup := seen[port]; dup {
						t.Errorf("port %d duplicate (replica=%d role=%s rank=%d)",
							port, replicaIdx, role, rankIdx)
					}
					seen[port] = struct{}{}
				}
			}
		}
	}
	if len(seen) != 6 {
		t.Errorf("expected 6 unique ports, got %d", len(seen))
	}
}

func TestAllocateForPlan_Idempotent(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := newCluster(1, 20000, 20100)
	p1 := pdPlan(2, 1, 1, 1)

	if err := alloc.AllocateForPlan(context.Background(), cluster, 42, p1); err != nil {
		t.Fatalf("first AllocateForPlan: %v", err)
	}
	firstCount := mem.Count()

	// Second call on a fresh plan instance with same endpoint must reuse rows.
	p2 := pdPlan(2, 1, 1, 1)
	if err := alloc.AllocateForPlan(context.Background(), cluster, 42, p2); err != nil {
		t.Fatalf("second AllocateForPlan: %v", err)
	}
	if mem.Count() != firstCount {
		t.Errorf("idempotent retry should not insert new rows: %d → %d",
			firstCount, mem.Count())
	}
	// Same ports materialized.
	for r := 0; r < p1.NumReplicas; r++ {
		for role := range p1.Ports[r] {
			for rank := range p1.Ports[r][role] {
				if p1.Ports[r][role][rank][0] != p2.Ports[r][role][rank][0] {
					t.Errorf("retry yielded different port for replica=%d role=%s rank=%d",
						r, role, rank)
				}
			}
		}
	}
}

func TestAllocateForPlan_RangeExhausted(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem)
	// Only 3 ports available, plan needs 4 (2 replicas × 2 roles).
	cluster := newCluster(1, 20000, 20002)
	p := pdPlan(2, 1, 1, 1)

	err := alloc.AllocateForPlan(context.Background(), cluster, 1, p)
	if err == nil {
		t.Fatalf("expected exhaustion error, got nil")
	}
	if mem.Count() != 0 {
		t.Errorf("partial commit on exhaustion: %d rows landed", mem.Count())
	}
}

func TestReleaseAll_Frees(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := newCluster(1, 20000, 20100)
	p := pdPlan(2, 1, 1, 1)

	_ = alloc.AllocateForPlan(context.Background(), cluster, 99, p)
	if mem.Count() != 4 {
		t.Fatalf("expected 4 allocations before release, got %d", mem.Count())
	}
	if err := alloc.ReleaseAll(context.Background(), 99); err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	if mem.Count() != 0 {
		t.Errorf("expected 0 after release, got %d", mem.Count())
	}
}

func TestAllocateForPlan_NoPortsForRoleSkipped(t *testing.T) {
	// PortsPerRank=0 → role contributes no slots.
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := newCluster(1, 20000, 20100)
	p := &plan.DeploymentPlan{
		NumReplicas: 2,
		Group: &plan.RoleGroup{
			Roles: []*plan.Role{
				{Name: "engine", Instances: 1, PortsPerRank: 0},
			},
		},
	}
	if err := alloc.AllocateForPlan(context.Background(), cluster, 5, p); err != nil {
		t.Fatalf("AllocateForPlan: %v", err)
	}
	if mem.Count() != 0 {
		t.Errorf("expected 0 allocations (PortsPerRank=0), got %d", mem.Count())
	}
	if len(p.Ports) != 2 {
		t.Errorf("Ports should still have NumReplicas entries (empty maps), got %d", len(p.Ports))
	}
	if len(p.Ports[0]) != 0 {
		t.Errorf("Ports[0] should be empty map, got %v", p.Ports[0])
	}
}

func TestAllocateForPlan_StaleAllocationsSelfHeal(t *testing.T) {
	// Partial / shape-mismatched allocations on disk are expected after an
	// endpoint spec edit (role count, instances, PortsPerRank changed).
	// AllocateForPlan releases them and re-allocates fresh — reconcile is
	// self-healing rather than demanding manual ReleaseAll.
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := newCluster(1, 20000, 20100)

	_ = mem.InsertAllocations(context.Background(), []Allocation{{
		ClusterID: 1, Port: 20000, EndpointID: 7,
		ReplicaIdx: 0, RoleName: "prefill", RankIdx: 0, PositionIdx: 0,
	}})

	p := pdPlan(1, 1, 1, 1) // needs 2 slots; 1 partial exists
	if err := alloc.AllocateForPlan(context.Background(), cluster, 7, p); err != nil {
		t.Fatalf("expected self-heal, got error: %v", err)
	}
	// After self-heal, exactly 2 rows for endpoint 7 (the partial row was
	// dropped and replaced as part of the fresh allocation).
	if mem.Count() != 2 {
		t.Errorf("expected 2 rows after self-heal, got %d", mem.Count())
	}
}

func TestAllocateForPlan_MultiPositionPerSlot(t *testing.T) {
	// PortsPerRank=2 (e.g. future K8s PD with HTTP+side_channel).
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := newCluster(1, 20000, 20100)
	p := pdPlan(1, 1, 1, 2)

	if err := alloc.AllocateForPlan(context.Background(), cluster, 3, p); err != nil {
		t.Fatalf("AllocateForPlan: %v", err)
	}
	if mem.Count() != 4 {
		t.Errorf("expected 4 allocations (2 roles × 1 rank × 2 positions), got %d", mem.Count())
	}
	if len(p.Ports[0]["prefill"][0]) != 2 {
		t.Errorf("prefill rank-0 should have 2 ports, got %v", p.Ports[0]["prefill"][0])
	}
	if p.Ports[0]["prefill"][0][0] == p.Ports[0]["prefill"][0][1] {
		t.Errorf("pos-0 and pos-1 collided: %v", p.Ports[0]["prefill"][0])
	}
}

func TestAllocateForPlan_DefaultPortRange(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := &v1.Cluster{
		ID:   1,
		Spec: &v1.ClusterSpec{
			// PortRange nil → fall back to v1.DefaultPortRange.
		},
	}
	p := pdPlan(1, 1, 1, 1)
	if err := alloc.AllocateForPlan(context.Background(), cluster, 1, p); err != nil {
		t.Fatalf("AllocateForPlan: %v", err)
	}
	got := p.Ports[0]["prefill"][0][0]
	if got < v1.DefaultPortRange.Start || got > v1.DefaultPortRange.End {
		t.Errorf("port %d outside default range %d-%d",
			got, v1.DefaultPortRange.Start, v1.DefaultPortRange.End)
	}
}

func TestAllocateForPlan_RejectsBadInput(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := newCluster(1, 20000, 20100)
	p := pdPlan(1, 1, 1, 1)

	if err := alloc.AllocateForPlan(context.Background(), nil, 1, p); err == nil {
		t.Errorf("expected error for nil cluster")
	}
	if err := alloc.AllocateForPlan(context.Background(), cluster, 0, p); err == nil {
		t.Errorf("expected error for endpointID=0")
	}
	if err := alloc.AllocateForPlan(context.Background(), cluster, 1, nil); err == nil {
		t.Errorf("expected error for nil plan")
	}
}
