package orchestrator

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/orchestrator/pdconfig"
	"github.com/neutree-ai/neutree/internal/portalloc"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

// isPDStrategy returns true when the endpoint requests strategy=pd with the
// supported placement.roles=same-host mode.
func isPDStrategy(ep *v1.Endpoint) bool {
	if ep.Spec == nil || ep.Spec.Strategy != "pd" {
		return false
	}

	return pdconfig.EffectivePlacementRoles(ep) == pdconfig.DefaultPlacementRoles
}

// SerializePDConfig flattens a pdconfig.PDConfig into the dict shape
// that the Python PD app_builder receives via Ray Serve Application Args.
// Keep keys and nesting stable across all engine-side P/D entrypoints.
func SerializePDConfig(cfg *pdconfig.PDConfig) map[string]interface{} {
	if cfg == nil {
		return nil
	}

	out := map[string]interface{}{
		"num_replicas": cfg.NumReplicas,
	}

	if cfg.Group != nil {
		out["group"] = serializeGroup(cfg.Group)
	}

	if cfg.Transfer != nil {
		out["transfer"] = serializeKVTransfer(cfg.Transfer)
	}

	if cfg.Ports != nil {
		out["ports"] = serializePorts(cfg.Ports)
	}

	return out
}

func serializeGroup(g *pdconfig.RoleGroup) map[string]interface{} {
	return map[string]interface{}{
		"roles": serializeRoles(g.Roles),
	}
}

func serializeRoles(rs []*pdconfig.Role) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(rs))

	for _, r := range rs {
		entry := map[string]interface{}{
			"name":           r.Name,
			"instances":      r.Instances,
			"ports_per_rank": r.PortsPerRank,
			"variables":      r.Variables,
			"env":            r.Env,
		}
		// Engine-side consumes the Ray-shape (num_cpus/num_gpus/memory bytes
		// + custom-resource map). raw ResourceSpec stays CP-internal for
		// audit; do not leak it over the wire.
		if r.RayResource != nil {
			entry["resources"] = serializeRayResource(r.RayResource)
		}

		out = append(out, entry)
	}

	return out
}

func serializeKVTransfer(kt *pdconfig.KVTransferConfig) map[string]interface{} {
	return map[string]interface{}{
		"connector": kt.Connector,
		"extra":     kt.Extra,
	}
}

// serializePorts passes through the (RoleGroup x role x rank x purpose) shape
// verbatim.
func serializePorts(ports []pdconfig.ReplicaPortMap) []map[string][]map[string]int {
	out := make([]map[string][]map[string]int, 0, len(ports))

	for _, replicaMap := range ports {
		entry := make(map[string][]map[string]int, len(replicaMap))

		for role, perRank := range replicaMap {
			ranks := make([]map[string]int, 0, len(perRank))

			for _, rankPorts := range perRank {
				copied := make(map[string]int, len(rankPorts))
				for purpose, port := range rankPorts {
					copied[purpose] = port
				}

				ranks = append(ranks, copied)
			}

			entry[role] = ranks
		}

		out = append(out, entry)
	}

	return out
}

// serializeRayResource flattens *v1.RayResourceSpec into the dict shape the
// Python app_builder feeds straight into `ray_actor_options`. Only emits keys
// that are set so the engine side can keep `**opts` semantics.
func serializeRayResource(r *v1.RayResourceSpec) map[string]interface{} {
	out := map[string]interface{}{}
	if r.NumCPUs != 0 {
		out["num_cpus"] = r.NumCPUs
	}

	if r.NumGPUs != 0 {
		out["num_gpus"] = r.NumGPUs
	}

	if r.Memory != 0 {
		out["memory"] = r.Memory
	}

	if len(r.Resources) > 0 {
		out["resources"] = r.Resources
	}

	return out
}

// applyPDBranch rewrites the partially built RayServeApplication for
// strategy=pd with placement.roles=same-host. Called by EndpointToApplication
// right before return when isPDStrategy(ep) is true.
//
// Full path:
//  1. Override import_path to app_pd_collocated:app_builder
//  2. Derive PDConfig (NumReplicas + Group + Transfer)
//  3. Convert each role's *v1.ResourceSpec -> *v1.RayResourceSpec via
//     acceleratorMgr (plugin-driven: NVIDIA / AMD / future Ascend). Writes
//     to cfg.Role.RayResource so the engine side consumes the same shape
//     standard serving uses, without re-implementing the plugin matrix in Python.
//  4. portAllocator.AllocateForPDConfig -> fills cfg.Ports deterministically
//     (idempotent on retry; same endpoint -> same ports)
//  5. SerializePDConfig + inject into Args so PDRouter / inner actors get
//     both the topology and the per-actor port env on startup
func applyPDBranch(ctx context.Context, ep *v1.Endpoint, cluster *v1.Cluster, engine *v1.Engine,
	acceleratorMgr accelerator.Manager, allocator portalloc.Allocator,
	app *dashboard.RayServeApplication) error {
	resolution, err := pdconfig.ResolveEngineCapabilities(ep, cluster, engine)
	if err != nil {
		return errors.Wrap(err, "PD engine capability resolution failed")
	}

	if resolution == nil {
		return errors.New("PD engine capability resolution returned nil")
	}

	app.ImportPath = resolution.RayServeEntrypoint

	cfg, err := pdconfig.DerivePDConfig(ep, resolution.KVConnector)
	if err != nil {
		return errors.Wrap(err, "derive PD config failed")
	}

	// Per-role accelerator-aware resource translation. acceleratorMgr is
	// optional only to keep test wiring trivial; production createOrUpdate
	// always passes one. Plugin-driven conversion (NumGPUs/custom-resource
	// keys per accelerator) stays single-sourced in Go.
	if acceleratorMgr != nil && cfg.Group != nil {
		for i, role := range cfg.Group.Roles {
			if role == nil || role.Resources == nil {
				continue
			}

			rr, err := convertToRay(acceleratorMgr, role.Resources)
			if err != nil {
				return errors.Wrapf(err, "convert role %q resources to Ray shape", role.Name)
			}

			cfg.Group.Roles[i].RayResource = rr
		}
	}

	// Port allocation is mandatory for the supported PD placement mode (each
	// NIXL side_channel port must be unique per actor). Refuse to render the
	// config if no allocator is wired - failing fast surfaces the
	// orchestrator-options misconfiguration rather than hitting a port
	// collision at actor start.
	if allocator == nil {
		return errors.New("strategy=pd with placement.roles=same-host requires a port allocator; orchestrator.Options.PortAllocator is nil")
	}

	if err := allocator.AllocateForPDConfig(ctx, cluster, ep.ID, cfg); err != nil {
		return errors.Wrapf(err, "port allocation failed for endpoint %d", ep.ID)
	}

	if app.Args == nil {
		app.Args = map[string]interface{}{}
	}

	pdConfig := SerializePDConfig(cfg)
	if ep.Metadata != nil {
		pdConfig["workspace"] = ep.Metadata.Workspace
		pdConfig["endpoint"] = ep.Metadata.Name
	}

	app.Args["pd_config"] = pdConfig

	return nil
}
