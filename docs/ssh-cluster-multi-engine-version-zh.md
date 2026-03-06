# SSH 集群多引擎版本支持

## 背景

### 问题描述

当前 SSH 集群架构中，所有推理引擎（vLLM、llama-cpp）预装在单一集群镜像 `neutree-serve:{version}` 中。同一集群内所有 Endpoint 必须使用相同的引擎版本。这带来几个问题：

- 无法在同一集群上同时运行 vLLM v0.11.2 和 v0.12.0
- 引擎升级需要重新构建整个集群镜像并重启所有节点
- 新引擎版本与集群镜像发布紧耦合

### 用户故事

1. 作为平台用户，我希望在同一个 SSH 集群内同时运行不同版本的推理引擎（如 vLLM v0.11.2 和 v0.12.0），这样不同 Endpoint 可以独立选择引擎版本，互不影响。
2. 作为平台用户，我希望能运行最新版本的 vLLM 引擎，而不用等待整个集群镜像的发布周期，这样可以第一时间用上新引擎的功能和性能优化。
3. 作为平台用户，我希望更新引擎版本时不需要重建集群，因为重建集群会中断集群上已经在运行的推理实例。
4. 作为平台用户，我希望升级引擎版本后发现问题时可以快速回退到之前的版本，而不影响集群上的其他 Endpoint。


### 目标

1. per-Endpoint 引擎容器隔离，让同一 Ray 集群上可以运行不同版本的引擎，同时兼容现有的单镜像部署方式。
2. 支持动态更新引擎而不需要重建集群，重建集群会中断已部署的推理实例。

---

## 当前架构

### 部署流程

```
Endpoint 创建 → EndpointToApplication() → Ray Serve Application
  - ImportPath: "serve.vllm.v0_11_2.app:app_builder"  (预装在集群镜像中)
  - RuntimeEnv: {env_vars only}
  - Args: {deployment_options, model, engine_args}
```

### 集群节点布局

```
宿主机
  └── Docker 容器: ray_container (neutree-serve:{version})
        ├── Ray Head/Worker 进程
        ├── 预装引擎 (vLLM, llama-cpp)
        ├── serve/ Python 模块 (每个引擎版本一个 app.py)
        └── 模型缓存: /home/ray/.neutree/models-cache/
              通过 --volume {hostPath}:{mountPath} 从宿主机挂载
```

### 关键约束

1. **Ray + Python 版本**：集群内所有节点 Ray 版本必须完全一致；Python 版本默认要求 patch 级一致（如 3.12.7），可通过 `RAY_DEFAULT_PYTHON_VERSION_MATCH_LEVEL=minor` 放宽为仅要求 minor 级一致（如 3.12.x）
2. **SSH 集群节点**：通过 Docker 容器运行 Ray，配置 `--privileged` 和 `--net=host`
3. **`import_path`**：指向必须在执行环境中可访问的 Python 模块


## 设计方案

### 方案选择

Ray 2.53.0 `runtime_env` 提供两种容器隔离机制，上游均使用 **Podman**：

| 特性 | `image_uri` | `container` |
|------|-------------|-------------|
| 状态 | 推荐（实验性） | 已弃用（2025.07 起） |
| 运行时 | Podman | Podman |
| GPU 支持 | **不可用** ([#58399](https://github.com/ray-project/ray/issues/58399)) | 通过 `run_options` |
| `run_options` | 不支持 | 支持（Docker CLI 语法） |

选 `runtime_env.container`，因为只有它支持通过 `run_options` 传入 GPU 参数。

上游 Ray 将 Podman 硬编码为容器运行时。我们在 Neutree 维护的 `ray-2.53.0-neutree` 分支中扩展 `runtime_env.container`，直接支持 Docker，不需要 Podman 或符号链接。

### 为什么选择 DOOD 而非 DIND

Engine 容器通过挂载宿主机的 Docker socket（`/var/run/docker.sock`）由宿主机 Docker daemon 创建，即 **Docker-outside-of-Docker (DOOD)** 模式。Engine 容器与 Ray 容器是宿主机上的**兄弟容器**，而非嵌套容器。

另一种方案是在 Ray 容器内运行独立的 Docker daemon，即 **Docker-in-Docker (DIND)** 模式。否决 DIND 的原因：

1. DIND 形成 3 层嵌套（宿主机 Docker → Ray 容器内 Docker daemon → Engine 容器），DOOD 只有 2 层。嵌套方案需要在 Ray 容器内安装和管理独立的 Docker daemon，增加镜像构建和维护成本。
2. DIND 要求 Ray 容器以 `--privileged` 模式运行才能启动内部 Docker daemon。当前 Ray 容器虽然已经用了 `--privileged`，但 DOOD 本身不需要这个特权。
3. DIND 内部的 Docker daemon 需要额外配置 nvidia-container-runtime，DOOD 直接用宿主机已有的 Docker + NVIDIA 运行时。

### 其他备选方案

| 方案 | 否决原因 |
|------|----------|
| `runtime_env.uv` / `runtime_env.pip` | 部分平台无预编译版本；serve 层代码不在 pip 包中 |
| `uv --target` site-packages 打包分发 | 架构复杂，需要额外的文件服务器进行分发；不支持资源隔离；需要额外机制同步 serve 层代码 |
| 多版本预装 | 镜像膨胀（每个 vLLM 约 2-5GB），CUDA 库冲突；不支持动态更新引擎版本 |

### 目标架构

```
宿主机
  │
  ├── Docker daemon (带 nvidia-container-runtime)
  │
  ├── ray_container (集群基础镜像: ubuntu:22.04 + Python 3.12 + Ray)
  │     ├── Ray Head/Worker 进程
  │     ├── docker CLI（DOOD）
  │     ├── accelerator/ (GPU 探测，仅依赖系统工具)
  │     └── /var/run/docker.sock (从宿主机挂载)
  │
  ├── Engine Container A (Endpoint A, vLLM v0.11.2)
  │     ├── runtime_env.container:
  │     │     image: neutree/engine-vllm:v0.11.2-ray2.53.0
  │     │     run_options: [--runtime=nvidia, --gpus all, -v hostPath:mountPath]
  │     ├── 基于社区镜像 vllm/vllm-openai:v0.11.2 + Ray neutree wheel
  │     ├── Ray worker 进程 (连接到 Ray 集群)
  │     ├── serve/vllm/v0_11_2/app.py → app_builder()
  │     └── vLLM v0.11.2 + AsyncLLM 引擎
  │
  └── Engine Container B (Endpoint B, vLLM v0.12.0)
        ├── runtime_env.container:
        │     image: neutree/engine-vllm:v0.12.0-ray2.53.0
        │     run_options: [--runtime=nvidia, --gpus all, -v hostPath:mountPath]
        ├── 基于社区镜像 vllm/vllm-openai:v0.12.0 + Ray neutree wheel
        ├── Ray worker 进程 (连接到 Ray 集群)
        ├── serve/vllm/v0_12_0/app.py → app_builder()
        └── vLLM v0.12.0 + AsyncLLM 引擎
```

Engine 容器和 Ray 容器是宿主机上的兄弟容器（DOOD 模式），都由宿主机的 Docker daemon 通过挂载的 docker.sock 创建。

### Ray Serve 部署链路

需要搞清楚代码在哪里执行：

```
ServeController (头节点, 集群镜像内)
  │  接收 PUT /api/serve/applications/
  │  不导入用户代码
  │
  ╰─ 提交 build_serve_application Ray Task
     runtime_env = {container: {...}, env_vars: {...}}
                │
                ▼
     Ray Task (在 Engine 容器内)
       │  import_attr("serve.vllm.v0_12_0.app:app_builder")
       │  app_builder(args) → 返回 Application DAG
       │  序列化 deployment configs → 返回给 ServeController
                │
                ▼
     ServeController 调度 replica actors
       │  Backend replicas (N 个, 带 GPU) → 在 Engine 容器内
       │  Controller replica (1 个, 仅 CPU) → 在 Engine 容器内
                │
                ▼
     Backend.__init__() → 下载模型 + 初始化 vLLM AsyncLLM
     Controller.__init__(backend_handle) → FastAPI 路由就绪
```

`import_path` 在 Engine 容器内解析，不在集群镜像中。容器化部署后，集群镜像不再需要引擎代码。

参考：[`build_serve_application`（Ray 源码）](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L1077)、[`apply_app_config` 提交 runtime_env](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L651)

---

## 详细设计

### 集群改动

#### 容器运行时配置

静态节点集群需要配置容器运行时（container runtime），指定 Engine 容器的创建方式。

当前只支持 Docker，后续可以扩展 Podman 等。

SSH 集群配置中新增 `container_runtime` 字段：

```go
type RaySSHProvisionClusterConfig struct {
    // ... 现有字段 ...
    ContainerRuntime string `json:"container_runtime"` // "docker" (默认)
}
```

影响范围：
- Ray fork 中 `runtime_env.container` 使用的容器 CLI
- Engine 容器的 `run_options` 格式（Docker 和 Podman 部分参数有差异）
- 不影响 Ray 容器本身（由宿主机 Docker daemon 管理）

#### 集群镜像改动

集群镜像精简为最小化 Ray 镜像，不预装 vLLM 等引擎。引擎代码完全在 Engine 容器内运行。

Base 镜像：`ubuntu:22.04`（NVIDIA 和 ROCm 共用）

Python 版本：3.12.12（从源码编译，3.12 系列最新安全版本，修复 CVE-2025-59375 等）

GPU 探测不依赖引擎包（vLLM、torch 等），两层探测链路只用到系统级组件：

| 探测层 | NVIDIA | AMD ROCm |
|--------|--------|----------|
| Ray 探测（`num_gpus`） | vendored pynvml → ctypes → `libnvidia-ml.so.1` | vendored pyamdsmi → ctypes → `librocm_smi64.so` |
| Neutree 探测（AcceleratorType Count） | subprocess → `nvidia-smi` | subprocess → `rocminfo` |
| Ray 监控（Dashboard 指标） | vendored pynvml → `libnvidia-ml.so.1` | vendored pyamdsmi → `librocm_smi64.so` |

加速卡差异：

| | NVIDIA | AMD ROCm |
|---|---|---|
| 驱动库来源 | `--runtime=nvidia` 运行时自动从宿主机注入 | 需镜像内安装（ROCm 用户态库不自动注入） |
| 额外安装包 | 无 | `rocm-smi-lib` + `rocminfo`（~几十 MB） |
| ROCm 版本兼容 | 不适用 | 需在宿主机 amdgpu 驱动兼容范围内（一年窗口），建议与 Engine 镜像 ROCm 版本一致 |

核心组件：

```
ubuntu:22.04
├── Python 3.12.12（源码编译）
├── Ray neutree fork wheel
├── docker.io + podman（Engine 容器 DOOD，当前使用 Docker，预留 Podman 支持）
├── util-linux、nfs-common（集群基础设施）
├── start.py + accelerator/（GPU 探测 + Ray 启动）
├── [ROCm only] rocm-smi-lib + rocminfo
└── 完毕（不含 vLLM、torch、llama-cpp 等引擎包）
```

环境变量：
- `RAY_DEFAULT_PYTHON_VERSION_MATCH_LEVEL=minor` — 放宽 Python patch 版本检查，允许集群镜像（3.12.12）与 Engine 镜像（3.12.x）patch 版本不一致

#### Ray 容器引擎隔离配置

Ray 容器在现有 `--privileged` 和 `--net=host` 基础上增加以下 Docker run_options：

| 参数 | 用途 |
|------|------|
| `--volume /var/run/docker.sock:/var/run/docker.sock` | 允许 Ray 通过 Docker socket 在宿主机上创建 Engine 容器 |
| `--volume /tmp:/tmp` | Ray 写入的临时文件（如容器环境配置脚本）需要对宿主机 Docker daemon 可见，因为 Engine 容器是宿主机级别的兄弟容器 |
| `--pid=host` | Engine 容器需要看到 Raylet 进程（父进程 PID）以进行进程生命周期管理 |
| `--ipc=host` | Ray Object Store 在 Ray 容器和 Engine 容器之间的共享内存通信 |

另外，在每个节点的启动命令前添加：

```
sudo chmod 666 /var/run/docker.sock
```

这授予 `ray` 用户（容器内非 root）访问 Docker socket 的权限。

这些选项在 `generateRayClusterConfig()` 中设置：

```go
rayClusterConfig.Docker.RunOptions = []string{
    "--privileged",
    // ... 现有选项 ...
    "--volume /var/run/docker.sock:/var/run/docker.sock",
    "--volume /tmp:/tmp",
    "--pid=host",
    "--ipc=host",
}
```

### 引擎改动

#### Engine 镜像

构建方式：在社区引擎镜像上叠一层，装 Ray neutree fork wheel + Neutree serve 层代码。不用从源码重新编译引擎。

版本约束：

| 维度 | 要求 | 说明 |
|------|------|------|
| Ray 版本 | 必须一致 | neutree fork 同版本 wheel |
| Python minor | 必须一致 | 集群镜像 3.12.x，Engine 镜像也须 3.12.x |
| Python patch | 允许不一致 | 通过 `RAY_DEFAULT_PYTHON_VERSION_MATCH_LEVEL=minor` 放宽 |

当前主流推理引擎社区镜像都用 Python 3.12（vLLM、SGLang 等），和集群镜像 Python 3.12.12 minor 版本一致。

命名约定：`{registry}/neutree/engine-{engine_name}:{engine_version}-ray{ray_version}`

示例：`registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0`

#### 引擎镜像注册

SSH 集群和 K8s 集群共用同一套引擎镜像。`EngineVersion.Images` map 中使用相同的 key（如 `nvidia_gpu`、`amd_gpu`），不区分集群类型。

Engine 镜像基于社区引擎镜像构建，叠加 Ray neutree fork wheel + Neutree serve 层代码。这些镜像在 K8s 集群和 SSH 集群上都可使用：K8s 部署时 Ray 相关组件不影响引擎运行；SSH 部署时 Ray worker 进程在 Engine 容器内启动并连接到 Ray 集群。

配置示例：

```go
// gpu.go — v0.12.0 注册
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

#### 引擎镜像上传

和 K8s 集群共用同一套 `neutree-cli engine import` 流程：

```
neutree-cli engine import --package engine-vllm-v0.12.0.tar.gz
```

Engine 包结构：

```
engine-vllm-v0.12.0.tar.gz
├── manifest.yaml          # 引擎元数据、版本、镜像规格
└── images/
    └── engine-vllm-v0.12.0-ray2.53.0.tar  # Docker 镜像 tar 文件
```

导入流程：

1. 解压 engine 包，读取 `manifest.yaml`
2. 通过 Docker client 加载镜像 tar：`docker.ImageLoad()`
3. 根据目标 Image Registry 重新 tag：`docker.ImageTag(sourceImage, targetImage)`
4. 推送到镜像仓库：`docker.ImagePush()`
5. 通过 API 创建/更新 Engine 及 EngineVersion 记录

SSH 和 K8s 共用同一套引擎镜像、同一套上传通道和 Image Registry 配置。

### 推理端点改动

#### 引擎镜像查找

查找流程与 K8s 一致，直接用加速卡类型作为 key：

```
1. cluster.Status.AcceleratorType → "nvidia_gpu"
2. engineVersion.GetImageForAccelerator("nvidia_gpu")
3. 找到 → 生成 runtime_env.container 配置
4. 未找到 → 失败
```

#### Engine 容器配置

配置流程：

```
1. 检查 SSH 集群类型
2. 从 cluster.Status.AcceleratorType 获取加速卡类型
3. 查找引擎镜像: engineVersion.GetImageForAccelerator(acceleratorType)
4. 未找到 → 返回错误
5. 构建镜像引用: {imageRegistry}/{imageName}:{tag}
6. 从 acceleratorMgr.GetEngineContainerRunOptions(acceleratorType) 获取加速卡 run_options
7. 追加模型缓存卷挂载（宿主机路径）
8. 返回 {"image": imageRef, "run_options": runOptions}
```

#### 加速卡特定的 Run Options

加速卡相关的 Docker 参数由加速卡插件系统提供，不在 orchestrator 里硬编码。

文件：`internal/accelerator/plugin/plugin.go` — `AcceleratorPluginHandle` 接口新增：

```go
// GetAcceleratorRuntimeConfig 返回该加速卡类型的静态 RuntimeConfig。
// 与 GetNodeRuntimeConfig 不同，此方法不需要 SSH 访问节点。
GetAcceleratorRuntimeConfig() v1.RuntimeConfig
```

文件：`internal/accelerator/manager.go` — `Manager` 接口新增：

```go
// GetEngineContainerRunOptions 返回 Engine 容器的 Docker run_options。
// 委托给已注册 plugin 的 GetAcceleratorRuntimeConfig()，将 RuntimeConfig 字段
// (Runtime, Options, Env) 转换为 Docker CLI 参数。
GetEngineContainerRunOptions(acceleratorType string) ([]string, error)
```

Manager 把 `RuntimeConfig` 转成 Docker run_options：
- `Runtime: "nvidia"` → `--runtime=nvidia`
- `Options: ["--gpus", "all"]` → `--gpus all`
- `Env: {"KEY": "val"}` → `-e KEY=val`

| 加速卡 | Plugin RuntimeConfig | 生成的 run_options |
|--------|---------------------|-------------------|
| NVIDIA GPU | `Runtime: "nvidia", Options: ["--gpus", "all"]` | `--runtime=nvidia --gpus all` |
| AMD GPU | `Runtime: "amd", Env: {"AMD_VISIBLE_DEVICES": "all"}` | `--runtime=amd -e AMD_VISIBLE_DEVICES=all` |

#### 卷挂载路径说明

DOOD 模式下需要注意路径问题。通过 docker.sock 创建的 Engine 容器由宿主机 Docker daemon 管理，卷挂载引用的是宿主机路径：

```
宿主机: /data/models → (docker -v) → Ray 容器: /home/ray/.neutree/models-cache/default
                    ↘ (docker -v) → Engine 容器: /home/ray/.neutree/models-cache/default

Ray 容器和 Engine 容器挂载相同的宿主机路径。
Engine 容器在相同的内部路径看到相同的模型文件。
```

`ModelCache.HostPath.Path`（如 `/data/models`）是宿主机路径。容器内挂载路径（`/home/ray/.neutree/models-cache/{name}`）是 Python 代码期望的路径。这个映射已在 `ClusterConfig.ModelCaches` 结构中可用。

#### NFS Model Registry 适配

问题：BentoML NFS 类型的 Model Registry 原来通过 `DockerNfsMounter` 在 Ray 容器内执行 `sudo mount -t nfs` 挂载 NFS。Engine 容器是宿主机上的兄弟容器，和 Ray 容器文件系统隔离，看不到 Ray 容器内的 NFS 挂载。

方案：用 Docker `--mount` NFS volume 选项，让 Docker daemon 在创建 Engine 容器时直接挂载 NFS。

版本分支：`isNewClusterVersion()` 辅助函数统一判断集群版本，在调用处做版本分支：

| 调用点 | <= v1.0.0（旧集群） | > v1.0.0（新集群） |
|--------|---------------------|---------------------|
| `CreateEndpoint()` | 调用 `connectSSHClusterEndpointModel(connect)` — 在 ray_container 内 NFS mount | 跳过（NFS 由 Engine 容器 run_options 处理） |
| `DeleteEndpoint()` | 调用 `connectSSHClusterEndpointModel(disconnect)` — 在 ray_container 内 NFS unmount | 跳过 |
| `EndpointToApplication()` | 跳过 `buildEngineContainerConfig()` | 调用 `buildEngineContainerConfig()` 构建 Engine 容器配置 |

这样旧集群不会触发 Engine 容器逻辑，新集群不会触发 ray_container 内的 NFS mount/unmount，也避免了不必要的 SSH 连接和 Ray 节点列表查询。

NFS 协议版本探测：Docker NFS volume mount 对协议版本要求严格（`volume-opt=type=nfs` vs `volume-opt=type=nfs4`），协议不匹配会导致容器启动失败。控制面在 `ModelRegistryController.Connect()` 阶段已经通过 `nfs.MountNFS()` 挂载了 NFS，内核协商的实际协议版本记录在 `/proc/mounts` 中。通过 `nfs.GetMountType()` 读取 `MountPoint.Type` 字段就能拿到协议版本，不需要额外的网络调用。

数据流：

```
CreateEndpoint() / DeleteEndpoint()
  └─ isNewClusterVersion(cluster)
       ├─ false (<=v1.0.0): connectSSHClusterEndpointModel() — ray_container 内 NFS mount
       └─ true  (> v1.0.0): 跳过

EndpointToApplication()
  └─ isNewClusterVersion(deployedCluster)
       ├─ false (<=v1.0.0): 跳过 buildEngineContainerConfig()
       └─ true  (> v1.0.0): buildEngineContainerConfig(..., modelRegistry)
            ├─ 检测 BentoML NFS model registry
            ├─ url.Parse(modelRegistry.Spec.Url) → 提取 server/path
            ├─ model_registry.NewModelRegistry() → registry.GetNFSType() → "nfs4"
            └─ run_options += --mount type=volume,
                 dst=/mnt/{workspace}/{endpoint},
                 volume-opt=type=nfs4,
                 volume-opt=o=addr={server},
                 volume-opt=device=:{path}
```

生成的 run_options 示例：

```
--mount type=volume,dst=/mnt/default/llama-endpoint,volume-opt=type=nfs4,volume-opt=o=addr=10.255.1.54,volume-opt=device=:/bentoml
```

---

## Ray Fork 注意事项

仓库：`neutree-ai/ray`（`ray-2.53.0-neutree` 分支）

### `container` 字段维护

`runtime_env.container` 在上游 Ray 中已弃用，但只有它支持通过 `run_options` 使用 GPU。Neutree Ray fork 需要：

1. 保留 `container` 字段的验证和执行逻辑
2. 抑制弃用警告（可选）
3. 确保 `run_options` 传递给容器 CLI

### Docker 后端扩展

上游 Ray 的 `runtime_env.container` 只支持 Podman。Neutree Ray fork 需要扩展容器后端，加上 Docker 支持：

- 在 `ray/_private/runtime_env/container.py` 中新增 Docker 后端
- 使用 `docker run` / `docker inspect` / `docker rm` 等标准 Docker CLI 命令
- 通过集群配置的 `container_runtime` 字段选择后端（当前默认 Docker）

需要测试的行为：
- 容器退出时的 `--rm` 行为
- 信号传播到 worker 进程
- Ray worker 崩溃时的容器清理

---


## 可观测性适配

### 现状

当前 Grafana 监控面板基于 vLLM v0.8.5 的 metrics 改造。vLLM 在版本迭代中会新增、重命名或移除 Prometheus metrics，不同版本暴露的指标集合不完全一致。

### 需要解决的问题

引入多引擎版本后，同一集群上可能同时运行 vLLM v0.11.2 和 v0.12.0，它们暴露的 metrics 可能存在差异：

- 指标名称变更（如 rename、新增 label）
- 新版本新增的指标在旧版本上不存在
- 旧版本的指标在新版本中被移除或替换

如果面板只适配单一版本的指标，会导致部分 panel 在其他版本的 Endpoint 上无数据或查询报错。

### 适配方案

1. 梳理 vLLM v0.8.5 → v0.11.2 → v0.12.0 之间的 metrics 变更，记录新增、重命名和移除的指标
2. 更新 Grafana 面板的 PromQL 查询，兼容多版本指标：
   - 对重命名的指标，使用 `or` 合并新旧指标名
   - 对新增的指标，panel 在旧版本 Endpoint 上显示为空而非报错
3. 考虑按引擎版本拆分面板行（row），或通过 Grafana 变量过滤 Endpoint 对应的引擎版本

---

## 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| `container` 字段从上游 Ray 移除 | Fork 需自行维护 | 在 fork 中固定行为；跟踪 `image_uri` GPU 修复 ([PR #60485](https://github.com/ray-project/ray/pull/60485)) |
| docker.sock 安全性（宿主机 Docker 访问） | 容器逃逸风险 | Ray 容器已以 `--privileged` 运行；相同信任边界 |
| Engine 镜像拉取延迟（首次部署） | Endpoint 启动慢 | 节点预拉取 Engine 镜像；在 Endpoint 状态中增加镜像拉取进度 |

## 参考

### Ray

- [Ray Runtime Environments](https://docs.ray.io/en/releases-2.53.0/ray-core/handling-dependencies.html#runtime-environments) — runtime_env 官方文档
- [runtime_env.container (Deprecated)](https://docs.ray.io/en/releases-2.53.0/ray-core/handling-dependencies.html#container-option-deprecated) — container 字段说明，2025.07 起弃用
- [runtime_env.image_uri (Experimental)](https://docs.ray.io/en/releases-2.53.0/ray-core/handling-dependencies.html#image-uri) — image_uri 字段说明
- [ray-project/ray#58399](https://github.com/ray-project/ray/issues/58399) — image_uri 不支持 GPU 的 issue
- [ray-project/ray#60485](https://github.com/ray-project/ray/pull/60485) — image_uri GPU 支持修复 PR
- [`build_serve_application` 源码](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L1077) — Ray Serve application 构建入口，runtime_env 在这里传入
- [`apply_app_config` 源码](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/serve/_private/application_state.py#L651) — 提交带 runtime_env 的 app config
- [`container.py` 源码](https://github.com/ray-project/ray/blob/ray-2.53.0/python/ray/_private/runtime_env/container.py) — runtime_env container 插件实现（Podman 后端）

### Docker

- [Docker Volume — NFS](https://docs.docker.com/engine/storage/volumes/#create-a-service-which-creates-an-nfs-volume) — `--mount type=volume,volume-opt=type=nfs` 用法
- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/overview.html) — `--runtime=nvidia` 和 `--gpus` 参数

### Neutree

- [neutree-ai/ray](https://github.com/neutree-ai/ray) — Neutree 维护的 Ray fork（`ray-2.53.0-neutree` 分支）
