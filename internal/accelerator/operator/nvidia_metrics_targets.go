package operator

import (
	"context"
	"fmt"
	"maps"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/operator/manifests"
	shared "github.com/neutree-ai/neutree/internal/accelerator/operator/metrics"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
)

type nvidiaExporterTarget struct {
	Managed bool
	Target  *v1.MetricsTargetProfile
}

func (r *NvidiaResourceRenderer) MetricsTargets(ctx context.Context, opCtx *OperatorContext) ([]metricsconfig.Target, error) {
	if r == nil || r.acceleratorMgr == nil || opCtx == nil || opCtx.Cluster == nil || opCtx.Cluster.Metadata == nil {
		return nil, fmt.Errorf("NVIDIA accelerator manager and cluster context are required")
	}

	profile, err := r.acceleratorMgr.GetAcceleratorProfile(ctx, NVIDIAAcceleratorType)
	if err != nil {
		return nil, fmt.Errorf("get NVIDIA accelerator profile: %w", err)
	}

	exporter, err := resolveNvidiaExporterTarget(opCtx, profile)
	if err != nil {
		return nil, err
	}

	selector := operatorScopeLabels(opCtx)
	selector[manifests.MetricsTargetLabel] = "node-agent"

	targets := []metricsconfig.Target{{
		JobName:   "nvidia-node-agent",
		Namespace: opCtx.Namespace, Selectors: metricsconfig.PodSelectors(selector), Port: manifests.NodeAgentPort, MetricsPath: "/metrics",
		RelabelConfigs: nvidiaPodRelabelConfigs(opCtx, manifests.NodeAgentPort, "namespace", "pod"),
	}}

	if target := exporter.Target; target != nil {
		targets = append(targets, metricsconfig.Target{
			JobName: "accelerator-exporter-nvidia-gpu", RelabelConfigs: nvidiaExporterRelabelConfigs(opCtx, target.Port),
			Namespace: target.Namespace, Selectors: metricsconfig.PodSelectors(target.PodSelector), Port: target.Port, MetricsPath: target.MetricsPath,
		})
	}

	if opCtx.Cluster.Spec != nil && opCtx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		if target := manifests.NvidiaVirtualizationMetricsTarget(opCtx.Namespace, profile); target != nil {
			targets = append(targets, metricsconfig.Target{
				JobName: "hami-vgpu-monitor", RelabelConfigs: nvidiaMonitorRelabelConfigs(opCtx, target.Port),
				Namespace: target.Namespace, Selectors: metricsconfig.PodSelectors(target.PodSelector), Port: target.Port, MetricsPath: target.MetricsPath,
			})
		}
	}

	return targets, nil
}

// Resolve lifecycle and discovery together so the renderer and registry use the same target.
func resolveNvidiaExporterTarget(opCtx *OperatorContext, profile *v1.AcceleratorProfile) (nvidiaExporterTarget, error) {
	if opCtx == nil || opCtx.Cluster == nil || profile == nil {
		return nvidiaExporterTarget{}, fmt.Errorf("NVIDIA operator context and profile are required")
	}

	if opCtx.Cluster.Spec != nil && opCtx.Cluster.Spec.Config.AcceleratorExporterMode() == v1.ClusterAcceleratorExporterModeExternal {
		if profile.ExternalMetricsTarget == nil {
			return nvidiaExporterTarget{}, fmt.Errorf("NVIDIA external exporter target is required in external mode")
		}

		target := *profile.ExternalMetricsTarget
		target.PodSelector = maps.Clone(target.PodSelector)

		if target.MetricsPath == "" {
			target.MetricsPath = "/metrics"
		}

		return nvidiaExporterTarget{Target: &target}, nil
	}

	return nvidiaExporterTarget{
		Managed: true,
		Target:  manifests.NvidiaManagedExporterTarget(opCtx.Cluster, opCtx.Namespace, profile),
	}, nil
}

func nvidiaExporterRelabelConfigs(opCtx *OperatorContext, port int) []promrelabel.RelabelConfig {
	acceleratorType := NVIDIAAcceleratorType
	rules := nvidiaPodRelabelConfigs(opCtx, port, "namespace", "pod")

	return append(rules, promrelabel.RelabelConfig{TargetLabel: "accelerator_type", Replacement: &acceleratorType})
}

func nvidiaMonitorRelabelConfigs(opCtx *OperatorContext, port int) []promrelabel.RelabelConfig {
	return shared.PodRelabelConfigs(opCtx.Cluster.GetName(), opCtx.Cluster.GetWorkspace(), port, "monitor_namespace", "monitor_pod")
}

func nvidiaPodRelabelConfigs(opCtx *OperatorContext, port int, namespaceLabel, podLabel string) []promrelabel.RelabelConfig {
	return shared.PodRelabelConfigs(opCtx.Cluster.GetName(), opCtx.Cluster.GetWorkspace(), port, namespaceLabel, podLabel)
}
