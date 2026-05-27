package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/deployment/pdconfig"
	"github.com/neutree-ai/neutree/internal/portalloc"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
)

// isPDStrategy returns true when the endpoint requests PD same-host
// (Phase 0 Demo dispatch key).
func isPDStrategy(ep *v1.Endpoint) bool {
	if ep.Spec == nil || ep.Spec.Strategy != "pd" {
		return false
	}

	return pdconfig.EffectivePlacementRoles(ep) == pdconfig.DefaultPlacementRoles
}

// pdImportPath returns the Ray Serve import path for a PD same-host endpoint.
// Phase 0 Demo only supports vLLM; the engine version is consumed verbatim
// after semver base-version stripping (matching the monolithic path).
func pdImportPath(ep *v1.Endpoint) (string, error) {
	if ep.Spec == nil || ep.Spec.Engine == nil {
		return "", errors.New("endpoint engine is not configured")
	}

	engine := strings.ReplaceAll(ep.Spec.Engine.Engine, "-", "_")
	if engine != "vllm" {
		return "", fmt.Errorf("PD same-host Demo only supports vllm (got %q)", ep.Spec.Engine.Engine)
	}

	base, err := semver.BaseVersion(ep.Spec.Engine.Version)
	if err != nil {
		klog.Warningf("engine version %q is not semver, using as-is for PD import path: %v",
			ep.Spec.Engine.Version, err)

		base = ep.Spec.Engine.Version
	}

	version := strings.NewReplacer(".", "_", "-", "_").Replace(base)

	return fmt.Sprintf("serve.%s.%s.app_pd_collocated:app_builder", engine, version), nil
}

// SerializePDConfig flattens a pdconfig.PDSameHostConfig into the dict shape
// that the Python app_builder receives via Ray Serve Application Args. Stable JSON
// representation — keys / nesting must match cluster-image-builder/serve/
// vllm/v0_17_1/app_pd_collocated.py.
func SerializePDConfig(cfg *pdconfig.PDSameHostConfig) map[string]interface{} {
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

// serializePorts passes through the (replica × role × rank × []int) shape
// verbatim. Per-position meaning is owned by the engine-side app.py.
func serializePorts(ports []pdconfig.ReplicaPortMap) []map[string][][]int {
	out := make([]map[string][][]int, 0, len(ports))

	for _, replicaMap := range ports {
		entry := make(map[string][][]int, len(replicaMap))

		for role, perRank := range replicaMap {
			entry[role] = perRank
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

// applyPDBranch rewrites the partially built RayServeApplication for the PD
// same-host strategy. Called by EndpointToApplication right before return
// when isPDStrategy(ep) is true.
//
// Full path (Demo + MVP — no fallback):
//  1. Override import_path to app_pd_collocated:app_builder
//  2. Derive PDSameHostConfig (NumReplicas + Group + Transfer)
//  3. Convert each role's *v1.ResourceSpec → *v1.RayResourceSpec via
//     acceleratorMgr (plugin-driven: NVIDIA / AMD / future Ascend). Writes
//     to cfg.Role.RayResource so the engine side consumes the same shape
//     monolithic uses, without re-implementing the plugin matrix in Python.
//  4. portAllocator.AllocateForPDSameHostConfig → fills cfg.Ports deterministically
//     (idempotent on retry; same endpoint → same ports)
//  5. SerializePDConfig + inject into Args so PDIngress / inner actors get
//     both the topology and the per-actor port env on startup
func applyPDBranch(ctx context.Context, ep *v1.Endpoint, cluster *v1.Cluster,
	acceleratorMgr accelerator.Manager, allocator portalloc.Allocator,
	app *dashboard.RayServeApplication) error {
	pdImport, err := pdImportPath(ep)
	if err != nil {
		return errors.Wrap(err, "PD import path resolution failed")
	}

	app.ImportPath = pdImport

	cfg, err := pdconfig.DerivePDSameHostConfig(ep)
	if err != nil {
		return errors.Wrap(err, "derive PD same-host config failed")
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

	// Port allocation is mandatory for PD same-host (each NIXL side_channel
	// port must be unique per actor). Refuse to render the config if no
	// allocator is wired — failing fast surfaces the orchestrator-options
	// misconfiguration rather than hitting a port collision at actor start.
	if allocator == nil {
		return errors.New("PD same-host requires a port allocator; orchestrator.Options.PortAllocator is nil")
	}

	if err := allocator.AllocateForPDSameHostConfig(ctx, cluster, ep.ID, cfg); err != nil {
		return errors.Wrapf(err, "port allocation failed for endpoint %d", ep.ID)
	}

	if app.Args == nil {
		app.Args = map[string]interface{}{}
	}

	app.Args["pd_config"] = SerializePDConfig(cfg)

	return nil
}
