package pdconfig

import (
	"fmt"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	DefaultPlacementRoles    = "same-host"
	DefaultPlacementReplicas = "spread-node"

	PortRoleRouter = "router"

	PortPurposeHTTP        = "http"
	PortPurposeSideChannel = "side_channel"
)

// PDConfig is the deterministic runtime config derived from EndpointSpec for
// strategy=pd. The current implementation requires placement.roles=same-host,
// but the config type is not a separate same-host deployment abstraction.
type PDConfig struct {
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

	// PortsPerRank gates whether each (RoleGroup x role x rank) slot needs
	// stable ports. Phase 1 PD uses two ports per P/D rank: http and
	// side_channel.
	PortsPerRank int
}

// KVTransferConfig describes prefill -> decode KV transfer for the current
// request path.
type KVTransferConfig struct {
	Connector string
	Extra     map[string]interface{}
}

// RankPortMap holds named ports for one role rank.
type RankPortMap map[string]int

// ReplicaPortMap holds port allocations for one RoleGroup replica.
//
//	cfg.Ports[replicaIdx][roleName][rankIdx][purpose] = port
type ReplicaPortMap map[string][]RankPortMap

func ValidatePD(ep *v1.Endpoint) error {
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

type EngineCapabilityResolution struct {
	Version            *v1.EngineVersion
	KVConnector        string
	RayServeEntrypoint string
}

func DerivePDConfig(ep *v1.Endpoint, connector string) (*PDConfig, error) {
	if err := ValidatePD(ep); err != nil {
		return nil, err
	}

	connector = strings.TrimSpace(connector)

	pf, de, _ := lookupPDRoles(ep.Spec.Roles)
	numReplicas := 1

	if ep.Spec.Replicas.Num != nil && *ep.Spec.Replicas.Num > 0 {
		numReplicas = *ep.Spec.Replicas.Num
	}

	prefillRole := roleFromSpec(*pf, roleReplicas(pf))
	prefillRole.PortsPerRank = 2
	decodeRole := roleFromSpec(*de, roleReplicas(de))
	decodeRole.PortsPerRank = 2

	return &PDConfig{
		NumReplicas: numReplicas,
		Group: &RoleGroup{
			Roles: []*Role{prefillRole, decodeRole},
		},
		Transfer: &KVTransferConfig{
			Connector: connector,
			Extra:     kvExtra(ep),
		},
	}, nil
}

func ResolveEngineCapabilities(ep *v1.Endpoint, cluster *v1.Cluster, engine *v1.Engine) (*EngineCapabilityResolution, error) {
	if ep == nil || ep.Spec == nil {
		return nil, fmt.Errorf("endpoint spec is nil")
	}

	if ep.Spec.Strategy != "pd" {
		return nil, nil
	}

	if cluster == nil || cluster.Spec == nil {
		return nil, fmt.Errorf("cluster spec is nil")
	}

	version, err := findEngineVersion(engine, ep)
	if err != nil {
		return nil, err
	}

	if version.Capabilities == nil || version.Capabilities.PD == nil {
		return nil, fmt.Errorf("engine %s version %s does not support strategy=pd",
			engineName(engine), ep.Spec.Engine.Version)
	}

	connector, err := ResolveKVConnector(ep, version)
	if err != nil {
		return nil, err
	}

	pd := version.Capabilities.PD
	if task := effectiveModelTask(ep); !containsTrimmed(pd.SupportedTasks, task) {
		return nil, fmt.Errorf("engine %s version %s does not support PD task %q",
			engineName(engine), ep.Spec.Engine.Version, task)
	}

	resolution := &EngineCapabilityResolution{
		Version:     version,
		KVConnector: connector,
	}

	switch cluster.Spec.Type {
	case v1.SSHClusterType:
		entrypoint, err := version.GetRayServeEntrypoint(v1.PDDeployMode)
		if err != nil {
			return nil, fmt.Errorf("engine %s version %s does not support PD on ssh/ray: %w",
				engineName(engine), ep.Spec.Engine.Version, err)
		}

		resolution.RayServeEntrypoint = strings.TrimSpace(entrypoint)
		if resolution.RayServeEntrypoint == "" {
			return nil, fmt.Errorf("engine %s version %s has an empty ray_serve PD entrypoint",
				engineName(engine), ep.Spec.Engine.Version)
		}
	case v1.KubernetesClusterType:
		if !version.HasDeployTemplate(v1.KubernetesClusterType, v1.PDDeployMode) {
			return nil, fmt.Errorf("engine %s version %s does not support PD on kubernetes",
				engineName(engine), ep.Spec.Engine.Version)
		}
	default:
		return nil, fmt.Errorf("unsupported cluster type %q for PD", cluster.Spec.Type)
	}

	return resolution, nil
}

func ResolveKVConnector(ep *v1.Endpoint, version *v1.EngineVersion) (string, error) {
	if version == nil || version.Capabilities == nil || version.Capabilities.PD == nil {
		return "", fmt.Errorf("engine version does not declare PD capabilities")
	}

	supported := version.Capabilities.PD.KVConnectors
	if len(supported) == 0 {
		return "", fmt.Errorf("engine version PD capabilities must declare at least one kv connector")
	}

	if !hasNonEmptyTrimmed(supported) {
		return "", fmt.Errorf("engine version PD capabilities must declare at least one kv connector")
	}

	connector := strings.TrimSpace(userKVConnector(ep))
	if connector == "" {
		return "", nil
	}

	if !containsTrimmed(supported, connector) {
		return "", fmt.Errorf("engine version PD capabilities do not support kv connector %q", connector)
	}

	return connector, nil
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

func userKVConnector(ep *v1.Endpoint) string {
	transfer := kvTransfer(ep)
	if transfer == nil {
		return ""
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

func findEngineVersion(engine *v1.Engine, ep *v1.Endpoint) (*v1.EngineVersion, error) {
	if engine == nil || engine.Spec == nil {
		return nil, fmt.Errorf("engine is nil")
	}

	if ep == nil || ep.Spec == nil || ep.Spec.Engine == nil {
		return nil, fmt.Errorf("endpoint engine is not configured")
	}

	for _, version := range engine.Spec.Versions {
		if version != nil && version.Version == ep.Spec.Engine.Version {
			return version, nil
		}
	}

	return nil, fmt.Errorf("engine %s version %s not found", engineName(engine), ep.Spec.Engine.Version)
}

func effectiveModelTask(ep *v1.Endpoint) string {
	if ep == nil || ep.Spec == nil || ep.Spec.Model == nil || ep.Spec.Model.Task == "" {
		return v1.TextGenerationModelTask
	}

	return ep.Spec.Model.Task
}

func engineName(engine *v1.Engine) string {
	if engine == nil || engine.Metadata == nil {
		return ""
	}

	return engine.Metadata.Name
}

func containsTrimmed(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}

	return false
}

func hasNonEmptyTrimmed(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}

	return false
}
