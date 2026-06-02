package portalloc

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/pdconfig"
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

func pdConfig(numReplicas, prefillInstances, decodeInstances, portsPerRank int) *pdconfig.PDConfig {
	return &pdconfig.PDConfig{
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

func TestAllocateForPDConfig_1P1D_OneReplica(t *testing.T) {
	mem := newTestStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 2)

	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 7, p); err != nil {
		t.Fatalf("AllocateForPDConfig: %v", err)
	}
	if mem.count() != 5 {
		t.Errorf("expected 5 allocations (router + 1 prefill http/side-channel + 1 decode http/side-channel), got %d", mem.count())
	}
	if len(p.Ports) != 1 {
		t.Fatalf("cfg.Ports len: got %d want 1", len(p.Ports))
	}
	if got := p.Ports[0]["router"][0]["http"]; got != 20000 {
		t.Errorf("router http port: got %d want 20000", got)
	}
	if got := p.Ports[0]["prefill"][0]["http"]; got != 20001 {
		t.Errorf("prefill rank-0 http port: got %d want 20001", got)
	}
	if got := p.Ports[0]["prefill"][0]["side_channel"]; got != 20002 {
		t.Errorf("prefill rank-0 side-channel port: got %d want 20002", got)
	}
	if got := p.Ports[0]["decode"][0]["http"]; got != 20003 {
		t.Errorf("decode rank-0 http port: got %d want 20003", got)
	}
	if got := p.Ports[0]["decode"][0]["side_channel"]; got != 20004 {
		t.Errorf("decode rank-0 side-channel port: got %d want 20004", got)
	}
}

func TestAllocateForPDConfig_PortsAreUnique_AcrossReplicas(t *testing.T) {
	mem := newTestStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(3, 1, 1, 2)

	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 11, p); err != nil {
		t.Fatalf("AllocateForPDConfig: %v", err)
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
	if len(seen) != 15 {
		t.Errorf("expected 15 unique ports, got %d", len(seen))
	}
}

func TestAllocateForPDConfig_Idempotent(t *testing.T) {
	mem := newTestStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p1 := pdConfig(2, 1, 1, 2)

	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 42, p1); err != nil {
		t.Fatalf("first AllocateForPDConfig: %v", err)
	}
	firstCount := mem.count()

	// Second call on a fresh config instance with same endpoint must reuse rows.
	p2 := pdConfig(2, 1, 1, 2)
	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 42, p2); err != nil {
		t.Fatalf("second AllocateForPDConfig: %v", err)
	}
	if mem.count() != firstCount {
		t.Errorf("idempotent retry should not insert new rows: %d → %d",
			firstCount, mem.count())
	}
	// Same ports materialized.
	for r := 0; r < p1.NumReplicas; r++ {
		for role := range p1.Ports[r] {
			for rank := range p1.Ports[r][role] {
				for purpose := range p1.Ports[r][role][rank] {
					if p1.Ports[r][role][rank][purpose] != p2.Ports[r][role][rank][purpose] {
						t.Errorf("retry yielded different port for replica=%d role=%s rank=%d purpose=%s",
							r, role, rank, purpose)
					}
				}
			}
		}
	}
}

func TestAllocateForPDConfig_RangeExhausted(t *testing.T) {
	mem := newTestStorage()
	alloc := New(mem, WithPortRange(v1.PortRangeSpec{Start: 20000, End: 21000}))
	// 1001 ports available, config needs 1005 (201 replicas × 5 ports).
	cluster := newCluster(1)
	p := pdConfig(201, 1, 1, 2)

	err := alloc.AllocateForPDConfig(context.Background(), cluster, 1, p)
	if err == nil {
		t.Fatalf("expected exhaustion error, got nil")
	}
	if mem.count() != 0 {
		t.Errorf("partial commit on exhaustion: %d rows landed", mem.count())
	}
}

func TestReleaseAll_Frees(t *testing.T) {
	mem := newTestStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(2, 1, 1, 2)

	_ = alloc.AllocateForPDConfig(context.Background(), cluster, 99, p)
	if mem.count() != 10 {
		t.Fatalf("expected 10 allocations before release, got %d", mem.count())
	}
	if err := alloc.ReleaseAll(context.Background(), 99); err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	if mem.count() != 0 {
		t.Errorf("expected 0 after release, got %d", mem.count())
	}
}

func TestAllocateForPDConfig_NoPortsForRoleSkipped(t *testing.T) {
	// PortsPerRank=0 → role contributes no slots.
	mem := newTestStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := &pdconfig.PDConfig{
		NumReplicas: 2,
		Group: &pdconfig.RoleGroup{
			Roles: []*pdconfig.Role{
				{Name: "engine", Instances: 1, PortsPerRank: 0},
			},
		},
	}
	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 5, p); err != nil {
		t.Fatalf("AllocateForPDConfig: %v", err)
	}
	if mem.count() != 2 {
		t.Errorf("expected one router allocation per replica, got %d", mem.count())
	}
	if len(p.Ports) != 2 {
		t.Errorf("Ports should still have NumReplicas entries (empty maps), got %d", len(p.Ports))
	}
	if len(p.Ports[0]) != 1 || p.Ports[0]["router"][0]["http"] == 0 {
		t.Errorf("Ports[0] should contain router http port only, got %v", p.Ports[0])
	}
}

func TestAllocateForPDConfig_StaleAllocationsSelfHeal(t *testing.T) {
	// Partial / shape-mismatched allocations on disk are expected after an
	// endpoint spec edit (role count, instances, port requirement changed).
	// AllocateForPDConfig releases them and re-allocates fresh; reconcile is
	// self-healing rather than demanding manual ReleaseAll.
	mem := newTestStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)

	_ = mem.InsertAllocations(context.Background(), []Allocation{{
		ClusterID: 1, Port: 20000, EndpointID: 7,
		RoleGroupIndex: 0, Role: "prefill", Rank: 0,
	}})

	p := pdConfig(1, 1, 1, 2) // needs 5 slots; 1 partial exists
	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 7, p); err != nil {
		t.Fatalf("expected self-heal, got error: %v", err)
	}
	// After self-heal, exactly 5 rows for endpoint 7 (the partial row was
	// dropped and replaced as part of the fresh allocation).
	if mem.count() != 5 {
		t.Errorf("expected 5 rows after self-heal, got %d", mem.count())
	}
}

func TestAllocateForPDConfig_RejectsUnsupportedPortCountPerRank(t *testing.T) {
	mem := newTestStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 3)

	err := alloc.AllocateForPDConfig(context.Background(), cluster, 3, p)
	if err == nil {
		t.Fatalf("expected error for PortsPerRank=3")
	}
	if mem.count() != 0 {
		t.Errorf("unsupported port count rejection should not insert rows, got %d", mem.count())
	}
}

func TestAllocateForPDConfig_DefaultPortRange(t *testing.T) {
	mem := newTestStorage()
	alloc := New(mem)
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 2)
	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 1, p); err != nil {
		t.Fatalf("AllocateForPDConfig: %v", err)
	}
	got := p.Ports[0]["prefill"][0]["http"]
	if got < v1.DefaultPortRange.Start || got > v1.DefaultPortRange.End {
		t.Errorf("port %d outside default range %d-%d",
			got, v1.DefaultPortRange.Start, v1.DefaultPortRange.End)
	}
}

func TestAllocateForPDConfig_RejectsBadConfiguredRange(t *testing.T) {
	mem := newTestStorage()
	alloc := New(mem, WithPortRange(v1.PortRangeSpec{Start: 20000, End: 20010}))
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 2)

	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 1, p); err == nil {
		t.Fatalf("expected error for too-small configured port range")
	}
}

func TestAllocateForPDConfig_RejectsBadInput(t *testing.T) {
	mem := newTestStorage()
	alloc := newAllocator(mem)
	cluster := newCluster(1)
	p := pdConfig(1, 1, 1, 2)

	if err := alloc.AllocateForPDConfig(context.Background(), nil, 1, p); err == nil {
		t.Errorf("expected error for nil cluster")
	}
	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 0, p); err == nil {
		t.Errorf("expected error for endpointID=0")
	}
	if err := alloc.AllocateForPDConfig(context.Background(), cluster, 1, nil); err == nil {
		t.Errorf("expected error for nil config")
	}
}
