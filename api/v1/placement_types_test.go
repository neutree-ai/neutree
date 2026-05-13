package v1

import (
	"encoding/json"
	"testing"
)

// TestEndpointSpecPDFields_RoundTrip verifies the Demo (Phase 0) PD fields
// serialize cleanly into JSON and parse back into typed structs. This is the
// minimal contract D-04 / D-05 will rely on (orchestrator branches on these
// fields after PostgREST JSON round-trip).
func TestEndpointSpecPDFields_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		spec EndpointSpec
	}{
		{
			name: "monolithic_empty_pd_fields",
			spec: EndpointSpec{
				Cluster:  "c1",
				Strategy: "monolithic",
			},
		},
		{
			name: "pd_same_host_two_roles",
			spec: EndpointSpec{
				Cluster:  "c1",
				Strategy: "pd",
				Placement: &PlacementSpec{
					Replicas: "spread-node",
					Roles:    "same-host",
				},
				Roles: []EndpointRoleSpec{
					{Name: "prefill"},
					{Name: "decode", Dependencies: []string{"prefill"}},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.spec)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got EndpointSpec
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Strategy != tc.spec.Strategy {
				t.Errorf("strategy mismatch: got %q want %q", got.Strategy, tc.spec.Strategy)
			}
			if len(got.Roles) != len(tc.spec.Roles) {
				t.Errorf("roles len mismatch: got %d want %d", len(got.Roles), len(tc.spec.Roles))
			}
			if tc.spec.Placement != nil {
				if got.Placement == nil || got.Placement.Roles != tc.spec.Placement.Roles {
					t.Errorf("placement.roles mismatch: %+v", got.Placement)
				}
			}
		})
	}
}

// TestPDStatus_RoundTrip ensures ReplicaStatus survives PostgREST JSON shape.
func TestPDStatus_RoundTrip(t *testing.T) {
	in := EndpointStatus{
		Phase:     EndpointPhaseRUNNING,
		Strategy:  "pd",
		Placement: "same-host",
		Replicas: []ReplicaStatus{
			{ID: "replica-0", NodeName: "node-a", Phase: "Ready"},
		},
	}
	raw, _ := json.Marshal(in)
	var got EndpointStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Replicas) != 1 || got.Replicas[0].NodeName != "node-a" {
		t.Errorf("replicas round-trip failed: %+v", got.Replicas)
	}
}
