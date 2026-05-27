package pdconfig

import (
	"reflect"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func num(n int) *int { return &n }

func TestDerivePDSameHostConfig(t *testing.T) {
	ep := &v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Replicas:  v1.ReplicaSpec{Num: num(2)},
			Placement: &v1.PlacementSpec{Roles: "same-host"},
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill", Replicas: &v1.ReplicaSpec{Num: num(1)}},
				{Name: "decode", Replicas: &v1.ReplicaSpec{Num: num(2)}},
			},
			KV: &v1.KVSpec{Transfer: &v1.KVTransferSpec{
				Connector: "mooncake",
				Extra:     map[string]interface{}{"pipeline": "request"},
			}},
		},
	}

	cfg, err := DerivePDSameHostConfig(ep)
	if err != nil {
		t.Fatalf("DerivePDSameHostConfig: %v", err)
	}
	if cfg.NumReplicas != 2 {
		t.Errorf("NumReplicas: got %d want 2", cfg.NumReplicas)
	}
	if cfg.Group == nil || len(cfg.Group.Roles) != 2 {
		t.Fatalf("roles: got %+v want prefill+decode", cfg.Group)
	}
	if cfg.Group.Roles[0].Name != "prefill" || cfg.Group.Roles[1].Name != "decode" {
		t.Errorf("role order: got [%s, %s] want [prefill, decode]",
			cfg.Group.Roles[0].Name, cfg.Group.Roles[1].Name)
	}
	if cfg.Group.Roles[1].Instances != 2 {
		t.Errorf("decode instances: got %d want 2", cfg.Group.Roles[1].Instances)
	}
	for _, role := range cfg.Group.Roles {
		if role.PortsPerRank != 1 {
			t.Errorf("role %q PortsPerRank: got %d want 1", role.Name, role.PortsPerRank)
		}
	}
	if cfg.Transfer == nil || cfg.Transfer.Connector != "mooncake" {
		t.Errorf("Transfer.Connector: got %+v want mooncake", cfg.Transfer)
	}
	if cfg.Transfer.Extra["pipeline"] != "request" {
		t.Errorf("transfer extra not propagated: %+v", cfg.Transfer.Extra)
	}
	if cfg.Ports != nil {
		t.Errorf("Ports should be nil before portalloc, got %v", cfg.Ports)
	}
}

func TestRoleShapeDoesNotExposeDeploymentOptions(t *testing.T) {
	if _, ok := reflect.TypeOf(Role{}).FieldByName("DeploymentOptions"); ok {
		t.Fatalf("pdconfig.Role must not expose DeploymentOptions")
	}
}

func TestDerivePDSameHostConfig_DefaultsKVConnector(t *testing.T) {
	cfg, err := DerivePDSameHostConfig(&v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Strategy: "pd",
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill"},
				{Name: "decode"},
			},
		},
	})
	if err != nil {
		t.Fatalf("DerivePDSameHostConfig: %v", err)
	}
	if cfg.Transfer == nil || cfg.Transfer.Connector != "nixl" {
		t.Fatalf("default connector: got %+v want nixl", cfg.Transfer)
	}
}

func TestEffectivePlacement_Defaults(t *testing.T) {
	ep := &v1.Endpoint{Spec: &v1.EndpointSpec{Strategy: "pd"}}
	if got := EffectivePlacementRoles(ep); got != "same-host" {
		t.Errorf("EffectivePlacementRoles: got %q want same-host", got)
	}
	if got := EffectivePlacementReplicas(ep); got != "spread-node" {
		t.Errorf("EffectivePlacementReplicas: got %q want spread-node", got)
	}
}

func TestValidatePDSameHost_Failures(t *testing.T) {
	tests := []struct {
		name    string
		ep      *v1.Endpoint
		wantSub string
	}{
		{
			name: "missing_prefill",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Strategy:  "pd",
				Placement: &v1.PlacementSpec{Roles: "same-host"},
				Roles:     []v1.EndpointRoleSpec{{Name: "decode"}},
			}},
			wantSub: "prefill and decode",
		},
		{
			name: "unsupported_placement",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Strategy:  "pd",
				Placement: &v1.PlacementSpec{Roles: "spread-host"},
			}},
			wantSub: "same-host",
		},
		{
			name: "unsupported_replica_placement",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Strategy:  "pd",
				Placement: &v1.PlacementSpec{Replicas: "spread-rack"},
				Roles: []v1.EndpointRoleSpec{
					{Name: "prefill"},
					{Name: "decode"},
				},
			}},
			wantSub: "placement.replicas",
		},
		{
			name: "zero_role_replicas",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Strategy: "pd",
				Roles: []v1.EndpointRoleSpec{
					{Name: "prefill", Replicas: &v1.ReplicaSpec{Num: num(0)}},
					{Name: "decode"},
				},
			}},
			wantSub: "replicas.num",
		},
		{
			name: "unsupported_role",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Strategy: "pd",
				Roles: []v1.EndpointRoleSpec{
					{Name: "prefill"},
					{Name: "decode"},
					{Name: "router"},
				},
			}},
			wantSub: "only support prefill and decode",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePDSameHost(tc.ep)
			if err == nil {
				t.Fatalf("expected error")
			}
			if got := err.Error(); !contains(got, tc.wantSub) {
				t.Errorf("error %q does not contain %q", got, tc.wantSub)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
