# SSH Cluster Multi-Engine Version Support

## 1. Background

### 1.1 Problem Statement

In the current SSH Cluster architecture, all inference engines (vLLM, llama-cpp) are pre-installed in a monolithic cluster image `neutree-serve:{version}`. All Endpoints within the same cluster must use the same engine version. This limits:

- **Engine version isolation**: Cannot run vLLM v0.11.2 and v0.12.0 simultaneously on the same cluster
- **Upgrade flexibility**: Engine upgrades require rebuilding the entire cluster image and restarting all nodes
- **Engine iteration speed**: New engine versions are tightly coupled with cluster image releases

### 1.2 Goal

Support running different versions of engines on the same Ray Cluster via per-Endpoint engine container isolation, while maintaining backward compatibility with existing single-image deployments.

---

## 2. Current Architecture

### 2.1 Deployment Flow

```
Endpoint creation → EndpointToApplication() → Ray Serve Application
  - ImportPath: "serve.vllm.v0_11_2.app:app_builder"  (pre-installed in cluster image)
  - RuntimeEnv: {env_vars only}
  - Args: {deployment_options, model, engine_args}
```

### 2.2 Cluster Node Layout

```
Host Machine
  └── Docker Container: ray_container (neutree-serve:{version})
        ├── Ray Head/Worker processes
        ├── Pre-installed engines (vLLM, llama-cpp)
        ├── serve/ Python modules (app.py per engine version)
        └── Model cache: /home/ray/.neutree/models-cache/
              mounted from host via: --volume {hostPath}:{mountPath}
```

### 2.3 Key Constraints

1. **Ray + Python version**: Must be identical across all nodes in a cluster (including patch version)
2. **SSH Cluster nodes**: Run Ray inside Docker containers, configured with `--privileged` and `--net=host`
3. **`import_path`**: Points to Python modules that must be accessible in the execution environment

### 2.4 Key Files

| File | Role |
|------|------|
| `cluster-image-builder/Dockerfile` | Monolithic cluster image |
| `cluster-image-builder/serve/vllm/v0_11_2/app.py` | vLLM app_builder implementation |
| `internal/orchestrator/ray_orchestrator.go` | Endpoint → RayServeApplication conversion |
| `internal/cluster/ray_ssh_operation.go` | Ray cluster node Docker configuration |
| `api/v1/engine_types.go` | EngineVersion type definition |

---

## 3. Design

### 3.1 Approach Selection

Ray 2.53.0 `runtime_env` provides two container isolation mechanisms. Both use **Podman** internally:

| Feature | `image_uri` | `container` |
|---------|-------------|-------------|
| Status | Recommended (experimental) | Deprecated (since 2025.07) |
| Runtime | Podman | Podman |
| GPU support | **Not available** ([#58399](https://github.com/ray-project/ray/issues/58399)) | Via `run_options` |
| `run_options` | Not supported | Supported (Docker CLI syntax) |

**Selected approach**: `runtime_env.container` — the only option that supports GPU via `run_options`.

Since Ray internally calls `podman` CLI, and we use Docker on the host, we symlink `docker` as `podman` inside the cluster image and mount the host's Docker socket. This "Docker-as-Podman" approach avoids installing Podman and leverages the existing Docker + NVIDIA runtime infrastructure.

### 3.2 Why Docker over Podman

Ray hardcodes Podman as the container runtime for `runtime_env.container`. We chose Docker (via `podman` → `docker` symlink + host docker.sock) over native Podman for two reasons:

1. **Podman-in-Docker adds architectural complexity**: Running Podman inside the Ray Docker container creates 3-layer nesting (Host → Docker → Podman) versus Docker-outside-of-Docker's 2-layer sibling model (Host → Docker for both Ray and Engine containers). The nested approach requires installing Podman plus additional CDI or runtime plugins inside the Ray container, complicating image builds, debugging, and maintenance.

2. **Podman has limited accelerator compatibility**: Podman relies on CDI (Container Device Interface) for GPU access, which has limited community adoption and testing.

### 3.3 Other Alternatives Considered

| Approach | Rejected Reason |
|----------|----------------|
| `runtime_env.uv` / `runtime_env.pip` | AMD ROCm has no prebuilt wheels (10-30 min compile); serve layer code not included in pip packages; Ray fork version conflicts |
| `uv --target` site-packages bundle | Complex architecture requiring extra file server for distribution; no resource isolation; serve layer code sync needed separately |
| Multi-version pre-install | Image bloat (each vLLM ~2-5GB), CUDA library conflicts; does not support dynamic engine version updates |

### 3.3 Target Architecture

```
Host Machine
  │
  ├── Docker daemon (with nvidia-container-runtime)
  │
  ├── ray_container (cluster base image, no engine pre-installed for new versions)
  │     ├── Ray Head/Worker processes
  │     ├── docker CLI (symlinked as podman)
  │     └── /var/run/docker.sock (mounted from host)
  │
  ├── Engine Container A (Endpoint A, vLLM v0.11.2)
  │     ├── runtime_env.container:
  │     │     image: neutree/engine-vllm:v0.11.2-ray2.53.0
  │     │     run_options: [--runtime=nvidia, --network host, -v hostPath:mountPath]
  │     ├── Ray worker process (connected to Ray cluster)
  │     ├── serve/vllm/v0_11_2/app.py → app_builder()
  │     └── vLLM v0.11.2 + AsyncLLM engine
  │
  └── Engine Container B (Endpoint B, vLLM v0.12.0)
        ├── runtime_env.container:
        │     image: neutree/engine-vllm:v0.12.0-ray2.53.0
        │     run_options: [--runtime=nvidia, --network host, -v hostPath:mountPath]
        ├── Ray worker process (connected to Ray cluster)
        ├── serve/vllm/v0_12_0/app.py → app_builder()
        └── vLLM v0.12.0 + AsyncLLM engine
```

Engine containers are **siblings** of the Ray container on the host (not nested). They are created by the host's Docker daemon via the mounted docker.sock.

### 3.4 Ray Serve Deployment Chain

Understanding where code executes is critical:

```
ServeController (head node, cluster image)
  │  Receives PUT /api/serve/applications/
  │  Does NOT import user code
  │
  ╰─ Submits build_serve_application Ray Task
     with runtime_env = {container: {...}, env_vars: {...}}
                │
                ▼
     Ray Task (inside engine container)
       │  import_attr("serve.vllm.v0_12_0.app:app_builder")
       │  app_builder(args) → returns Application DAG
       │  Serializes deployment configs → returns to ServeController
                │
                ▼
     ServeController schedules replica actors
       │  Backend replicas (N, with GPU) → inside engine container
       │  Controller replica (1, CPU only) → inside engine container
                │
                ▼
     Backend.__init__() → downloads model + initializes vLLM AsyncLLM
     Controller.__init__(backend_handle) → FastAPI routes ready
```

Key: The `import_path` is resolved **inside the engine container**, not in the cluster image. The cluster image does not need the engine code for containerized deployments.

Reference: [`build_serve_application` in Ray source](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L1077), [`apply_app_config` submitting with runtime_env](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L651)

---

## 4. Detailed Design

### 4.1 Cluster Image Changes

**File**: `cluster-image-builder/Dockerfile`

Install Docker CLI and create a `podman` symlink (Ray calls `podman` internally):

```dockerfile
USER root
RUN apt-get update && apt-get install -y docker.io \
    && ln -sf /usr/bin/docker /usr/bin/podman
USER ray
```

No Podman, no CDI generation, no nvidia-container-toolkit changes.

### 4.2 Ray Container Configuration for Engine Isolation

**File**: `internal/cluster/ray_ssh_operation.go`

For engine container support, the Ray container needs additional Docker run_options beyond the existing `--privileged` and `--net=host`:

| Option | Purpose |
|--------|---------|
| `--volume /var/run/docker.sock:/var/run/docker.sock` | Allow Ray to create engine containers on host via Docker socket |
| `--volume /tmp:/tmp` | Ray writes tmp files (e.g., container env setup scripts) that must be visible to the host Docker daemon since engine containers are host-level siblings |
| `--pid=host` | Engine containers need to see Raylet process (parent PID) for process lifecycle management |
| `--ipc=host` | Shared memory for Ray Object Store communication between Ray container and engine containers |

Additionally, a startup command is prepended to each node's start command:

```
sudo chmod 666 /var/run/docker.sock
```

This grants the `ray` user (non-root inside the container) access to the Docker socket.

These options are set in `generateRayClusterConfig()`:

```go
rayClusterConfig.Docker.RunOptions = []string{
    "--privileged",
    // ... existing options ...
    "--volume /var/run/docker.sock:/var/run/docker.sock",
    "--volume /tmp:/tmp",
    "--pid=host",
    "--ipc=host",
}
```

### 4.3 Engine Images

**New file**: `cluster-image-builder/Dockerfile.engine-vllm`

Each engine version is built as an independent image. Requirements:
- **Same Ray version + Python version** as the cluster base image
- Specific engine version
- Contains `serve/` directory (app.py modules + shared components)

```dockerfile
FROM <same-ray-base-as-cluster>

# Install specific vLLM version
RUN pip install vllm==0.12.0

# Copy serve modules
COPY serve ./serve
COPY downloader ./downloader
COPY *.py ./
```

**Naming convention**: `{registry}/neutree/engine-{engine_name}:{engine_version}-ray{ray_version}`

Example: `registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0`

### 4.4 Engine Image Lookup (SSH Key Prefix)

**File**: `api/v1/engine_types.go`

Reuse the existing `EngineVersion.Images` map with an SSH-specific key prefix, consistent with the K8s engine image lookup mechanism (`kubernetes_orchestrator_resource.go:getImageForAccelerator()`).

No new field is needed — SSH cluster engine images are stored in the same `Images` map with a `ssh_` key prefix.

**Key convention:**

| Key Pattern | Usage | Example |
|-------------|-------|---------|
| `nvidia_gpu` | K8s engine image | `EngineImage{ImageName: "vllm/vllm-openai", Tag: "v0.11.2"}` |
| `ssh_nvidia_gpu` | SSH cluster engine container | `EngineImage{ImageName: "neutree/engine-vllm", Tag: "v0.12.0-ray2.53.0"}` |
| `ssh_amd_gpu` | SSH cluster engine container (AMD) | `EngineImage{ImageName: "neutree/engine-vllm-rocm", Tag: "v0.12.0-ray2.53.0"}` |

New constant and helper:

```go
const SSHEngineImageKeyPrefix = "ssh_"

func GetSSHImageKey(acceleratorType string) string {
    return SSHEngineImageKeyPrefix + acceleratorType
}
```

**Lookup flow:**

```
1. cluster.Status.AcceleratorType → "nvidia_gpu"
2. GetSSHImageKey("nvidia_gpu") → "ssh_nvidia_gpu"
3. engineVersion.GetImageForAccelerator("ssh_nvidia_gpu")
4. Found → generate runtime_env.container config
5. Not found → no container config, use pre-installed engine (backward compatible)
```

**Example configuration:**

```go
// gpu.go — v0.12.0 registration
{
    Version:      "v0.12.0",
    ValuesSchema: vllmDefaultEngineSchema,
    Images: map[string]*v1.EngineImage{
        "ssh_nvidia_gpu": {
            ImageName: "neutree/engine-vllm",
            Tag:       "v0.12.0-ray2.53.0",
        },
    },
}
```

**Backward compatibility:** Engine versions without `ssh_*` keys in their `Images` map (e.g., v0.11.2) will naturally fall through to the pre-installed engine path — `GetImageForAccelerator("ssh_nvidia_gpu")` returns nil, so no `runtime_env.container` is generated.

### 4.5 Engine Container Configuration

**File**: `internal/orchestrator/ray_orchestrator.go`

#### 4.5.1 Function Signature Changes

`EndpointToApplication()` adds `engine *v1.Engine` parameter:

```go
func EndpointToApplication(endpoint *v1.Endpoint, deployedCluster *v1.Cluster,
    modelRegistry *v1.ModelRegistry, engine *v1.Engine,
    imageRegistry *v1.ImageRegistry,
    acceleratorMgr accelerator.Manager) (dashboard.RayServeApplication, error)
```

`buildEngineContainerConfig()` accepts engine, imageRegistry, and acceleratorMgr:

```go
func buildEngineContainerConfig(endpoint *v1.Endpoint, cluster *v1.Cluster,
    engine *v1.Engine, imageRegistry *v1.ImageRegistry,
    acceleratorMgr accelerator.Manager,
    modelCaches []v1.ModelCache) map[string]interface{}
```

#### 4.5.2 Engine Container Config Flow

```
1. Check SSH cluster type → non-SSH returns nil
2. Get cluster accelerator type from cluster.Status.AcceleratorType
3. Lookup SSH engine image: engineVersion.GetImageForAccelerator("ssh_" + acceleratorType)
4. If not found → return nil (backward compatible, use pre-installed engine)
5. Build image ref: {imageRegistry}/{imageName}:{tag}
6. Get accelerator run_options from acceleratorMgr.GetEngineContainerRunOptions(acceleratorType)
7. Append model cache volume mounts (host paths)
8. Return {"image": imageRef, "run_options": runOptions}
```

#### 4.5.3 Accelerator-Specific Run Options

Accelerator-specific Docker flags are provided by the accelerator plugin system, not hardcoded in the orchestrator.

**File**: `internal/accelerator/plugin/plugin.go` — `AcceleratorPluginHandle` interface adds:

```go
// GetAcceleratorRuntimeConfig returns the static RuntimeConfig for this accelerator type.
// Unlike GetNodeRuntimeConfig, this does NOT require SSH access to a node.
GetAcceleratorRuntimeConfig() v1.RuntimeConfig
```

**File**: `internal/accelerator/manager.go` — `Manager` interface adds:

```go
// GetEngineContainerRunOptions returns Docker run_options for engine containers.
// Delegates to the registered plugin's GetAcceleratorRuntimeConfig() and converts
// RuntimeConfig fields (Runtime, Options, Env) to Docker CLI flags.
GetEngineContainerRunOptions(acceleratorType string) ([]string, error)
```

The Manager implementation converts `RuntimeConfig` to Docker run_options:
- `Runtime: "nvidia"` → `--runtime=nvidia`
- `Options: ["--gpus", "all"]` → `--gpus all`
- `Env: {"KEY": "val"}` → `-e KEY=val`

| Accelerator | Plugin RuntimeConfig | Resulting run_options |
|-------------|---------------------|----------------------|
| NVIDIA GPU | `Runtime: "nvidia", Options: ["--gpus", "all"]` | `--runtime=nvidia --gpus all` |
| AMD GPU | `Runtime: "amd", Env: {"AMD_VISIBLE_DEVICES": "all"}, Options: ["--device=/dev/kfd", "--device=/dev/dri", "--group-add", "video"]` | `--device=/dev/kfd --device=/dev/dri --group-add video -e AMD_VISIBLE_DEVICES=all` |

#### 4.5.4 Volume Mount Path Consideration

This is the critical difference from Podman-in-Docker. With docker.sock, engine containers are created by the **host's Docker daemon**, so volume mounts reference **host paths**:

```
Host: /data/models → (docker -v) → Ray container: /home/ray/.neutree/models-cache/default
                  ↘ (docker -v) → Engine container: /home/ray/.neutree/models-cache/default

Both ray_container and engine containers mount the same host path.
The engine container sees the same model files at the same internal path.
```

The `ModelCache.HostPath.Path` (e.g., `/data/models`) is the host path. The internal container mount path (`/home/ray/.neutree/models-cache/{name}`) is what the Python code expects. This mapping is already available in the `ClusterConfig.ModelCaches` struct.

#### 4.5.5 NFS Model Registry Adaptation

**Problem**: BentoML NFS-type Model Registries were previously mounted inside the Ray container via `DockerNfsMounter` running `sudo mount -t nfs`. Engine containers are sibling containers on the host created via docker.sock, completely isolated from the Ray container's filesystem, and cannot see NFS mounts inside the Ray container.

**Solution**: Use Docker `--mount` NFS volume options so the Docker daemon mounts NFS directly when creating the Engine container.

**Version branching**: The `isNewClusterVersion()` helper function provides unified version checking. Version branching is done at the call sites, not internally:

| Call Site | <= v1.0.0 (old cluster) | > v1.0.0 (new cluster) |
|-----------|-------------------------|------------------------|
| `CreateEndpoint()` | Call `connectSSHClusterEndpointModel(connect)` — NFS mount inside ray_container | Skip (NFS handled by Engine container run_options) |
| `DeleteEndpoint()` | Call `connectSSHClusterEndpointModel(disconnect)` — NFS unmount inside ray_container | Skip |
| `EndpointToApplication()` | Skip `buildEngineContainerConfig()` | Call `buildEngineContainerConfig()` to build Engine container config |

This ensures old clusters never trigger Engine container logic, and new clusters never trigger NFS mount/unmount inside ray_container, avoiding unnecessary SSH connections and Ray node list queries.

**NFS protocol version detection**: Docker NFS volume mount is strict about protocol version (`volume-opt=type=nfs` vs `volume-opt=type=nfs4`) — a mismatch causes container startup failure. The control plane already mounts NFS during `ModelRegistryController.Connect()` via `nfs.MountNFS()`, and the kernel-negotiated protocol version is recorded in `/proc/mounts`. Reading `MountPoint.Type` via `nfs.GetMountType()` provides the protocol version at zero extra cost.

**NFS mount logic consolidated inside `buildEngineContainerConfig()`**: Since NFS detection and `--mount` option generation are only used in `buildEngineContainerConfig()`, the complete NFS handling logic (URL parsing, protocol detection, `--mount` generation) is encapsulated within this function via the `modelRegistry` parameter, rather than pre-building intermediate structs at the call site.

**Error handling**: NFS detection failures return explicit errors to block deployment, preventing Engine containers from starting without model access:
- URL parse failure → `failed to parse model registry URL`
- Model registry creation failure → `failed to create model registry for NFS type detection`
- NFS type detection failure → `failed to detect NFS type from control-plane mount`
- No NFS mount on control plane → `NFS mount not found on control-plane`

**Files**:
- `internal/nfs/nfs.go` — New `GetMountType()` reads NFS mount filesystem type from `/proc/mounts`
- `pkg/model_registry/model_registry.go` — `ModelRegistry` interface adds `GetNFSType()` method
- `pkg/model_registry/file_based.go` — `nfsFile.GetNFSType()` delegates to `nfs.GetMountType()`
- `internal/orchestrator/ray_orchestrator.go` — `isNewClusterVersion()` version check; `CreateEndpoint()`/`DeleteEndpoint()` call-site version branching; `buildEngineContainerConfig()` with built-in NFS detection and error handling

**Data flow**:

```
CreateEndpoint() / DeleteEndpoint()
  └─ isNewClusterVersion(cluster)
       ├─ false (<=v1.0.0): connectSSHClusterEndpointModel() — NFS mount inside ray_container
       └─ true  (> v1.0.0): skip

EndpointToApplication()
  └─ isNewClusterVersion(deployedCluster)
       ├─ false (<=v1.0.0): skip buildEngineContainerConfig()
       └─ true  (> v1.0.0): buildEngineContainerConfig(..., modelRegistry)
            ├─ Detect BentoML NFS model registry
            ├─ url.Parse(modelRegistry.Spec.Url) → extract server/path
            ├─ model_registry.NewModelRegistry() → registry.GetNFSType() → "nfs4"
            └─ run_options += --mount type=volume,
                 dst=/mnt/{workspace}/{endpoint},
                 volume-opt=type=nfs4,
                 volume-opt=o=addr={server},
                 volume-opt=device=:{path}
```

**Example generated run_options**:

```
--mount type=volume,dst=/mnt/default/llama-endpoint,volume-opt=type=nfs4,volume-opt=o=addr=10.255.1.54,volume-opt=device=:/bentoml
```

### 4.6 Update Caller

**File**: `internal/orchestrator/ray_orchestrator.go`

In `createOrUpdate()`, pass `ctx.Engine`:

```go
// Before:
newApp, err := EndpointToApplication(ctx.Endpoint, ctx.Cluster, ctx.ModelRegistry,
    ctx.ImageRegistry, o.acceleratorMgr)

// After:
newApp, err := EndpointToApplication(ctx.Endpoint, ctx.Cluster, ctx.ModelRegistry,
    ctx.Engine, ctx.ImageRegistry, o.acceleratorMgr)
```

### 4.7 Backward Compatibility

When `EngineVersion.Images` does not contain `ssh_*` keys (legacy engine versions):
- No `runtime_env.container` is set
- Behavior is identical to the current architecture
- `import_path` resolves against the cluster image's pre-installed engine
- No code path changes for existing deployments

---

## 5. Ray Fork Considerations

**Repository**: `neutree-ai/ray` (`ray-2.53.0-neutree` branch)

### 5.1 `container` Field Maintenance

The `runtime_env.container` field is deprecated in upstream Ray but is the only option supporting GPU via `run_options`. The Neutree Ray fork must:

1. Retain `container` field validation and execution logic
2. Optionally suppress deprecation warnings
3. Ensure `run_options` are passed through to the container CLI

### 5.2 Docker CLI Compatibility

Ray internally calls `podman run` with the configured `run_options`. Since we symlink `docker` as `podman`, verify:

- `podman run --runtime=nvidia` → `docker run --runtime=nvidia` ✓ (standard Docker flag)
- `podman run --network host` → `docker run --network host` ✓
- `podman run -v /path:/path` → `docker run -v /path:/path` ✓

Most `podman run` flags are Docker-compatible. Edge cases to test:
- `--rm` behavior on container exit
- Signal propagation to the worker process
- Container cleanup on Ray worker crash

**Podman-specific flags to handle:**

Ray's internal container management code (`ray/_private/runtime_env/container.py`) may use Podman-specific CLI flags. In the Neutree Ray fork, verify and adjust:

- `--format json` (Podman inspect) → may need `--format '{{json .}}'` for Docker
- `--cidfile` behavior differences
- Container logging driver defaults

The `podman` → `docker` symlink handles command routing, but flag compatibility must be verified for each Ray version upgrade.

---

## 6. Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| `container` field removed from Ray upstream | Fork must self-maintain | Pin behavior in fork; track `image_uri` GPU fix ([PR #60485](https://github.com/ray-project/ray/pull/60485)) |
| Docker CLI not 100% Podman-compatible | Container startup failure | Test all `run_options` flags; keep flag set minimal |
| docker.sock security (host Docker access) | Container escape risk | Ray container already runs with `--privileged`; same trust boundary |
| Engine image pull latency (first deploy) | Slow Endpoint startup | Pre-pull engine images on nodes; add image pull status to Endpoint status |
| Host path mapping errors | Model not found in engine container | Validate HostPath existence; clear error messages |

---

## 7. Migration Path

### 7.1 When Ray fixes `image_uri` GPU support

When [PR #60485](https://github.com/ray-project/ray/pull/60485) is merged:

1. `runtime_env.container` → `runtime_env.image_uri`
2. Engine images unchanged (same container image)
3. `run_options` no longer needed (Ray handles GPU automatically)
4. Remove `container` field maintenance from fork
5. Remove `podman` symlink from cluster image

### 7.2 Incremental adoption

1. **Phase 1**: Ship without `ssh_*` image keys — all existing behavior preserved
2. **Phase 2**: Publish engine images for new engine versions with `ssh_*` image keys configured
3. **Phase 3**: Existing engine versions can optionally add `ssh_*` image keys to enable isolation

---

## 8. Testing Plan

1. **Docker symlink**: Verify `podman run` → `docker run` works inside Ray container
2. **GPU passthrough**: Engine container `torch.cuda.is_available() == True` with `--runtime=nvidia`
3. **Model access**: Engine container reads model files via host-path volume mount
4. **Multi-version isolation**: Two Endpoints on same cluster with different vLLM versions
5. **Inference correctness**: Both Endpoints respond correctly to chat/embedding requests
6. **Resource scheduling**: GPU resources allocated per Ray scheduling (not double-counted)
7. **Rolling upgrade**: Upgrade one Endpoint's engine version without affecting others
8. **Backward compatibility**: Endpoint without `ssh_*` image key uses cluster pre-installed engine

---

## 9. Implementation Checklist

- [x] `cluster-image-builder/Dockerfile` — Install docker CLI, symlink as podman
- [x] `cluster-image-builder/Dockerfile.engine-vllm` — Engine image Dockerfile
- [x] `cluster-image-builder/serve/vllm/v0_12_0/app.py` — vLLM v0.12.0 app with updated imports
- [x] `internal/cluster/ray_ssh_operation.go` — docker.sock, /tmp, --pid=host, --ipc=host, chmod
- [ ] `api/v1/engine_types.go` — Add `SSHEngineImageKeyPrefix` and `GetSSHImageKey()`
- [ ] `internal/accelerator/plugin/plugin.go` — Add `GetAcceleratorRuntimeConfig()` to `AcceleratorPluginHandle`
- [ ] `internal/accelerator/plugin/gpu.go` — Implement `GetAcceleratorRuntimeConfig()`, add `ssh_nvidia_gpu` image
- [ ] `internal/accelerator/plugin/amd_gpu.go` — Implement `GetAcceleratorRuntimeConfig()`, add `ssh_amd_gpu` image
- [ ] `internal/accelerator/plugin/client.go` — Implement `GetAcceleratorRuntimeConfig()` (empty default)
- [ ] `internal/accelerator/manager.go` — Add `GetEngineContainerRunOptions()` to Manager interface and implementation
- [ ] `internal/orchestrator/ray_orchestrator.go` — Rewrite `buildEngineContainerConfig()` with engine + acceleratorMgr
- [ ] `internal/orchestrator/ray_orchestrator_test.go` — Update tests for new signatures and SSH key logic
- [ ] Mock regeneration — mockery for Manager and AcceleratorPluginHandle
- [ ] Ray fork validation — Verify `container` field + Docker CLI compatibility
