package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEndpointSpecPDFields_RoundTrip verifies the final PD API fields
// serialize cleanly into JSON and parse back into typed structs.
func TestEndpointSpecPDFields_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		spec EndpointSpec
	}{
		{
			name: "standard_empty_pd_fields",
			spec: EndpointSpec{
				Cluster:  "c1",
				Strategy: "standard",
			},
		},
		{
			name: "pd_roles_same_host_placement",
			spec: EndpointSpec{
				Cluster:  "c1",
				Strategy: "pd",
				Placement: &PlacementSpec{
					Replicas: "spread-node",
					Roles:    "same-host",
				},
				Roles: []EndpointRoleSpec{
					{Name: "prefill"},
					{Name: "decode"},
				},
				KV: &KVSpec{
					Transfer: &KVTransferSpec{
						Connector: "nixl",
						Extra: map[string]interface{}{
							"buffer_size": float64(5000000000),
						},
					},
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
			if strings.Contains(string(raw), "deployment_options") || strings.Contains(string(raw), "dependencies") {
				t.Fatalf("role-local legacy fields leaked into JSON: %s", raw)
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
			if tc.spec.KV != nil {
				if got.KV == nil || got.KV.Transfer == nil {
					t.Fatalf("kv.transfer did not round-trip: %+v", got.KV)
				}
				if got.KV.Transfer.Connector != tc.spec.KV.Transfer.Connector {
					t.Errorf("kv.transfer.connector mismatch: got %q want %q",
						got.KV.Transfer.Connector, tc.spec.KV.Transfer.Connector)
				}
			}
		})
	}
}

// TestPDStatus_RoundTrip ensures ReplicaStatus survives PostgREST JSON shape.
func TestPDStatus_RoundTrip(t *testing.T) {
	in := EndpointStatus{
		Phase:         EndpointPhaseRUNNING,
		Strategy:      "pd",
		Placement:     "same-host",
		TotalReplicas: 2,
		ReadyReplicas: 1,
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
	if got.TotalReplicas != 2 || got.ReadyReplicas != 1 {
		t.Errorf("replica counters round-trip failed: total=%d ready=%d",
			got.TotalReplicas, got.ReadyReplicas)
	}
}
