package portalloc

import (
	"context"
	"encoding/json"
	"fmt"

	postgrest "github.com/supabase-community/postgrest-go"
)

// PortAllocationTable is the PostgREST table name (matches migration 061).
const PortAllocationTable = "cluster_port_allocations"

// PostgRESTStorage backs portalloc with PostgREST against api.cluster_port_allocations.
type PostgRESTStorage struct {
	client *postgrest.Client
}

// NewPostgRESTStorage returns a PostgREST-backed Storage.
func NewPostgRESTStorage(client *postgrest.Client) *PostgRESTStorage {
	return &PostgRESTStorage{client: client}
}

func (s *PostgRESTStorage) ListAllocationsByCluster(_ context.Context, clusterID int) ([]Allocation, error) {
	resp, _, err := s.client.
		From(PortAllocationTable).
		Select("*", "", false).
		Filter("cluster_id", "eq", fmt.Sprintf("%d", clusterID)).
		Execute()
	if err != nil {
		return nil, err
	}
	var out []Allocation
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgRESTStorage) ListAllocationsByEndpoint(_ context.Context, endpointID int) ([]Allocation, error) {
	resp, _, err := s.client.
		From(PortAllocationTable).
		Select("*", "", false).
		Filter("endpoint_id", "eq", fmt.Sprintf("%d", endpointID)).
		Execute()
	if err != nil {
		return nil, err
	}
	var out []Allocation
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgRESTStorage) InsertAllocations(_ context.Context, allocations []Allocation) error {
	if len(allocations) == 0 {
		return nil
	}
	// PostgREST batch insert; on UNIQUE violation Postgres returns 409 which
	// surfaces as an error here. allocator.AllocateForPlan guards against
	// duplicate (replica, role, rank, position) so a 409 indicates a race
	// across reconcilers — caller should retry the full Compile+Allocate
	// flow from a fresh state read.
	if _, _, err := s.client.
		From(PortAllocationTable).
		Insert(allocations, false, "", "", "").
		Execute(); err != nil {
		return err
	}
	return nil
}

func (s *PostgRESTStorage) DeleteAllocationsByEndpoint(_ context.Context, endpointID int) error {
	if _, _, err := s.client.
		From(PortAllocationTable).
		Delete("", "").
		Filter("endpoint_id", "eq", fmt.Sprintf("%d", endpointID)).
		Execute(); err != nil {
		return err
	}
	return nil
}
