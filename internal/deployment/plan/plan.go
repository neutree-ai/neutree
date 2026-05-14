// Package plan defines the deployment IR produced by Strategy.Compile and
// consumed by Orchestrator.
//
//	DeploymentPlan
//	├── NumReplicas int                    HA routing-domain count
//	├── Group       *RoleGroup             single template, replicated NumReplicas times
//	│     ├── Placement *PlacementSpec     covers all roles in this group
//	│     └── Roles     []*Role            one entry per role (prefill / decode / engine / stage-N / ep-leader / …)
//	├── Transfer    *KVTransferConfig      PD only; nil for monolithic
//	├── Cache       *KVCacheConfig         any strategy; nil = no cache offload
//	└── Ports       []ReplicaPortMap       portalloc fills; mirrors NumReplicas × Group.Roles × Role.Instances
//
// Engine-agnostic skeleton + engine-private bags. Renderer (Ray / K8s) reads
// the skeleton; per-engine app.py / template reads the bags.
package plan

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// DeploymentPlan is the IR top level. Each of the NumReplicas HA routing
// domains is a complete instantiation of Group.
type DeploymentPlan struct {
	NumReplicas int
	Group       *RoleGroup
	Transfer    *KVTransferConfig
	Cache       *KVCacheConfig
	Ports       []ReplicaPortMap
}

// RoleGroup is the placement-constraint boundary within one routing domain.
// All Roles inside one RoleGroup share Placement and are gang-scheduled.
type RoleGroup struct {
	Placement *PlacementSpec
	Roles     []*Role
}

// Role is one engine-agnostic role inside the RoleGroup. Phase 1 PD uses
// {prefill, decode}; Phase 2 monolithic uses {engine}; Phase 3 TP+PP uses
// {stage-0, stage-1, …}; Phase 4 wide-EP uses {ep-leader, ep-worker}.
type Role struct {
	Name              string
	Instances         int
	Resources         *v1.ResourceSpec
	Variables         map[string]interface{}
	Env               map[string]string
	DeploymentOptions map[string]interface{}
}

// ReplicaPortMap holds port allocations for one replica. Mirrors the IR
// hierarchy exactly — no redundant (Replica, Role, Rank) keying.
//
//	plan.Ports[replicaIdx][roleName][rankIdx] = []int (ordered port list)
//
// IR keeps the port list engine-agnostic: an ordered slice of allocated
// integers. Per-position meaning (which port is the HTTP engine port, which
// is NIXL side_channel, which is SGLang bootstrap, etc.) is a CONVENTION
// owned by the near-engine side — per-engine app.py for Ray, per-engine
// K8s template for Kubernetes. Strategy.Compile + portalloc cooperatively
// decide how many ports per slot (PD vLLM needs 2; SGLang PD prefill needs
// 3; monolithic needs 1).
type ReplicaPortMap map[string][][]int
