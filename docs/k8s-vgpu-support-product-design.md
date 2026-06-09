# Kubernetes vGPU 支持产品设计

本文定义 Neutree 在 Kubernetes 集群上提供 vGPU 支持的产品设计。产品层展示 Neutree 对象语义，不要求用户理解底层实现方案、Kubernetes annotation 或组件部署细节。

## 背景与目标

Kubernetes vGPU 支持将物理 GPU 切分为可调度的 vGPU 资源。Neutree 在集群管理、资源视图、Endpoint 配置、Endpoint 状态和监控入口中提供统一产品体验。

产品目标：

- Kubernetes 集群提供 GPU 虚拟化开关。
- 当前版本仅支持 NVIDIA GPU。
- 集群版本大于等于 `v1.1.0` 时提供 GPU 虚拟化开关。
- 集群详情展示 vGPU 支持状态和不可用原因。
- 集群资源使用物理 GPU、显存和算力语义。
- Endpoint 创建表单支持 GPU 数量、GPU 型号、显存和算力比例。
- Endpoint 详情展示实际分配到的设备、显存和算力。
- 集群和 Endpoint 提供 vGPU Dashboard 入口。

非目标：

- 非 Kubernetes 集群不提供 GPU 虚拟化开关。
- 集群版本小于 `v1.1.0` 不提供 GPU 虚拟化开关。
- 非 NVIDIA GPU 不提供 vGPU 支持。
- NVIDIA MIG 虚拟化模式不在当前版本范围内。
- 用户侧不输入底层 Kubernetes vGPU 资源名。
- 产品页面不展示 chart values、Kubernetes webhook、DaemonSet、Deployment 等底层对象。
- Dashboard 不替代 Endpoint 资源状态；Dashboard 仅表达监控数据。

## 用户与场景

用户角色：

| 角色 | 目标 | 核心页面 |
| --- | --- | --- |
| 平台管理员 | 开启或关闭集群 GPU 虚拟化，处理 vGPU 支持状态问题 | 集群创建、集群编辑、集群详情 |
| 推理服务用户 | 创建使用 vGPU 的 Endpoint，查看资源分配结果 | Endpoint 创建、Endpoint 详情 |
| 运维人员 | 查看集群 GPU 压力、Endpoint vGPU 使用和异常状态 | 集群 Dashboard、Endpoint Dashboard |

核心场景：

- 平台管理员在 Kubernetes 集群上开启 GPU 虚拟化。
- 平台管理员通过 vGPU 支持状态确认集群是否满足 vGPU 调度条件。
- 推理服务用户创建 vGPU Endpoint，配置 GPU 数量、型号、显存和算力。
- 推理服务用户在 Endpoint 详情查看实际分配的 GPU 设备。
- 运维人员从集群或 Endpoint 进入 Dashboard 排查显存、算力和物理卡压力。

## 用户故事

### 多个小模型共享 H20

场景：

- 用户拥有一批小模型推理服务。
- 每个小模型只需要 H20 的部分显存和部分算力。
- Kubernetes 原生 GPU 资源语义以整卡为基本分配单位，单个 `nvidia.com/gpu` 不能被多个应用实例共享。
- 小模型按整卡部署会造成 H20 显存和算力利用率偏低。

目标：

- 用户在 Kubernetes 集群内开启 GPU 虚拟化。
- 多个小模型 Endpoint 共享同一张 H20 的不同 vGPU 资源切片。
- 每个 Endpoint 只申请自身需要的显存和算力比例。

产品行为：

- Endpoint 表单支持选择 GPU 型号 `H20`，并配置 GPU 数量、显存和算力百分比。
- Endpoint 详情展示资源分配摘要和副本设备明细。
- 集群资源视图展示 H20 物理卡、显存池和算力池的 Used/Total。

验收结果：

- 多个小模型 Endpoint 能共享同一张 H20。
- Endpoint 详情能展示每个副本分配到的设备 UUID、显存和算力。
- 集群资源视图使用物理 H20、显存和算力表达资源。

## 功能范围

本版本包含：

- 集群 GPU 虚拟化开关。
- NVIDIA GPU vGPU 支持。
- vGPU 支持状态展示。
- 集群级资源汇总。
- 节点级资源和设备明细。
- Endpoint vGPU 资源配置。
- Endpoint 资源分配摘要和副本设备明细。
- 集群级 vGPU Dashboard 入口。
- Endpoint 级 vGPU Dashboard 入口。
- GPU 虚拟化状态、vGPU 支持状态、资源状态和指标缺失的空态与错误态。

本版本不包含：

- 非 Kubernetes 集群虚拟化。
- 非 NVIDIA GPU 虚拟化。
- NVIDIA MIG 虚拟化模式。
- DRA 模式。
- 用户手动接管已有 vGPU 支持组件。
- Ray Endpoint vGPU 资源状态。

## 用户流程

### 开启集群虚拟化

流程：

1. 用户进入 Kubernetes 集群创建或编辑页面。
2. 用户打开“启用 GPU 虚拟化”开关。
3. 用户提交集群配置。
4. 集群详情展示 vGPU 支持状态。
5. vGPU 支持状态为 `Ready` 后，Endpoint 创建页展示 vGPU 资源配置。

异常路径：

- 集群类型不是 Kubernetes：页面不展示虚拟化开关。
- GPU device plugin 冲突：集群详情展示 vGPU 支持 `NotReady` 和后端 reason。
- 不存在可虚拟化 GPU 节点：集群详情展示无候选节点状态。

### 创建 vGPU Endpoint

流程：

1. 用户进入 Endpoint 创建页面。
2. 用户选择已开启虚拟化且 vGPU 支持 Ready 的 Kubernetes 集群。
3. 页面展示 vGPU 资源配置区。
4. 用户配置 GPU 数量、GPU 型号、显存模式、显存值和算力百分比。
5. 用户提交 Endpoint。
6. 后端使用 GPU 拓扑感知调度策略创建 Endpoint 工作负载。
7. Endpoint 详情展示部署状态。
8. 调度完成后，Endpoint 详情展示资源分配摘要和副本设备明细。

异常路径：

- vGPU 支持 `NotReady`：vGPU 配置区禁用并展示原因。
- Pod 未调度：Endpoint 详情展示调度中。
- `status.resources` 缺失：Endpoint 详情展示等待资源分配信息。

### 查看监控

集群监控流程：

1. 用户进入集群详情。
2. 集群开启虚拟化时展示集群 vGPU Dashboard 入口。
3. 用户进入集群 Dashboard，查看集群 vGPU 使用、物理卡压力和容器活跃情况。

Endpoint 监控流程：

1. 用户进入 vGPU Endpoint 详情。
2. 页面展示 Endpoint vGPU Dashboard 入口。
3. 用户进入 Endpoint Dashboard，查看该推理实例的 vGPU 配额、使用百分比、副本明细和物理卡压力。

## 页面与信息架构

页面层级：

```text
集群
  -> 创建/编辑：虚拟化开关
  -> 详情：vGPU 支持状态，集群资源汇总，节点资源，集群 Dashboard
  -> 节点详情：设备级显存，算力，健康状态

Endpoint
  -> 创建/编辑：vGPU 资源配置
  -> 详情：资源分配摘要，副本分配明细，Endpoint Dashboard
```

页面职责：

| 页面 | 页面职责 |
| --- | --- |
| 集群创建/编辑 | 配置 `accelerator_virtualization` |
| 集群详情 | 展示 vGPU 支持状态、集群资源、Dashboard 入口 |
| 节点详情 | 展示节点物理 GPU 和设备级分配 |
| Endpoint 创建/编辑 | 配置 Endpoint vGPU 资源需求 |
| Endpoint 详情 | 展示 Endpoint 资源分配和监控入口 |
| Grafana Dashboard | 展示运行指标和趋势 |

## 集群虚拟化

### 创建和编辑

仅 Kubernetes 集群展示虚拟化开关。

控件：

| 控件 | 提交字段 | 语义 |
| --- | --- | --- |
| 启用 GPU 虚拟化 | `spec.accelerator_virtualization.enabled` | 开启或关闭集群加速器虚拟化 |

交互规则：

- 非 Kubernetes 集群不展示虚拟化开关。
- 非 Kubernetes 集群不提交 `enabled=true`。
- 产品页面不开放虚拟化高级配置。
- vGPU 支持配置由后端和加速器插件生成。

### vGPU 支持状态

集群开启虚拟化后，集群详情展示 vGPU 支持状态。

展示字段：

| 展示项 | 语义 |
| --- | --- |
| 组件名称 | 固定为 vGPU 支持 |
| 版本 | vGPU 支持组件版本 |
| 状态 | `Ready` 或 `NotReady` |
| 管理状态 | 是否由 Neutree 管理 |
| 原因 | 状态原因 |
| 说明 | 状态说明 |

状态语义：

- `Ready`：集群满足 Endpoint vGPU 调度和监控条件。
- `NotReady`：页面展示后端返回的 reason 和 message。
- 状态缺失：页面展示状态同步中。

不可用原因：

- 集群类型不支持。
- 没有加速器插件支持虚拟化。
- GPU device plugin 冲突。
- 已存在非 Neutree 管理的 vGPU 支持组件。
- 没有可开启虚拟化的 GPU 节点。

### 集群资源视图

集群资源视图展示物理 GPU 语义。

资源类字段统一按 `Used/Total` 展示。`Used` 表示已分配资源，`Total` 表示可分配资源总量。

集群汇总字段：

| 展示项 | 语义 |
| --- | --- |
| 物理 GPU | 物理卡 Used/Total |
| GPU 型号分布 | 按产品型号分组的物理卡数量 |
| 显存 | 虚拟化显存池 Used/Total |
| 算力 | 虚拟化算力池 Used/Total，单张物理卡 Total 为 100 |
| 虚拟化状态 | 集群虚拟化是否开启 |

集群视图不展示设备列表，也不展示底层 HAMi 调度数量。

节点列表字段：

| 展示项 | 语义 |
| --- | --- |
| 节点名称 | Kubernetes Node 名称 |
| GPU 型号 | 节点 GPU 产品型号 |
| 物理 GPU | 节点物理卡 Used/Total |
| 显存 | 节点虚拟化显存池 Used/Total |
| 算力 | 节点虚拟化算力池 Used/Total |
| 虚拟化状态 | 节点虚拟化状态 |
| 健康状态 | 设备健康汇总 |

节点详情字段：

| 展示项 | 语义 |
| --- | --- |
| 设备 UUID | 物理 GPU UUID |
| GPU 型号 | 设备产品型号 |
| 健康状态 | 设备健康状态 |
| 显存 | 该卡显存 Used/Total |
| 算力 | 该卡算力 Used/Total，Total 为 100 |

### 集群 Dashboard

集群开启虚拟化后展示集群 vGPU Dashboard 入口。

入口规则：

- `accelerator_virtualization.enabled=true` 时展示入口。
- vGPU 支持状态为 `NotReady` 时，入口旁展示状态警告。
- 虚拟化关闭时不展示入口。

Dashboard 变量：

| 变量 | 来源 |
| --- | --- |
| `Cluster` | 集群指标标签 |
| `node` | 节点筛选 |

## Endpoint vGPU

### 创建和编辑

Endpoint vGPU 配置在目标集群开启虚拟化后展示。

可创建条件：

- 目标集群类型为 Kubernetes。
- 目标集群版本大于等于 `v1.1.0`。
- `spec.accelerator_virtualization.enabled=true`。
- vGPU 支持状态为 `Ready`。

NVIDIA GPU vGPU Endpoint 使用 GPU 拓扑感知调度。该调度策略由后端固定生成，前端不提供配置项。

资源控件：

| 控件 | ResourceSpec 字段 | 语义 | 校验 |
| --- | --- | --- | --- |
| GPU 数量 | `resources.gpu` | GPU 设备数量 | 大于 0 |
| GPU 类型 | `resources.accelerator.type` | 加速器类型，固定为 `nvidia_gpu` | 必填 |
| GPU 型号 | `resources.accelerator.product` | 部署节点的 GPU 产品型号 | 字段为空表示不指定型号 |
| 显存模式 | `virtualization.memory_mib` / `virtualization.memory_percent` | MiB 或百分比二选一 | 互斥 |
| 显存 MiB | `resources.accelerator.virtualization.memory_mib` | 每个 vGPU 显存 MiB | 大于 0 |
| 显存百分比 | `resources.accelerator.virtualization.memory_percent` | 每个 vGPU 显存百分比 | 1 到 100 |
| 算力百分比 | `resources.accelerator.virtualization.core_percent` | 每个 vGPU 算力百分比 | 1 到 100 |

提交示例：

```json
{
  "resources": {
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

显存百分比模式：

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

前端不提供底层 Kubernetes vGPU 资源名输入：

- `nvidia.com/gpumem`
- `nvidia.com/gpumem-percentage`
- `nvidia.com/gpucores`

这些字段由后端根据 Neutree ResourceSpec 生成。

### Endpoint 资源视图

Endpoint 详情展示 `status.resources`。

Endpoint 资源摘要按 `Used/Total` 展示。副本明细展示单条实际分配值。

摘要字段：

| 展示项 | 数据字段 | 语义 |
| --- | --- | --- |
| GPU 型号 | `summary.products` key | 分配到的产品型号 |
| 显存 | `summary.products[product].memory_mib` / Endpoint 资源请求 | 该 Endpoint 显存 Used/Total |
| 算力 | `summary.products[product].core_units` / Endpoint 资源请求 | 该 Endpoint 算力 Used/Total |
| 副本数量 | `replicas` 聚合 | 有资源分配信息的副本数量 |

副本明细字段：

| 展示项 | 数据字段 | 语义 |
| --- | --- | --- |
| 副本 ID | `replicas[].replica_id` | Endpoint 副本标识 |
| 实例 ID | `replicas[].instance_id` | 后端实例标识 |
| 节点 | `replicas[].node_id` | 副本所在节点 |
| 设备 UUID | `replicas[].devices[].uuid` | 分配到的物理 GPU |
| GPU 型号 | `replicas[].devices[].product` | 设备产品型号 |
| 显存 | `replicas[].devices[].memory_mib` | 该设备分配给副本的显存 |
| 算力 | `replicas[].devices[].core_units` | 该设备分配给副本的算力 |

展示规则：

- `status.resources` 存在时展示摘要和副本明细。
- `status.resources` 缺失时展示“等待资源分配信息”。
- Pod 未调度时展示调度中。
- 多设备分配按设备行展示。
- 资源状态缺失不展示为 0。

### Endpoint Dashboard

vGPU Endpoint 展示 Endpoint vGPU Dashboard 入口。

入口规则：

- Endpoint spec 含虚拟化资源字段时展示入口。
- Endpoint status 含 vGPU 分配信息时展示入口。
- Endpoint 未使用 vGPU 时不展示入口。
- 指标未出现时 Dashboard 展示 no data。

Dashboard 变量：

| 变量 | 来源 |
| --- | --- |
| `Cluster` | 集群指标标签 |
| `workspace` | Endpoint workspace |
| `endpoint` | Endpoint 名称 |
| `namespace` | 副本命名空间筛选 |
| `pod` | 副本筛选 |
| `node` | 节点筛选 |
| `container` | 容器筛选 |
| `device_uuid` | 设备筛选 |

`workspace` 和 `endpoint` 限定该推理实例。`namespace`、`pod`、`node`、`container` 和 `device_uuid` 用于在该推理实例范围内继续下钻。底层 HAMi workload 为 Pod，产品层统一展示为 Endpoint Replica。

## 监控面板含义

### 集群 Dashboard

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

- Endpoint Replicas：Endpoint 副本数量。
- vGPU Memory Limit：Endpoint vGPU 显存配额。
- vGPU Memory Used：Endpoint vGPU 显存使用量。
- vGPU Memory Usage Percentage：Endpoint vGPU 显存使用百分比。
- Avg vGPU SM Usage Percentage：Endpoint 平均 vGPU 算力使用百分比。
- Endpoint vGPU SM Usage Percentage：Endpoint vGPU 算力使用百分比趋势。
- Endpoint vGPU Memory Usage Percentage：Endpoint vGPU 显存使用百分比趋势。
- Replica vGPU Allocation Details：副本到物理设备和 vdevice 的分配明细。
- Replica vGPU Runtime Details：副本 vGPU 运行明细，包含显存使用量、显存配额、显存使用百分比、算力使用百分比和距离上次 kernel 执行时间。
- Endpoint vGPU Memory Breakdown：Endpoint vGPU 显存来源拆分。
- Seconds Since Last Container Kernel：距离上次 kernel 执行时间。
- Endpoint Physical GPU Usage Percentage：Endpoint 占用物理 GPU 的整体使用百分比。
- Endpoint Physical GPU Memory Used：Endpoint 占用物理 GPU 的整体显存使用。

物理 GPU 面板代表对应物理卡整体压力，不代表 Endpoint 独占资源消耗。

## 权限与可见性

权限沿用 Neutree 现有集群、Endpoint 和监控权限模型。

规则：

- 具备集群管理权限的用户查看和修改集群虚拟化配置。
- 具备集群查看权限的用户查看 vGPU 支持状态、集群资源和节点资源。
- 具备 Endpoint 创建权限的用户创建 vGPU Endpoint。
- 具备 Endpoint 查看权限的用户查看 Endpoint 资源分配状态。
- Dashboard 入口遵循对应集群或 Endpoint 的查看权限。
- 节点设备明细遵循集群资源查看权限。

## 空态与错误态

虚拟化未开启：

- 不展示 vGPU 支持组件错误。
- 不展示 vGPU Dashboard 入口。
- Endpoint 不展示 vGPU 控件。

vGPU 支持安装中：

- 集群详情展示状态同步中。
- Endpoint vGPU 创建禁用。

vGPU 支持 NotReady：

- 展示后端 reason 和 message。
- Endpoint vGPU 创建禁用。
- 提供集群状态入口。

无 GPU 候选节点：

- 集群详情展示没有可虚拟化节点。
- 资源视图展示空资源。

Endpoint 资源状态缺失：

- 展示等待分配信息。
- 不展示为 0。

监控指标缺失：

- Dashboard 展示 no data。
- no data 不等同于使用百分比为 0。

## 验收标准

集群体验：

- Kubernetes 集群展示虚拟化开关。
- 非 Kubernetes 集群不展示虚拟化开关。
- 集群详情展示 vGPU 支持状态、版本、管理状态、reason、message。
- 集群资源汇总使用物理 GPU 语义。
- 节点资源展示显存、算力和设备健康状态。
- 集群 Dashboard 入口按虚拟化状态展示。

Endpoint 体验：

- Endpoint 表单展示 GPU 数量、GPU 类型、GPU 型号、显存模式、显存值、算力百分比。
- Endpoint 表单提交 Neutree ResourceSpec 字段。
- 底层 Kubernetes vGPU 资源名不作为用户输入项。
- 显存 MiB 与显存百分比互斥。
- Endpoint 详情展示 summary 和 replicas。
- 资源状态缺失时展示等待或不可用。
- Endpoint Dashboard 入口只对 vGPU Endpoint 展示。

监控体验：

- 集群 Dashboard 表达集群整体 vGPU 使用。
- Endpoint Dashboard 表达单个推理实例的 vGPU 使用和副本运行明细。
- 物理 GPU 指标表达物理卡整体压力。
- no data 不被展示为 0。

权限体验：

- 集群虚拟化配置入口遵循集群管理权限。
- Endpoint vGPU 创建入口遵循 Endpoint 创建权限。
- Dashboard 入口遵循对应对象查看权限。

## 版本外范围

- Endpoint 表单中的显存模式说明增强。
- 集群资源视图中的虚拟化状态筛选。
- 节点设备到 Endpoint 的反向跳转。
- Dashboard no data 状态说明增强。
