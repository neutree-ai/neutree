package orchestrator

import (
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

func stubRayApp(importPath string) dashboard.RayServeApplication {
	return dashboard.RayServeApplication{
		Name:       "ep1",
		ImportPath: importPath,
		Args:       map[string]interface{}{},
	}
}

func num(n int) *int { return &n }

func TestIsPDStrategy(t *testing.T) {
	cases := []struct {
		name string
		ep   *v1.Endpoint
		want bool
	}{
		{"nil_spec", &v1.Endpoint{}, false},
		{"monolithic", &v1.Endpoint{Spec: &v1.EndpointSpec{Strategy: "monolithic"}}, false},
		{"pd_no_placement", &v1.Endpoint{Spec: &v1.EndpointSpec{Strategy: "pd"}}, false},
		{"pd_same_host", &v1.Endpoint{Spec: &v1.EndpointSpec{
			Strategy:  "pd",
			Placement: &v1.PlacementSpec{Roles: "same-host"},
		}}, true},
		{"pd_other_placement", &v1.Endpoint{Spec: &v1.EndpointSpec{
			Strategy:  "pd",
			Placement: &v1.PlacementSpec{Roles: "spread-host"},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPDStrategy(tc.ep); got != tc.want {
				t.Errorf("isPDStrategy: got %v want %v", got, tc.want)
			}
		})
	}
}

func TestPDImportPath_VLLM(t *testing.T) {
	ep := &v1.Endpoint{Spec: &v1.EndpointSpec{
		Engine: &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.17.1"},
	}}
	got, err := pdImportPath(ep)
	if err != nil {
		t.Fatalf("pdImportPath: %v", err)
	}
	want := "serve.vllm.v0_17_1.app_pd_collocated:app_builder"
	if got != want {
		t.Errorf("import_path: got %q want %q", got, want)
	}
}

func TestPDImportPath_RejectsNonVLLM(t *testing.T) {
	ep := &v1.Endpoint{Spec: &v1.EndpointSpec{
		Engine: &v1.EndpointEngineSpec{Engine: "sglang", Version: "v0.5.10"},
	}}
	if _, err := pdImportPath(ep); err == nil || !strings.Contains(err.Error(), "vllm") {
		t.Errorf("expected sglang to be rejected, got err=%v", err)
	}
}

func TestSerializePlan_Shape(t *testing.T) {
	cpu := "1"
	gpu := "1"
	p := &plan.DeploymentPlan{
		KVConfig: &plan.KVConfig{
			Transfer: &plan.KVTransferConfig{Connector: "nixl"},
		},
		Replicas: []*plan.Replica{
			{
				ID: "replica-0",
				Pools: []*plan.Pool{
					{
						Name:      "prefill",
						Instances: 1,
						Resources: &v1.ResourceSpec{CPU: &cpu, GPU: &gpu},
						Placement: &plan.PlacementSpec{Strategy: plan.STRICT_PACK, Granularity: "node"},
					},
				},
				Affinity: []*plan.CrossPoolAffinity{
					{FromPool: "decode", ToPool: "prefill", Type: "co-locate", Granularity: "node"},
				},
			},
		},
	}
	out := SerializePlan(p)
	if out["kv_config"] == nil {
		t.Errorf("expected kv_config")
	}
	replicas := out["replicas"].([]map[string]interface{})
	if len(replicas) != 1 || replicas[0]["id"] != "replica-0" {
		t.Errorf("replicas serialized wrong: %+v", replicas)
	}
	pools := replicas[0]["pools"].([]map[string]interface{})
	if len(pools) != 1 || pools[0]["name"] != "prefill" {
		t.Errorf("pools serialized wrong: %+v", pools)
	}
	plc := pools[0]["placement"].(map[string]interface{})
	if plc["strategy"] != "STRICT_PACK" {
		t.Errorf("placement strategy: %+v", plc)
	}
}

func TestApplyPDBranch_RewritesImportAndInjectsPlan(t *testing.T) {
	ep := &v1.Endpoint{
		Metadata: &v1.Metadata{Name: "ep1"},
		Spec: &v1.EndpointSpec{
			Replicas:  v1.ReplicaSpec{Num: num(1)},
			Strategy:  "pd",
			Placement: &v1.PlacementSpec{Roles: "same-host"},
			Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.17.1"},
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill"},
				{Name: "decode", Dependencies: []string{"prefill"}},
			},
		},
	}
	// EndpointToApplication-style partial app the helper receives.
	app := stubRayApp("serve.vllm.v0_17_1.app:app_builder")

	if err := applyPDBranch(ep, &app); err != nil {
		t.Fatalf("applyPDBranch: %v", err)
	}
	if app.ImportPath != "serve.vllm.v0_17_1.app_pd_collocated:app_builder" {
		t.Errorf("import_path not rewritten: %q", app.ImportPath)
	}
	if app.Args["plan"] == nil {
		t.Errorf("plan not injected into Args")
	}
}
