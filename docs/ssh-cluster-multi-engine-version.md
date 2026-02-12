# SSH Cluster Multi-Engine Version Support

## 1. Background

### 1.1 Problem Statement

In the current SSH Cluster architecture, all inference engines (vLLM, llama-cpp) are pre-installed in a monolithic cluster image `neutree-serve:{version}`. All Endpoints within the same cluster must use the same engine version. This limits:

- **Engine version isolation**: Cannot run vLLM v0.11.2 and v0.12.0 simultaneously on the same cluster
- **Upgrade flexibility**: Engine upgrades require rebuilding the entire cluster image and restarting all nodes
- **Engine iteration speed**: New engine versions are tightly coupled with cluster image releases

### 1.2 Goal

Support running different versions of vLLM/engines on the same Ray Cluster via per-Endpoint engine container isolation, while maintaining backward compatibility with existing single-image deployments.

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

### 3.2 Alternatives Considered

| Approach | Rejected Reason |
|----------|----------------|
| Podman-in-Docker | Requires installing Podman + nvidia-container-toolkit inside Ray container; CDI GPU support less mature than Docker's nvidia runtime |
| Sidecar container + gRPC | Excessive complexity: container lifecycle management, port allocation, resource accounting split |
| `runtime_env.pip` | vLLM installation involves CUDA compilation, 10-30 min cold start |
| Multi-version pre-install | Image bloat (each vLLM ~2-5GB), CUDA library conflicts |

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

### 4.2 Docker Socket Mounting

**File**: `internal/cluster/ray_ssh_operation.go`

Add docker.sock volume mount to `generateRayClusterConfig()`:

```go
rayClusterConfig.Docker.RunOptions = []string{
    "--privileged",
    // ... existing options ...
    "--volume /var/run/docker.sock:/var/run/docker.sock",
}
```

This allows Ray (via the `podman` → `docker` symlink) to create engine containers on the host.

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

### 4.4 EngineVersion API Extension

**File**: `api/v1/engine_types.go`

Add `ContainerImages` to `EngineVersion`:

```go
type EngineVersion struct {
    // ... existing fields ...

    // ContainerImages specifies per-accelerator engine container images for
    // SSH cluster runtime_env.container isolation.
    // When empty, falls back to the pre-installed engine in the cluster image (backward compatible).
    // Keys are accelerator types (e.g., "nvidia_gpu", "amd_gpu").
    ContainerImages map[string]*EngineImage `json:"container_images,omitempty" yaml:"container_images,omitempty"`
}
```

This is intentionally separate from the existing `Images` field:
- `Images`: K8s Deployment images (used by kubernetes orchestrator)
- `ContainerImages`: SSH cluster runtime_env container images

Example configuration:

```json
{
  "version": "v0.12.0",
  "images": {
    "nvidia_gpu": {"image_name": "neutree/vllm-cuda", "tag": "v0.12.0"}
  },
  "container_images": {
    "nvidia_gpu": {"image_name": "registry.example.com/neutree/engine-vllm", "tag": "v0.12.0-ray2.53.0"}
  }
}
```

### 4.5 EndpointToApplication() Modification

**File**: `internal/orchestrator/ray_orchestrator.go`

#### 4.5.1 Function Signature Change

Add `engine *v1.Engine` parameter:

```go
func EndpointToApplication(endpoint *v1.Endpoint, deployedCluster *v1.Cluster,
    modelRegistry *v1.ModelRegistry, engine *v1.Engine,
    acceleratorMgr accelerator.Manager) (dashboard.RayServeApplication, error) {
```

#### 4.5.2 Container Runtime Env Generation

After the existing `app.RuntimeEnv["env_vars"]` assignment, add container configuration:

```go
// Generate runtime_env.container if engine version has container images
if containerImage := getEngineContainerImage(engine, endpoint.Spec.Engine.Version,
    deployedCluster); containerImage != nil {

    imageRef := fmt.Sprintf("%s:%s", containerImage.ImageName, containerImage.Tag)

    runOptions := []string{
        "--runtime=nvidia",
        "-e NVIDIA_VISIBLE_DEVICES=all",
        "--network host",
    }

    // Mount model caches using HOST paths (docker.sock creates containers on host)
    modelCaches, _ := util.GetClusterModelCache(*deployedCluster)
    for _, mc := range modelCaches {
        if mc.HostPath != nil {
            containerMountPath := filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, mc.Name)
            runOptions = append(runOptions,
                fmt.Sprintf("-v %s:%s", mc.HostPath.Path, containerMountPath))
        }
    }

    app.RuntimeEnv["container"] = map[string]interface{}{
        "image":       imageRef,
        "run_options": runOptions,
    }
}
```

#### 4.5.3 Helper Function

```go
// getEngineContainerImage returns the container image for the engine version
// matching the cluster's accelerator type. Returns nil if no container image
// is configured (backward-compatible fallback to cluster pre-installed engine).
func getEngineContainerImage(engine *v1.Engine, version string,
    cluster *v1.Cluster) *v1.EngineImage {

    if engine == nil || engine.Spec == nil {
        return nil
    }

    // Find matching engine version
    var engineVersion *v1.EngineVersion
    for _, v := range engine.Spec.Versions {
        if v.Version == version {
            engineVersion = v
            break
        }
    }
    if engineVersion == nil || engineVersion.ContainerImages == nil {
        return nil
    }

    // Get cluster accelerator type
    acceleratorType := ""
    if cluster.Status != nil && cluster.Status.AcceleratorType != nil {
        acceleratorType = *cluster.Status.AcceleratorType
    } else if cluster.Spec != nil && cluster.Spec.Config != nil && cluster.Spec.Config.AcceleratorType != nil {
        acceleratorType = *cluster.Spec.Config.AcceleratorType
    }
    if acceleratorType == "" {
        return nil
    }

    return engineVersion.ContainerImages[acceleratorType]
}
```

#### 4.5.4 Volume Mount Path Consideration

This is the critical difference from Podman-in-Docker. With docker.sock, engine containers are created by the **host's Docker daemon**, so volume mounts reference **host paths**:

```
Host: /data/models → (docker -v) → Ray container: /home/ray/.neutree/models-cache/default
                  ↘ (docker -v) → Engine container: /home/ray/.neutree/models-cache/default

Both ray_container and engine containers mount the same host path.
The engine container sees the same model files at the same internal path.
```

The `ModelCache.HostPath.Path` (e.g., `/data/models`) is the host path. The internal container mount path (`/home/ray/.neutree/models-cache/{name}`) is what the Python code expects. This mapping is already available in the `ClusterConfig.ModelCaches` struct.

### 4.6 Update Caller

**File**: `internal/orchestrator/ray_orchestrator.go`

In `createOrUpdate()`, pass `ctx.Engine`:

```go
// Before:
newApp, err := EndpointToApplication(ctx.Endpoint, ctx.Cluster, ctx.ModelRegistry, o.acceleratorMgr)

// After:
newApp, err := EndpointToApplication(ctx.Endpoint, ctx.Cluster, ctx.ModelRegistry, ctx.Engine, o.acceleratorMgr)
```

### 4.7 Backward Compatibility

When `EngineVersion.ContainerImages` is empty (legacy engine versions):
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

1. **Phase 1**: Ship with `ContainerImages` empty — all existing behavior preserved
2. **Phase 2**: Publish engine images for new engine versions with `ContainerImages` configured
3. **Phase 3**: Existing engine versions can optionally add `ContainerImages` to enable isolation

---

## 8. Testing Plan

1. **Docker symlink**: Verify `podman run` → `docker run` works inside Ray container
2. **GPU passthrough**: Engine container `torch.cuda.is_available() == True` with `--runtime=nvidia`
3. **Model access**: Engine container reads model files via host-path volume mount
4. **Multi-version isolation**: Two Endpoints on same cluster with different vLLM versions
5. **Inference correctness**: Both Endpoints respond correctly to chat/embedding requests
6. **Resource scheduling**: GPU resources allocated per Ray scheduling (not double-counted)
7. **Rolling upgrade**: Upgrade one Endpoint's engine version without affecting others
8. **Backward compatibility**: Endpoint without `ContainerImages` uses cluster pre-installed engine

---

## 9. Implementation Checklist

- [ ] `cluster-image-builder/Dockerfile` — Install docker CLI, symlink as podman
- [ ] `cluster-image-builder/Dockerfile.engine-vllm` — New engine image Dockerfile
- [ ] `internal/cluster/ray_ssh_operation.go` — Mount docker.sock in Ray container
- [ ] `api/v1/engine_types.go` — Add `ContainerImages` field to `EngineVersion`
- [ ] `internal/orchestrator/ray_orchestrator.go` — Add `engine` param to `EndpointToApplication()`, generate `runtime_env.container`
- [ ] `internal/orchestrator/ray_orchestrator.go` — Update `createOrUpdate()` caller
- [ ] `internal/orchestrator/ray_orchestrator_test.go` — Update existing tests, add container image tests
- [ ] Ray fork validation — Verify `container` field + Docker CLI compatibility
