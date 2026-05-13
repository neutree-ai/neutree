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
	if len(p.Replicas) != 2 {
		t.Errorf("replicas: got %d want 2", len(p.Replicas))
	}
	if len(p.Replicas[0].Pools) != 1 || p.Replicas[0].Pools[0].Name != "engine" {
		t.Errorf("expected single engine pool, got %+v", p.Replicas[0].Pools)
	}
	if p.KVConfig != nil {
		t.Errorf("monolithic should have nil KVConfig, got %+v", p.KVConfig)
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
	if len(p.Replicas) != 2 {
		t.Errorf("replicas: got %d want 2", len(p.Replicas))
	}
	r0 := p.Replicas[0]
	if len(r0.Pools) != 2 {
		t.Fatalf("pools: got %d want 2", len(r0.Pools))
	}
	for _, pool := range r0.Pools {
		if pool.Placement == nil || pool.Placement.Strategy != plan.STRICT_PACK {
			t.Errorf("pool %s placement not STRICT_PACK: %+v", pool.Name, pool.Placement)
		}
	}
	if r0.Affinity[0].Type != "co-locate" {
		t.Errorf("affinity.type: got %s want co-locate", r0.Affinity[0].Type)
	}
	if p.KVConfig == nil || p.KVConfig.Transfer.Connector != "nixl" {
		t.Errorf("kv transfer connector: got %+v want nixl", p.KVConfig)
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
	if p.KVConfig.Transfer.Connector != "mooncake" {
		t.Errorf("connector: got %s want mooncake", p.KVConfig.Transfer.Connector)
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
