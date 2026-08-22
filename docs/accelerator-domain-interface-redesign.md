# Accelerator Domain Interface Redesign

## Document Control

- Status: draft for review
- Decision: replace dynamic REST accelerator plugins with compile-time accelerator modules.
- Constraints: one accelerator type per cluster; no dynamically registered third-party accelerator plugins.

## Context

`internal/accelerator.Manager` currently acts as a service locator for node detection, SSH runtime configuration, container run options, image suffix selection, resource conversion, resource parsing, metrics exporter profiles, and virtualization configuration. It also owns the `/v1/plugin/register` REST path.

This boundary cannot make context-sensitive decisions. `GetAcceleratorProfile(ctx, type)` has no cluster or endpoint input, while product selection, virtualization, and endpoint configuration depend on both. The external REST path also forwards SSH credentials for node discovery and configures TLS verification off.

Current dependency evidence:

- `internal/accelerator/manager.go` exposes all accelerator operations through `Manager` and constructs external REST plugins.
- `internal/orchestrator/util.go` selects converters only by accelerator type; the conversion boundary lacks cluster context.
- `internal/resource/kubernetes_resource_client.go` and `internal/resource/ray_resource_client.go` enumerate all registered parsers.
- `internal/cluster/component/hami/scope.go` and `internal/cluster/component/metrics/exporters.go` enumerate all plugins to plan cluster components.
- `internal/observability/neutreemetrics` directly understands DCGM, NVML, and NVIDIA device identity.

## Goals

1. Register fixed accelerator capabilities when a control-plane or node-agent binary starts.
2. Detect one accelerator selection per cluster, then route following operations to that type.
3. Make endpoint planning explicitly dependent on cluster selection and capability.
4. Hide Kubernetes, Ray, Docker, HAMi, DRA, DCGM, and vendor command details behind domain contracts.
5. Add accelerator-level inventory, allocations, and metrics without vendor conditions in generic node-agent code.

## Non-Goals

- Heterogeneous clusters with multiple selected accelerator types.
- Runtime loading, HTTP registration, or RPC support for third-party accelerator implementations.
- Replacing the existing public Cluster, Endpoint, or resource API in this change.
- Selecting engine versions. The engine domain continues to validate engine compatibility against accelerator constraints.

## Current Dependency Map

| Consumer | Current dependency | Required behavior |
| --- | --- | --- |
| Static-node reconciler | `DetectAccelerator` | Detect hardware on one static node. |
| SSH/Ray cluster reconciler | `GetNodeAcceleratorType`, `GetNodeRuntimeConfig`, `GetImageSuffix` | Select cluster type, configure Ray containers, and choose cluster-image variants. |
| Static-node cluster planner | `GetAcceleratorProfile` | Add accelerator runtime and exporter components to nodes. |
| Kubernetes and Ray endpoint orchestrators | `GetConverter`, `GetEngineContainerRunOptions` | Render accelerator requests and workload runtime configuration. |
| Kubernetes and Ray resource clients | `GetAllParsers` | Convert observed backend resources to Neutree accelerator groups. |
| HAMi component | `SupportPlugins`, `GetPlugin` | Select virtualization support, node scope, and component configuration. |
| Metrics component | `SupportPlugins`, `GetAcceleratorProfile` | Plan a managed accelerator exporter. |
| Node-agent metrics server | DCGM/NVML-specific functions | Build device snapshots, enrich inventory, and normalize accelerator metrics. |

## Module Boundary

Create `pkg/accelerator` as the public accelerator contract package. It contains accelerator domain types, interfaces, and immutable snapshots. It must not import `internal/` packages or expose transport clients, credentials, Kubernetes API clients, Ray clients, Docker arguments, or Helm value patches.

Each OSS or Enterprise binary registers a fixed module set at startup. A module exposes only the capabilities it implements.

```go
type Module struct {
    Definition          Definition
    Detector            ClusterDetector
    ClusterPlanner      ClusterPlanner
    EndpointPlanner     EndpointPlanner
    ResourceInterpreter ResourceInterpreter
    NodeAgentAdapter    NodeAgentAdapterFactory
}

type Registry interface {
    DetectAll(ctx context.Context, in ClusterDetectionInput) (*ClusterAcceleratorSelection, error)
    Module(acceleratorType string) (Module, bool)
}
```

Registration rejects empty or duplicate types and validates capabilities required by enabled cluster modes. There is no HTTP registration handler, liveness loop, external plugin type, or remote plugin client.

## Detection and Selection

`DetectAll` is the only operation permitted to enumerate modules. It is a registry operation; all subsequent work uses the selected module.

```go
type ClusterDetector interface {
    Detect(ctx context.Context, in ClusterDetectionInput) (*Detection, error)
}

type ClusterAcceleratorSelection struct {
    Type       string
    Product    string
    Nodes      []NodeAcceleratorState
    Source     SelectionSource
}
```

For SSH/static-node clusters, core owns node access and provides a scoped command runner. Credentials never cross a module boundary.

For Kubernetes clusters, core lists endpoint-schedulable nodes and creates an observation from allocatable resources, labels, annotations, installed RuntimeClasses, device-plugin or operator state, and available node-agent inventory. Modules receive the observation, never a kubeconfig or Kubernetes client. CPU-only system/control-plane nodes are ignored. Multiple accelerator types, or products requiring incompatible runtime profiles, on eligible workload nodes block reconciliation.

`spec.config.accelerator_type` is a desired selection when present; detection validates it. Otherwise the detected selection is written to cluster status as observed state.

## Cluster Planning

The selected module produces accelerator-specific cluster intent from a cluster snapshot and backend observation.

```go
type ClusterPlanner interface {
    PlanCluster(ctx context.Context, in ClusterAccelerationInput) (*ClusterAccelerationPlan, error)
}

type ClusterAccelerationPlan struct {
    Runtime        ClusterRuntimeRequirement
    Virtualization *VirtualizationRequirement
    Metrics        *MetricsCollectionRequirement
    Capability     ClusterAcceleratorCapability
}
```

The plan contains semantic requirements rather than Docker flags or Helm patches. Backend component renderers translate it to Ray Docker configuration, Kubernetes resources, HAMi manifests, or a future DRA implementation. This localizes HAMi-to-DRA and Docker-to-device-request changes.

Cluster planning replaces `GetNodeRuntimeConfig` and `GetImageSuffix` in SSH/Ray reconciliation, `GetAcceleratorProfile` in static-node planning, and plugin enumeration in HAMi and metrics component planning.

## Endpoint Planning

Endpoint planning is cluster-aware. The endpoint controller loads the observed selection and resolved cluster capability before calling the selected module.

```go
type EndpointPlanner interface {
    PlanEndpoint(ctx context.Context, in EndpointAccelerationInput) (*EndpointAcceleratorPlan, error)
}

type EndpointAcceleratorInput struct {
    Cluster    ClusterSnapshot
    Selection  ClusterAcceleratorSelection
    Capability ClusterAcceleratorCapability
    Endpoint   EndpointSnapshot
}

type EndpointAcceleratorPlan struct {
    Request      AcceleratorResourceRequest
    Placement    PlacementConstraints
    DeviceAccess DeviceAccessRequirement
    RuntimeHints RuntimeHints
}
```

The plan can express product/count/memory/core intent, virtualized-device annotations, RuntimeClass requirements, environment variables, and device access. Kubernetes, Ray, and static-Docker renderers own the conversion to backend-specific request, limit, annotation, runtime-env, and command-line formats.

This replaces the split `GetConverter` and `GetEngineContainerRunOptions` calls so resource conversion and runtime configuration cannot diverge from cluster virtualization state.

## Observed Resource Processing

Backend clients keep responsibility for listing nodes, Pods, and Ray state and calculating generic CPU/memory availability. They normalize those results before calling the selected interpreter.

```go
type ResourceInterpreter interface {
    InterpretKubernetes(in KubernetesResourceInventory) (*AcceleratorResourceObservation, error)
    InterpretRay(in RayResourceInventory) (*AcceleratorResourceObservation, error)
}
```

`AcceleratorResourceObservation` contains accelerator groups, products, devices, allocations, and accelerator metadata only. It cannot overwrite generic CPU/memory information or persist status. Core merges it into `ResourceStatus` and owns sorting, errors, and status writes.

Single-type cluster selection means Kubernetes and Ray resource clients resolve one interpreter by `status.accelerator_type` instead of accepting maps from `GetAllParsers()`.

## Node-Agent Adapter

The node agent runs in a separate process and selects a compiled adapter by the cluster's selected type. It does not call the control-plane registry remotely.

```go
type NodeAgentAdapter interface {
    Discover(ctx context.Context) (*v1.NodeDeviceSnapshot, error)
    EnrichHardware(ctx context.Context, snapshot *v1.NodeDeviceSnapshot) error
    NormalizeMetrics(ctx context.Context, raw string) ([]MetricSample, error)
}
```

The NVIDIA adapter absorbs existing DCGM and NVML handling. An Ascend adapter can consume its exporter and local tooling without adding NPU-specific metric names, labels, or device identity rules to generic metric, allocation, or snapshot code. The generic path correlates normalized devices with endpoint and replica identities and publishes stable metrics.

## Accelerator Profile

`AcceleratorProfile` is not retained as a control-plane dependency because its fields belong to different resolved plans:

| Existing field | New owner |
| --- | --- |
| `AcceleratorType` | `Definition` and `ClusterAcceleratorSelection` |
| `ClusterRuntime` | `ClusterAccelerationPlan.Runtime` |
| `EngineRuntime` | `EndpointAcceleratorPlan.DeviceAccess` and `RuntimeHints` |
| `MetricsExporter` | `ClusterAccelerationPlan.Metrics` |

An API/UI adapter may assemble a display-only profile from these outputs. Reconcilers must not use that view to make decisions.

## Wiring and Package Ownership

`cmd/neutree-core/app/options` creates the accelerator manager before controller factories run. The new registry must be built there, stored in `CoreConfig`, and injected into controllers as narrow consumer interfaces. An `app.Builder` method alone is insufficient because it runs after this configuration path.

| Package | Ownership |
| --- | --- |
| `pkg/accelerator` | Public contracts, snapshots, plans, semantic quantity, and metric types. |
| `internal/accelerator` | Registry implementation and OSS NVIDIA/AMD module wiring. |
| `internal/cluster`, `internal/orchestrator` | Backend observation acquisition and rendering of plans. |
| `internal/resource` | Generic resource aggregation and status persistence. |
| `internal/observability/neutreemetrics` | Generic metric pipeline and node-agent adapter selection. |
| Enterprise module | Enterprise accelerator module and node-agent adapter, linked by Enterprise binaries. |

## Compatibility and Removal

The following implementation surface is removed in one breaking release:

- `/v1/plugin/register`, `RegisterRequest`, and external plugin API paths.
- `acceleratorRestPlugin`, its HTTP client, ping-based removal, and `ExternalPluginType`.
- `Manager` methods exposing converters, parsers, profiles, image suffixes, or container run options.

NVIDIA and AMD first adapt to the new contracts. Enterprise modules register at binary startup. After consumers migrate, delete the REST implementation and old manager together. Do not retain a compatibility path that silently chooses one implementation.

## Failure Semantics

- Unknown selected type, duplicate registration, or a missing required module capability prevents startup.
- Detection disagreement across eligible workload nodes blocks cluster readiness with a clear status error.
- An endpoint requiring an unavailable product, virtualization mode, or runtime capability is rejected before backend rendering.
- Backend observation failures are retryable reconcile errors; unsupported accelerator resources are not treated as generic custom resources.
- Node-agent adapter failures degrade accelerator inventory/metrics reporting and retain the last valid snapshot where existing status semantics allow it.

## Security and Isolation

- Only locally linked modules register capabilities.
- Snapshots omit SSH private keys, kubeconfigs, storage credentials, and control-plane tokens.
- SSH execution remains in core behind a scoped execution capability.
- Kubernetes API clients remain in core; modules receive observations rather than client access.
- Backend renderers validate runtime hints, annotations, environment variables, resource names, and image references before use.

## Testing

### Unit test

- Registry registration, duplicate rejection, selected-type routing, and detection aggregation.
- SSH and Kubernetes detection consistency, including CPU-only system nodes and mixed eligible accelerator nodes.
- Module cluster and endpoint plans for supported products and virtualization modes.
- Resource interpreter and node-agent adapter normalization using vendor fixtures.

### DB test

- Cluster status persists observed selection without overwriting desired configuration.
- Resource status persists generic and selected accelerator observations as one coherent snapshot.

### E2E test

- Create SSH and Kubernetes accelerator clusters and verify detection and selected status.
- Deploy an accelerator endpoint and verify rendered backend resources and runtime configuration.
- Verify selected accelerator inventory and normalized accelerator-level metrics reach the observability path.
- Verify mixed-type eligible nodes and unsupported endpoint products fail with actionable status errors.

Manual testing is not required when the required accelerator environments are available to E2E. Hardware variants unavailable to E2E remain explicit environment gaps rather than a replacement test strategy.

## Decision Log

| Status | Decision | Rationale |
| --- | --- | --- |
| Confirmed | No dynamic third-party accelerator plugins. | Avoid remote credential forwarding and an unbounded compatibility protocol. |
| Confirmed | One selected accelerator type per cluster. | Enables `DetectAll` once and directed capability lookup afterward. |
| Confirmed | Kubernetes detection uses core-collected observations. | Avoid exposing kubeconfigs or clients. |
| Confirmed | Endpoint plans receive cluster selection and capability. | Virtualization and product configuration are cluster-dependent. |
| Confirmed | Node-agent adapters are separate from control-plane planners. | Local metrics and inventory have different runtime dependencies. |
| Deferred | Heterogeneous multi-accelerator clusters. | Revisit with scheduling, status, and endpoint selection semantics. |
| Deferred | Exact DRA renderer model. | Preserve semantic virtualization intent; choose renderer when DRA is in scope. |
