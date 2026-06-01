package portalloc

import (
	"context"
	"fmt"
	"sync"
)

// MemoryStorage is an in-process Storage implementation. Used by unit tests
// and optional single-process runs that don't want to depend on PostgREST
// roundtrips.
type MemoryStorage struct {
	mu          sync.Mutex
	allocations []Allocation
	nextID      int
}

// NewMemoryStorage returns an empty MemoryStorage.
func NewMemoryStorage() *MemoryStorage { return &MemoryStorage{} }

func (m *MemoryStorage) ListAllocationsByCluster(_ context.Context, clusterID int) ([]Allocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Allocation, 0, len(m.allocations))

	for _, a := range m.allocations {
		if a.ClusterID == clusterID {
			out = append(out, a)
		}
	}

	return out, nil
}

func (m *MemoryStorage) ListAllocationsByEndpoint(_ context.Context, endpointID int) ([]Allocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Allocation, 0, len(m.allocations))

	for _, a := range m.allocations {
		if a.EndpointID == endpointID {
			out = append(out, a)
		}
	}

	return out, nil
}

func (m *MemoryStorage) InsertAllocations(_ context.Context, allocations []Allocation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Pre-validate uniqueness against existing state to mimic PG PK / UNIQUE
	// constraints. Atomicity is "all-or-nothing": no row inserted if any
	// would conflict.
	existingPort := make(map[[2]int]struct{}, len(m.allocations))
	existingSlot := make(map[[6]string]struct{}, len(m.allocations))

	for _, a := range m.allocations {
		existingPort[[2]int{a.ClusterID, a.Port}] = struct{}{}
		existingSlot[slotFingerprint(a)] = struct{}{}
	}

	for _, a := range allocations {
		if _, dup := existingPort[[2]int{a.ClusterID, a.Port}]; dup {
			return fmt.Errorf("memstorage: port %d on cluster %d already allocated",
				a.Port, a.ClusterID)
		}

		if _, dup := existingSlot[slotFingerprint(a)]; dup {
			return fmt.Errorf(
				"memstorage: slot (cluster=%d, endpoint=%d, role_group_index=%d, role=%s, rank=%d, purpose=%s) already allocated",
				a.ClusterID, a.EndpointID, a.RoleGroupIndex, a.Role, a.Rank, a.Purpose,
			)
		}

		existingPort[[2]int{a.ClusterID, a.Port}] = struct{}{}
		existingSlot[slotFingerprint(a)] = struct{}{}
	}

	// All checks passed; commit.
	for _, a := range allocations {
		m.nextID++
		a.ID = m.nextID
		m.allocations = append(m.allocations, a)
	}

	return nil
}

func (m *MemoryStorage) DeleteAllocationsByEndpoint(_ context.Context, endpointID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.allocations[:0]

	for _, a := range m.allocations {
		if a.EndpointID != endpointID {
			kept = append(kept, a)
		}
	}

	m.allocations = kept

	return nil
}

// Count returns the total number of stored allocations (debug helper for tests).
func (m *MemoryStorage) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.allocations)
}

func slotFingerprint(a Allocation) [6]string {
	return [6]string{
		fmt.Sprintf("%d", a.ClusterID),
		fmt.Sprintf("%d", a.EndpointID),
		fmt.Sprintf("%d", a.RoleGroupIndex),
		a.Role,
		fmt.Sprintf("%d", a.Rank),
		a.Purpose,
	}
}
