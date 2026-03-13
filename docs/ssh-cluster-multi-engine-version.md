# SSH Cluster Multi-Engine Version Support

## Background

In the current SSH Cluster architecture, all inference engines (vLLM, llama-cpp) are pre-installed in a monolithic cluster image `neutree-serve:{version}`. All Endpoints within the same cluster must use the same engine version. This causes several issues:

- Cannot run vLLM v0.11.2 and v0.12.0 simultaneously on the same cluster
- Engine upgrades require rebuilding the entire cluster image and restarting all nodes
- New engine versions are tightly coupled with cluster image releases

### Goals

1. Per-Endpoint engine container isolation, enabling different engine versions to run on the same Ray Cluster while maintaining backward compatibility with existing single-image deployments.
2. Support dynamic engine updates without cluster rebuilds, as rebuilding disrupts deployed inference instances.

---

## Design Decisions

### Approach: `runtime_env.container` with Docker (DOOD)

Ray 2.53.0 `runtime_env` provides two container isolation mechanisms:

| Feature | `image_uri` | `container` |
|---------|-------------|-------------|
| Status | Recommended (experimental) | Deprecated (since 2025.07) |
| GPU support | **Not available** ([#58399](https://github.com/ray-project/ray/issues/58399)) | Via `run_options` |

**Selected**: `runtime_env.container` -- the only option supporting GPU via `run_options`. Extended in the Neutree Ray fork (`ray-2.53.0-neutree`) to support Docker directly (upstream only supports Podman).

### Why DOOD over DIND

Engine containers are sibling containers created by the host's Docker daemon via mounted docker.sock (**Docker-outside-of-Docker**).

DIND (Docker-in-Docker) was rejected because:

1. 3-layer nesting (host -> Docker daemon in Ray container -> Engine container) vs DOOD's 2 layers, adding complexity
2. Requires installing/managing a separate Docker daemon + nvidia-container-runtime inside the Ray container
3. DOOD reuses the host's existing Docker + NVIDIA runtime directly

### Alternatives Rejected

| Approach | Reason |
|----------|--------|
| `runtime_env.uv` / `pip` | No prebuilt packages on some platforms; serve layer code not in pip packages |
| `uv --target` bundle | Complex distribution arch; no resource isolation |
| Multi-version pre-install | Image bloat (each vLLM ~2-5GB), CUDA conflicts; no dynamic updates |

---

## Architecture

```
Host Machine
  |
  +-- Docker daemon (with nvidia-container-runtime)
  |
  +-- ray_container (cluster base image: ubuntu:22.04 + Python 3.12 + Ray)
  |     +-- Ray Head/Worker processes
  |     +-- docker CLI (DOOD)
  |     +-- /var/run/docker.sock (mounted from host)
  |
  +-- Engine Container A (Endpoint A, vLLM v0.11.2)
  |     +-- runtime_env.container:
  |     |     image: neutree/engine-vllm:v0.11.2-ray2.53.0
  |     |     run_options: [--runtime=nvidia, --gpus all, -v hostPath:mountPath]
  |     +-- Ray worker -> serve/vllm/v0_11_2/app.py -> vLLM AsyncLLM
  |
  +-- Engine Container B (Endpoint B, vLLM v0.12.0)
        +-- runtime_env.container:
        |     image: neutree/engine-vllm:v0.12.0-ray2.53.0
        |     run_options: [--runtime=nvidia, --gpus all, -v hostPath:mountPath]
        +-- Ray worker -> serve/vllm/v0_12_0/app.py -> vLLM AsyncLLM
```

### Ray Serve Deployment Chain

Understanding where code executes:

```
ServeController (head node, inside cluster image)
  |  Does NOT import user code
  |
  +-- build_serve_application Ray Task
       runtime_env = {container: {...}, env_vars: {...}}
       |
       v  (inside Engine container)
       import_attr("serve.vllm.v0_12_0.app:app_builder")
       -> returns Application DAG -> back to ServeController
       |
       v
       ServeController schedules replicas:
         Backend (N, GPU)     -> Engine container (with --gpus all)
         Controller (1, CPU)  -> Engine container (no GPU run_options)
```

The `import_path` is resolved inside the Engine container. The cluster image no longer needs engine code.

---

## Key Design Points

### Cluster Image

Minimized to lean Ray base (`ubuntu:22.04` + Python 3.12 + Ray). No pre-installed engines. GPU detection uses only system-level tools (`nvidia-smi`, `rocminfo`), not engine packages.

Ray container additional run_options for DOOD:

| Option | Purpose |
|--------|---------|
| `-v /var/run/docker.sock:...` | Create Engine containers on host |
| `-v /tmp:/tmp` | Ray tmp files visible to host Docker daemon |
| `--pid=host` | Engine containers see Raylet process for lifecycle management |
| `--ipc=host` | Ray Object Store shared memory between containers |

### Engine Images

Built on top of community engine images (e.g., `vllm/vllm-openai:v0.12.0`) + Ray neutree wheel + serve layer code. No source compilation needed.

Naming: `{registry}/neutree/engine-{name}:{version}-ray{ray_version}`

Version constraints: Ray version must match exactly; Python minor version must match (3.12.x); patch may differ via `RAY_DEFAULT_PYTHON_VERSION_MATCH_LEVEL=minor`.

### Accelerator Run Options

Provided by the accelerator plugin system (`AcceleratorPluginHandle.GetAcceleratorRuntimeConfig()`), not hardcoded:

| Accelerator | run_options |
|-------------|-------------|
| NVIDIA GPU | `--runtime=nvidia --gpus all` |
| AMD GPU | `--runtime=amd -e AMD_VISIBLE_DEVICES=all` |

### NFS Model Registry

Engine containers are host-level siblings isolated from Ray container's filesystem. NFS mounts inside Ray container are invisible to them.

Solution: Docker `--mount type=volume,volume-opt=type=nfs4,...` so Docker daemon mounts NFS directly when creating Engine containers. NFS protocol version auto-detected from `/proc/mounts` via `nfs.GetMountType()`.

### Backward Compatibility

Version branching via `isNewClusterVersion()`:

| Operation | Old cluster (<=v1.0.0) | New cluster (>v1.0.0) |
|-----------|------------------------|----------------------|
| CreateEndpoint | NFS mount inside ray_container | Skip (handled by Engine container) |
| DeleteEndpoint | NFS unmount inside ray_container | Skip |
| EndpointToApplication | No engine container config | Build engine container config |

### Observability

- `engine_version` label injected in vLLM metrics via `ENGINE_VERSION` env var
- Grafana PromQL uses `or` for renamed metrics (new name left, old name right) to support both v0.8.5 and v0.11.2+

---

## Risks

| Risk | Mitigation |
|------|------------|
| `container` field removed from upstream Ray | Pin in fork; track `image_uri` GPU fix ([PR #60485](https://github.com/ray-project/ray/pull/60485)) |
| docker.sock security | New clusters drop `--privileged`; docker group membership only |
| Engine image pull latency | Pre-pull on nodes; add pull progress to Endpoint status |

## References

- [Ray runtime_env.container (Deprecated)](https://docs.ray.io/en/releases-2.53.0/ray-core/handling-dependencies.html#container-option-deprecated)
- [ray-project/ray#58399](https://github.com/ray-project/ray/issues/58399) -- image_uri GPU not supported
- [Docker Volume NFS](https://docs.docker.com/engine/storage/volumes/#create-a-service-which-creates-an-nfs-volume)
- [neutree-ai/ray](https://github.com/neutree-ai/ray) (`ray-2.53.0-neutree` branch)
