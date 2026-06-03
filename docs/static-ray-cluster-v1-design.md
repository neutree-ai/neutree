# Static Ray Cluster V1 Design

## 背景

当前 Ray 静态集群的生命周期、节点初始化、镜像预热、指标采集等逻辑主要由集群级 reconcile 串行推进。这样会带来两个问题：

- 单个节点 SSH 慢、镜像拉取失败、worker 启动失败，会阻塞整个集群同步。
- GPU、Node、Engine 指标在 Ray 与 Kubernetes 两种形态下来源和语义不统一，后续 dashboard、告警、调度很难稳定消费。

V1 目标是把静态 Ray 集群拆成独立的集群资源和节点资源，并建立“节点本地归一化 + head 汇聚”的 cluster-local metrics plane。

## 目标

- 引入 `StaticNodeCluster` 和 `StaticNode` 两个独立资源，分别承担集群级编排和节点级收敛。
- 将节点上的常驻组件抽象为 `NodeComponent`，第一版作为 `StaticNode` 的嵌入式工作负载。
- 静态集群 V1 不再依赖 `ray up` 启动 head node，head 和 worker 都由 `StaticNodeReconciler` 收敛。
- 每个 `StaticNode` 上运行 daemonset-like 的 `neutree-metrics` component。
- `neutree-metrics` 只暴露 `/health` 和 `/metrics` 两个 API。
- 当 `/metrics` 被调用时，`neutree-metrics` 采集本机 node-exporter 和 accelerator exporter 指标，归一化后返回 `neutree_*` canonical metrics。
- head node 上运行 `vmagent`，由它 scrape 每个节点的 `neutree-metrics`，并 remote write。
- 引入 `StaticNode.spec.warm` 语义，由 `StaticNodeReconciler` 负责预热节点资源。V1 仅实现 image warm。
- 引入 optional `AcceleratorProfile`，让 accelerator plugin 声明 runtime 和资源转换相关能力。
- accelerator exporter 镜像、端口、run options 等部署资产由 accelerator plugin profile 声明。
- Neutree metrics 组件作为独立 daemonset-like 组件负责节点级指标归一化，并内置维护 canonical metrics mapping。
- 保留现有 `ResourceConverter` / `ResourceParser`，复杂资源转换不强行 profile 化。

## 非目标

- V1 不把 `NodeComponent` 做成独立资源。
- V1 不实现 vmagent HA。
- V1 不实现节点本地 agent。
- V1 不实现通用 component 插件系统。
- V1 不复用 Ray autoscaler 的 node provider / local cluster state 作为静态节点生命周期来源。
- V1 不支持 Ray autoscaler 管理静态节点，不依赖 `ray_bootstrap_config.yaml` / `ray_bootstrap_key.pem`。
- V1 不实现 model warm，只预留语义。
- V1 不做全量指标归一化，只先覆盖基础 Node、GPU canonical metric contract。
- V1 `neutree-metrics` 不处理 Ray / Engine 指标归一化，Ray / Engine 指标先保留 raw scrape 或现有 bridge 机制。
- V1 不让 accelerator plugin 维护 `neutree_*` canonical metrics mapping 或 normalization rules。
- V1 不做自动 rollback。

## 资源关系

```text
StaticNodeCluster
  -> StaticNode[]
      -> NodeComponent[]
```

`StaticNodeCluster` 和 `StaticNode` 都是一等资源，拥有独立的 `spec`、`status`、`generation`、`conditions` 和 reconcile queue。

`NodeComponent` 第一版嵌入 `StaticNode.spec` / `StaticNode.status`。

## StaticNodeCluster

`StaticNodeCluster` 表示一个静态 Ray 集群的集群级期望状态。

主要职责：

- 管理集群版本、镜像仓库、remote write 地址。
- 管理 head node 选择和静态节点列表。
- 生成或更新对应的 `StaticNode` 资源。
- 聚合 `StaticNode.status`，计算集群 phase。
- 生成每个节点的 `neutree-metrics` 配置和 head-local `vmagent` 配置。
- 推进升级状态机。

示例结构：

```yaml
spec:
  workspace: default
  version: v1.2.0
  image_registry: registry.example.com
  metrics_remote_write_url: http://vm:8480/insert/0/prometheus/
  head:
    node_name: head-0
  nodes:
    - name: head-0
      ip: 10.0.0.10
      role: head
    - name: worker-0
      ip: 10.0.0.11
      role: worker
  upgrade_strategy:
    stop_start: true
status:
  phase: Ready
  desired_nodes: 2
  ready_nodes: 2
  head_ready: true
  metrics_ready: true
```

## StaticNode

`StaticNode` 表示静态集群中的一个节点。

主要职责：

- 保存单节点期望状态。
- 保存该节点应该运行的 `NodeComponent` 列表。
- 保存节点 warm 状态和 component 健康状态。
- 独立执行 SSH、Docker、image pull、component reconcile。

示例结构：

```yaml
spec:
  cluster: static-node-cluster-a
  ip: 10.0.0.10
  role: head
  accelerator_type: nvidia_gpu
  ssh_auth_ref: cluster-a-ssh
  warm:
    images:
      - name: ray-runtime
        ref: registry.example.com/neutree/serve:v1.2.0
        required: true
      - name: engine-vllm
        ref: registry.example.com/neutree/engine-vllm:v0.11.2-ray2.53.0
        required: false
  components:
    - name: ray-head
      type: ray-head
    - name: node-exporter
      type: node-exporter
    - name: dcgm-exporter
      type: accelerator-exporter
    - name: vmagent
      type: metrics-agent
    - name: neutree-metrics
      type: metrics-normalizer
status:
  phase: Ready
  warm:
    ready: true
  components:
    ray-head:
      ready: true
    node-exporter:
      ready: true
    dcgm-exporter:
      ready: true
    vmagent:
      ready: true
    neutree-metrics:
      ready: true
```

## NodeComponent

`NodeComponent` 是 Neutree 在节点上管理的常驻工作负载。

V1 支持以下 component type：

- `ray-head`
- `ray-worker`
- `node-exporter`
- `accelerator-exporter`
- `metrics-agent`
- `metrics-normalizer`

`NodeComponentSpec` 应表达：

- `name`
- `type`
- `image`
- `command`
- `args`
- `env`
- `ports`
- `volumes`
- `docker_run_options`
- `config_files`
- `health_check`
- `dependencies`
- `restart_policy`
- `config_hash`

`NodeComponentStatus` 应表达：

- `ready`
- `phase`
- `observed_hash`
- `observed_image`
- `reason`
- `message`
- `last_transition_time`

## StaticNode.spec.warm

`StaticNode.spec.warm` 表示节点需要提前准备好的资源集合。

V1 只实现 image warm：

```yaml
spec:
  warm:
    images:
      - name: ray-runtime
        ref: registry.example.com/neutree/serve:v1.2.0
        required: true
```

未来可扩展：

- `warm.models`
- `warm.files`
- `warm.volumes`
- `warm.runtime_dependencies`

`StaticNodeClusterReconciler` 负责生成每个节点的 `spec.warm`。

`StaticNodeReconciler` 负责执行：

- `docker image inspect`
- 缺失时 `docker pull`
- 记录 digest、phase、reason

`StaticNodeClusterReconciler` 聚合 `StaticNode.status.warm`，required warm 全部 ready 后才能进入 stop/start 阶段。

## Reconcile 分层

### StaticNodeClusterReconciler

集群级 reconcile 不执行长耗时 SSH 操作。

流程：

```text
1. 读取 StaticNodeCluster。
2. 根据 spec 生成 StaticNode desired state。
3. 创建或更新 StaticNode。
4. 删除不再需要的 StaticNode。
5. 聚合 StaticNode.status。
6. 计算 StaticNodeCluster.status.phase。
7. 生成或触发每个节点的 `neutree-metrics` 配置和 head-local `vmagent` 配置 reconcile。
8. 推进升级状态机。
```

### StaticNodeReconciler

节点级 reconcile 独立处理单个节点。

流程：

```text
1. SSH 到节点。
2. 检查 Docker/runtime。
3. 执行 docker login。
4. 收敛 spec.warm。
5. 根据 components 计算 config hash。
6. 创建、重建或检查 NodeComponent。
7. 执行 component health check。
8. 更新 StaticNode.status。
```

## Ray Up 拆解

当前 SSH Ray 集群中，head node 由 `ray up` 处理，worker node 已经由 Neutree 通过 SSH + DockerCommandRunner 独立启动。改成 `StaticNode` 模型后，head node 也应该走与 worker 相同的节点级 reconcile，不再调用 `ray up`。

本节按 Ray 2.53.0 autoscaler 源码分析，主要涉及：

- `python/ray/autoscaler/_private/commands.py`
- `python/ray/autoscaler/_private/updater.py`
- `python/ray/autoscaler/_private/command_runner.py`
- `python/ray/autoscaler/_private/docker.py`
- `python/ray/autoscaler/_private/local/node_provider.py`

当前 `ray up` head 流程主要做了这些事情：

- 读取 Ray cluster YAML，处理 CLI override。
- `prepare_config` 填默认值、合并 setup commands、校验 docker config、补齐 node type 资源信息。
- provider bootstrap，并按 config hash 做本地 provider config cache。
- 生成本地 Ray cluster config 和临时 SSH key。
- 通过 provider 查询或创建 head node，并维护 provider tags。
- 基于 accelerator runtime 修改 head Docker image / runtime / env / run options。
- 执行 `ray up --disable-usage-stats --no-config-cache -y -v`，初始化 head。
- 首次初始化默认带 `--no-restart`，升级时允许 restart。
- 通过 Ray autoscaler local provider 启动或复用 head Docker container。
- 在 head container 内执行 `HeadStartRayCommands`。
- 将 `ray_bootstrap_config.yaml`、`ray_bootstrap_key.pem` 等 Ray bootstrap 文件放到 Ray 预期位置，用于 head 上的 autoscaler/monitor。
- 计算 runtime hash 和 file mounts contents hash，用于跳过 file sync / initialization / setup。
- 等 SSH ready，并把节点状态标记为 waiting/syncing/setting-up/up-to-date。
- 处理 file_mounts 和 cluster_synced_files，必要时通过 rsync 同步到 host 或 container。
- Docker run 前检查当前 container image 和 bind mounts，不匹配时 stop/recreate。
- 执行 docker login、model cache 目录创建和权限修正。
- 启动 Ray head：GCS 6379、dashboard 8265、client 10001、raylet metrics 54311 等固定端口。
- 给 head raylet 写入 Neutree 版本 label。
- 启动后通过 dashboard API 和 head raylet 状态做健康检查。

Ray `up` 的源码链路：

```text
ray up
  -> create_or_update_cluster
      -> load yaml
      -> apply CLI overrides
      -> _bootstrap_config
          -> prepare_config / validate_config
          -> provider.bootstrap_config
          -> optional config cache
      -> get_or_create_head_node
          -> provider.non_terminated_nodes(head tag)
          -> hash_launch_conf(head node config + auth)
          -> create or reuse head node
          -> hash_runtime_conf(file_mounts + config)
          -> _set_up_config_for_head_node
          -> NodeUpdaterThread(head).do_update
```

`NodeUpdater.do_update` 的关键步骤：

```text
1. set node status = waiting-for-ssh.
2. wait SSH ready by running uptime on host.
3. compare runtime hash and file_mounts hash.
4. sync file_mounts / cluster_synced_files by rsync when needed.
5. run initialization_commands on host when runtime hash changed.
6. run command_runner.run_init:
   - check docker installed.
   - pull image.
   - check running container image and bind mounts.
   - stop/recreate container when image or mounts are stale.
   - copy bootstrap config/key into container specially.
7. run setup_commands in auto env.
8. run ray_start_commands in auto env.
9. set node status = up-to-date and store runtime hash/file hash tags.
```

Ray DockerCommandRunner 的额外处理：

- file mounts 先同步到宿主机的 Docker mount staging 目录。
- 普通 file mounts 会作为 bind mounts 进入 container，或者在 container 已运行时 rsync 进 container。
- `~/ray_bootstrap_config.yaml` 和 `~/ray_bootstrap_key.pem` 不作为普通 bind mount 处理，而是在 container 内显式 rsync/copy。
- 自动检查 Docker image 是否和期望一致。
- 自动检查 bind mounts 是否满足期望，不满足则重新初始化 container。
- 自动读取 image 的 HOME，用于展开 container 内 `~`。
- 自动探测 nvidia container runtime，必要时补 `--runtime=nvidia`。
- 自动计算 shm size，除非配置禁用或用户已经指定 `--shm-size`。

V1 中应该保留的语义：

- accelerator runtime 对 Ray runtime container 的影响。
- Ray runtime image、container name、host network、ulimit、shm、DOOD 相关 Docker run options。
- docker login、model cache host 目录初始化、权限修正。
- `ray stop` 后再启动，避免旧 raylet/GCS/dashboard 残留。
- head start command 的端口、metrics port、dashboard port、Ray client port、labels。
- dashboard API 和 head raylet alive 双层健康检查。
- upgrade 中的 warm、stop、start、verify 阶段语义。
- 组件配置远程写入，例如 vmagent scrape config、`neutree-metrics` config。
- runtime config hash 和 file config hash，用于判断是否重启 worker。
- container image / bind mounts drift 检测。

V1 中应该移除或弱化的 `ray up` 语义：

- 本地 Ray cluster config 临时目录作为事实状态。
- `ray_bootstrap_config.yaml` / `ray_bootstrap_key.pem` 作为 head 必需配置。
- Ray autoscaler local provider 负责启动 head 的语义。
- `ray up --no-config-cache` / `--no-restart` 作为幂等控制手段。
- `ray down` 作为全局 stop 手段。
- Ray autoscaler 管理静态 worker 生命周期。
- `NodeProvisionStatus` 作为塞在 `Cluster.status` 中的静态节点事实状态。
- Ray provider tag 作为节点状态来源。
- Ray config cache 作为 provider bootstrap 结果缓存。
- Ray CLI prompt、usage prompt、useful commands 输出等交互行为。

差异矩阵：

| 当前 `ray up` head 行为 | 当前代码来源 | 是否必要 | StaticNode V1 处理方式 |
| --- | --- | --- | --- |
| YAML load、CLI override、prepare/validate/bootstrap config、config cache | Ray `create_or_update_cluster` / `_bootstrap_config` | 部分必要 | Neutree 自己已经有 API schema 和 resource model；只保留必要默认值计算，不需要 Ray config cache |
| provider 查询/创建 head，并打 launch/status/name tags | Ray `get_or_create_head_node` | 不必要 | `StaticNodeCluster` 创建 `StaticNode(head)`；状态下沉到 `StaticNode.status` |
| launch hash 判断是否重建 head | Ray `hash_launch_conf` + provider tags | 必要但换实现 | 用 `NodeComponent.config_hash` / `observed_hash` 表达，不依赖 provider tags |
| runtime hash / file_mounts contents hash | Ray `hash_runtime_conf` | 必要但换实现 | 用 component config hash 覆盖 command、image、run options、config_files 内容 |
| 等 SSH ready，状态从 waiting-for-ssh 到 syncing/setting-up/up-to-date | Ray `NodeUpdater.do_update` | 必要 | `StaticNode.status.conditions` 表达 SSHReady、FilesReady、ComponentsReady |
| 生成本地 Ray cluster config、临时 SSH key、local state | `generateConfig` / `raySSHLocalConfigGenerator` | 部分必要 | SSH key 仍需要本地临时文件用于 Neutree 自己 SSH；Ray cluster config 不再作为生命周期事实状态，也不写入 head |
| `_set_up_config_for_head_node` 改写 head 上使用的 autoscaler config、注入 bootstrap config/key | Ray `_set_up_config_for_head_node` | 不必要 | V1 不支持 Ray autoscaler，因此不生成 `ray_bootstrap_config.yaml` / `ray_bootstrap_key.pem` |
| file_mounts / cluster_synced_files rsync | Ray `NodeUpdater.sync_file_mounts` | 部分必要 | model cache host path 已由 Neutree 管；组件配置使用 command_runner file interface；无需保留通用 Ray file_mounts 语义 |
| 根据 accelerator 修改 Docker image suffix、runtime、env、options | `buildAcceleratorDockerConfig` | 必要 | 迁移为 `ray-head` / `ray-worker` component 的 Docker config 生成逻辑，继续通过 AcceleratorManager 获取 runtime config |
| 执行 `ray up --disable-usage-stats --no-config-cache -y -v` | `upCluster` | 不必要 | 删除。StaticNode 直接 SSH 到节点，执行 Docker 初始化和 Ray start command |
| `--no-restart` 控制是否重启 head | `upCluster(restart)` | 不必要 | 由 `config_hash` / `observed_hash` 控制是否重建 component；需要重启时显式 stop/start |
| Ray local provider 启动 head Docker container | Ray autoscaler 内部 | 不必要 | 由 `DockerCommandRunner.RunInit` 或新的 NodeComponent runtime 负责启动容器 |
| Docker image pull / inspect / container running check | Ray DockerCommandRunner / Neutree `DockerCommandRunner.RunInit` | 必要 | 复用到 head；`spec.warm.images` 负责预拉取，RunInit 保底检查 |
| container image / bind mounts drift 检查，不匹配时 stop/recreate | Ray DockerCommandRunner | 必要 | Neutree DockerCommandRunner 需要补齐 image/mount drift 检查，或者由 component config hash 驱动 recreate |
| bootstrap config/key 特殊 copy 到 container | Ray DockerCommandRunner | 不必要 | V1 不支持 Ray autoscaler，不需要把 Ray bootstrap config/key 写入 container |
| 自动 nvidia runtime 探测 | Ray DockerCommandRunner | 不建议保留 | 改由 accelerator plugin profile / RuntimeConfig 决定，避免 Ray 自动探测和 plugin 冲突 |
| host 初始化命令，例如 docker login、model cache host 目录创建 | `InitializationCommands` / `mutateModelCaches` | 必要 | StaticNode 在 host 环境执行，失败只影响对应节点 |
| 容器内 docker login，用于 DOOD runtime_env 拉 engine image | `HeadStartRayCommands` / `StaticWorkerStartRayCommands` prepend | 必要 | 作为 `ray-head` / `ray-worker` container init command 保留 |
| Docker run options：host network、ulimit、RAY env、docker.sock、/tmp、pid/ipc、旧版本 privileged | `generateRayClusterConfig` | 必要 | 迁移为 Ray runtime worker 的 run options；按版本和 accelerator profile 合成 |
| head start command：`python /home/ray/start.py --head`、GCS/dashboard/client/metrics ports、labels | `HeadStartRayCommands` | 必要 | 作为 `ray-head` worker command 保留 |
| worker start command：`python /home/ray/start.py --address=$RAY_HEAD_IP:6379`、static label | `StaticWorkerStartRayCommands` | 必要 | 作为 `ray-worker` worker command 保留 |
| Ray autoscaling config：`--autoscaling-config=~/ray_bootstrap_config.yaml` | `HeadStartRayCommands` | 不必要 | V1 head start command 移除 `--autoscaling-config`；静态 worker 由 `StaticNodeReconciler` 启动 |
| autoscaler v2 env：`RAY_enable_autoscaler_v2`、`RAY_CLOUD_INSTANCE_ID`、`RAY_NODE_TYPE_NAME` | Ray `get_or_create_head_node` | 不必要 | V1 不支持 Ray autoscaler，不注入这些 env |
| usage stats env 注入 | Ray `NodeUpdater` start phase | 不必要 | Neutree 统一禁用或显式配置，不保留 Ray CLI prompt 逻辑 |
| dashboard API 健康检查和 alive head raylet 检查 | `checkHeadNodeHealth` / `initHeadNode` | 必要 | 保留为 `ray-head` component health check 和 StaticNodeCluster verify |
| `ray down` 停整个集群 | `downCluster` | 不必要 | StaticNodeCluster 推进 StopCluster；StaticNode 分别停止 `ray-worker`、`ray-head` |
| `NodeProvisionStatus` 聚合在 Cluster status | `reconcileWorkerNode` / `setNodePrivisionStatus` | 不必要 | 下沉到 `StaticNode.status.components[*]` 和 `StaticNode.status.phase` |

初步结论：

- `ray up` 本身不是必须的，必须保留的是它背后的 Docker/Ray 启动参数、初始化命令和健康检查。
- 当前 worker 启动路径已经接近 StaticNode 语义，可以作为实现基础。
- 不支持 Ray autoscaler 后，head 节点不需要远程写入 Ray bootstrap/config file。
- head 节点仍需要补 Ray DockerCommandRunner 的 container drift 检查，或者由 Neutree component config hash 显式驱动 recreate。
- Ray autoscaler local provider、local cluster state、`ray up --no-restart`、`ray down` 不应继续作为 V1 静态集群核心语义。
- 远程写配置仍然需要，但目标是 vmagent、`neutree-metrics`、NodeComponent env/config，不是 Ray autoscaler bootstrap。

替代关系：

```text
ray up head
  -> StaticNode(head).components[ray-head]

ray up generated config files
  -> V1 drops Ray bootstrap config files
  -> StaticNode component config_files only for Neutree-managed components

ray down
  -> StaticNodeCluster phase StopCluster
  -> StaticNode component stop

ray up --no-restart
  -> config_hash / observed_hash
  -> only restart changed components
```

head node 的 `ray-head` worker 启动流程：

```text
1. SSH to head node.
2. Check Docker/runtime.
3. Run host initialization commands.
4. Warm required images.
5. Write Neutree-managed component config files, if the component has config_files.
6. Start or recreate ray runtime container.
7. Run container initialization commands.
8. Run ray stop.
9. Run head start command.
10. Verify dashboard and alive head raylet.
11. Update StaticNode.status.components[ray-head].
```

worker node 的 `ray-worker` 继续使用类似流程，只是 start command 使用 `--address=<head_ip>:6379`。

## Head 与 Worker 启动差异

在 V1 不支持 Ray autoscaler 的前提下，head node 和 worker node 的启动流程应该尽量统一。

统一流程：

```text
1. SSH to node.
2. Check Docker/runtime.
3. Run host initialization commands.
4. Warm required images.
5. Write Neutree-managed config files, if the component has config_files.
6. Start or recreate Ray runtime container.
7. Run container initialization commands.
8. Run ray stop.
9. Run role-specific Ray start command.
10. Run role-specific health check.
11. Update StaticNode.status.components[*].
```

必要差异：

| 项 | Head | Worker |
| --- | --- | --- |
| Ray start command | `python /home/ray/start.py --head --port=6379 --dashboard-host=0.0.0.0 ...` | `python /home/ray/start.py --address=<head_ip>:6379 ...` |
| autoscaler config | V1 不传 `--autoscaling-config` | 不涉及 |
| Ray role | 创建 GCS、dashboard、Ray client server、head raylet | 加入已有 head |
| labels | cluster version label | static node label + cluster version label |
| required ordering | 必须先于 worker ready | 必须等 head GCS/dashboard ready 后启动 |
| health check | dashboard API reachable + alive head raylet | Ray dashboard `ListNodes` 中该 node alive |
| failure impact | `StaticNodeCluster` not ready | `StaticNodeCluster` degraded，不阻塞其他节点 reconcile |

可以统一的差异：

- Docker image、container name、run options 生成。
- accelerator runtime/env/options 注入。
- docker login 和 model cache host 初始化。
- image warm。
- `ray stop` before start。
- container create/recreate 策略。
- config hash / observed hash 判断。
- component status 更新。

head node 额外运行的 `vmagent` 不是 `ray-head` 启动差异，而是 head 节点上多一个 `metrics-agent` NodeComponent。

结论：

- V1 不应保留两套 Ray 启动流程。
- 应实现一个通用 `RayRuntimeComponentReconciler`，根据 `role=head|worker` 生成不同 Ray start command 和 health check。
- head 与 worker 的核心差异是 Ray role，不是节点初始化或 Docker 生命周期。

## Command Runner File Interface

当前 `command_runner` 主要提供远程命令执行能力，`SSHCommandRunner.Run` 和 `DockerCommandRunner.Run` 都以 command string 为中心。新架构需要把组件配置写到远端节点，例如：

- head node 上的 vmagent scrape config。
- 每个节点上的 `neutree-metrics` config。
- NodeComponent 的环境文件、启动配置和健康检查配置。

V1 不支持 Ray autoscaler，因此不需要远程写 Ray `ray_bootstrap_config.yaml` / `ray_bootstrap_key.pem`。

建议在 command_runner 上封装远程文件接口，而不是在各个 reconciler 中拼 heredoc / echo。

接口示意：

```go
type FileClient interface {
    WriteFile(ctx context.Context, path string, content []byte, opts WriteFileOptions) error
    WriteFileIfChanged(ctx context.Context, path string, content []byte, opts WriteFileOptions) (changed bool, err error)
    ReadFile(ctx context.Context, path string) ([]byte, error)
    Stat(ctx context.Context, path string) (*FileInfo, error)
    Remove(ctx context.Context, path string) error
}

type WriteFileOptions struct {
    Mode         string
    Owner        string
    Group        string
    Sudo         bool
    Atomic       bool
    CreateParent bool
}
```

实现建议：

- `SSHCommandRunner` 提供 `Files() FileClient`。
- `DockerCommandRunner` 暴露 host file client，用于写宿主机上的配置文件。
- 大文件使用本地临时文件 + `scp` 上传，避免命令行长度限制。
- 小文件也可以复用同一套上传路径，不为 heredoc 单独开分支。
- 写入流程默认是 atomic：上传到远端临时路径，再 `install` / `mv` 到目标路径。
- `WriteFileIfChanged` 先比较远端 sha256，不变则不写文件，不触发 component restart。
- 支持 `Sudo=true`，用于写 `/etc/neutree/`、`/opt/neutree/` 等 root-owned 路径。
- 所有文件写入都进入 `config_hash`，StaticNode 通过 hash 决定是否重建对应 worker。

典型调用：

```text
StaticNodeClusterReconciler renders vmagent scrape config
  -> StaticNode(head).components[vmagent].config_files
  -> StaticNodeReconciler
  -> command_runner.Files().WriteFileIfChanged(...)
  -> restart vmagent only when config changed
```

`neutree-metrics` 配置也走相同路径：

```text
StaticNodeClusterReconciler renders per-node neutree-metrics config
  -> StaticNode(node).components[neutree-metrics].config_files
  -> StaticNodeReconciler writes file on that node
  -> restart neutree-metrics only when config changed
```

## Metrics Plane

V1 使用 daemonset-like 的节点级归一化模型：

- 每个 `StaticNode` 运行一个 `neutree-metrics` component。
- head node 运行一个 `vmagent` component。
- `vmagent` scrape 所有节点的 `neutree-metrics /metrics`，并 remote write。

数据流：

```text
node-local node-exporter / accelerator exporter
  -> node-local neutree-metrics /metrics
  -> head-local vmagent
  -> remote_write
  -> central VictoriaMetrics or external backend
  -> dashboard / alert / API
```

`neutree-metrics` 不主动 remote write，也不需要后台持续采集。它只在 `/metrics` 被调用时执行一次本机采集和归一化。

V1 可保留 raw metrics scrape：

```text
Ray metrics / node-exporter / accelerator exporter / engine metrics
  -> head-local vmagent
  -> remote_write
```

raw metrics 用于排障和过渡。dashboard、告警、API 默认消费 `neutree_*` canonical metrics。Ray / Engine 指标 V1 不由 `neutree-metrics` 归一化。

head node 运行：

- `ray-head`
- `node-exporter`
- accelerator exporter, if applicable
- `neutree-metrics`
- `vmagent`

worker node 运行：

- `ray-worker`
- `node-exporter`
- accelerator exporter, if applicable
- `neutree-metrics`

`neutree-metrics` 只暴露：

- `GET /health`
- `GET /metrics`

`/health` 用于进程存活和基础配置检查。

`/metrics` 返回 Prometheus text format。每次被调用时，它在本机执行：

```text
1. scrape local node-exporter.
2. scrape local accelerator exporter, if configured.
3. normalize labels and units.
4. return neutree_* canonical metrics.
```

`vmagent` 至少 scrape 每个节点：

```text
job=neutree-metrics
source=neutree-metrics
target=<node_ip>:<neutree_metrics_port>
```

如果开启 raw metrics，也 scrape Ray、node-exporter、accelerator exporter 和 engine raw targets。

统一 labels：

- `workspace`
- `static_node_cluster`
- `cluster_type=ray`
- `node`
- `node_ip`
- `node_role`
- `source`

## Neutree Metrics Component

V1 需要基础 metrics mapping，但 mapping 不放在 accelerator plugin 中维护，而是由独立的 `neutree-metrics` daemonset-like 组件内置维护并在 `/metrics` 被调用时执行归一化。

原因：

- metrics mapping 是观测平面的产品语义，应该由 Neutree 统一定义和演进。
- dashboard、告警、API 查询需要消费稳定的 `neutree_*` canonical metrics。
- plugin 更适合处理 accelerator 识别、runtime 和资源转换，不适合承担观测规则维护。
- plugin 可以决定 accelerator exporter 的镜像、端口和运行参数。
- `neutree-metrics` 负责按请求采集本机 raw target、执行 label 规范化、单位转换、指标重命名和必要聚合。
- `vmagent` 负责把 `neutree-metrics` 输出的 canonical metrics remote write 到后端。

`neutree-metrics` 组件维护 mapping registry：

```text
node-exporter -> node metrics mapping
accelerator_type/exporter_kind -> accelerator metrics mapping
```

示例：

```yaml
accelerator_profile:
  accelerator_type: nvidia_gpu
  metrics:
    exporter:
      kind: dcgm-exporter
      component_type: accelerator-exporter
      image: nvcr.io/nvidia/k8s/dcgm-exporter:3.3.9-3.6.1-ubuntu22.04
      port: 9400
      docker_run_options:
        - --net=host
        - --gpus all
        - --cap-add=SYS_ADMIN

neutree_metrics_component:
  node_mappings:
    - canonical: neutree_node_memory_used_bytes
      raw: node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes
      transform: expression
  accelerator_mappings:
    nvidia_gpu:
      exporter_kind: dcgm-exporter
      mappings:
        - canonical: neutree_gpu_utilization_ratio
          raw: DCGM_FI_DEV_GPU_UTIL
          transform: divide_by_100
        - canonical: neutree_gpu_memory_used_bytes
          raw: DCGM_FI_DEV_FB_USED
          transform: mib_to_bytes
```

`StaticNodeClusterReconciler` 根据节点的 `accelerator_type` 和 accelerator profile 生成：

- `StaticNode.spec.components` 中的 exporter component。
- 每个节点的 `neutree-metrics` 配置。
- head-local `vmagent` 配置。

`neutree-metrics /metrics` 运行时负责：

- scrape 本机 node-exporter。
- scrape 本机 accelerator exporter, if configured。
- 为 raw series 补齐统一 labels。
- 将 raw metrics 转成 `neutree_*` canonical metrics。
- 返回 canonical metrics。

API contract：

- `GET /health`：只做进程存活和配置检查，不触发昂贵 exporter scrape。
- `GET /metrics`：触发一次本机 exporter scrape，返回 Prometheus text format。
- `/metrics` 的 scrape timeout 应小于 vmagent scrape timeout。
- 单个 exporter scrape 失败时，返回已成功归一化的指标，并输出 `neutree_metrics_scrape_up{target=...}=0`。
- node-exporter 是 V1 必需依赖；accelerator exporter 是按 `accelerator_type` 和 profile 决定的可选依赖。

`vmagent` 运行时负责：

- scrape 每个节点的 `neutree-metrics /metrics`。
- remote write canonical metrics。
- 按配置可选 scrape raw metrics。

未知 accelerator 的处理：

- 如果 profile 未声明 `metrics.exporter`，不部署 accelerator exporter。
- 如果 profile 声明了 `metrics.exporter`，可以部署 exporter 并 scrape raw metrics。
- 如果 `neutree-metrics` 没有对应 mapping，不生成 `neutree_*` accelerator canonical metrics。
- 保留 Ray / Engine raw metrics；按配置也可以保留 node-exporter / accelerator exporter raw metrics。
- `StaticNode.status` 标记 `AcceleratorMetricsReady=False`，reason 为 `UnsupportedAcceleratorMetrics`。
- 不影响 Ray 集群启动和 endpoint 部署。

## AcceleratorProfile

现有 accelerator plugin 已支持：

- `GetNodeAccelerator`
- `GetNodeRuntimeConfig`
- `GetContainerRuntimeConfig`
- `ResourceConverter`
- `ResourceParser`

V1 新增 optional `AcceleratorProfile`，用于描述 accelerator runtime、资源转换能力和 metrics exporter 部署资产。

示例：

```yaml
accelerator_type: nvidia_gpu
cluster_runtime:
  image_suffix: ""
endpoint_runtime:
  runtime: nvidia
  options:
    - --gpus all
  env:
    ACCELERATOR_TYPE: gpu
metrics:
  exporter:
    kind: dcgm-exporter
    component_type: accelerator-exporter
    image: nvcr.io/nvidia/k8s/dcgm-exporter:3.3.9-3.6.1-ubuntu22.04
    port: 9400
    docker_run_options:
      - --net=host
      - --gpus all
      - --cap-add=SYS_ADMIN
resource_defaults:
  ray_resource_name: GPU
  kubernetes_resource_name: nvidia.com/gpu
```

调用边界：

```text
EndpointReconciler / StaticNodeClusterReconciler / StaticNodeReconciler
  -> AcceleratorManager
      -> cached AcceleratorProfile
      -> internal or external accelerator plugin
```

外部 plugin 只提供能力声明和资源转换逻辑，不执行 SSH，不启动容器，不参与实际 reconcile。

accelerator exporter 镜像、端口和运行参数通过 plugin profile 下发。`StaticNodeClusterReconciler` 负责把 profile 转成 `NodeComponent` 和组件配置。`neutree-metrics` 根据 `accelerator_type` / `exporter_kind` 执行 canonical metrics mapping。

## Profile 与 Converter 边界

`AcceleratorProfile` 是描述型能力。

适合表达：

- image suffix
- runtime/env/options
- metrics exporter image/port/run options
- 简单 resource defaults

不适合表达：

- node component templates
- metric mappings
- vmagent scrape config
- normalization rules

`ResourceConverter` / `ResourceParser` 是行为型能力。

继续负责：

- product group 转换
- 多产品型号映射
- MIG/topology/NUMA 等复杂规则
- 复杂 validation/fallback
- 旧 schema 兼容

资源转换优先级：

```text
1. plugin ResourceConverter / ResourceParser
2. profile.resource_defaults generic converter
3. unsupported
```

## Endpoint 接入

Endpoint 需要 accelerator 信息，但不直接调用 plugin REST。

Endpoint 通过 `AcceleratorManager` 获取：

- endpoint runtime
- engine image variant keys
- env/options
- resource conversion

Endpoint 不消费：

- node exporter component
- vmagent component
- neutree-metrics component
- metrics scrape config

这些属于 `StaticNodeCluster` / `StaticNode`。

## 升级语义

保留当前升级语义：

```text
Warm -> StopCluster -> StartCluster -> Verify
```

新架构下，`StaticNodeCluster` 推进阶段，`StaticNode` 执行节点级动作。

### Warm

`StaticNodeClusterReconciler` 更新每个 `StaticNode.spec.warm`。

`StaticNodeReconciler` 独立拉取 images。

required image 全部 ready 后，集群进入 stop 阶段。

### StopCluster

V1 推荐顺序：

```text
1. stop workers
2. stop head
```

只停止受影响的 `ray-head` / `ray-worker`，不停止 node-exporter、accelerator exporter、`neutree-metrics`、vmagent，除非它们自身版本或配置发生变化。

### StartCluster

V1 推荐顺序：

```text
1. start head
2. wait head dashboard ready
3. start workers
4. wait workers alive
5. verify cluster
```

### 仅升级 Ray

Ray-only upgrade 只影响：

- `ray-head`
- `ray-worker`
- target Ray runtime image
- affected engine images, if needed for endpoint recovery

不影响：

- `node-exporter`
- accelerator exporter
- `neutree-metrics`
- `vmagent`

`neutree-metrics` 和 vmagent 继续运行，Ray metrics 在 stop/start 期间短暂缺失。

## 状态语义

`StaticNodeCluster.status.phase`：

- `Provisioning`
- `Warming`
- `Stopping`
- `Starting`
- `Verifying`
- `Ready`
- `Degraded`
- `Failed`

`StaticNode.status.phase`：

- `Pending`
- `Warming`
- `Reconciling`
- `Ready`
- `Degraded`
- `Failed`

故障语义：

- head Ray 不 ready：`StaticNodeCluster` not ready。
- 单个节点 `neutree-metrics` 不 ready：对应 `StaticNode` canonical metrics degraded。
- head vmagent 不 ready：`MetricsRemoteWriteReady=False`，metrics remote write degraded。
- 单个 node-exporter 不 ready：对应 `StaticNode` node metrics degraded。
- 单个 accelerator exporter 不 ready：对应 `StaticNode` accelerator metrics degraded。
- 单个 worker node 不 ready：`StaticNodeCluster` degraded，不阻塞其他节点。

## V1 任务拆分

1. 定义 `StaticNodeCluster` / `StaticNode` 资源模型。
2. 新增 optional `AcceleratorProfile`。
3. 定义 `NodeComponent` 嵌入模型。
4. 梳理并迁移 `ray up` head node 必需语义，移除不必要的 Ray autoscaler/local state 依赖。
5. 扩展 command_runner file interface，支持远程写文件和 `WriteFileIfChanged`。
6. 实现 `StaticNodeClusterReconciler`。
7. 实现 `StaticNodeReconciler`。
8. 实现 `ray-head` / `ray-worker` worker，由 `StaticNodeReconciler` 启停 Ray runtime container。
9. 实现 daemonset-like `neutree-metrics` worker 和每节点归一化配置下发。
10. 实现 head-local vmagent component 和 scrape config 下发。
11. 实现 `spec.warm.images`。
12. 接入 Endpoint accelerator profile。
13. 定义基础 metrics contract。
14. 实现 Neutree metrics component 内置 mapping，覆盖基础 Node、GPU 指标归一化。
15. 实现升级状态机：Warm、Stop、Start、Verify。

## V1 测试设计

### Unit test

- `command_runner`：
  - `WriteFileIfChanged` 在远端 hash 一致时不执行 `scp` 和 install。
  - `WriteFile` 支持创建父目录、上传临时文件、按 mode/owner/group install，并在 atomic 模式下通过 staged file + `mv` 替换。
  - Docker runtime init 支持传入 role-specific run options，head 启动只使用 `HeadRunOptions`，worker 启动继续使用 `WorkerRunOptions`。
- SSH Ray cluster：
  - head node 初始化、重建、升级启动不再调用 `ray up`，改为 `startHeadNode`。
  - `startHeadNode` 复用节点级 SSH + Docker 启动流程，执行 `InitializationCommands`、`RunInitWithRunOptions(HeadRunOptions)` 和 `HeadStartRayCommands`。
  - worker node 启动路径保持 `StaticWorkerStartRayCommands`，并继续注入 `RAY_HEAD_IP`。
  - 生成的 head Ray start command 不再包含 `--autoscaling-config=~/ray_bootstrap_config.yaml`。
  - head dashboard 验证失败时保留 port 8265 的可操作错误提示。
  - 版本升级流程保持 `PrePull -> Stop -> StartHead -> ReconcileWorker`，其中 StartHead 使用静态 head 启动流程。

### DB test

第一版不新增数据库 schema、RLS、trigger 或 migration 时，不需要新增 DB test。后续落地独立 `StaticNodeCluster` / `StaticNode` 持久化资源时，需要补充：

- `StaticNodeCluster` 与 `StaticNode` CRUD、workspace 隔离、权限校验。
- `StaticNode.status.components` 局部更新不会阻塞或覆盖 `StaticNodeCluster.status`。
- warm / component status 字段的 JSON schema 和查询过滤。

### E2E test

E2E 需要真实 SSH Ray 静态集群环境，开发完成后在该 gate 阻塞。第一版建议用 `cluster` label 增加或复用场景：

- 新建 1 head + 1 worker SSH Ray cluster，验证 head 启动过程中控制面不执行 `ray up`，Ray dashboard ready，worker ALIVE。
- 修改 Ray/serve image version，验证 `spec.warm.images` 或现有 PrePull 先拉取所有节点镜像，再 stop/start Ray。
- Ray-only upgrade 后，验证 exporter、`neutree-metrics`、vmagent 不被重启；如果第一版尚未落地这些 worker，则在 E2E 中记录为 blocked subcase。
- 验证 head start command 不依赖 Ray autoscaler config，head container 内不存在对 `ray_bootstrap_config.yaml` 的启动参数依赖。
- 验证恢复场景：占用 8265 或 dashboard 不可达时，Cluster status error message 包含 head IP 和 port 8265 提示。

## V1 验收标准

- `StaticNodeCluster` 更新不会被单个节点的 SSH、image pull、worker 启动阻塞。
- `StaticNode` 可独立 reconcile，并记录每个 component 的状态。
- head node 启动不再调用 `ray up`，而是通过 `StaticNode` 的 `ray-head` component 收敛。
- command_runner 支持远程写文件，vmagent 和 `neutree-metrics` 配置可通过 `config_files` 下发。
- 配置文件未变化时不重启对应 component。
- 每个节点上的 `neutree-metrics` 只暴露 `/health` 和 `/metrics`。
- 每个节点上的 `neutree-metrics /metrics` 能按请求 scrape 本机 node-exporter、accelerator exporter，并输出基础 `neutree_*` canonical metrics。
- head node 上 vmagent 能 scrape 所有节点的 `neutree-metrics /metrics` 并 remote write。
- 修改 `StaticNodeCluster.spec.version` 后，可执行 warm、stop、start、verify 流程。
- Ray-only upgrade 不重启 exporter、`neutree-metrics` 和 vmagent。
- accelerator plugin 可通过 profile 提供 runtime 配置。
- accelerator plugin 可通过 profile 提供 accelerator exporter 镜像、端口和运行参数。
- Neutree metrics component 可根据 accelerator type 和 exporter kind 生成基础 Node/GPU canonical metrics。
- 复杂资源转换继续通过 `ResourceConverter` / `ResourceParser` 完成。
