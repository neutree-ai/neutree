package orchestrator

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/pdconfig"
	"github.com/neutree-ai/neutree/internal/portalloc"
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

func pdEngine(version, entrypoint string) *v1.Engine {
	ev := &v1.EngineVersion{
		Version: version,
		Capabilities: &v1.EngineVersionCapabilities{
			PD: &v1.PDCapabilitySpec{
				KVConnectors:   []string{"nixl", "mooncake"},
				SupportedTasks: []string{v1.TextGenerationModelTask},
			},
		},
	}
	ev.SetDeployTemplate(v1.RayServeDeployTarget, v1.PDDeployMode, entrypoint)

	return &v1.Engine{
		Metadata: &v1.Metadata{Name: "vllm"},
		Spec:     &v1.EngineSpec{Versions: []*v1.EngineVersion{ev}},
	}
}

func TestIsPDStrategy(t *testing.T) {
	cases := []struct {
		name string
		ep   *v1.Endpoint
		want bool
	}{
		{"nil_spec", &v1.Endpoint{}, false},
		{"standard", &v1.Endpoint{Spec: &v1.EndpointSpec{Strategy: "standard"}}, false},
		{"pd_no_placement_defaults_roles_same_host", &v1.Endpoint{Spec: &v1.EndpointSpec{Strategy: "pd"}}, true},
		{"pd_roles_same_host", &v1.Endpoint{Spec: &v1.EndpointSpec{
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

func TestResolveEngineCapabilities_RayServeEntrypoint(t *testing.T) {
	ep := &v1.Endpoint{Spec: &v1.EndpointSpec{
		Strategy: "pd",
		Engine:   &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.20.0"},
		Model:    &v1.ModelSpec{Task: v1.TextGenerationModelTask},
	}}
	cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType}}
	got, err := pdconfig.ResolveEngineCapabilities(ep, cluster, pdEngine("v0.20.0", "serve.vllm.v0_20_0.app_pd_collocated:app_builder"))
	if err != nil {
		t.Fatalf("ResolveEngineCapabilities: %v", err)
	}
	want := "serve.vllm.v0_20_0.app_pd_collocated:app_builder"
	if got.RayServeEntrypoint != want {
		t.Errorf("entrypoint: got %q want %q", got.RayServeEntrypoint, want)
	}
}

func TestResolveEngineCapabilities_RejectsMissingPDCapability(t *testing.T) {
	ep := &v1.Endpoint{Spec: &v1.EndpointSpec{
		Strategy: "pd",
		Engine:   &v1.EndpointEngineSpec{Engine: "sglang", Version: "v0.5.10"},
	}}
	cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType}}
	engine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "sglang"},
		Spec:     &v1.EngineSpec{Versions: []*v1.EngineVersion{{Version: "v0.5.10"}}},
	}

	if _, err := pdconfig.ResolveEngineCapabilities(ep, cluster, engine); err == nil || !strings.Contains(err.Error(), "strategy=pd") {
		t.Errorf("expected missing PD capability to be rejected, got err=%v", err)
	}
}

func TestSerializePDConfig_Shape(t *testing.T) {
	cpu := "1"
	gpu := "1"
	cfg := &pdconfig.PDConfig{
		NumReplicas: 2,
		Transfer:    &pdconfig.KVTransferConfig{Connector: "nixl"},
		Group: &pdconfig.RoleGroup{
			Roles: []*pdconfig.Role{
				{
					Name:      "prefill",
					Instances: 1,
					Resources: &v1.ResourceSpec{CPU: &cpu, GPU: &gpu},
				},
				{Name: "decode", Instances: 1},
			},
		},
	}
	out := SerializePDConfig(cfg)
	if out["num_replicas"] != 2 {
		t.Errorf("num_replicas: got %v want 2", out["num_replicas"])
	}
	group := out["group"].(map[string]interface{})
	if _, ok := group["placement"]; ok {
		t.Errorf("placement should not be serialized in pd_config: %v", group["placement"])
	}
	roles := group["roles"].([]map[string]interface{})
	if len(roles) != 2 || roles[0]["name"] != "prefill" {
		t.Errorf("roles: %+v", roles)
	}
	if _, ok := roles[0]["deployment_options"]; ok {
		t.Errorf("deployment_options should not be serialized in pd_config role: %v", roles[0])
	}
	if _, ok := out["cache"]; ok {
		t.Errorf("cache should be omitted when nil, got %v", out["cache"])
	}
	if _, ok := out["ports"]; ok {
		t.Errorf("ports should be omitted when nil, got %v", out["ports"])
	}
}

func TestSerializePDConfig_PortsPassthrough(t *testing.T) {
	// portalloc fills Ports with named purpose slots per role rank; serializer passes through.
	cfg := &pdconfig.PDConfig{
		NumReplicas: 1,
		Group: &pdconfig.RoleGroup{
			Roles: []*pdconfig.Role{{Name: "prefill", Instances: 1}, {Name: "decode", Instances: 1}},
		},
		Ports: []pdconfig.ReplicaPortMap{
			{
				"router":  {{pdconfig.PortPurposeHTTP: 20000}},
				"prefill": {{pdconfig.PortPurposeHTTP: 20001, pdconfig.PortPurposeSideChannel: 20002}},
				"decode":  {{pdconfig.PortPurposeHTTP: 20003, pdconfig.PortPurposeSideChannel: 20004}},
			},
		},
	}
	out := SerializePDConfig(cfg)
	ports := out["ports"].([]map[string][]map[string]int)
	if len(ports) != 1 {
		t.Fatalf("ports len: got %d want 1", len(ports))
	}
	if ports[0]["router"][0][pdconfig.PortPurposeHTTP] != 20000 {
		t.Errorf("router http port: got %v", ports[0]["router"][0])
	}
	if ports[0]["prefill"][0][pdconfig.PortPurposeHTTP] != 20001 {
		t.Errorf("prefill rank-0 port: got %v", ports[0]["prefill"][0])
	}
	if ports[0]["decode"][0][pdconfig.PortPurposeSideChannel] != 20004 {
		t.Errorf("decode rank-0 port: got %v", ports[0]["decode"][0])
	}
}

func TestApplyPDBranch_RewritesImportAndInjectsPDConfig(t *testing.T) {
	ep := &v1.Endpoint{
		ID:       42,
		Metadata: &v1.Metadata{Workspace: "ws1", Name: "ep1"},
		Spec: &v1.EndpointSpec{
			Replicas: v1.ReplicaSpec{Num: num(1)},
			Strategy: "pd",
			Engine:   &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.20.0"},
			Model:    &v1.ModelSpec{Task: v1.TextGenerationModelTask},
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill"},
				{Name: "decode"},
			},
		},
	}
	cluster := &v1.Cluster{
		ID:   1,
		Spec: &v1.ClusterSpec{Type: v1.SSHClusterType},
	}
	engine := pdEngine("v0.20.0", "serve.vllm.v0_20_0.app_pd_collocated:app_builder")
	allocator := portalloc.New(
		portalloc.NewMemoryStorage(),
		portalloc.WithPortRange(v1.PortRangeSpec{Start: 20000, End: 21000}),
	)
	app := stubRayApp("serve.vllm.v0_20_0.app:app_builder")

	if err := applyPDBranch(context.Background(), ep, cluster, engine, nil, allocator, &app); err != nil {
		t.Fatalf("applyPDBranch: %v", err)
	}
	if app.ImportPath != "serve.vllm.v0_20_0.app_pd_collocated:app_builder" {
		t.Errorf("import_path not rewritten: %q", app.ImportPath)
	}
	configArgs, ok := app.Args["pd_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("pd_config not injected as map, got %T", app.Args["pd_config"])
	}
	if configArgs["num_replicas"] != 1 {
		t.Errorf("pd_config.num_replicas: got %v want 1", configArgs["num_replicas"])
	}
	if configArgs["workspace"] != "ws1" || configArgs["endpoint"] != "ep1" {
		t.Errorf("pd_config identity: got workspace=%v endpoint=%v", configArgs["workspace"], configArgs["endpoint"])
	}
	// portalloc must have populated group entry and prefill/decode ports.
	ports, ok := configArgs["ports"].([]map[string][]map[string]int)
	if !ok {
		t.Fatalf("pd_config.ports not serialized as expected: %T %v", configArgs["ports"], configArgs["ports"])
	}
	if len(ports) != 1 ||
		len(ports[0]["router"][0]) != 1 ||
		len(ports[0]["prefill"][0]) != 2 ||
		len(ports[0]["decode"][0]) != 2 {
		t.Errorf("port allocation shape wrong: %v", ports)
	}
}

func TestApplyPDBranch_RequiresAllocator(t *testing.T) {
	ep := &v1.Endpoint{
		ID:       1,
		Metadata: &v1.Metadata{Name: "ep1"},
		Spec: &v1.EndpointSpec{
			Replicas:  v1.ReplicaSpec{Num: num(1)},
			Strategy:  "pd",
			Placement: &v1.PlacementSpec{Roles: "same-host"},
			Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.20.0"},
			Model:     &v1.ModelSpec{Task: v1.TextGenerationModelTask},
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill"},
				{Name: "decode"},
			},
		},
	}
	cluster := &v1.Cluster{ID: 1, Spec: &v1.ClusterSpec{Type: v1.SSHClusterType}}
	engine := pdEngine("v0.20.0", "serve.vllm.v0_20_0.app_pd_collocated:app_builder")
	app := stubRayApp("serve.vllm.v0_20_0.app:app_builder")

	err := applyPDBranch(context.Background(), ep, cluster, engine, nil, nil, &app)
	if err == nil {
		t.Fatalf("expected error when allocator is nil")
	}
	if !strings.Contains(err.Error(), "port allocator") {
		t.Errorf("error should mention port allocator, got %q", err.Error())
	}
}
