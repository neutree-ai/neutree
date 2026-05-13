package orchestrator

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
	"github.com/neutree-ai/neutree/internal/deployment/strategy"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
)

// isPDStrategy returns true when the endpoint requests PD same-host
// (Phase 0 Demo dispatch key).
func isPDStrategy(ep *v1.Endpoint) bool {
	if ep.Spec == nil || ep.Spec.Strategy != "pd" {
		return false
	}
	if ep.Spec.Placement == nil {
		return false
	}
	return ep.Spec.Placement.Roles == "same-host"
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

// SerializePlan flattens a plan.DeploymentPlan into the dict shape that the
// Python app_builder receives via Ray Serve Application Args. Stable JSON
// representation — keys / nesting must match cluster-image-builder/serve/
// vllm/v0_17_1/app_pd_collocated.py.
func SerializePlan(p *plan.DeploymentPlan) map[string]interface{} {
	if p == nil {
		return nil
	}
	out := map[string]interface{}{
		"replicas": serializeReplicas(p.Replicas),
	}
	if p.KVConfig != nil {
		out["kv_config"] = serializeKVConfig(p.KVConfig)
	}
	return out
}

func serializeReplicas(rs []*plan.Replica) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(rs))
	for _, r := range rs {
		out = append(out, map[string]interface{}{
			"id":       r.ID,
			"pools":    serializePools(r.Pools),
			"affinity": serializeAffinity(r.Affinity),
		})
	}
	return out
}

func serializePools(ps []*plan.Pool) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(ps))
	for _, p := range ps {
		entry := map[string]interface{}{
			"name":               p.Name,
			"instances":          p.Instances,
			"variables":          p.Variables,
			"env":                p.Env,
			"deployment_options": p.DeploymentOptions,
		}
		if p.Resources != nil {
			entry["resources"] = serializeResources(p.Resources)
		}
		if p.Placement != nil {
			entry["placement"] = map[string]interface{}{
				"strategy":    placementStrategyName(p.Placement.Strategy),
				"granularity": p.Placement.Granularity,
			}
		}
		out = append(out, entry)
	}
	return out
}

func serializeAffinity(as []*plan.CrossPoolAffinity) []map[string]interface{} {
	if len(as) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(as))
	for _, a := range as {
		out = append(out, map[string]interface{}{
			"from_pool":   a.FromPool,
			"to_pool":     a.ToPool,
			"type":        a.Type,
			"granularity": a.Granularity,
		})
	}
	return out
}

func serializeKVConfig(kv *plan.KVConfig) map[string]interface{} {
	out := map[string]interface{}{}
	if kv.Transfer != nil {
		out["transfer"] = map[string]interface{}{
			"connector": kv.Transfer.Connector,
			"extra":     kv.Transfer.Extra,
		}
	}
	if kv.Cache != nil {
		out["cache"] = map[string]interface{}{
			"connector": kv.Cache.Connector,
			"extra":     kv.Cache.Extra,
		}
	}
	return out
}

func serializeResources(r *v1.ResourceSpec) map[string]interface{} {
	out := map[string]interface{}{}
	if r.CPU != nil {
		out["cpu"] = *r.CPU
	}
	if r.GPU != nil {
		out["gpu"] = *r.GPU
	}
	if r.Memory != nil {
		out["memory"] = *r.Memory
	}
	if r.Accelerator != nil {
		out["accelerator"] = r.Accelerator
	}
	return out
}

func placementStrategyName(s plan.PlacementStrategy) string {
	switch s {
	case plan.STRICT_PACK:
		return "STRICT_PACK"
	case plan.PACK:
		return "PACK"
	case plan.SPREAD:
		return "SPREAD"
	case plan.STRICT_SPREAD:
		return "STRICT_SPREAD"
	default:
		return ""
	}
}

// applyPDBranch rewrites the partially built RayServeApplication for the PD
// same-host strategy. Called by EndpointToApplication right before return
// when isPDStrategy(ep) is true.
//
// Phase 0 Demo:
//   - Override import_path to app_pd_collocated:app_builder
//   - Compile plan via strategy.Get("pd")
//   - Inject `plan` and `kv_config` into Args
//   - Keep existing `model`, `deployment_options`, `backend_container` so the
//     Python actor can reuse the model download + runtime_env code path
func applyPDBranch(ep *v1.Endpoint, app *dashboard.RayServeApplication) error {
	pdImport, err := pdImportPath(ep)
	if err != nil {
		return errors.Wrap(err, "PD import path resolution failed")
	}
	app.ImportPath = pdImport

	s, err := strategy.Get("pd")
	if err != nil {
		return errors.Wrap(err, "pd strategy not registered")
	}
	p, err := s.Compile(ep)
	if err != nil {
		return errors.Wrap(err, "pd strategy compile failed")
	}

	if app.Args == nil {
		app.Args = map[string]interface{}{}
	}
	app.Args["plan"] = SerializePlan(p)
	return nil
}
