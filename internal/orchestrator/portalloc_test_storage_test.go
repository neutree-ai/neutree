package orchestrator

import (
	"context"
	"fmt"
	"sync"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/portalloc"
)

type testPortAllocationStorage struct {
	mu          sync.Mutex
	allocations []portalloc.Allocation
	nextID      int
}

func newTestPortAllocator() portalloc.Allocator {
	return portalloc.New(
		&testPortAllocationStorage{},
		portalloc.WithPortRange(v1.PortRangeSpec{Start: 20000, End: 21000}),
	)
}

func (s *testPortAllocationStorage) ListAllocationsByCluster(
	_ context.Context,
	clusterID int,
) ([]portalloc.Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]portalloc.Allocation, 0, len(s.allocations))
	for _, a := range s.allocations {
		if a.ClusterID == clusterID {
			out = append(out, a)
		}
	}

	return out, nil
}

func (s *testPortAllocationStorage) ListAllocationsByEndpoint(
	_ context.Context,
	endpointID int,
) ([]portalloc.Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]portalloc.Allocation, 0, len(s.allocations))
	for _, a := range s.allocations {
		if a.EndpointID == endpointID {
			out = append(out, a)
		}
	}

	return out, nil
}

func (s *testPortAllocationStorage) InsertAllocations(
	_ context.Context,
	allocations []portalloc.Allocation,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existingPort := make(map[[2]int]struct{}, len(s.allocations))
	existingSlot := make(map[[6]string]struct{}, len(s.allocations))

	for _, a := range s.allocations {
		existingPort[[2]int{a.ClusterID, a.Port}] = struct{}{}
		existingSlot[portAllocationSlotFingerprint(a)] = struct{}{}
	}

	for _, a := range allocations {
		if _, dup := existingPort[[2]int{a.ClusterID, a.Port}]; dup {
			return fmt.Errorf("test storage: port %d on cluster %d already allocated", a.Port, a.ClusterID)
		}

		if _, dup := existingSlot[portAllocationSlotFingerprint(a)]; dup {
			return fmt.Errorf(
				"test storage: slot (cluster=%d, endpoint=%d, role_group_index=%d, role=%s, rank=%d, purpose=%s) already allocated",
				a.ClusterID, a.EndpointID, a.RoleGroupIndex, a.Role, a.Rank, a.Purpose,
			)
		}

		existingPort[[2]int{a.ClusterID, a.Port}] = struct{}{}
		existingSlot[portAllocationSlotFingerprint(a)] = struct{}{}
	}

	for _, a := range allocations {
		s.nextID++
		a.ID = s.nextID
		s.allocations = append(s.allocations, a)
	}

	return nil
}

func (s *testPortAllocationStorage) DeleteAllocationsByEndpoint(
	_ context.Context,
	endpointID int,
) error {
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

func portAllocationSlotFingerprint(a portalloc.Allocation) [6]string {
	return [6]string{
		fmt.Sprintf("%d", a.ClusterID),
		fmt.Sprintf("%d", a.EndpointID),
		fmt.Sprintf("%d", a.RoleGroupIndex),
		a.Role,
		fmt.Sprintf("%d", a.Rank),
		a.Purpose,
	}
}
