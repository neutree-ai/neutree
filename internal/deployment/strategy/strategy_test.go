package strategy

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
)

func num(n int) *int { return &n }

func TestGet_UnknownStrategy(t *testing.T) {
	_, err := Get("nonexistent")
	if err == nil {
		t.Fatalf("expected error for unknown strategy")
	}
}

func TestMonolithic_Compile_DefaultRole(t *testing.T) {
	s, err := Get("monolithic")
	if err != nil {
		t.Fatalf("Get(monolithic): %v", err)
	}
	ep := &v1.Endpoint{
		Metadata: &v1.Metadata{Name: "ep1"},
		Spec: &v1.EndpointSpec{
			Replicas: v1.ReplicaSpec{Num: num(2)},
		},
	}
	p, err := s.Compile(ep)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if p.NumReplicas != 2 {
		t.Errorf("NumReplicas: got %d want 2", p.NumReplicas)
	}
	if p.Group == nil || len(p.Group.Roles) != 1 || p.Group.Roles[0].Name != "engine" {
		t.Errorf("expected single engine role, got %+v", p.Group)
	}
	if got := p.Group.Roles[0].PortsPerRank; got != 1 {
		t.Errorf("monolithic engine PortsPerRank: got %d want 1 (HTTP)", got)
	}
	if p.Transfer != nil {
		t.Errorf("monolithic should have nil Transfer, got %+v", p.Transfer)
	}
}

func TestPD_Validate_Failures(t *testing.T) {
	s, _ := Get("pd")
	tests := []struct {
		name    string
		ep      *v1.Endpoint
		wantSub string
	}{
		{
			name: "missing_prefill",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Placement: &v1.PlacementSpec{Roles: "same-host"},
				Roles:     []v1.EndpointRoleSpec{{Name: "decode", Dependencies: []string{"prefill"}}},
			}},
			wantSub: "prefill and decode",
		},
		{
			name: "decode_missing_dep",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Placement: &v1.PlacementSpec{Roles: "same-host"},
				Roles: []v1.EndpointRoleSpec{
					{Name: "prefill"},
					{Name: "decode"},
				},
			}},
			wantSub: "dependencies: [prefill]",
		},
		{
			name: "unsupported_placement",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Placement: &v1.PlacementSpec{Roles: "spread-host"},
			}},
			wantSub: "same-host",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Validate(tc.ep)
			if err == nil {
				t.Fatalf("expected error")
			}
			if got := err.Error(); !contains2(got, tc.wantSub) {
				t.Errorf("error %q does not contain %q", got, tc.wantSub)
			}
		})
	}
}

func TestPD_Compile_SameHost1P1D(t *testing.T) {
	s, _ := Get("pd")
	ep := &v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Replicas:  v1.ReplicaSpec{Num: num(2)},
			Placement: &v1.PlacementSpec{Roles: "same-host"},
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill"},
				{Name: "decode", Dependencies: []string{"prefill"}},
			},
		},
	}
	p, err := s.Compile(ep)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if p.NumReplicas != 2 {
		t.Errorf("NumReplicas: got %d want 2", p.NumReplicas)
	}
	if p.Group == nil {
		t.Fatalf("expected Group, got nil")
	}
	if p.Group.Placement == nil || p.Group.Placement.Strategy != plan.STRICT_PACK {
		t.Errorf("Group.Placement: got %+v want STRICT_PACK", p.Group.Placement)
	}
	if p.Group.Placement.Granularity != "node" {
		t.Errorf("Granularity: got %q want node", p.Group.Placement.Granularity)
	}
	if len(p.Group.Roles) != 2 {
		t.Fatalf("roles: got %d want 2", len(p.Group.Roles))
	}
	if p.Group.Roles[0].Name != "prefill" || p.Group.Roles[1].Name != "decode" {
		t.Errorf("role order: got [%s, %s] want [prefill, decode]",
			p.Group.Roles[0].Name, p.Group.Roles[1].Name)
	}
	// Ray PD: prefill / decode each need 1 port (NIXL side_channel only, no HTTP).
	for _, role := range p.Group.Roles {
		if role.PortsPerRank != 1 {
			t.Errorf("Role %q PortsPerRank: got %d want 1 (NIXL side_channel only)",
				role.Name, role.PortsPerRank)
		}
	}
	if p.Transfer == nil || p.Transfer.Connector != "nixl" {
		t.Errorf("Transfer.Connector: got %+v want nixl", p.Transfer)
	}
	if p.Cache != nil {
		t.Errorf("Cache should stay nil for Phase 1, got %+v", p.Cache)
	}
	if p.Ports != nil {
		t.Errorf("Ports should be nil before portalloc, got %v", p.Ports)
	}
}

func TestPD_Compile_KVConnectorOverride(t *testing.T) {
	s, _ := Get("pd")
	ep := &v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Placement: &v1.PlacementSpec{Roles: "same-host"},
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill"},
				{Name: "decode", Dependencies: []string{"prefill"}},
			},
			DeploymentOptions: map[string]interface{}{
				"kv": map[string]interface{}{
					"transfer": map[string]interface{}{
						"connector": "mooncake",
					},
				},
			},
		},
	}
	p, err := s.Compile(ep)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if p.Transfer.Connector != "mooncake" {
		t.Errorf("connector: got %s want mooncake", p.Transfer.Connector)
	}
}

func contains2(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
