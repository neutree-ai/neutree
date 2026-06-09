# Kubernetes HAMi vGPU 后端设计

本文定义 Neutree 在 Kubernetes 集群中接入 HAMi vGPU 的后端设计。文档描述 API 契约、虚拟化语义、集群资源视图、Endpoint 资源视图、HAMi 组件、监控和约束边界，不展开底层解析器、适配器和聚合器的代码细节。

相关产品设计文档：`docs/k8s-vgpu-support-product-design.md`。

## 背景与目标

HAMi 接入后，Kubernetes 节点上的 `nvidia.com/gpu` 是 HAMi 底层调度数量，不等同于物理 GPU 数量。Neutree 需要在保留统一资源语义的前提下完成三件事：

- 集群层支持开启 NVIDIA GPU 虚拟化，并由 Neutree 管理 HAMi 组件。
- Endpoint 层使用 Neutree 资源字段描述 vGPU 需求，由后端转换为 HAMi 调度资源。
- 资源视图使用 Neutree 语义表达物理 GPU、显存、算力和 Endpoint 实际分配结果。

目标：

- Kubernetes 集群具备 GPU 虚拟化开关。
- 当前版本仅支持 NVIDIA GPU 虚拟化。
- HAMi scheduler、device plugin、monitor、webhook 由 Neutree 管理。
- GPU Operator device plugin 与 HAMi device plugin 冲突时阻止启用。
- 集群资源视图展示物理 GPU 数量，不展示 HAMi 底层调度数量。
- Endpoint 资源配置保留 GPU 数量、产品型号、显存 MiB、显存百分比、算力百分比。
- Endpoint 状态展示副本实际分配到的设备 UUID、显存和算力。
- HAMi 指标接入 Prometheus，并提供集群级与 Endpoint 级 Grafana Dashboard。

非目标：

- 非 Kubernetes 集群不支持 HAMi 虚拟化。
- 非 NVIDIA GPU 虚拟化不纳入该版本。
- NVIDIA MIG 虚拟化模式不纳入该版本。
- HAMi DRA 模式不纳入该版本。
- 非 Neutree 管理的 HAMi 组件不被接管。
- 数据库不承载 HAMi 专用校验逻辑。

## 设计原则

- 对外 API 只暴露 Neutree 对象语义，底层 Kubernetes、GPU Operator、HAMi 对象差异由后端封装。
- 加速器插件负责加速器能力判断、虚拟化配置生成和资源转换语义。
- ResourceClient 返回已经转换完成的集群、节点和 Endpoint 资源对象。
- 资源视图按集群、节点、Endpoint 拆分表达，设备明细仅出现在节点或 Endpoint 分配明细中。
- HAMi 是 NVIDIA GPU 虚拟化集成方案，虚拟化能力归属于 NVIDIA GPU 插件。
- 组件状态必须写回集群状态，失败原因必须可观测。

## 总体方案

控制面流程：

```text
ClusterSpec.accelerator_virtualization
  -> API middleware 校验
  -> 加速器插件解析集群虚拟化能力
  -> 生成候选节点、节点 label、HAMi config_patch
  -> 加速器虚拟化 Component 执行 preflight
  -> 配置候选节点虚拟化 label
  -> 渲染并应用 HAMi chart manifests
  -> 管理 scheduler TLS 和 webhook CA
  -> 检查 scheduler、device plugin、monitor、webhook 状态
  -> 写回 ClusterStatus.component_status.accelerator_virtualization
```

资源视图流程：

```text
Kubernetes Node / Pod / HAMi annotation
  -> ResourceClient 读取底层对象
  -> 加速器插件转换资源语义
  -> ClusterResource / NodeResource / EndpointResourceStatus
```

模块职责：

- Cluster API：保存虚拟化意图、组件状态、资源状态。
- Accelerator Plugin：提供虚拟化能力、节点筛选、配置补丁、资源转换。
- 加速器虚拟化 Component：管理 HAMi 生命周期和状态闭环。
- ResourceClient：屏蔽 Kubernetes 对象和 HAMi annotation 差异。
- Metrics Component：接入 HAMi monitor 指标，并部署 kube-state-metrics 提供 Endpoint 归属标签。
- Grafana Dashboard：提供集群级和 Endpoint 级监控视图。

## 集群虚拟化

### API 契约

Cluster Spec 字段：

| 字段 | 类型 | 语义 |
| --- | --- | --- |
| `spec.accelerator_virtualization.enabled` | boolean | 是否开启集群加速器虚拟化 |
| `spec.accelerator_virtualization.config_patch` | object | 后端和加速器插件使用的虚拟化方案配置补丁，不作为产品页面输入 |

请求示例：

```json
{
  "spec": {
    "accelerator_virtualization": {
      "enabled": true
    }
  }
}
```

Cluster Status 字段：

| 字段 | 类型 | 语义 |
| --- | --- | --- |
| `status.component_status.accelerator_virtualization.phase` | string | `Ready` 或 `NotReady` |
| `status.component_status.accelerator_virtualization.managed` | boolean | 是否由 Neutree 管理 |
| `status.component_status.accelerator_virtualization.version` | string | 虚拟化组件版本 |
| `status.component_status.accelerator_virtualization.reason` | string | 状态原因 |
| `status.component_status.accelerator_virtualization.message` | string | 状态说明 |

状态示例：

```json
{
  "status": {
    "component_status": {
      "accelerator_virtualization": {
        "phase": "Ready",
        "managed": true,
        "version": "v2.9.0",
        "reason": "",
        "message": ""
      }
    }
  }
}
```

### 校验

API middleware 校验：

- `accelerator_virtualization` 必须是对象。
- `enabled` 必须是布尔值。
- `config_patch` 必须是对象。
- `enabled=true` 仅允许 Kubernetes 集群。
- `enabled=true` 要求集群版本大于等于 `v1.1.0`；`v1.1.0-nightly-*` 按基础版本 `v1.1.0` 处理。
- 产品页面不提交 `config_patch`；后端和加速器插件生成 HAMi values 补丁。

HAMi preflight 校验：

- 集群版本必须大于等于 `v1.1.0`。
- `config_patch` 顶层字段仅允许 `devicePlugin`、`scheduler`、`global`、`dra`。
- `dra.enabled` 固定为 `false`。
- `scheduler.patch.enabled` 固定为 `false`。
- `scheduler.certManager.enabled` 固定为 `false`。
- `devicePlugin.migStrategy` 固定为 `none`。
- 已存在非 Neutree 管理的 HAMi webhook、Deployment、DaemonSet、Service 时停止。
- GPU Operator device plugin 处于启用状态时停止。
- NVIDIA MIG 虚拟化模式处于启用状态时停止。

### NVIDIA 虚拟化能力

NVIDIA GPU 插件输出集群虚拟化配置：

| 输出 | 语义 |
| --- | --- |
| `supported` | NVIDIA GPU 是否支持集群虚拟化 |
| `blocking_reasons` | 阻止启用虚拟化的原因 |
| `candidate_nodes` | 参与虚拟化的节点列表 |
| `node_scope_label` | 作用于节点的虚拟化 label |
| `config_patch` | 注入 HAMi chart values 的配置补丁 |

候选节点来源：

- 节点 label `nvidia.com/gpu.present=true`。
- 节点 `status.capacity["nvidia.com/gpu"]` 为正数。
- 节点 `status.allocatable["nvidia.com/gpu"]` 为正数。
- 节点 label `nvidia.com/mig.strategy` 为空或 `none`。

节点虚拟化 label：

```text
neutree.ai/nvidia_vgpu_enabled=true
```

节点显式设置 `neutree.ai/nvidia_vgpu_enabled=false` 时，后端不覆盖该节点配置。

GPU Operator 语义：

- `driver.enabled=true` 时，HAMi device plugin 使用 `nvidiaDriverRoot=/run/nvidia/driver`。
- `devicePlugin.enabled=true` 时，HAMi 启用被阻止，因为 GPU Operator device plugin 与 HAMi device plugin 不能同时持有 `nvidia.com/gpu`。
- `mig.strategy=single|mixed` 时，HAMi 启用被阻止；当前版本不支持 NVIDIA MIG 虚拟化模式。

### 集群资源视图

集群资源视图使用 `status.resource_info` 表达资源。

核心结构：

| 字段 | 语义 |
| --- | --- |
| `resource_info.allocatable` | 集群总可分配资源 |
| `resource_info.available` | 集群剩余可用资源 |
| `resource_info.accelerator_metadata` | 加速器产品元数据 |
| `resource_info.node_resources` | 节点维度资源状态 |

HAMi 集群资源语义：

- 节点 allocatable 中的 `nvidia.com/gpu` 是 HAMi 底层调度数量，不进入 Neutree 资源视图。
- 物理 GPU 数量来自 HAMi 节点注册信息。
- `accelerator_groups[nvidia_gpu].quantity` 表示物理 GPU 数量。
- `accelerator_groups[nvidia_gpu].products[product].quantity` 表示该型号物理 GPU 数量。
- `products[product].virtualization.memory_mib` 表示该型号 GPU 的虚拟化显存池。
- `products[product].virtualization.core_units` 表示该型号 GPU 的虚拟化算力池。
- 节点算力总量按物理 GPU 数量乘以 100 计算。
- 节点显存总量按物理 GPU 数量乘以单卡显存计算。
- `nvidia.com/gpumem-percentage` 不在节点资源中展示；Pod 使用百分比请求时，资源计算将其换算为显存 MiB。

节点资源视图：

- `node_resources[node].allocatable` 表示节点总资源。
- `node_resources[node].available` 表示节点剩余资源。
- `node_resources[node].devices` 表示设备级资源池。

设备字段：

| 字段 | 语义 |
| --- | --- |
| `uuid` | 物理 GPU UUID |
| `product` | GPU 型号 |
| `health` | 设备健康状态 |
| `allocatable.memory_mib` | 设备显存容量 |
| `allocatable.core_units` | 设备算力容量 |
| `available.memory_mib` | 设备可用显存 |
| `available.core_units` | 设备可用算力 |

设备明细仅属于节点维度，不进入集群汇总层。

## Endpoint vGPU

### Endpoint 资源语义

Endpoint 使用 `spec.resources` 描述资源需求。

资源字段：

| 字段 | 类型 | 语义 |
| --- | --- | --- |
| `resources.cpu` | string | CPU core 数量 |
| `resources.memory` | string | 内存 GiB |
| `resources.gpu` | string | GPU 设备数量 |
| `resources.accelerator.type` | string | 加速器类型，固定为 `nvidia_gpu` |
| `resources.accelerator.product` | string | GPU 产品型号 |
| `resources.accelerator.virtualization.memory_mib` | string | 每个 vGPU 的显存 MiB |
| `resources.accelerator.virtualization.memory_percent` | string | 每个 vGPU 的显存百分比 |
| `resources.accelerator.virtualization.core_percent` | string | 每个 vGPU 的算力百分比 |

Endpoint vGPU 请求示例：

```json
{
  "resources": {
    "cpu": "8",
    "memory": "32",
    "gpu": "1",
    "accelerator": {
      "type": "nvidia_gpu",
      "product": "Tesla-T4",
      "virtualization.memory_mib": "15360",
      "virtualization.core_percent": "100"
    }
  }
}
```

显存百分比请求示例：

```json
{
  "resources": {
    "gpu": "1",
    "accelerator": {
      "type": "nvidia_gpu",
      "product": "Tesla-T4",
      "virtualization.memory_percent": "50",
      "virtualization.core_percent": "50"
    }
  }
}
```

字段约束：

- `resources.gpu` 必须大于 0。
- `resources.accelerator.type` 必须存在且为 `nvidia_gpu`。
- `virtualization.memory_mib` 与 `virtualization.memory_percent` 互斥。
- `virtualization.memory_mib` 必须大于 0。
- `virtualization.memory_percent` 范围为 1 到 100。
- `virtualization.core_percent` 范围为 1 到 100。
- HAMi 原始资源名不作为用户侧资源字段。

### Endpoint 资源校验

API middleware 在 Endpoint 创建和更新时校验 vGPU 资源请求。

校验输入：

- Endpoint `spec.resources`。
- Endpoint 所属集群 `status.resource_info`。
- 更新 Endpoint 时的原 Endpoint 资源状态。

校验规则：

- 请求的加速器产品必须存在于集群可用资源中。
- `resources.gpu` 不能超过该产品的可用物理 GPU 数量。
- 每个 vGPU 的显存请求必须能被该产品的可用显存池满足。
- 每个 vGPU 的算力请求必须能被该产品的可用算力池满足。
- 节点设备明细存在时，每个请求的 GPU 必须匹配一张健康、同型号、可用显存和可用算力均满足请求的物理 GPU。
- 更新 Endpoint 时，原 Endpoint 已占用的资源先回收到可用资源后再校验。

显存和算力的校验语义：

- `virtualization.memory_mib` 按每个 vGPU 的显存 MiB 校验。
- `virtualization.memory_percent` 按产品单卡总显存换算为每个 vGPU 的显存 MiB 后校验。
- `virtualization.core_percent` 按每个 vGPU 的算力百分比校验，100 表示一张物理 GPU 的完整算力份额。
- 产品池校验用于判断整体余量，设备级校验用于避免资源碎片导致的调度失败。

### HAMi 资源转换

Kubernetes HAMi 场景下，后端将 Endpoint 资源字段转换为 Pod requests、limits 和 nodeSelector。

转换关系：

| Neutree 字段 | HAMi/Kubernetes 字段 | 语义 |
| --- | --- | --- |
| `resources.gpu` | `requests/limits["nvidia.com/gpu"]` | GPU 设备数量 |
| `virtualization.memory_mib` | `requests/limits["nvidia.com/gpumem"]` | 每个 vGPU 的显存 MiB |
| `virtualization.memory_percent` | `requests/limits["nvidia.com/gpumem-percentage"]` | 每个 vGPU 的显存百分比 |
| `virtualization.core_percent` | `requests/limits["nvidia.com/gpucores"]` | 每个 vGPU 的算力百分比 |
| `accelerator.product` | `nodeSelector["nvidia.com/gpu.product"]` | 部署节点的 GPU 型号 |
| NVIDIA GPU 虚拟化调度策略 | `annotations["hami.io/gpu-scheduler-policy"]` | 固定为 `topology-aware` |

Pod annotation 示例：

```yaml
metadata:
  annotations:
    hami.io/gpu-scheduler-policy: topology-aware
```

Pod 资源示例：

```yaml
resources:
  requests:
    nvidia.com/gpu: "1"
    nvidia.com/gpumem: "15360"
    nvidia.com/gpucores: "100"
  limits:
    nvidia.com/gpu: "1"
    nvidia.com/gpumem: "15360"
    nvidia.com/gpucores: "100"
```

使用显存百分比时：

```yaml
resources:
  requests:
    nvidia.com/gpu: "1"
    nvidia.com/gpumem-percentage: "50"
    nvidia.com/gpucores: "50"
  limits:
    nvidia.com/gpu: "1"
    nvidia.com/gpumem-percentage: "50"
    nvidia.com/gpucores: "50"
```

GPU 产品型号转换为节点选择条件：

```yaml
nodeSelector:
  nvidia.com/gpu.product: "Tesla-T4"
```

HAMi 调度器根据 Pod resources 和节点状态完成节点内物理设备选择。

### Endpoint 资源状态

Endpoint Status 增加 `resources` 字段：

```json
{
  "status": {
    "resources": {
      "summary": {
        "products": {
          "Tesla-T4": {
            "memory_mib": 15360,
            "core_units": 100
          }
        }
      },
      "replicas": [
        {
          "instance_id": "endpoint-pod-0",
          "replica_id": "endpoint-pod-0",
          "node_id": "gpu-node-1",
          "devices": [
            {
              "uuid": "GPU-xxxx",
              "product": "Tesla-T4",
              "memory_mib": 15360,
              "core_units": 100,
              "node_id": "gpu-node-1"
            }
          ]
        }
      ]
    }
  }
}
```

字段语义：

| 字段 | 语义 |
| --- | --- |
| `summary.products[product].memory_mib` | Endpoint 在该型号上分配的显存总量 |
| `summary.products[product].core_units` | Endpoint 在该型号上分配的算力总量 |
| `replicas[].instance_id` | 后端实例标识 |
| `replicas[].replica_id` | Endpoint 副本标识 |
| `replicas[].node_id` | 副本所在节点 |
| `replicas[].devices[].uuid` | 分配到的物理 GPU UUID |
| `replicas[].devices[].product` | 分配到的 GPU 型号 |
| `replicas[].devices[].memory_mib` | 该设备分配给副本的显存 |
| `replicas[].devices[].core_units` | 该设备分配给副本的算力 |

Kubernetes HAMi 场景中，`instance_id` 和 `replica_id` 使用 Pod 名称。资源状态来自 Pod 资源配置和 HAMi 分配注解。

### Endpoint 资源视图

Endpoint 资源视图表达推理实例实际分配结果。

资源状态生成规则：

- Pod 未调度时不生成设备分配。
- HAMi 未写入分配注解时不生成设备分配。
- 已分配设备按 Pod、节点、设备 UUID、产品型号、显存、算力输出。
- 多设备分配按设备数组输出。
- 资源状态缺失表示分配信息不可用，不表示资源使用为 0。

## 加速器虚拟化组件

### 生命周期

加速器虚拟化 Component 基于 HAMi 管理以下对象：

- scheduler Deployment、Service、ServiceAccount、RBAC。
- device plugin DaemonSet。
- device plugin monitor Service。
- mutating webhook。
- scheduler TLS Secret。

启用虚拟化时执行：

1. preflight 校验。
2. 节点虚拟化 label 配置。
3. TLS Secret 生成或轮转。
4. HAMi manifests 渲染和应用。
5. webhook CA bundle 写入。
6. 组件状态检查。
7. Cluster Status 写回。

关闭虚拟化时执行：

1. 删除 Neutree 管理的 HAMi manifests。
2. 清理 TLS Secret。
3. 写回 `NotReady` 状态。

### Chart 渲染

HAMi 使用内置 Helm chart 渲染 Kubernetes manifests。后端直接应用渲染结果，不写 Helm release 状态。

固定配置：

- HAMi 版本：`v2.9.0`。
- `dra.enabled=false`。
- `scheduler.patch.enabled=false`。
- `scheduler.certManager.enabled=false`。
- scheduler Service 类型为 `ClusterIP`。
- device plugin monitor Service 类型为 `ClusterIP`。
- device plugin `migStrategy` 固定为 `none`。
- device plugin 通过节点虚拟化 label 选择节点。
- NVIDIA GPU 虚拟化默认使用 HAMi GPU 拓扑感知调度：`scheduler.defaultSchedulerPolicy.gpuSchedulerPolicy=topology-aware`。
- kube-scheduler 镜像 tag 按集群 Kubernetes 版本选择。
- HAMi 镜像通过集群 image registry 策略解析。

GPU 拓扑感知调度配置结果：

- HAMi scheduler 启动参数包含 `--gpu-scheduler-policy=topology-aware`。
- NVIDIA device plugin 设置 `ENABLE_TOPOLOGY_SCORE=true`。
- vGPU Endpoint Pod 设置 `hami.io/gpu-scheduler-policy=topology-aware`。

Kubernetes scheduler tag：

| Kubernetes minor | kube-scheduler tag |
| --- | --- |
| 1.26 | v1.26.15 |
| 1.27 | v1.27.16 |
| 1.28 | v1.28.15 |
| 1.29 | v1.29.14 |
| 1.30 | v1.30.14 |
| 1.31 | v1.31.14 |
| 1.32 | v1.32.13 |

版本选择规则：

- 集群 Kubernetes major/minor 命中内置表时，使用表中 kube-scheduler tag。
- 集群 Kubernetes major/minor 未命中内置表时，使用集群当前 Kubernetes 版本生成 kube-scheduler tag。
- 未命中内置表的 kube-scheduler 镜像不纳入默认离线镜像清单，由客户上传到集群离线 registry。

### TLS

Neutree 管理 scheduler webhook TLS：

- CA 有效期 10 年。
- serving certificate 有效期 1 年。
- serving certificate 到期前 30 天内轮转。
- webhook `caBundle` 由后端写入。

### 状态闭环

HAMi 状态检查项：

- scheduler Deployment updated 且 ready。
- device plugin DaemonSet desired/current/ready 满足期望。
- monitor Service 存在。
- TLS Secret 存在且证书有效。
- webhook CA bundle 已写入。

检查结果写入 `status.component_status.accelerator_virtualization`。

## 监控与 Grafana

### 指标采集

Prometheus scrape 目标：

- HAMi device plugin monitor。
- 端口：`9394`。
- 命名空间：集群命名空间。
- kube-state-metrics。
- 端口：`8080`。
- 采集范围：集群命名空间内 Pod，资源类型限定为 Pod。

目标标签：

- `neutree_cluster`
- `workspace`
- `node`
- `monitor_namespace`
- `monitor_pod`

监控版本范围：

| 集群版本 | Metrics Component 行为 | vGPU 监控能力 |
| --- | --- | --- |
| `< v1.1.0` | 保持原 Metrics Component 部署路径，不部署 kube-state-metrics，不增加扩展指标采集配置 | 不提供 HAMi vGPU Dashboard 数据前置依赖 |
| `>= v1.1.0` | 部署 kube-state-metrics，采集 HAMi monitor 和 kube-state-metrics 指标 | 提供集群级和 Endpoint 级 vGPU Dashboard 数据前置依赖 |
| `v1.1.0-nightly-*` | 按基础版本 `v1.1.0` 处理 | 同 `>= v1.1.0` |

版本配置：

- kube-state-metrics 镜像版本固定为 `v2.15.0`。
- kube-state-metrics 镜像使用集群 image registry 策略解析为 `{{ .ImagePrefix }}/kube-state-metrics/kube-state-metrics:v2.15.0`。
- kube-state-metrics 是 Metrics Component 的通用 Pod 标签指标组件，不属于 HAMi 组件。
- kube-state-metrics 仅在集群版本大于等于 `v1.1.0` 时纳入 Metrics Component 状态检查。
- 低于 `v1.1.0` 的集群不检查 kube-state-metrics Deployment 状态。

HAMi workload 指标是 Pod 维度，`namespace` 和 `pod` 标签必须保留。Endpoint Dashboard 通过 `namespace,pod` 将 HAMi 指标关联到 Neutree Endpoint 归属标签。

Endpoint 归属指标：

- kube-state-metrics 由 Metrics Component 部署在集群命名空间内。
- kube-state-metrics 仅采集当前集群命名空间内的 Pod 指标。
- kube-state-metrics 通过 `--metric-labels-allowlist=pods=[app,cluster,workspace,endpoint,engine,engine_version]` 暴露 Neutree Endpoint 归属标签。
- `kube_pod_labels` 必须包含 `namespace`、`pod`、`label_workspace` 和 `label_endpoint`。
- `hami_*` runtime 指标通过 `on(namespace,pod) group_left(label_workspace,label_endpoint)` 关联 `kube_pod_labels`。
- `workspace` 和 `endpoint` 变量用于限定单个 Neutree 推理实例。
- `namespace`、`pod`、`node`、`container` 和 `device_uuid` 变量用于实例内下钻。

### 集群 Dashboard

集群 Dashboard 对齐 HAMi WebUI 集群概览语义，展示集群整体 vGPU 分配、使用、资源概览和节点分布。

面板：

- Core Allocation Percentage：GPU 算力分配百分比。
- Memory Allocation Percentage：GPU 显存分配百分比。
- Core Usage Percentage：GPU 算力使用百分比。
- Memory Usage Percentage：GPU 显存使用百分比。
- Node Overview：节点可调度和禁止调度数量。
- GPU Type Distribution：GPU 类型分布。
- GPU Nodes：GPU 节点数。
- Physical GPUs：物理 GPU 卡数。
- Endpoints：vGPU Endpoint 数量。
- Endpoint Replicas：vGPU Endpoint 副本数量。
- GPU Memory Total：GPU 显存总量。
- GPU Core Allocation/Usage Percentage Trend：GPU 算力分配百分比和使用百分比趋势。
- GPU Memory Allocation/Usage Percentage Trend：GPU 显存分配百分比和使用百分比趋势。
- Node Core Top 5：节点算力分配百分比和使用百分比 Top 5。
- Node Memory Top 5：节点显存分配百分比和使用百分比 Top 5。
- Node Replica Count Top 5：节点副本数量 Top 5。
- Node Replica Distribution：节点副本数量分布。

### Endpoint Dashboard

Endpoint Dashboard 展示单个推理实例的 vGPU 分配和运行状态。

面板：

- Endpoint Replicas：Endpoint 副本数量。
- vGPU Memory Limit：vGPU 显存配额。
- vGPU Memory Used：vGPU 显存使用量。
- vGPU Memory Usage Percentage：vGPU 显存使用百分比。
- Avg vGPU SM Usage Percentage：平均 vGPU 算力使用百分比。
- Endpoint vGPU SM Usage Percentage：vGPU 算力使用百分比趋势。
- Endpoint vGPU Memory Usage Percentage：vGPU 显存使用百分比趋势。
- Replica vGPU Allocation Details：副本到设备的 vGPU 分配明细。
- Replica vGPU Runtime Details：副本 vGPU 运行明细，包含显存使用量、显存配额、显存使用百分比、算力使用百分比和距离上次 kernel 执行时间。
- Endpoint vGPU Memory Breakdown：Endpoint vGPU 显存来源拆分。
- Seconds Since Last Container Kernel：距离上次 kernel 执行时间。
- Endpoint Physical GPU Usage Percentage：Endpoint 占用物理 GPU 的整体使用百分比。
- Endpoint Physical GPU Memory Used：Endpoint 占用物理 GPU 的整体显存使用。

物理 GPU 面板通过 Endpoint 已分配 device UUID 过滤 host 指标，保留 host 指标原始单位和值。该视图表示物理卡整体压力，不表示 Endpoint 独占消耗。

## 离线镜像

HAMi 相关镜像合并到 Kubernetes 集群离线包，不作为独立离线包交付。

默认集群离线包包含：

- `docker.io/projecthami/hami:v2.9.0`
- `registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0`
- `registry.k8s.io/kube-scheduler:v1.26.15`
- `registry.k8s.io/kube-scheduler:v1.27.16`
- `registry.k8s.io/kube-scheduler:v1.28.15`
- `registry.k8s.io/kube-scheduler:v1.29.14`
- `registry.k8s.io/kube-scheduler:v1.30.14`
- `registry.k8s.io/kube-scheduler:v1.31.14`
- `registry.k8s.io/kube-scheduler:v1.32.13`

集群 Kubernetes 版本未命中内置 kube-scheduler tag 表时，后端仍按当前 Kubernetes 版本配置 kube-scheduler 镜像 tag。该 tag 对应镜像由客户上传到集群离线 registry。

## 安全与约束

- 仅 Kubernetes 集群允许开启 HAMi 虚拟化。
- `config_patch` 仅允许受控字段。
- DRA、scheduler patch、cert-manager 模式固定关闭。
- MIG 虚拟化模式固定关闭。
- 非 Neutree 管理的 HAMi 资源阻止启用。
- GPU Operator device plugin 与 HAMi device plugin 冲突时阻止启用。
- Endpoint 用户输入不暴露 HAMi 原始资源名。
- 组件失败原因写入集群状态。

## 后续工作

- 扩展 Ray Endpoint 的 vGPU 资源状态。
- 支持更多加速器虚拟化方案。
- 增加 HAMi pending allocation 资源语义。
- 补充安装、调度、资源视图和 Dashboard 的端到端测试。
