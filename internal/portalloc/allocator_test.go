package portalloc

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/pdconfig"
)

func newCluster(id int) *v1.Cluster {
	return &v1.Cluster{
		ID:   id,
		Spec: &v1.ClusterSpec{},
	}
}

func newAllocator(storage Storage) Allocator {
	return New(storage, WithPortRange(v1.PortRangeSpec{Start: 20000, End: 21000}))
}

func pdConfig(numReplicas, prefillInstances, decodeInstances, portsPerRank int) *pdconfig.PDSameHostConfig {
	return &pdconfig.PDSameHostConfig{
		NumReplicas: numReplicas,
		Group: &pdconfig.RoleGroup{
			Roles: []*pdconfig.Role{
				{Name: "prefill", Instances: prefillInstances, PortsPerRank: portsPerRank},
				{Name: "decode", Instances: decodeInstances, PortsPerRank: portsPerRank},
			},
		},
		Transfer: &pdconfig.KVTransferConfig{Connector: "nixl"},
	}
}

func TestAllocateForPDSameHostConfig_PDSameHost_1P1D_OneReplica(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 1)

	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 7, p); err != nil {
		t.Fatalf("AllocateForPDSameHostConfig: %v", err)
	}
	if mem.Count() != 2 {
		t.Errorf("expected 2 allocations (1 prefill + 1 decode), got %d", mem.Count())
	}
	if len(p.Ports) != 1 {
		t.Fatalf("cfg.Ports len: got %d want 1", len(p.Ports))
	}
	if got := p.Ports[0]["prefill"][0][0]; got != 20000 {
		t.Errorf("prefill rank-0 port: got %d want 20000", got)
	}
	if got := p.Ports[0]["decode"][0][0]; got != 20001 {
		t.Errorf("decode rank-0 port: got %d want 20001", got)
	}
}

func TestAllocateForPDSameHostConfig_PortsAreUnique_AcrossReplicas(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(3, 1, 1, 1)

	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 11, p); err != nil {
		t.Fatalf("AllocateForPDSameHostConfig: %v", err)
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

func TestAllocateForPDSameHostConfig_Idempotent(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p1 := pdConfig(2, 1, 1, 1)

	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 42, p1); err != nil {
		t.Fatalf("first AllocateForPDSameHostConfig: %v", err)
	}
	firstCount := mem.Count()

	// Second call on a fresh config instance with same endpoint must reuse rows.
	p2 := pdConfig(2, 1, 1, 1)
	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 42, p2); err != nil {
		t.Fatalf("second AllocateForPDSameHostConfig: %v", err)
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

func TestAllocateForPDSameHostConfig_RangeExhausted(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem, WithPortRange(v1.PortRangeSpec{Start: 20000, End: 21000}))
	// 1001 ports available, config needs 1002 (501 replicas × 2 roles).
	cluster := newCluster(1)
	p := pdConfig(501, 1, 1, 1)

	err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 1, p)
	if err == nil {
		t.Fatalf("expected exhaustion error, got nil")
	}
	if mem.Count() != 0 {
		t.Errorf("partial commit on exhaustion: %d rows landed", mem.Count())
	}
}

func TestReleaseAll_Frees(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(2, 1, 1, 1)

	_ = alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 99, p)
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

func TestAllocateForPDSameHostConfig_NoPortsForRoleSkipped(t *testing.T) {
	// PortsPerRank=0 → role contributes no slots.
	mem := NewMemoryStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := &pdconfig.PDSameHostConfig{
		NumReplicas: 2,
		Group: &pdconfig.RoleGroup{
			Roles: []*pdconfig.Role{
				{Name: "engine", Instances: 1, PortsPerRank: 0},
			},
		},
	}
	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 5, p); err != nil {
		t.Fatalf("AllocateForPDSameHostConfig: %v", err)
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

func TestAllocateForPDSameHostConfig_StaleAllocationsSelfHeal(t *testing.T) {
	// Partial / shape-mismatched allocations on disk are expected after an
	// endpoint spec edit (role count, instances, port requirement changed).
	// AllocateForPDSameHostConfig releases them and re-allocates fresh; reconcile is
	// self-healing rather than demanding manual ReleaseAll.
	mem := NewMemoryStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)

	_ = mem.InsertAllocations(context.Background(), []Allocation{{
		ClusterID: 1, Port: 20000, EndpointID: 7,
		RoleGroupIndex: 0, Role: "prefill", Rank: 0,
	}})

	p := pdConfig(1, 1, 1, 1) // needs 2 slots; 1 partial exists
	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 7, p); err != nil {
		t.Fatalf("expected self-heal, got error: %v", err)
	}
	// After self-heal, exactly 2 rows for endpoint 7 (the partial row was
	// dropped and replaced as part of the fresh allocation).
	if mem.Count() != 2 {
		t.Errorf("expected 2 rows after self-heal, got %d", mem.Count())
	}
}

func TestAllocateForPDSameHostConfig_RejectsMultiPortPerRank(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 2)

	err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 3, p)
	if err == nil {
		t.Fatalf("expected error for PortsPerRank=2")
	}
	if mem.Count() != 0 {
		t.Errorf("multi-port rejection should not insert rows, got %d", mem.Count())
	}
}

func TestAllocateForPDSameHostConfig_DefaultPortRange(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem)
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 1)
	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 1, p); err != nil {
		t.Fatalf("AllocateForPDSameHostConfig: %v", err)
	}
	got := p.Ports[0]["prefill"][0][0]
	if got < v1.DefaultPortRange.Start || got > v1.DefaultPortRange.End {
		t.Errorf("port %d outside default range %d-%d",
			got, v1.DefaultPortRange.Start, v1.DefaultPortRange.End)
	}
}

func TestAllocateForPDSameHostConfig_RejectsBadConfiguredRange(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := New(mem, WithPortRange(v1.PortRangeSpec{Start: 20000, End: 20010}))
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 1)

	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 1, p); err == nil {
		t.Fatalf("expected error for too-small configured port range")
	}
}

func TestAllocateForPDSameHostConfig_RejectsBadInput(t *testing.T) {
	mem := NewMemoryStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 1)

	if err := alloc.AllocateForPDSameHostConfig(context.Background(), nil, 1, p); err == nil {
		t.Errorf("expected error for nil cluster")
	}
	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 0, p); err == nil {
		t.Errorf("expected error for endpointID=0")
	}
	if err := alloc.AllocateForPDSameHostConfig(context.Background(), cluster, 1, nil); err == nil {
		t.Errorf("expected error for nil config")
	}
}
