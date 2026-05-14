package plan

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestRoleFromSpec_DerivedAndUserMerge(t *testing.T) {
	spec := v1.EndpointRoleSpec{
		Name: "decode",
		DeploymentOptions: map[string]interface{}{
			"scheduler": map[string]interface{}{"type": "user-override"},
			"custom":    42,
		},
	}
	derived := map[string]interface{}{
		"scheduler": map[string]interface{}{"type": "chwbl", "key": "prefix"},
	}

	role := RoleFromSpec(spec, 2, derived)

	if role.Name != "decode" {
		t.Errorf("name: got %q want decode", role.Name)
	}
	if role.Instances != 2 {
		t.Errorf("instances: got %d want 2", role.Instances)
	}
	// user wins on scheduler key collision
	got := role.DeploymentOptions["scheduler"].(map[string]interface{})["type"]
	if got != "user-override" {
		t.Errorf("scheduler.type: got %v want user-override", got)
	}
	// derived-only key absent? No — non-colliding keys keep both
	if role.DeploymentOptions["custom"] != 42 {
		t.Errorf("custom key missing")
	}
}

func TestMergeOpts_NilTolerated(t *testing.T) {
	if got := MergeOpts(nil, nil); got != nil {
		t.Errorf("both nil should return nil, got %v", got)
	}
	if got := MergeOpts(map[string]interface{}{"a": 1}, nil); got["a"] != 1 {
		t.Errorf("derived-only failed: %v", got)
	}
	if got := MergeOpts(nil, map[string]interface{}{"a": 1}); got["a"] != 1 {
		t.Errorf("user-only failed: %v", got)
	}
}

func TestDeploymentPlan_ZeroValues(t *testing.T) {
	// Demo runs with Ports=nil (no portalloc), Transfer set for PD,
	// Cache nil. Verify the zero-value shape composes cleanly.
	p := &DeploymentPlan{
		NumReplicas: 3,
		Group: &RoleGroup{
			Placement: &PlacementSpec{Strategy: STRICT_PACK, Granularity: "node"},
			Roles: []*Role{
				{Name: "prefill", Instances: 1},
				{Name: "decode", Instances: 1},
			},
		},
		Transfer: &KVTransferConfig{Connector: "nixl"},
	}
	if p.NumReplicas != 3 {
		t.Errorf("NumReplicas: got %d want 3", p.NumReplicas)
	}
	if p.Transfer == nil || p.Transfer.Connector != "nixl" {
		t.Errorf("expected Transfer.Connector=nixl, got %+v", p.Transfer)
	}
	if p.Ports != nil {
		t.Errorf("expected nil Ports before portalloc, got %v", p.Ports)
	}
	if p.Cache != nil {
		t.Errorf("expected nil Cache for Demo, got %v", p.Cache)
	}
	if len(p.Group.Roles) != 2 {
		t.Errorf("roles len: got %d want 2", len(p.Group.Roles))
	}
}

func TestReplicaPortMap_LookupPattern(t *testing.T) {
	// Simulates what portalloc.AllocateForPlan would write. The IR carries
	// an ordered []int per slot; per-position meaning is engine convention
	// (e.g., vLLM PD: [engine_port, nixl_side_channel]).
	p := &DeploymentPlan{
		NumReplicas: 2,
		Ports: []ReplicaPortMap{
			// replica 0
			{
				"prefill": {{20000, 20001}}, // rank 0: 2 ports
				"decode":  {{20003, 20004}}, // rank 0: 2 ports
			},
			// replica 1
			{
				"prefill": {{20006, 20007}},
				"decode":  {{20009, 20010}},
			},
		},
	}
	if len(p.Ports) != p.NumReplicas {
		t.Fatalf("len(Ports)=%d should == NumReplicas=%d", len(p.Ports), p.NumReplicas)
	}
	// Lookup pattern: plan.Ports[replicaIdx][roleName][rankIdx][positionIdx]
	if got := p.Ports[1]["prefill"][0][1]; got != 20007 {
		t.Errorf("replica-1 prefill rank-0 pos-1: got %d want 20007", got)
	}
	if got := p.Ports[0]["decode"][0][0]; got != 20003 {
		t.Errorf("replica-0 decode rank-0 pos-0: got %d want 20003", got)
	}
}
