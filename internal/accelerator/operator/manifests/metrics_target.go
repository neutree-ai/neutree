package manifests

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
	shared "github.com/neutree-ai/neutree/internal/accelerator/operator/metrics"
)

func NvidiaVirtualizationMetricsTarget(namespace string, profile *v1.AcceleratorProfile) *v1.MetricsTargetProfile {
	if profile == nil || profile.VirtualizationMetricsTarget == nil {
		return nil
	}

	target := shared.ResolveMetricsTargetNamespace(profile.VirtualizationMetricsTarget, namespace)
	target.MetricsPath = shared.MetricsPath(target.MetricsPath)

	return target
}

// NvidiaManagedExporterTarget is shared by direct manifests and the operator's target registry.
func NvidiaManagedExporterTarget(cluster *v1.Cluster, namespace string, profile *v1.AcceleratorProfile) *v1.MetricsTargetProfile {
	if profile == nil || profile.MetricsExporter == nil {
		return nil
	}

	exporter := profile.MetricsExporter

	port := exporter.Port
	if port == 0 {
		port = ExporterPort
	}

	metricsPath := shared.MetricsPath(exporter.MetricsPath)
	name := shared.AcceleratorExporterName(v1.AcceleratorTypeNVIDIAGPU.String(), exporter.Name)

	return &v1.MetricsTargetProfile{
		Namespace: namespace, Port: port, MetricsPath: metricsPath,
		PodSelector: objectLabels(cluster, name, "accelerator-exporter", v1.AcceleratorTypeNVIDIAGPU.String()),
	}
}
