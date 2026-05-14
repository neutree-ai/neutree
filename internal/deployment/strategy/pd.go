package strategy

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
)

// PD is the prefill / decode disaggregation strategy. Phase 0 (Demo) only
// implements placement.roles == "same-host" with NIXL + cuda_ipc transport.
type PD struct{}

func init() { Register(&PD{}) }

func (s *PD) Name() string { return "pd" }

// Validate enforces only the Demo-minimum invariants:
//   - placement.roles must be "same-host" (or empty → defaulted)
//   - roles must include both "prefill" and "decode"
//   - decode role must declare dependencies: [prefill]
//
// Full Validate (engine-version capability check, GPU capacity check, etc.)
// lands in MVP PR-08.
func (s *PD) Validate(ep *v1.Endpoint) error {
	if ep.Spec == nil {
		return fmt.Errorf("endpoint spec is nil")
	}
	roles := getPlacementRoles(ep)
	if roles != "" && roles != "same-host" {
		return fmt.Errorf("pd Demo only supports placement.roles=same-host (got %q)", roles)
	}
	pf, de := getPDRoles(ep.Spec.Roles)
	if pf == nil || de == nil {
		return fmt.Errorf("pd requires roles to contain both prefill and decode")
	}
	if !contains(de.Dependencies, "prefill") {
		return fmt.Errorf("decode role must declare dependencies: [prefill]")
	}
	return nil
}

// Compile compiles a same-host PD endpoint into a DeploymentPlan with one
// RoleGroup containing prefill + decode Roles co-located via STRICT_PACK.
// Transfer defaults to NIXL.
func (s *PD) Compile(ep *v1.Endpoint) (*plan.DeploymentPlan, error) {
	if err := s.Validate(ep); err != nil {
		return nil, err
	}

	pf, de := getPDRoles(ep.Spec.Roles)
	numReplicas := 1
	if ep.Spec.Replicas.Num != nil && *ep.Spec.Replicas.Num > 0 {
		numReplicas = *ep.Spec.Replicas.Num
	}
	pfPerReplica := roleReplicas(pf)
	dePerReplica := roleReplicas(de)

	decodeDerived := map[string]interface{}{
		"scheduler": map[string]interface{}{"type": "chwbl", "key": "prefix"},
	}

	// Port requirements per role for the (PD same-host × vLLM × Ray) combo.
	//
	// Ray actors are called via actor-handle RPC — there is no HTTP engine
	// server, so the only port needed per actor is the NIXL side_channel
	// for the cuda_ipc handshake between paired prefill / decode actors:
	//   pos-0 = VLLM_NIXL_SIDE_CHANNEL_PORT
	//
	// K8s Form A (Phase 2+) renders a separate HTTP engine port for each
	// container; that's a K8s-renderer concern handled at template time,
	// not in IR.
	//
	// SGLang variant adds bootstrap port for prefill in a future strategy
	// branch.
	prefillRole := plan.RoleFromSpec(*pf, pfPerReplica, nil)
	prefillRole.PortsPerRank = 1
	decodeRole := plan.RoleFromSpec(*de, dePerReplica, decodeDerived)
	decodeRole.PortsPerRank = 1

	return &plan.DeploymentPlan{
		NumReplicas: numReplicas,
		Group: &plan.RoleGroup{
			Placement: &plan.PlacementSpec{
				Strategy:    plan.STRICT_PACK,
				Granularity: "node",
			},
			Roles: []*plan.Role{prefillRole, decodeRole},
		},
		Transfer: &plan.KVTransferConfig{
			Connector: getKVConnector(ep, "nixl"),
			Extra:     getKVExtra(ep),
		},
		// Cache stays nil (Tier 1 LMCache handled via engine_args passthrough).
		// Ports stays nil — portalloc.AllocateForPlan fills in MVP.
	}, nil
}

// --- helpers ---

func getPlacementRoles(ep *v1.Endpoint) string {
	if ep.Spec.Placement == nil {
		return ""
	}
	return ep.Spec.Placement.Roles
}

func getPDRoles(roles []v1.EndpointRoleSpec) (prefill, decode *v1.EndpointRoleSpec) {
	for i := range roles {
		switch roles[i].Name {
		case "prefill":
			prefill = &roles[i]
		case "decode":
			decode = &roles[i]
		}
	}
	return prefill, decode
}

func roleReplicas(r *v1.EndpointRoleSpec) int {
	if r == nil || r.Replicas == nil || r.Replicas.Num == nil {
		return 1
	}
	if *r.Replicas.Num <= 0 {
		return 1
	}
	return *r.Replicas.Num
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func getKVConnector(ep *v1.Endpoint, def string) string {
	kv := getKVMap(ep)
	if kv == nil {
		return def
	}
	transfer, _ := kv["transfer"].(map[string]interface{})
	if transfer == nil {
		return def
	}
	if c, ok := transfer["connector"].(string); ok && c != "" {
		return c
	}
	return def
}

func getKVExtra(ep *v1.Endpoint) map[string]interface{} {
	kv := getKVMap(ep)
	if kv == nil {
		return nil
	}
	transfer, _ := kv["transfer"].(map[string]interface{})
	if transfer == nil {
		return nil
	}
	extra, _ := transfer["extra"].(map[string]interface{})
	return extra
}

func getKVMap(ep *v1.Endpoint) map[string]interface{} {
	if ep.Spec == nil || ep.Spec.DeploymentOptions == nil {
		return nil
	}
	kv, _ := ep.Spec.DeploymentOptions["kv"].(map[string]interface{})
	return kv
}
