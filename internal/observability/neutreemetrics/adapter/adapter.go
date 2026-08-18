// Package adapter implements the NodeAgent accelerator adapter framework.
//
// The adapter framework mirrors the controller-side accelerator plugin
// injection pattern (internal/accelerator/plugin): a package-level registry
// keyed by accelerator type, populated via Register() from init() functions.
// The shared NodeAgent layer selects an adapter when --accelerator-type is
// set, calls BuildMetrics to convert vendor exporter output into Neutree's
// canonical accelerator samples, and serializes the result through the
// existing normalizer's common sample exit. An unregistered accelerator type
// fails NodeAgent startup fast.
package adapter

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
)

// Accelerator converts a vendor accelerator exporter's raw output into
// Neutree's canonical accelerator metrics. Only registered adapters are
// selected by the NodeAgent; a configured type without a registered adapter
// fails startup fast.
type Accelerator interface {
	// Type returns the accelerator type this adapter handles (for example
	// "nvidia_gpu"). It must match the planner-supplied --accelerator-type.
	Type() string

	// BuildMetrics converts the raw evidence collected by the shared layer into
	// Neutree's canonical accelerator metrics. Results use only existing generic
	// metric IDs with validated labels.
	BuildMetrics(ctx context.Context, evidence AcceleratorEvidence) (AcceleratorMetricResult, error)
}

// AcceleratorEvidence is the raw, uninterpreted evidence the shared layer
// collected during one scrape cycle. It carries the accelerator exporter text
// and the shared scheduler evidence (endpoint allocations, hardware info, and
// explicit replica usage) so the adapter owns all vendor semantics.
type AcceleratorEvidence struct {
	// AcceleratorType is the explicitly configured type, used by the adapter to
	// self-check that it was selected correctly.
	AcceleratorType string

	// ExporterText is the raw accelerator exporter Prometheus text. The adapter
	// parses vendor metric names itself.
	ExporterText string

	// ExporterUp reports whether the accelerator exporter scrape succeeded. The
	// adapter skips physical sample parsing when it is false.
	ExporterUp bool

	// Labels are the node-level canonical labels applied to emitted samples.
	Labels model.CanonicalLabels

	// EndpointAllocations are the endpoint replica accelerator allocations
	// discovered from scheduler evidence (PodResources / Ray / HAMi).
	EndpointAllocations []model.EndpointAllocation

	// GPUHardwareInfos are the discovered accelerator hardware attributes.
	GPUHardwareInfos []model.GPUHardwareInfo

	// EndpointReplicaGPUUsages are explicit per-replica usage records from the
	// scheduler-side usage provider.
	EndpointReplicaGPUUsages []model.EndpointReplicaGPUUsage
}

// AcceleratorMetricResult is the adapter's restricted output: a discovered
// device snapshot plus the canonical neutree_* samples it generated. Adapters
// may only emit existing generic metric IDs with validated labels.
type AcceleratorMetricResult struct {
	// DeviceSnapshots are the discovered/attributed device allocations for
	// inventory and allocation consumers.
	DeviceSnapshots []v1.DeviceAllocation

	// Samples are the adapter-generated canonical accelerator samples.
	Samples []normalizer.Sample
}

var (
	adapters = make(map[string]Accelerator)
)

// Register adds an accelerator adapter to the package-level registry keyed by
// its Type. Adapters register from init() to mirror the core plugin injection
// pattern.
func Register(a Accelerator) {
	adapters[a.Type()] = a
}

// GetLocalAccelerators returns the package-level adapter registry.
func GetLocalAccelerators() map[string]Accelerator {
	return adapters
}
