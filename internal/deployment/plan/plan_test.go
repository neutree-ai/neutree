package plan

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestPoolFromRole_DerivedAndUserMerge(t *testing.T) {
	role := v1.EndpointRoleSpec{
		Name: "decode",
		DeploymentOptions: map[string]interface{}{
			"scheduler": map[string]interface{}{"type": "user-override"},
			"custom":    42,
		},
	}
	derived := map[string]interface{}{
		"scheduler": map[string]interface{}{"type": "chwbl", "key": "prefix"},
	}
	placement := &PlacementSpec{Strategy: STRICT_PACK, Granularity: "node"}

	pool := PoolFromRole(role, 2, placement, derived)

	if pool.Name != "decode" {
		t.Errorf("name: got %q want decode", pool.Name)
	}
	if pool.Instances != 2 {
		t.Errorf("instances: got %d want 2", pool.Instances)
	}
	if pool.Placement.Strategy != STRICT_PACK {
		t.Errorf("placement.strategy: got %v want STRICT_PACK", pool.Placement.Strategy)
	}
	// user wins on scheduler key collision
	got := pool.DeploymentOptions["scheduler"].(map[string]interface{})["type"]
	if got != "user-override" {
		t.Errorf("scheduler.type: got %v want user-override", got)
	}
	// derived-only key absent? No — both keep their non-colliding keys
	if pool.DeploymentOptions["custom"] != 42 {
		t.Errorf("custom key missing")
	}
}

func TestMakeReplicas_Count(t *testing.T) {
	rs := MakeReplicas(3, func(i int) *Replica {
		return &Replica{ID: "replica-x"}
	})
	if len(rs) != 3 {
		t.Errorf("len: got %d want 3", len(rs))
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

func TestKVConfig_NilTransferForMonolithic(t *testing.T) {
	// At Demo level we only assert the type composes; semantics live in strategy.
	kv := &KVConfig{}
	if kv.Transfer != nil {
		t.Errorf("expected nil Transfer when zero-valued")
	}
	kv.Transfer = &KVTransferConfig{Connector: "nixl"}
	if kv.Transfer.Connector != "nixl" {
		t.Errorf("transfer.connector round-trip failed")
	}
}
