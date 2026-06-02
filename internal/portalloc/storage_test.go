package portalloc

import (
	"context"
	"fmt"
	"sync"
)

type testStorage struct {
	mu          sync.Mutex
	allocations []Allocation
	nextID      int
}

func newTestStorage() *testStorage { return &testStorage{} }

func (s *testStorage) ListAllocationsByCluster(_ context.Context, clusterID int) ([]Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Allocation, 0, len(s.allocations))
	for _, a := range s.allocations {
		if a.ClusterID == clusterID {
			out = append(out, a)
		}
	}

	return out, nil
}

func (s *testStorage) ListAllocationsByEndpoint(_ context.Context, endpointID int) ([]Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Allocation, 0, len(s.allocations))
	for _, a := range s.allocations {
		if a.EndpointID == endpointID {
			out = append(out, a)
		}
	}

	return out, nil
}

func (s *testStorage) InsertAllocations(_ context.Context, allocations []Allocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existingPort := make(map[[2]int]struct{}, len(s.allocations))
	existingSlot := make(map[[6]string]struct{}, len(s.allocations))

	for _, a := range s.allocations {
		existingPort[[2]int{a.ClusterID, a.Port}] = struct{}{}
		existingSlot[slotFingerprint(a)] = struct{}{}
	}

	for _, a := range allocations {
		if _, dup := existingPort[[2]int{a.ClusterID, a.Port}]; dup {
			return fmt.Errorf("test storage: port %d on cluster %d already allocated", a.Port, a.ClusterID)
		}

		if _, dup := existingSlot[slotFingerprint(a)]; dup {
			return fmt.Errorf(
				"test storage: slot (cluster=%d, endpoint=%d, role_group_index=%d, role=%s, rank=%d, purpose=%s) already allocated",
				a.ClusterID, a.EndpointID, a.RoleGroupIndex, a.Role, a.Rank, a.Purpose,
			)
		}

		existingPort[[2]int{a.ClusterID, a.Port}] = struct{}{}
		existingSlot[slotFingerprint(a)] = struct{}{}
	}

	for _, a := range allocations {
		s.nextID++
		a.ID = s.nextID
		s.allocations = append(s.allocations, a)
	}

	return nil
}

func (s *testStorage) DeleteAllocationsByEndpoint(_ context.Context, endpointID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := s.allocations[:0]
	for _, a := range s.allocations {
		if a.EndpointID != endpointID {
			kept = append(kept, a)
		}
	}

	s.allocations = kept

	return nil
}

func (s *testStorage) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.allocations)
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
