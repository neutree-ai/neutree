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

	cfg, err := DerivePDSameHostConfig(ep, "mooncake")
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

func TestDerivePDSameHostConfig_AllowsTemplateOwnedDefaultKVConnector(t *testing.T) {
	cfg, err := DerivePDSameHostConfig(&v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Strategy: "pd",
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill"},
				{Name: "decode"},
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("DerivePDSameHostConfig: %v", err)
	}
	if cfg.Transfer == nil || cfg.Transfer.Connector != "" {
		t.Fatalf("Transfer.Connector: got %+v want empty connector", cfg.Transfer)
	}
}

func TestResolveKVConnector(t *testing.T) {
	version := &v1.EngineVersion{
		Capabilities: &v1.EngineVersionCapabilities{
			PD: &v1.PDCapabilitySpec{KVConnectors: []string{"nixl", "mooncake"}},
		},
	}

	got, err := ResolveKVConnector(&v1.Endpoint{Spec: &v1.EndpointSpec{}}, version)
	if err != nil {
		t.Fatalf("ResolveKVConnector omitted: %v", err)
	}
	if got != "" {
		t.Fatalf("omitted connector: got %q want empty", got)
	}

	got, err = ResolveKVConnector(&v1.Endpoint{Spec: &v1.EndpointSpec{
		KV: &v1.KVSpec{Transfer: &v1.KVTransferSpec{Connector: "mooncake"}},
	}}, version)
	if err != nil {
		t.Fatalf("ResolveKVConnector explicit: %v", err)
	}
	if got != "mooncake" {
		t.Fatalf("explicit connector: got %q want mooncake", got)
	}

	_, err = ResolveKVConnector(&v1.Endpoint{Spec: &v1.EndpointSpec{
		KV: &v1.KVSpec{Transfer: &v1.KVTransferSpec{Connector: "unsupported"}},
	}}, version)
	if err == nil || !contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported connector error, got %v", err)
	}

	_, err = ResolveKVConnector(&v1.Endpoint{Spec: &v1.EndpointSpec{}}, &v1.EngineVersion{
		Capabilities: &v1.EngineVersionCapabilities{PD: &v1.PDCapabilitySpec{}},
	})
	if err == nil || !contains(err.Error(), "at least one") {
		t.Fatalf("expected empty connectors error, got %v", err)
	}

	_, err = ResolveKVConnector(&v1.Endpoint{Spec: &v1.EndpointSpec{}}, &v1.EngineVersion{
		Capabilities: &v1.EngineVersionCapabilities{PD: &v1.PDCapabilitySpec{KVConnectors: []string{" "}}},
	})
	if err == nil || !contains(err.Error(), "at least one") {
		t.Fatalf("expected blank connectors error, got %v", err)
	}
}

func TestResolveEngineCapabilities_Ray(t *testing.T) {
	ep := &v1.Endpoint{Spec: &v1.EndpointSpec{
		Strategy: "pd",
		Engine:   &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.20.0"},
		Model:    &v1.ModelSpec{Task: v1.TextGenerationModelTask},
	}}
	cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType}}
	engine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "vllm"},
		Spec: &v1.EngineSpec{Versions: []*v1.EngineVersion{
			{
				Version: "v0.20.0",
				DeployTemplate: map[string]map[string]string{
					v1.RayServeDeployTarget: {
						v1.PDDeployMode: "c2VydmUudmxsbS52MF8yMF8wLmFwcF9wZF9jb2xsb2NhdGVkOmFwcF9idWlsZGVy",
					},
				},
				Capabilities: &v1.EngineVersionCapabilities{
					PD: &v1.PDCapabilitySpec{
						KVConnectors:   []string{"nixl", "mooncake"},
						SupportedTasks: []string{v1.TextGenerationModelTask},
					},
				},
			},
		}},
	}

	resolution, err := ResolveEngineCapabilities(ep, cluster, engine)
	if err != nil {
		t.Fatalf("ResolveEngineCapabilities: %v", err)
	}
	if resolution.KVConnector != "" {
		t.Fatalf("KVConnector: got %q want empty", resolution.KVConnector)
	}
	if resolution.RayServeEntrypoint != "serve.vllm.v0_20_0.app_pd_collocated:app_builder" {
		t.Fatalf("RayServeEntrypoint: got %q", resolution.RayServeEntrypoint)
	}
}

func TestResolveEngineCapabilities_Failures(t *testing.T) {
	baseEndpoint := &v1.Endpoint{Spec: &v1.EndpointSpec{
		Strategy: "pd",
		Engine:   &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.20.0"},
		Model:    &v1.ModelSpec{Task: v1.TextGenerationModelTask},
	}}

	tests := []struct {
		name    string
		ep      *v1.Endpoint
		cluster *v1.Cluster
		engine  *v1.Engine
		wantSub string
	}{
		{
			name:    "missing_pd_capability",
			ep:      baseEndpoint,
			cluster: &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType}},
			engine: &v1.Engine{Metadata: &v1.Metadata{Name: "vllm"}, Spec: &v1.EngineSpec{Versions: []*v1.EngineVersion{
				{Version: "v0.20.0"},
			}}},
			wantSub: "does not support strategy=pd",
		},
		{
			name:    "missing_ray_entrypoint",
			ep:      baseEndpoint,
			cluster: &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType}},
			engine:  engineWithPDCapability(nil),
			wantSub: "does not support PD on ssh/ray",
		},
		{
			name:    "unsupported_task",
			ep:      &v1.Endpoint{Spec: &v1.EndpointSpec{Strategy: "pd", Engine: &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.20.0"}, Model: &v1.ModelSpec{Task: v1.TextEmbeddingModelTask}}},
			cluster: &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType}},
			engine:  engineWithPDCapability(map[string]map[string]string{v1.RayServeDeployTarget: {v1.PDDeployMode: "aW1wb3J0OnBhdGg="}}),
			wantSub: "task",
		},
		{
			name:    "kubernetes_template_required",
			ep:      baseEndpoint,
			cluster: &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}},
			engine:  engineWithPDCapability(map[string]map[string]string{v1.RayServeDeployTarget: {v1.PDDeployMode: "aW1wb3J0OnBhdGg="}}),
			wantSub: "kubernetes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveEngineCapabilities(tc.ep, tc.cluster, tc.engine)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func engineWithPDCapability(templates map[string]map[string]string) *v1.Engine {
	return &v1.Engine{
		Metadata: &v1.Metadata{Name: "vllm"},
		Spec: &v1.EngineSpec{Versions: []*v1.EngineVersion{
			{
				Version:        "v0.20.0",
				DeployTemplate: templates,
				Capabilities: &v1.EngineVersionCapabilities{
					PD: &v1.PDCapabilitySpec{
						KVConnectors:   []string{"nixl"},
						SupportedTasks: []string{v1.TextGenerationModelTask},
					},
				},
			},
		}},
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
