package storage

import (
	"context"
	"encoding/json"
	"fmt"
)

// CLUSTER_PORT_ALLOCATION_TABLE is the PostgREST table name for allocated
// endpoint runtime ports.
const CLUSTER_PORT_ALLOCATION_TABLE = "cluster_port_allocations"

// PortAllocation is one persisted port row matching api.cluster_port_allocations.
type PortAllocation struct {
	ID             int    `json:"id,omitempty"`
	ClusterID      int    `json:"cluster_id"`
	Port           int    `json:"port"`
	EndpointID     int    `json:"endpoint_id"`
	RoleGroupIndex int    `json:"role_group_index"`
	Role           string `json:"role"`
	Rank           int    `json:"rank"`
	Purpose        string `json:"purpose"`
	AllocatedAt    string `json:"allocated_at,omitempty"`
}

// PortAllocationStorage persists port allocator state.
type PortAllocationStorage interface {
	ListAllocationsByCluster(ctx context.Context, clusterID int) ([]PortAllocation, error)
	ListAllocationsByEndpoint(ctx context.Context, endpointID int) ([]PortAllocation, error)
	InsertAllocations(ctx context.Context, allocations []PortAllocation) error
	DeleteAllocationsByEndpoint(ctx context.Context, endpointID int) error
}

var _ PortAllocationStorage = (*postgrestStorage)(nil)

func (s *postgrestStorage) ListAllocationsByCluster(_ context.Context, clusterID int) ([]PortAllocation, error) {
	resp, _, err := s.postgrestClient.
		From(CLUSTER_PORT_ALLOCATION_TABLE).
		Select("*", "", false).
		Filter("cluster_id", "eq", fmt.Sprintf("%d", clusterID)).
		Execute()
	if err != nil {
		return nil, err
	}

	var out []PortAllocation
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, err
	}

	return out, nil
}

func (s *postgrestStorage) ListAllocationsByEndpoint(_ context.Context, endpointID int) ([]PortAllocation, error) {
	resp, _, err := s.postgrestClient.
		From(CLUSTER_PORT_ALLOCATION_TABLE).
		Select("*", "", false).
		Filter("endpoint_id", "eq", fmt.Sprintf("%d", endpointID)).
		Execute()
	if err != nil {
		return nil, err
	}

	var out []PortAllocation
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, err
	}

	return out, nil
}

func (s *postgrestStorage) InsertAllocations(_ context.Context, allocations []PortAllocation) error {
	if len(allocations) == 0 {
		return nil
	}

	// PostgREST batch insert; on UNIQUE violation Postgres returns 409 which
	// surfaces as an error here. portalloc guards against duplicate
	// (role_group_index, role, rank, purpose) rows, so a 409 indicates a race
	// across reconcilers. The caller should retry from a fresh state read.
	if _, _, err := s.postgrestClient.
		From(CLUSTER_PORT_ALLOCATION_TABLE).
		Insert(allocations, false, "", "", "").
		Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteAllocationsByEndpoint(_ context.Context, endpointID int) error {
	if _, _, err := s.postgrestClient.
		From(CLUSTER_PORT_ALLOCATION_TABLE).
		Delete("", "").
		Filter("endpoint_id", "eq", fmt.Sprintf("%d", endpointID)).
		Execute(); err != nil {
		return err
	}

	return nil
}
