// Package adapter implements the NodeAgent accelerator adapter framework.
//
// The adapter framework mirrors the controller-side accelerator plugin
// injection pattern (internal/accelerator/plugin): a package-level registry
// keyed by accelerator type, populated via Register() from init() functions.
// The shared NodeAgent layer selects an adapter when --accelerator-type is
// set, discovers hardware independently of exporter availability, then
// dispatches only the capability required by the explicit cluster type. An
// unregistered accelerator type or missing capability fails NodeAgent startup
// fast.
package adapter

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

// Accelerator converts a vendor accelerator exporter's raw output into
// Neutree's canonical accelerator metrics. Only registered adapters are
// selected by the NodeAgent; a configured type without a registered adapter
// fails startup fast.
type Accelerator interface {
	// Type returns the accelerator type this adapter handles (for example
	// "nvidia_gpu"). It must match the planner-supplied --accelerator-type.
	Type() string

	// DiscoverHardware returns the hardware identity snapshot for the local
	// node. The shared layer uses this for device snapshots and then passes the
	// snapshot back into topology-specific metric builders.
	DiscoverHardware(ctx context.Context) (model.AcceleratorHardwareSnapshot, error)
}

type KubernetesAccelerator interface {
	Accelerator
	BuildKubernetesMetrics(
		ctx context.Context,
		hardware model.AcceleratorHardwareSnapshot,
		evidence KubernetesAcceleratorEvidence,
	) (AcceleratorMetricResult, error)
}

type StaticAccelerator interface {
	Accelerator
	BuildStaticMetrics(
		ctx context.Context,
		hardware model.AcceleratorHardwareSnapshot,
		evidence StaticAcceleratorEvidence,
	) (AcceleratorMetricResult, error)
}

type CommonAcceleratorEvidence struct {
	ExporterText string
	ExporterUp   bool
	// Labels are copied by the shared layer and carry no vendor-specific
	// interpretation.
	Labels model.CanonicalLabels
}

type KubernetesAcceleratorEvidence struct {
	Common CommonAcceleratorEvidence
	// AllocationAvailable distinguishes failed collection from a valid empty
	// allocation set.
	AllocationAvailable bool
	// PodResources preserves kubelet resource names and device IDs exactly as
	// observed; their vendor semantics remain with the adapter.
	PodResources []model.PodResource
	// EndpointPods preserves per-pod metadata and raw annotations for adapter
	// owned joins with PodResources.
	EndpointPods []EndpointPodEvidence
}

type StaticAcceleratorEvidence struct {
	Common CommonAcceleratorEvidence
	// AllocationAvailable distinguishes unavailable Ray/process evidence from a
	// valid observation with no actors.
	AllocationAvailable bool
	RayEvidence         RayEvidence
}

type EndpointPodEvidence struct {
	Namespace   string
	Name        string
	UID         string
	NodeName    string
	Labels      map[string]string
	Annotations map[string]string
}

// ProcessInfo is a raw process observation without accelerator semantics.
type ProcessInfo = model.ProcessInfo

// RayActor reuses the existing Ray Dashboard actor representation.
type RayActor = dashboard.Actor

type RayEvidence struct {
	// Actors retain the Ray Dashboard's required_resources and local PID fields.
	Actors []RayActor
	// ActorProcesses is keyed by Actor PID. DescendantPIDs are generic process
	// topology evidence; adapters match their own exporter process IDs and retain
	// responsibility for all vendor interpretation.
	ActorProcesses map[int]ProcessInfo
}

// AcceleratorMetricResult is the adapter's restricted output. Hardware
// inventory comes only from Accelerator.DiscoverHardware; this result carries
// workload allocations and canonical neutree_* samples.
type AcceleratorMetricResult struct {
	Allocations []v1.StaticNodeAllocationStatus

	// Samples are the adapter-generated canonical accelerator samples.
	Samples []normalizer.Sample
}

type MetricDescriptor struct {
	Name               string
	LabelNames         []string
	RequiredLabelNames []string
}

type MetricDescriptorProvider interface {
	MetricDescriptors() []MetricDescriptor
}

type Registry struct {
	accelerators map[string]Accelerator
	descriptors  []MetricDescriptor
}

// adapters is populated from init() functions at package load and is read-only
// afterwards, so reads need no synchronization. This is the same contract as the
// controller plugin registry it mirrors (internal/accelerator/plugin), which
// keeps a mutex only because it registers at runtime.
var (
	adapters = make(map[string]Accelerator)
)

// Register adds an accelerator adapter to the package-level registry keyed by
// its Type. Adapters register from init() to mirror the core plugin injection
// pattern.
func Register(a Accelerator) {
	if isNilAccelerator(a) {
		return
	}

	adapters[a.Type()] = a
}

// GetLocalAccelerators returns the package-level adapter registry.
func GetLocalAccelerators() map[string]Accelerator {
	result := make(map[string]Accelerator, len(adapters))
	for typ, accel := range adapters {
		result[typ] = accel
	}

	return result
}

func NewRegistry(accelerators ...Accelerator) (Registry, error) {
	result := Registry{
		accelerators: make(map[string]Accelerator, len(accelerators)),
	}
	seenDescriptors := map[string]struct{}{}

	for _, accel := range accelerators {
		if isNilAccelerator(accel) {
			return Registry{}, fmt.Errorf("accelerator adapter is nil")
		}

		typ := strings.TrimSpace(accel.Type())

		if typ == "" {
			return Registry{}, fmt.Errorf("accelerator adapter type is required")
		}

		if _, ok := result.accelerators[typ]; ok {
			return Registry{}, fmt.Errorf("duplicate accelerator adapter %q", typ)
		}

		result.accelerators[typ] = accel

		provider, ok := accel.(MetricDescriptorProvider)
		if !ok {
			continue
		}

		for _, descriptor := range provider.MetricDescriptors() {
			name := strings.TrimSpace(descriptor.Name)

			if name == "" {
				return Registry{}, fmt.Errorf("accelerator adapter %q declares an empty metric descriptor name", typ)
			}

			if _, exists := seenDescriptors[name]; exists {
				return Registry{}, fmt.Errorf("duplicate accelerator metric descriptor %q", name)
			}

			seenDescriptors[name] = struct{}{}

			result.descriptors = append(result.descriptors, MetricDescriptor{
				Name:               name,
				LabelNames:         append([]string{}, descriptor.LabelNames...),
				RequiredLabelNames: append([]string{}, descriptor.RequiredLabelNames...),
			})
		}
	}

	sort.Slice(result.descriptors, func(i, j int) bool {
		return result.descriptors[i].Name < result.descriptors[j].Name
	})

	return result, nil
}

func LocalRegistry() (Registry, error) {
	accelerators := make([]Accelerator, 0, len(adapters))
	for _, accel := range adapters {
		accelerators = append(accelerators, accel)
	}

	sort.Slice(accelerators, func(i, j int) bool {
		return accelerators[i].Type() < accelerators[j].Type()
	})

	return NewRegistry(accelerators...)
}

func (r Registry) Get(typ string) (Accelerator, bool) {
	if len(r.accelerators) == 0 {
		return nil, false
	}

	accel, ok := r.accelerators[typ]

	return accel, ok
}

func (r Registry) Accelerators() map[string]Accelerator {
	result := make(map[string]Accelerator, len(r.accelerators))
	for typ, accel := range r.accelerators {
		result[typ] = accel
	}

	return result
}

func (r Registry) MetricDescriptors() []MetricDescriptor {
	return append([]MetricDescriptor{}, r.descriptors...)
}

func (r Registry) Types() []string {
	types := make([]string, 0, len(r.accelerators))

	for typ := range r.accelerators {
		types = append(types, typ)
	}

	sort.Strings(types)

	return types
}

func isNilAccelerator(accel Accelerator) bool {
	if accel == nil {
		return true
	}

	value := reflect.ValueOf(accel)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return value.IsNil()
	default:
		return false
	}
}
