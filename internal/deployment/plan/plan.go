// Package plan defines the deployment IR produced by Strategy.Compile and
// consumed by Orchestrator. Phase 0 (Demo) carries only the minimum shape
// needed to drive `(strategy, placement.roles)` → Ray app_builder dispatch.
//
// Pool intentionally has NO AllocatedPorts field at Demo — port allocation
// is an MVP concern (PR-portalloc). Replica intentionally has NO KVTransfers
// or Dependencies fields — both were retired in the IR design convergence;
// KV topology is discovered by the connector runtime (NIXL bootstrap) and
// Pool startup order is enforced by the underlying framework.
package plan

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// DeploymentPlan is the IR top level. Orchestrator loops over Replicas to
// create resources; KVConfig is read once per Pool render.
type DeploymentPlan struct {
	Replicas []*Replica
	KVConfig *KVConfig
}

// Replica is one complete endpoint replica (routing domain). Same-host PD
// places all Pools of a Replica on a single node (STRICT_PACK / Pod boundary).
type Replica struct {
	ID       string
	Pools    []*Pool
	Affinity []*CrossPoolAffinity
}

// Pool is a homogeneous group of actors inside one Replica.
type Pool struct {
	Name              string
	Instances         int
	Resources         *v1.ResourceSpec
	Variables         map[string]interface{}
	Env               map[string]string
	DeploymentOptions map[string]interface{}
	Placement         *PlacementSpec
}
