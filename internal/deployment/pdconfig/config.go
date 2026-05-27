package pdconfig

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	DefaultPlacementRoles    = "same-host"
	DefaultPlacementReplicas = "spread-node"
	DefaultKVConnector       = "nixl"
)

// PDSameHostConfig is the deterministic runtime config derived from
// EndpointSpec for one same-host P/D endpoint. It is not a general deployment
// abstraction; Ray and K8s conversion code consume it as local render input.
type PDSameHostConfig struct {
	NumReplicas int
	Group       *RoleGroup
	Transfer    *KVTransferConfig
	Ports       []ReplicaPortMap
}

// RoleGroup is the lifecycle and health boundary for one routing domain.
type RoleGroup struct {
	Roles []*Role
}

// Role is one prefill/decode execution-unit template within a RoleGroup.
type Role struct {
	Name      string
	Instances int

	Resources *v1.ResourceSpec

	// RayResource is populated by the Ray orchestrator after accelerator-aware
	// resource conversion. K8s conversion can use Resources directly.
	RayResource *v1.RayResourceSpec

	Variables map[string]interface{}
	Env       map[string]string

	// PortsPerRank tells portalloc how many stable ports are needed for each
	// (replica x role x rank) slot.
	PortsPerRank int
}

// KVTransferConfig describes prefill -> decode KV transfer for the current
// request path.
type KVTransferConfig struct {
	Connector string
	Extra     map[string]interface{}
}

// ReplicaPortMap holds port allocations for one RoleGroup replica.
//
//	cfg.Ports[replicaIdx][roleName][rankIdx] = []int
type ReplicaPortMap map[string][][]int

func ValidatePDSameHost(ep *v1.Endpoint) error {
	if ep.Spec == nil {
		return fmt.Errorf("endpoint spec is nil")
	}

	if ep.Spec.Strategy != "" && ep.Spec.Strategy != "pd" {
		return fmt.Errorf("pd config requires strategy=pd (got %q)", ep.Spec.Strategy)
	}

	if roles := placementRoles(ep); roles != "" && roles != DefaultPlacementRoles {
		return fmt.Errorf("pd only supports placement.roles=%s (got %q)", DefaultPlacementRoles, roles)
	}

	if replicas := placementReplicas(ep); replicas != "" && replicas != DefaultPlacementReplicas && replicas != "pack-node" && replicas != "none" {
		return fmt.Errorf("pd only supports placement.replicas=spread-node|pack-node|none (got %q)", replicas)
	}

	pf, de, err := lookupPDRoles(ep.Spec.Roles)
	if err != nil {
		return err
	}

	if pf == nil || de == nil {
		return fmt.Errorf("pd requires roles to contain both prefill and decode")
	}

	if err := validateRoleReplicas("prefill", pf); err != nil {
		return err
	}

	if err := validateRoleReplicas("decode", de); err != nil {
		return err
	}

	return nil
}

func DerivePDSameHostConfig(ep *v1.Endpoint) (*PDSameHostConfig, error) {
	if err := ValidatePDSameHost(ep); err != nil {
		return nil, err
	}

	pf, de, _ := lookupPDRoles(ep.Spec.Roles)
	numReplicas := 1

	if ep.Spec.Replicas.Num != nil && *ep.Spec.Replicas.Num > 0 {
		numReplicas = *ep.Spec.Replicas.Num
	}

	prefillRole := roleFromSpec(*pf, roleReplicas(pf))
	prefillRole.PortsPerRank = 1
	decodeRole := roleFromSpec(*de, roleReplicas(de))
	decodeRole.PortsPerRank = 1

	return &PDSameHostConfig{
		NumReplicas: numReplicas,
		Group: &RoleGroup{
			Roles: []*Role{prefillRole, decodeRole},
		},
		Transfer: &KVTransferConfig{
			Connector: kvConnector(ep, DefaultKVConnector),
			Extra:     kvExtra(ep),
		},
	}, nil
}

func roleFromSpec(spec v1.EndpointRoleSpec, instances int) *Role {
	return &Role{
		Name:      spec.Name,
		Instances: instances,
		Resources: spec.Resources,
		Variables: spec.Variables,
		Env:       spec.Env,
	}
}

func placementRoles(ep *v1.Endpoint) string {
	if ep.Spec.Placement == nil {
		return ""
	}

	return ep.Spec.Placement.Roles
}

func EffectivePlacementRoles(ep *v1.Endpoint) string {
	if placementRoles(ep) == "" {
		return DefaultPlacementRoles
	}

	return placementRoles(ep)
}

func EffectivePlacementReplicas(ep *v1.Endpoint) string {
	if placementReplicas(ep) == "" {
		return DefaultPlacementReplicas
	}

	return placementReplicas(ep)
}

func placementReplicas(ep *v1.Endpoint) string {
	if ep.Spec.Placement == nil {
		return ""
	}

	return ep.Spec.Placement.Replicas
}

func lookupPDRoles(roles []v1.EndpointRoleSpec) (prefill, decode *v1.EndpointRoleSpec, err error) {
	for i := range roles {
		switch roles[i].Name {
		case "prefill":
			if prefill != nil {
				return nil, nil, fmt.Errorf("pd roles must not contain duplicate prefill")
			}

			prefill = &roles[i]
		case "decode":
			if decode != nil {
				return nil, nil, fmt.Errorf("pd roles must not contain duplicate decode")
			}

			decode = &roles[i]
		default:
			return nil, nil, fmt.Errorf("pd roles only support prefill and decode (got %q)", roles[i].Name)
		}
	}

	return prefill, decode, nil
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

func validateRoleReplicas(name string, r *v1.EndpointRoleSpec) error {
	if r == nil || r.Replicas == nil || r.Replicas.Num == nil {
		return nil
	}

	if *r.Replicas.Num < 1 {
		return fmt.Errorf("pd role %s replicas.num must be >= 1, got %d", name, *r.Replicas.Num)
	}

	return nil
}

func kvConnector(ep *v1.Endpoint, def string) string {
	transfer := kvTransfer(ep)
	if transfer == nil || transfer.Connector == "" {
		return def
	}

	return transfer.Connector
}

func kvExtra(ep *v1.Endpoint) map[string]interface{} {
	transfer := kvTransfer(ep)
	if transfer == nil {
		return nil
	}

	return transfer.Extra
}

func kvTransfer(ep *v1.Endpoint) *v1.KVTransferSpec {
	if ep.Spec == nil || ep.Spec.KV == nil {
		return nil
	}

	return ep.Spec.KV.Transfer
}
