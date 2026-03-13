# SSH Cluster Multi-Engine Version Support

## Background

### Problem Statement

In the current SSH Cluster architecture, all inference engines (vLLM, llama-cpp) are pre-installed in a monolithic cluster image `neutree-serve:{version}`. All Endpoints within the same cluster must use the same engine version. This causes several issues:

- Cannot run vLLM v0.11.2 and v0.12.0 simultaneously on the same cluster
- Engine upgrades require rebuilding the entire cluster image and restarting all nodes
- New engine versions are tightly coupled with cluster image releases

### User Stories

1. As a platform user, I want to run different versions of inference engines (e.g. vLLM v0.11.2 and v0.12.0) simultaneously on the same SSH cluster, so that different Endpoints can independently choose engine versions without affecting each other.
2. As a platform user, I want to run the latest vLLM engine version without waiting for the entire cluster image release cycle, so I can immediately use new engine features and performance improvements.
3. As a platform user, I want to update engine versions without rebuilding the cluster, because rebuilding a cluster disrupts running inference instances.
4. As a platform user, I want to quickly roll back to a previous engine version if issues are found after upgrading, without affecting other Endpoints on the cluster.

### Goals

1. Per-Endpoint engine container isolation, enabling different engine versions to run on the same Ray Cluster while maintaining backward compatibility with existing single-image deployments.
2. Support dynamic engine updates without cluster rebuilds, as rebuilding disrupts deployed inference instances.

---

## Current Architecture

### Deployment Flow

```
Endpoint creation -> EndpointToApplication() -> Ray Serve Application
  - ImportPath: "serve.vllm.v0_11_2.app:app_builder"  (pre-installed in cluster image)
  - RuntimeEnv: {env_vars only}
  - Args: {deployment_options, model, engine_args}
```

### Cluster Node Layout

```
Host Machine
  +-- Docker Container: ray_container (neutree-serve:{version})
        +-- Ray Head/Worker processes
        +-- Pre-installed engines (vLLM, llama-cpp)
        +-- serve/ Python modules (one app.py per engine version)
        +-- Model cache: /home/ray/.neutree/models-cache/
              mounted from host via: --volume {hostPath}:{mountPath}
```

### Key Constraints

1. **Ray + Python version**: Ray version must be identical across all nodes in a cluster; Python version defaults to patch-level match (e.g. 3.12.7), relaxable to minor-level match (e.g. 3.12.x) via `RAY_DEFAULT_PYTHON_VERSION_MATCH_LEVEL=minor`
2. **SSH Cluster nodes**: Run Ray inside Docker containers, configured with `--privileged` and `--net=host`
3. **`import_path`**: Points to Python modules that must be accessible in the execution environment

## Design

### Approach Selection

Ray 2.53.0 `runtime_env` provides two container isolation mechanisms, both using **Podman** upstream:

| Feature | `image_uri` | `container` |
|---------|-------------|-------------|
| Status | Recommended (experimental) | Deprecated (since 2025.07) |
| Runtime | Podman | Podman |
| GPU support | **Not available** ([#58399](https://github.com/ray-project/ray/issues/58399)) | Via `run_options` |
| `run_options` | Not supported | Supported (Docker CLI syntax) |

**Selected approach**: `runtime_env.container` -- the only option that supports GPU via `run_options`.

Upstream Ray hardcodes Podman as the container runtime. We extend `runtime_env.container` in the Neutree-maintained `ray-2.53.0-neutree` branch to directly support Docker, without needing Podman or symlinks.

### Why DOOD over DIND

Engine containers are created by the host's Docker daemon via the mounted Docker socket (`/var/run/docker.sock`), i.e. **Docker-outside-of-Docker (DOOD)** mode. Engine containers are **sibling containers** of the Ray container on the host, not nested.

An alternative is running an independent Docker daemon inside the Ray container, i.e. **Docker-in-Docker (DIND)**. DIND was rejected because:

1. DIND creates 3-layer nesting (host Docker -> Docker daemon inside Ray container -> Engine container), while DOOD has only 2 layers. The nested approach requires installing and managing an independent Docker daemon inside the Ray container, increasing image build and maintenance costs.
2. DIND requires the Ray container to run in `--privileged` mode to start the internal Docker daemon. While the current Ray container already uses `--privileged`, DOOD itself does not need this privilege.
3. The Docker daemon inside DIND needs additional nvidia-container-runtime configuration, while DOOD directly uses the host's existing Docker + NVIDIA runtime.

### Other Alternatives Considered

| Approach | Rejected Reason |
|----------|----------------|
| `runtime_env.uv` / `runtime_env.pip` | No prebuilt packages on some platforms; serve layer code not included in pip packages |
| `uv --target` site-packages bundle | Complex architecture requiring extra file server for distribution; no resource isolation; serve layer code sync needed separately |
| Multi-version pre-install | Image bloat (each vLLM ~2-5GB), CUDA library conflicts; does not support dynamic engine version updates |

### Target Architecture

```
Host Machine
  |
  +-- Docker daemon (with nvidia-container-runtime)
  |
  +-- ray_container (cluster base image: ubuntu:22.04 + Python 3.12 + Ray)
  |     +-- Ray Head/Worker processes
  |     +-- docker CLI (DOOD)
  |     +-- accelerator/ (GPU detection, system tools only)
  |     +-- /var/run/docker.sock (mounted from host)
  |
  +-- Engine Container A (Endpoint A, vLLM v0.11.2)
  |     +-- runtime_env.container:
  |     |     image: neutree/engine-vllm:v0.11.2-ray2.53.0
  |     |     run_options: [--runtime=nvidia, --gpus all, -v hostPath:mountPath]
  |     +-- Based on community image vllm/vllm-openai:v0.11.2 + Ray neutree wheel
  |     +-- Ray worker process (connected to Ray cluster)
  |     +-- serve/vllm/v0_11_2/app.py -> app_builder()
  |     +-- vLLM v0.11.2 + AsyncLLM engine
  |
  +-- Engine Container B (Endpoint B, vLLM v0.12.0)
        +-- runtime_env.container:
        |     image: neutree/engine-vllm:v0.12.0-ray2.53.0
        |     run_options: [--runtime=nvidia, --gpus all, -v hostPath:mountPath]
        +-- Based on community image vllm/vllm-openai:v0.12.0 + Ray neutree wheel
        +-- Ray worker process (connected to Ray cluster)
        +-- serve/vllm/v0_12_0/app.py -> app_builder()
        +-- vLLM v0.12.0 + AsyncLLM engine
```

Engine containers and Ray container are sibling containers on the host (DOOD mode), both created by the host's Docker daemon via the mounted docker.sock.

### Ray Serve Deployment Chain

Understanding where code executes is critical:

```
ServeController (head node, inside cluster image)
  |  Receives PUT /api/serve/applications/
  |  Does NOT import user code
  |
  +-- Submits build_serve_application Ray Task
     runtime_env = {container: {...}, env_vars: {...}}
                |
                v
     Ray Task (inside engine container)
       |  import_attr("serve.vllm.v0_12_0.app:app_builder")
       |  app_builder(args) -> returns Application DAG
       |  Serializes deployment configs -> returns to ServeController
                |
                v
     ServeController schedules replica actors
       |  Backend replicas (N, with GPU) -> inside Engine container (runtime_env.container)
       |  Controller replica (1, CPU only) -> also inside Engine container (no engine-specific startup params)
                |
                v
     Backend.__init__() -> downloads model + initializes vLLM AsyncLLM
     Controller.__init__(backend_handle) -> FastAPI routes ready
```

The `import_path` is resolved inside the Engine container, not in the cluster image. For containerized deployments, the cluster image no longer needs engine code.

**runtime_env split**: Both Backend and Controller replicas run inside the Engine container (`runtime_env.container`), but Backend is configured with engine-specific startup parameters (e.g., GPU `run_options: --gpus all`), while Controller does not have these parameters and only uses CPU. The split is implemented by generating different `runtime_env` for Backend and non-Backend deployments in `EndpointToApplication()`.

Reference: [`build_serve_application` in Ray source](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L1077), [`apply_app_config` submitting with runtime_env](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L651)

---

## Detailed Design

### Cluster Changes

#### Container Runtime Configuration

Static node clusters need a container runtime configuration to specify how Engine containers are created.

Currently only Docker is supported; Podman etc. can be extended later.

New `container_runtime` field in SSH cluster config:

```go
type RaySSHProvisionClusterConfig struct {
    // ... existing fields ...
    ContainerRuntime string `json:"container_runtime"` // "docker" (default)
}
```

Impact:
- Container CLI used by `runtime_env.container` in Ray fork
- `run_options` format for Engine containers (Docker and Podman have some parameter differences)
- Does not affect the Ray container itself (managed by host Docker daemon)

#### Cluster Image Changes

The cluster image is minimized to a lean Ray image without pre-installed engines like vLLM. Engine code runs entirely inside Engine containers.

Base image: `ubuntu:22.04` (shared by NVIDIA and ROCm)

Python version: 3.12.12 (compiled from source, latest security release in 3.12 series, fixes CVE-2025-59375 etc.)

GPU detection does not depend on engine packages (vLLM, torch, etc.). The two-layer detection chain uses only system-level components:

| Detection Layer | NVIDIA | AMD ROCm |
|----------------|--------|----------|
| Ray detection (`num_gpus`) | vendored pynvml -> ctypes -> `libnvidia-ml.so.1` | vendored pyamdsmi -> ctypes -> `librocm_smi64.so` |
| Neutree detection (AcceleratorType Count) | subprocess -> `nvidia-smi` | subprocess -> `rocminfo` |
| Ray monitoring (Dashboard metrics) | vendored pynvml -> `libnvidia-ml.so.1` | vendored pyamdsmi -> `librocm_smi64.so` |

Accelerator differences:

| | NVIDIA | AMD ROCm |
|---|---|---|
| Driver library source | `--runtime=nvidia` automatically injects from host | Must be installed in image (ROCm userspace libs not auto-injected) |
| Additional packages | None | `rocm-smi-lib` + `rocminfo` (~tens of MB) |
| ROCm version compatibility | N/A | Must be within host amdgpu driver compatibility window (~1 year); recommended to match Engine image ROCm version |

Core components:

```
ubuntu:22.04
+-- Python 3.12.12 (compiled from source)
+-- Ray neutree fork wheel
+-- docker.io + podman (Engine container DOOD, currently Docker, Podman reserved)
+-- util-linux, nfs-common (cluster infrastructure)
+-- start.py + accelerator/ (GPU detection + Ray startup)
+-- [ROCm only] rocm-smi-lib + rocminfo
+-- Done (no vLLM, torch, llama-cpp or other engine packages)
```

Environment variables:
- `RAY_DEFAULT_PYTHON_VERSION_MATCH_LEVEL=minor` -- relax Python patch version check, allowing cluster image (3.12.12) and Engine image (3.12.x) to differ in patch version

#### Ray Container Engine Isolation Configuration

The Ray container needs additional Docker run_options beyond the existing `--privileged` and `--net=host`:

| Option | Purpose |
|--------|---------|
| `--volume /var/run/docker.sock:/var/run/docker.sock` | Allow Ray to create Engine containers on host via Docker socket |
| `--volume /tmp:/tmp` | Ray writes tmp files (e.g. container env setup scripts) that must be visible to the host Docker daemon since Engine containers are host-level siblings |
| `--pid=host` | Engine containers need to see Raylet process (parent PID) for process lifecycle management |
| `--ipc=host` | Shared memory for Ray Object Store communication between Ray container and Engine containers |

The serve container runs as root, which already has access to the Docker socket. No additional permission changes are needed.

These options are set in `generateRayClusterConfig()`:

```go
rayClusterConfig.Docker.RunOptions = []string{
    // ... common options ...
    "-e RAY_EXPERIMENTAL_RUNTIME_ENV_CONTAINER_RUNTIME=docker",
    "--volume /var/run/docker.sock:/var/run/docker.sock",
    "--volume /tmp:/tmp",
    "--pid=host",
    "--ipc=host",
}
```

### Engine Changes

#### Engine Images

Build approach: layer on top of community engine images, installing Ray neutree fork wheel + Neutree serve layer code. No need to recompile engines from source.

Version constraints:

| Dimension | Requirement | Notes |
|-----------|-------------|-------|
| Ray version | Must match | Same version neutree fork wheel |
| Python minor | Must match | Cluster image 3.12.x, Engine image must also be 3.12.x |
| Python patch | May differ | Relaxed via `RAY_DEFAULT_PYTHON_VERSION_MATCH_LEVEL=minor` |

Current mainstream inference engine community images all use Python 3.12 (vLLM, SGLang, etc.), matching the cluster image's Python 3.12.12 at the minor version level.

Naming convention: `{registry}/neutree/engine-{engine_name}:{engine_version}-ray{ray_version}`

Example: `registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0`

#### Engine Image Registration

SSH clusters and K8s clusters share the same set of engine images. The `EngineVersion.Images` map uses the same keys (e.g. `nvidia_gpu`, `amd_gpu`) without distinguishing cluster types.

Engine images are built on top of community engine images, adding Ray neutree fork wheel + Neutree serve layer code. These images work on both K8s and SSH clusters: on K8s, the Ray components do not affect engine operation; on SSH, the Ray worker process starts inside the Engine container and connects to the Ray cluster.

Configuration example:

```go
// gpu.go -- v0.12.0 registration
{
    Version:      "v0.12.0",
    ValuesSchema: vllmDefaultEngineSchema,
    Images: map[string]*v1.EngineImage{
        "nvidia_gpu": {
            ImageName: "neutree/engine-vllm",
            Tag:       "v0.12.0-ray2.53.0",
        },
    },
}
```

#### Engine Image Upload

Shares the same `neutree-cli engine import` flow as K8s clusters:

```
neutree-cli engine import --package engine-vllm-v0.12.0.tar.gz
```

Engine package structure:

```
engine-vllm-v0.12.0.tar.gz
+-- manifest.yaml          # Engine metadata, version, image specs
+-- images/
    +-- engine-vllm-v0.12.0-ray2.53.0.tar  # Docker image tar file
```

Import flow:

1. Extract engine package, read `manifest.yaml`
2. Load image tar via Docker client: `docker.ImageLoad()`
3. Re-tag based on target Image Registry: `docker.ImageTag(sourceImage, targetImage)`
4. Push to image registry: `docker.ImagePush()`
5. Create/update Engine and EngineVersion records via API

SSH and K8s share the same engine images, upload pipeline, and Image Registry configuration.

### Endpoint Changes

#### Engine Image Lookup

Lookup flow is consistent with K8s, using accelerator type directly as the key:

```
1. cluster.Status.AcceleratorType -> "nvidia_gpu"
2. engineVersion.GetImageForAccelerator("nvidia_gpu")
3. Found -> generate runtime_env.container config
4. Not found -> fail
```

#### Engine Container Configuration

Configuration flow:

```
1. Check SSH cluster type
2. Get accelerator type from cluster.Status.AcceleratorType
3. Lookup engine image: engineVersion.GetImageForAccelerator(acceleratorType)
4. Not found -> return error
5. Build image ref: {imageRegistry}/{imageName}:{tag}
6. Get accelerator run_options from acceleratorMgr.GetEngineContainerRunOptions(acceleratorType)
7. Append model cache volume mounts (host paths)
8. Return {"image": imageRef, "run_options": runOptions}
```

#### Accelerator-Specific Run Options

Accelerator-specific Docker flags are provided by the accelerator plugin system, not hardcoded in the orchestrator.

**File**: `internal/accelerator/plugin/plugin.go` -- `AcceleratorPluginHandle` interface adds:

```go
// GetAcceleratorRuntimeConfig returns the static RuntimeConfig for this accelerator type.
// Unlike GetNodeRuntimeConfig, this does NOT require SSH access to a node.
GetAcceleratorRuntimeConfig() v1.RuntimeConfig
```

**File**: `internal/accelerator/manager.go` -- `Manager` interface adds:

```go
// GetEngineContainerRunOptions returns Docker run_options for engine containers.
// Delegates to the registered plugin's GetContainerRuntimeConfig() and converts
// RuntimeConfig fields (Runtime, Options, Env) to Docker CLI flags.
GetEngineContainerRunOptions(acceleratorType string) ([]string, error)
```

The Manager converts `RuntimeConfig` to Docker run_options:
- `Runtime: "nvidia"` -> `--runtime=nvidia`
- `Options: ["--gpus", "all"]` -> `--gpus all`
- `Env: {"KEY": "val"}` -> `-e KEY=val`

| Accelerator | Plugin RuntimeConfig | Resulting run_options |
|-------------|---------------------|----------------------|
| NVIDIA GPU | `Runtime: "nvidia", Options: ["--gpus", "all"]` | `--runtime=nvidia --gpus all` |
| AMD GPU | `Runtime: "amd", Env: {"AMD_VISIBLE_DEVICES": "all"}` | `--runtime=amd -e AMD_VISIBLE_DEVICES=all` |

#### Volume Mount Path Considerations

In DOOD mode, path handling requires attention. Engine containers created via docker.sock are managed by the host's Docker daemon, so volume mounts reference host paths:

```
Host: /data/models -> (docker -v) -> Ray container: /home/ray/.neutree/models-cache/default
                   \-> (docker -v) -> Engine container: /home/ray/.neutree/models-cache/default

Both Ray container and Engine containers mount the same host path.
The Engine container sees the same model files at the same internal path.
```

`ModelCache.HostPath.Path` (e.g. `/data/models`) is the host path. The internal container mount path (`/home/ray/.neutree/models-cache/{name}`) is what the Python code expects. This mapping is already available in the `ClusterConfig.ModelCaches` struct.

#### NFS Model Registry Adaptation

**Problem**: BentoML NFS-type Model Registries were previously mounted inside the Ray container via `DockerNfsMounter` running `sudo mount -t nfs`. Engine containers are sibling containers on the host, isolated from the Ray container's filesystem, and cannot see NFS mounts inside the Ray container.

**Solution**: Use Docker `--mount` NFS volume options so the Docker daemon mounts NFS directly when creating the Engine container.

**Version branching**: The `isNewClusterVersion()` helper function provides unified version checking. Version branching is done at the call sites:

| Call Site | <= v1.0.0 (old cluster) | > v1.0.0 (new cluster) |
|-----------|-------------------------|------------------------|
| `CreateEndpoint()` | Call `connectSSHClusterEndpointModel(connect)` -- NFS mount inside ray_container | Skip (NFS handled by Engine container run_options) |
| `DeleteEndpoint()` | Call `connectSSHClusterEndpointModel(disconnect)` -- NFS unmount inside ray_container | Skip |
| `EndpointToApplication()` | Skip `buildEngineContainerConfig()` | Call `buildEngineContainerConfig()` to build Engine container config |

This ensures old clusters never trigger Engine container logic, and new clusters never trigger NFS mount/unmount inside ray_container, also avoiding unnecessary SSH connections and Ray node list queries.

**NFS protocol version detection**: Docker NFS volume mount is strict about protocol version (`volume-opt=type=nfs` vs `volume-opt=type=nfs4`) -- a mismatch causes container startup failure. The control plane already mounts NFS during `ModelRegistryController.Connect()` via `nfs.MountNFS()`, and the kernel-negotiated protocol version is recorded in `/proc/mounts`. Reading `MountPoint.Type` via `nfs.GetMountType()` provides the protocol version at zero extra cost.

**Data flow**:

```
CreateEndpoint() / DeleteEndpoint()
  +-- isNewClusterVersion(cluster)
       +-- false (<=v1.0.0): connectSSHClusterEndpointModel() -- NFS mount inside ray_container
       +-- true  (> v1.0.0): skip

EndpointToApplication()
  +-- isNewClusterVersion(deployedCluster)
       +-- false (<=v1.0.0): skip buildEngineContainerConfig()
       +-- true  (> v1.0.0): buildEngineContainerConfig(..., modelRegistry)
            +-- Detect BentoML NFS model registry
            +-- url.Parse(modelRegistry.Spec.Url) -> extract server/path
            +-- model_registry.NewModelRegistry() -> registry.GetNFSType() -> "nfs4"
            +-- run_options += --mount type=volume,
                 dst=/mnt/{workspace}/{endpoint},
                 volume-opt=type=nfs4,
                 volume-opt=o=addr={server},
                 volume-opt=device=:{path}
```

Example generated run_options:

```
--mount type=volume,dst=/mnt/default/llama-endpoint,volume-opt=type=nfs4,volume-opt=o=addr=10.255.1.54,volume-opt=device=:/bentoml
```

---

## Ray Fork Considerations

**Repository**: `neutree-ai/ray` (`ray-2.53.0-neutree` branch)

### `container` Field Maintenance

The `runtime_env.container` field is deprecated in upstream Ray but is the only option supporting GPU via `run_options`. The Neutree Ray fork must:

1. Retain `container` field validation and execution logic
2. Optionally suppress deprecation warnings
3. Ensure `run_options` are passed through to the container CLI

### Docker Backend Extension

Upstream Ray's `runtime_env.container` only supports Podman. The Neutree Ray fork needs to extend the container backend to add Docker support:

- Add a Docker backend in `ray/_private/runtime_env/container.py`
- Use standard Docker CLI commands: `docker run` / `docker inspect` / `docker rm`
- Select backend via cluster config's `container_runtime` field (currently defaults to Docker)

Behaviors to test:
- `--rm` behavior on container exit
- Signal propagation to the worker process
- Container cleanup on Ray worker crash

---

## Observability Adaptation

### Current State

Current Grafana dashboards are based on vLLM v0.8.5 metrics. vLLM adds, renames, or removes Prometheus metrics across version iterations -- different versions expose different metric sets.

The metrics exposure path is unchanged after containerization: Ray actors inside Engine containers register metrics with Ray, which exposes them through Ray node metric ports (Raylet 54311, Dashboard 44227). vmagent scrapes as before.

### Metrics Changes (v0.8.5 -> v0.11.2)

#### Renamed / Replaced Metrics

| v0.8.5 Metric | v0.11.2 Metric | Notes |
|---|---|---|
| `vllm:time_per_output_token_seconds` | `vllm:inter_token_latency_seconds` | Old name deprecated in v0.11.2, will be removed in v0.15.0 |
| `vllm:gpu_cache_usage_perc` | `vllm:kv_cache_usage_perc` | Semantics: GPU cache -> KV cache; old name deprecated |
| `vllm:gpu_prefix_cache_queries` | `vllm:prefix_cache_queries` | Dropped `gpu_` prefix; old name deprecated |
| `vllm:gpu_prefix_cache_hits` | `vllm:prefix_cache_hits` | Same as above |

#### New Metrics in v0.11.2

| Metric | Type | Description |
|---|---|---|
| `vllm:inter_token_latency_seconds` | Histogram | Replaces `time_per_output_token_seconds` |
| `vllm:request_time_per_output_token_seconds` | Histogram | Per-request TPOT |
| `vllm:kv_cache_usage_perc` | Gauge | Replaces `gpu_cache_usage_perc` |
| `vllm:engine_sleep_state` | Gauge | Engine sleep state |
| `vllm:corrupted_requests` | Counter | Corrupted request count |
| `vllm:external_prefix_cache_queries/hits` | Counter | External prefix cache |
| `vllm:mm_cache_queries/hits` | Counter | Multimodal cache |

#### Removed in v0.11.2

| Metric | Type | Reason |
|---|---|---|
| `vllm:num_requests_swapped` | Gauge | v1 engine has no swap mechanism |
| `vllm:cpu_cache_usage_perc` | Gauge | v1 engine has no CPU cache |
| `vllm:cpu_prefix_cache_hit_rate` | Gauge | Replaced by queries/hits counters |
| `vllm:gpu_prefix_cache_hit_rate` | Gauge | Same as above |
| `vllm:spec_decode_*` | Gauge/Counter | v1 speculative decoding differs |
| `vllm:model_forward_time_milliseconds` | Histogram | v1 engine no longer exposes |
| `vllm:model_execute_time_milliseconds` | Histogram | Same as above |

> Counter `_total` suffix changes (e.g., `prompt_tokens_total` -> `prompt_tokens`) have no practical impact -- Prometheus client library auto-appends `_total` for counters.

### Adaptation Plan: Single Dashboard + PromQL `or` + engine_version Label

**Do not split into per-version dashboards**, because:

- Core metrics (latency, throughput, queue depth, cache) are stable across versions, covering 80%+ of panels
- Comparing multiple Endpoints on the same cluster is the typical use case; split dashboards require switching back and forth
- Each new engine version would add another dashboard, linear maintenance cost

#### Inject engine_version Label

Add an `engine_version` label to vLLM metrics, enabling Grafana filtering and grouping by engine version.

**v0.11.2+** (`NeutreeRayStatLogger`):

```python
extra_labels = {
    "deployment": ctx.deployment,
    "replica": ctx.replica_tag,
    "engine_version": os.environ.get("ENGINE_VERSION", "unknown"),
}
```

**v0.8.5** (`_SanitizedRayStatLogger` labels dict):

```python
labels = {
    "deployment": ctx.deployment,
    "replica": ctx.replica_tag,
    "model_name": self.engine.engine.model_config.served_model_name,
    "engine_version": os.environ.get("ENGINE_VERSION", "unknown"),
}
```

`ENGINE_VERSION` env var (e.g., `vllm_v0.11.2`) is injected via `runtime_env.env_vars` in `EndpointToApplication()`.

Add Grafana variable `EngineVersion`:

```
label_values(engine_version)
```

Users can filter panel data by engine version, or group within the same panel for cross-version comparison.

#### PromQL `or` for Renamed Metrics

**Use PromQL `or` for renamed metrics** (new name on left, old name on right):

- `or` semantics: left operand takes priority; when both sides have matching labels, only left is kept
- v0.11.2 exposes both deprecated and new metrics -- new metric (left) takes priority
- v0.8.5 only has the old metric -- falls through to right side
- When v0.8.5 is retired, just remove the `or` clause

#### Panels Requiring PromQL Changes

**1. `time_per_output_token_seconds` -> `inter_token_latency_seconds`** (~3-4 panels)

```promql
-- Before
histogram_quantile(0.95, rate(vllm:time_per_output_token_seconds_bucket{...}[5m]))

-- After
histogram_quantile(0.95,
  rate(vllm:inter_token_latency_seconds_bucket{...}[5m])
  or
  rate(vllm:time_per_output_token_seconds_bucket{...}[5m])
)
```

**2. `gpu_cache_usage_perc` -> `kv_cache_usage_perc`** (1 panel)

```promql
-- Before
vllm:gpu_cache_usage_perc{...}

-- After
vllm:kv_cache_usage_perc{...} or vllm:gpu_cache_usage_perc{...}
```

**3. Removed metrics** (no PromQL changes needed)

`num_requests_swapped`, `cpu_cache_usage_perc` -- panels naturally show no data on v1 engine; add explanation in panel description.

#### Impact Scope

Of 23 metrics in the dashboard, only 2 metrics (~5 panels) need PromQL updates. The rest remain unchanged.

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| `container` field removed from upstream Ray | Fork must self-maintain | Pin behavior in fork; track `image_uri` GPU fix ([PR #60485](https://github.com/ray-project/ray/pull/60485)) |
| docker.sock security (host Docker access) | Container escape risk | New clusters do not use `--privileged`; ray user accesses docker.sock via docker group membership only |
| Engine image pull latency (first deploy) | Slow Endpoint startup | Pre-pull engine images on nodes; add image pull progress to Endpoint status |

## References

### Ray

- [Ray Runtime Environments](https://docs.ray.io/en/releases-2.53.0/ray-core/handling-dependencies.html#runtime-environments) -- runtime_env official docs
- [runtime_env.container (Deprecated)](https://docs.ray.io/en/releases-2.53.0/ray-core/handling-dependencies.html#container-option-deprecated) -- container field, deprecated since 2025.07
- [runtime_env.image_uri (Experimental)](https://docs.ray.io/en/releases-2.53.0/ray-core/handling-dependencies.html#image-uri) -- image_uri field
- [ray-project/ray#58399](https://github.com/ray-project/ray/issues/58399) -- image_uri GPU not supported issue
- [ray-project/ray#60485](https://github.com/ray-project/ray/pull/60485) -- image_uri GPU support fix PR
- [`build_serve_application` source](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L1077) -- Ray Serve application build entry, runtime_env passed here
- [`apply_app_config` source](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L651) -- Submitting app config with runtime_env
- [`container.py` source](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/_private/runtime_env/container.py) -- runtime_env container plugin implementation (Podman backend)

### Docker

- [Docker Volume -- NFS](https://docs.docker.com/engine/storage/volumes/#create-a-service-which-creates-an-nfs-volume) -- `--mount type=volume,volume-opt=type=nfs` usage
- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/overview.html) -- `--runtime=nvidia` and `--gpus` flags

### Neutree

- [neutree-ai/ray](https://github.com/neutree-ai/ray) -- Neutree-maintained Ray fork (`ray-2.53.0-neutree` branch)
