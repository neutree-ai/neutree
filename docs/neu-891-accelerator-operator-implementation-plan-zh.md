# NEU-891 AcceleratorOperator 实现方案

本文记录 NEU-891 恢复方案的代码组织方式和兼容边界，基于分支
`feat/neu-891-accelerator-operator`。本文只描述实现设计，不代表已经完成代码开发。

## 部署模式

部署模式只保留两种：

```go
type DependencyDeployMode string

const (
    DependencyDeployModeLegacy   DependencyDeployMode = "legacy"
    DependencyDeployModeOperator DependencyDeployMode = "operator"
)
```

Accelerator Package 提供：

```go
func PluginDependencyDeployMode(
    acceleratorType string,
    clusterType v1.ClusterType,
) DependencyDeployMode
```

判断规则是：

```text
当前 accelerator 在当前 clusterType 注册了对应 OperatorFactory
  → operator

没有注册对应 OperatorFactory
  → legacy
```

不根据本次 reconcile 中 Driver/Toolkit 是否 Ready 来切换部署模式。Provider 暂时失败
时仍保持 `operator` 模式，由对应 `AcceleratorOperator` 负责等待和恢复；不能回退到
旧 Profile 路径，否则旧组件可能重新启动并阻塞 provider 恢复。

当前只在 Kubernetes 集群流程执行 `KubernetesOperatorFactory`。静态集群保留
`StaticClusterOperatorFactory` 的注册和模式判断，但本轮不接入新的 reconcile 流程。

## Plugin 注册

已有 accelerator plugin 继续负责 accelerator 类型、资源转换、engine runtime 和
基础匹配。注册信息增加可选 factory：

```go
type PluginRegistration struct {
    Plugin                       accelerator.Plugin
    KubernetesOperatorFactory    KubernetesOperatorFactory
    StaticClusterOperatorFactory StaticClusterOperatorFactory
}
```

没有任何 factory 的 plugin 自动进入 `legacy` 模式，不需要修改已有 plugin 的行为。

`Matcher` 保留，负责判断 plugin 是否适用于当前 Cluster：

```go
type Matcher interface {
    MatchCluster(*v1.Cluster) bool
}
```

Kubernetes reconcile 先执行 matcher，再查询部署模式：

```text
遍历 accelerator plugin
  → MatchCluster(cluster)
  → 不匹配：跳过
  → 匹配：PluginDependencyDeployMode(acceleratorType, KubernetesClusterType)
```

Matcher 不负责判断 GPU Operator 是否 Ready。ClusterPolicy、provider Pod、revision、
Ready 和启动时间都属于 `AcceleratorOperator`。

## Kubernetes reconcile 流程

Native Kubernetes cluster reconcile 按 accelerator 逐个处理：

```text
plugin 匹配
  → deployMode=operator
      → AcceleratorManager.OperatorForKubernetesCluster(acceleratorType)
      → 创建 AcceleratorOperator
      → operator.Reconcile(ctx)
      → operator.Status(ctx)

  → deployMode=legacy
      → 继续现有 MetricsComponent Profile/config inject
      → 继续现有 HAMiComponent Profile/config inject
```

如果 deploy mode 为 `operator` 但 factory 创建失败，直接返回错误并保持当前资源状态，
不能静默回退到 `legacy`。

同一个 Cluster 可以同时存在两种模式：

```text
NVIDIA 有 AcceleratorOperator → NVIDIA 走 operator
AMD 没有 AcceleratorOperator  → AMD 走 legacy
```

因此 Metrics 和 HAMi 必须按 accelerator type 过滤，不能因为一个 accelerator 进入
operator 模式就关闭整个公共组件。

## AcceleratorOperator 职责

`AcceleratorOperator` 是某个 accelerator 的完整 Kubernetes 部署和兼容实现，负责：

- NodeAgent 部署和 accelerator runtime 配置；
- accelerator exporter 部署；
- device plugin 部署；
- 该 accelerator 的 driver/toolkit owner 识别；
- provider Ready 判断；
- accelerator 专用 Node label 或 placement；
- 依赖恢复后的基础 Pod startedAt 检查；
- accelerator 专用 Pod 重建和状态；
- 该 accelerator 相关的 admission/recovery 边界。

建议接口位于内部部署层：

```go
type AcceleratorOperator interface {
    Name() string
    AcceleratorType() string

    Reconcile(context.Context, *OperatorContext) (OperatorResult, error)
    Status(context.Context, *OperatorContext) (*v1.ComponentStatus, error)
    Delete(context.Context, *OperatorContext) error
}
```

`OperatorContext` 提供 Cluster、Kubernetes client、namespace、镜像前缀和 image pull
secret 等公共输入。通用 cluster reconciler 只负责创建 context、调用生命周期方法、
收集状态和处理错误，不解释厂商资源的 Ready 语义。

AcceleratorOperator 的 Status 结果是 accelerator 级状态的唯一来源。它可以读取
HamiControlPlaneReady，但 HamiComponent 不反向读取或维护 device plugin、NodeAgent、
exporter 的状态，避免两个组件同时写入同一个 accelerator stack 状态。



## Operator 模式的 Manifest 模板归属

Operator 模式下，NodeAgent、accelerator exporter 和 device plugin 的 manifest template
以及渲染 values 都由 AcceleratorOperator 负责生成。MetricsComponent 和 HamiComponent
不再通过参数注入或复用完整组件生命周期来生成这些 operator-owned 资源。

纯模板和渲染函数放在 operator 的独立子包，例如：

internal/accelerator/operator/manifests/
  node_agent.go
  accelerator_exporter.go
  device_plugin.go
  values.go

该子包只依赖 API 类型和通用 manifest 工具，不依赖 MetricsComponent、HAMiComponent
或 AcceleratorManager，避免 import cycle。父级 AcceleratorOperator 负责：

- 根据 provider、ClusterPolicy 和 accelerator 配置生成 values；
- 选择需要的 NodeAgent、exporter、device plugin 模板；
- 注入 owner label、deployment mode、nvidia.com/gpu.deploy.client selector 和 affinity；
- Apply、Status、Delete 和恢复。

Renderer 只属于 AcceleratorOperator，MetricsComponent 和 HamiComponent 不复用
Operator renderer，也不依赖 operator/manifests 子包。Legacy 模式继续使用现有
MetricsComponent/HamiComponent 的 Profile/config inject 和旧模板，保证已有
accelerator 行为不变。

当某个 accelerator 后续增加 AcceleratorOperator 后，该 accelerator 的 Legacy
渲染路径再单独移除；在迁移完成前，其他没有 Operator 的 accelerator 仍保留旧路径。
模板复用不是本轮目标，资源 owner 和 reconcile 边界必须保持分离。

### Hami device plugin 模板

当前 Hami device plugin 通过 Helm chart 渲染。新结构直接从 chart 中抽取
device-plugin manifests，形成 operator 可调用的 DevicePluginTemplate；不再在运行时
渲染完整 chart 后筛选 DaemonSet：

type DevicePluginTemplate struct {
    Name     string
    Manifest string
}

type DevicePluginValues struct {
    Namespace      string
    ImagePrefix    string
    NodeScopeLabel NodeScopeLabel
    ConfigPatch    map[string]interface{}
}

Hami 虚拟化配置解析仍然产生 device plugin 所需的 mode、candidate nodes、scope label
和 config patch。Operator 将这些结果转换成 DevicePluginValues，调用
DevicePluginTemplate 渲染并管理 device plugin DaemonSet。

Operator 模式下：

Hami 配置解析
  → DevicePluginTemplate + DevicePluginValues
  → AcceleratorOperator 渲染 device plugin
  → AcceleratorOperator 管理 device plugin 状态和恢复

Legacy 模式下：

Hami 配置解析
  → 原有 Hami chart/config inject
  → HamiComponent 管理旧路径

Operator 只抽取和渲染 device plugin DaemonSet、ConfigMap、ServiceAccount、
ClusterRole 和 ClusterRoleBinding；不引入 Hami monitor Service、ServiceMonitor 或
其他服务发现对象，accelerator exporter 的采集由 Neutree MetricsTargetRegistry 负责。
Operator 模式下，Hami scheduler、webhook、TLS、RBAC、控制面和 device plugin 的
渲染/生命周期都由 AcceleratorOperator 处理；Hami 只提供必要的虚拟化配置解析能力，
不复用或调用 Operator renderer。HamiControlPlaneReady、device plugin Ready、
NodeAgent/exporter Ready 和 AcceleratorStackReady 的状态归属按 Operator 方案维护。

Legacy 模式下仍保留现有 HamiComponent 控制面和旧 device plugin/config inject；
对应 accelerator 完成 Operator 迁移后，再删除该 accelerator 的 Legacy 渲染分支。

## Renderer 归属

ResourceRenderer 的定义保持不变，但 NVIDIA renderer 的实现归属调整为
AcceleratorOperator。cluster 层只负责创建 OperatorContext 和注入 renderer，不实现
NVIDIA 的资源组合、owner label、dependency selector 或 NodeAgent affinity。

新路径不依赖已有 MetricsComponent/HAMiComponent 的完整 Reconcile 生命周期。Renderer
可以复用已有 manifest template 或配置模型，但只做纯渲染：

- NodeAgent resource；
- accelerator exporter resource；
- device plugin resource；
- Hami control-plane resource；
- Hami device-plugin resource；
- operator owner label、nvidia.com/gpu.deploy.client selector、affinity 和 toolkit-ready init。

Renderer 不负责 Apply、Status、ClusterPolicy、Node scope 更新或 vmagent ConfigMap 写入。
这些行为分别由 AcceleratorOperator、Hami control-plane 和 MetricsComponent 负责。

Operator/manifests 子包提供新路径的纯渲染函数；Legacy 路径继续调用
HamiComponent 自己的 Reconcile。这样新路径的 accelerator 组件最终由
AcceleratorOperator 内聚管理，公共 component 只保留兼容旧路径的职责。

由于 Operator package 不能反向依赖内部 AcceleratorManager，renderer 依赖最小的
ProfileProvider/PluginProvider 能力接口，避免形成 import cycle。后续如果现有模板无法
满足某个 accelerator 的部署要求，在 Operator package 内新增专用模板，不扩展公共
Metrics/HAMi 生命周期。


## Operator 资源 Apply

Kubernetes Operator 渲染出的资源由 Operator 直接使用仓库现有的 Kubernetes deployer
Apply，而不是让 cluster 层再次接管资源写入：

AcceleratorOperator
  → Renderer.Render()
  → deploy.NewKubernetesDeployer(...)
  → Apply

这样 Operator 同时拥有模板、values、owner、Apply/Delete 和恢复边界。deployer 负责
last-applied 配置、资源变更和删除语义；Renderer 只返回资源对象，不直接调用 Kubernetes
API。

MetricsComponent 仍负责 Legacy/公共 Metrics 资源及 vmagent ConfigMap；它不参与
Operator-owned NodeAgent、exporter 和 device plugin 的 Apply。

## GPU Operator 模式

NVIDIA 的 `AcceleratorOperator` 内部再识别具体安装方式：

```text
ClusterPolicy 的 driver.enabled=true 且 toolkit.enabled=true
  → 进入 NVIDIA GPU Operator 驱动管理模式
  → Operator-owned NodeAgent/exporter/device plugin 注入 toolkit-ready init
  → GPU Operator < 26.7 时按 Driver Pod 状态维护 nvidia.com/gpu.deploy.client
  → GPU Operator >= 26.7 时不修改 nvidia.com/gpu.deploy.client

ClusterPolicy 不存在，或 driver/toolkit 未同时 enabled
  → 仍由 NvidiaAcceleratorOperator 处理兼容路径
  → 不进入 GPU Operator client-label 协调
  → 使用 OS/platform 依赖和默认组件路径

混合或责任无法确认
  → operator 返回 Unknown/Blocked
  → 不回退到 legacy
```

`ClusterPolicy` 只决定 NVIDIA Operator 内部的 provider 策略，不决定顶层的
`DependencyDeployMode`。顶层模式已经由是否注册 `KubernetesOperatorFactory` 决定。

## Metrics 和 HAMi 边界

公共组件只处理 `legacy` accelerator：

```text
legacy accelerator
  → MetricsComponent 使用现有 Profile 的 exporter/NodeAgent 配置
  → HAMiComponent 使用现有 config inject 和 device plugin 配置

operator accelerator
  → MetricsComponent 不处理该 accelerator
  → HAMiComponent 不处理该 accelerator
  → AcceleratorOperator 管理完整组件
```

Hami 在 operator 模式下不再直接部署 accelerator device plugin。若 Operator 需要
HAMi scheduler、webhook、TLS、RBAC 或配置，则由对应 `AcceleratorOperator` 或其
专用 control-plane renderer 管理。不能让 Hami chart 和 AcceleratorOperator 同时
创建同一个 device plugin DaemonSet。


### Kubernetes 指标配置协同

本轮只接入 Kubernetes 集群。Static cluster 的 vmagent/file_sd 生成路径保持原样，
不在本轮改造。

Kubernetes 模式下，Operator 不直接写 vmagent-config ConfigMap。每次 cluster
reconcile 创建一个内存中的 MetricsTargetRegistry：

AcceleratorOperator 注册 operator-owned targets
Legacy/Public Metrics 组件注册 legacy/common targets
  → MetricsComponent 汇总 registry snapshot
  → 生成完整 vmagent 配置
  → 与 Metrics 资源一起 Apply

这样同一个 ConfigMap 只有 MetricsComponent 一个写入者，避免多个 Operator 与公共
组件互相覆盖配置。Target 使用 owner 做替换和清理，例如 operator/nvidia；
Operator 删除或切换后，本轮不再注册的旧 target 会从最终配置中消失。

Registry 只描述采集意图，例如 Pod selector、namespace、port、path、job name 和
accelerator labels，不暴露 ConfigMap、vmagent YAML 或其他后端细节。Operator 通过
OperatorContext 使用 Registry；它不直接调用 ConfigMap API。

Legacy 路径保持兼容：没有 AcceleratorOperator 的 accelerator 仍由现有
Profile/config inject 生成 exporter、NodeAgent 和原有采集目标。公共 router、
inference、node、cache 等 scrape job 继续由 MetricsComponent 生成。

Provider 或 Operator reconcile 失败时，不提交本次新的 target snapshot，保留现有
vmagent ConfigMap；Metrics 配置 Apply 失败时保留旧配置并在下一轮重试，不回退到
Legacy。


### Hami 逻辑拆分

Hami 相关逻辑分成两类，不能把所有内容都迁移到 AcceleratorOperator：

#### Ascend 硬件切分配置注入

Ascend 的硬件切分配置注入保持现状，本轮不迁移、不改动。它属于已有的公共配置注入
路径，继续由现有 Hami/accelerator 配置逻辑处理，包括已有的资源切分参数和配置渲染。

这部分不参与 NVIDIA AcceleratorOperator 的 device plugin 生命周期，也不使用 NVIDIA
GPU Operator 的 client-label 和 startedAt 恢复规则。

#### 与 device plugin 强耦合的 Hami 逻辑

对于由 AcceleratorOperator 接管的 accelerator，以下 Hami 逻辑全部收束到对应的
AcceleratorOperator：

- candidate node 预选；
- 节点 scope label/annotation 的启用和关闭；
- 节点不调度或禁用逻辑；
- device plugin 的 config patch；
- driver root、MIG strategy、device split 和资源名称；
- device plugin DaemonSet 的渲染、部署、状态和恢复；
- device plugin 与 NodeAgent/exporter 的启动协调。

这些逻辑和实际 device plugin 的资源注册、设备发现以及调度行为强相关，不能继续由
独立的 HamiComponent 和 AcceleratorOperator 分别维护。

两种模式的边界如下：

~~~text
legacy accelerator
  → Ascend 等现有硬件切分配置继续走原有 Hami/config inject
  → 现有 HamiComponent 继续负责旧路径下的节点预选、配置和 device plugin

operator accelerator
  → AcceleratorOperator 负责节点预选和不调度逻辑
  → AcceleratorOperator 生成 device plugin 配置并部署 device plugin
  → AcceleratorOperator 负责 NodeAgent/exporter 及其恢复
  → HamiComponent 不再处理该 accelerator 的 device plugin 生命周期
~~~

如果 operator accelerator 仍需要 Hami scheduler、webhook、TLS、RBAC 或控制面配置，
则由 AcceleratorOperator 调用共享的 Hami control-plane renderer；Hami chart 和
AcceleratorOperator 不能同时创建同一个 device plugin DaemonSet。

虚拟化 mode、supported resources 和基础配置解析可以抽取为共享的
VirtualizationConfigProvider，但节点预选、节点不调度和 device plugin 生命周期必须
由 AcceleratorOperator 执行，避免配置解析和实际设备部署分离。

Hami 只维护控制面状态：

~~~text
HamiControlPlaneReady
~~~

以下状态全部由 AcceleratorOperator 维护：

- Driver/Container Toolkit provider 状态；
- toolkit-ready init 和 client-label 协调；
- NodeAgent Ready；
- accelerator exporter Ready；
- device plugin Ready；
- AcceleratorStackReady。

启用 Operator 模式的虚拟化实例时，由 AcceleratorOperator 组合
HamiControlPlaneReady 和自身的 AcceleratorStackReady，决定 accelerator 依赖是否
满足。HamiComponent 不维护 device plugin 或完整 accelerator stack 状态。

Ascend 旧配置注入路径继续使用原有状态判断，不强制接入新的 AcceleratorOperator 状态。

## NVIDIA 组件恢复

NVIDIA Operator 模式下，独立节点 label 和 startedAt 补偿属于
`NvidiaAcceleratorOperator`：

```text
GPU Operator 双管理模式
  → NodeAgent/exporter/device plugin 添加 toolkit-ready initContainer
  → GPU Operator < 26.7：Driver Pod Running 时 client=true，否则 client=false
  → GPU Operator >= 26.7：不修改 client label
  → toolkit-ready 缺失时，基础组件 init 等待

provider 恢复
  → 计算当前 provider container startedAt
  → 检查基础组件 Pod 当前 container startedAt
  → 任一基础容器不晚于 provider 边界时，按当前 Pod UID 重建
```

基础组件必须属于当前 NVIDIA Operator 管理范围；推理实例 Pod 不加入该 gate，也不
使用基础组件时间补偿。单卡驱逐不在本轮功能范围内。

## 资源 owner 和模式切换

资源需要记录部署模式和 owner：

```yaml
neutree.ai/accelerator-deployment-mode: operator
neutree.ai/accelerator-operator: nvidia
```

Legacy 资源使用：

```yaml
neutree.ai/accelerator-deployment-mode: legacy
```

同一个 accelerator 的两条路径不能同时创建资源。模式切换时：

```text
legacy → operator
  → 清理 legacy owner 资源
  → Operator 接管

operator → legacy
  → Operator 删除或释放 owner 资源
  → 恢复 Profile/config inject
```

如果 operator factory 暂时不可用或 API Server 读取失败，保持已有模式和资源，不进行
推测性切换。

## 兼容边界

| 场景 | 处理路径 |
|---|---|
| accelerator 没有 OperatorFactory | 永久使用 legacy Profile/config inject |
| accelerator 有 KubernetesOperatorFactory | Kubernetes 集群始终使用 operator |
| accelerator 有 StaticClusterOperatorFactory | 仅静态集群未来使用 operator；本轮不接入 |
| Operator provider 暂时 NotReady | 仍是 operator，等待或进入受阻状态 |
| ClusterPolicy 缺失或 driver/toolkit 未同时 enabled | 不进入 GPU Operator client-label 协调，由 AcceleratorOperator 处理 OS/platform 路径 |
| API Server 读取失败 | 保持已有模式和 label，不覆盖为成功或切换路径 |
| GPU Operator 版本未知 | 在 ClusterPolicy 满足双 enabled 时，默认按 Neutree 管理路径处理 |
| CPU/非 accelerator 节点 | 不进入 accelerator operator |
| 单卡驱逐 | 不在本轮范围 |

## GPU Operator 驱动管理模式的最新最终方案

本轮只把 ClusterPolicy 的 driver.enabled=true 且 toolkit.enabled=true 作为 GPU Operator
驱动管理模式。driver 或 toolkit 任一关闭时，不执行本轮 client-label 协调。

在 GPU Operator 管理模式下，Operator-owned NodeAgent、accelerator exporter 和 device
plugin 的 Pod 模板都包含 toolkit-ready initContainer，等待：

/run/nvidia/validations/toolkit-ready

该检查复用 GPU Operator PR #2881 的增强逻辑，同时确认 `/run/nvidia/validations/toolkit-ready`
存在，并确认 Linux NVIDIA 内核模块已加载（`grep -q '^nvidia ' /proc/modules`），或处于
WSL2 的 `/dev/dxg` 路径。Neutree 不要求 initContainer 完成后再设置
`nvidia.com/gpu.deploy.client=true`，否则会因为 NodeSelector 形成自身调度闭环；Label 负责
允许基础组件调度，initContainer 负责阻止依赖未完成时主容器启动。

机器重启后 `/run` 的 tmpfs 内容清空，增强检查可以阻止基础组件过早启动。Driver Pod 或
容器在同一节点重建时，只有重新创建的基础组件 Pod 才会重新执行该检查；因此 Driver Manager
必须能够通过 client Label 或自身驱逐机制移除 Neutree 基础组件 Pod。Neutree 只保证自己的
组件不占用或阻塞 Driver Manager，无法处理未接入该依赖 Gate 的第三方 GPU 组件。需要由集群
集成方为这类第三方组件配置相同的 NodeSelector 或驱逐机制。

GPU Operator 版本低于 26.7 时，控制面读取当前节点 Driver Pod 的运行状态：Driver Pod
相关主容器 Running 且 Pod Ready 时设置 nvidia.com/gpu.deploy.client=true，否则设置为
false。GPU Operator 26.7 及以后版本不修改该 label，由 Operator 自己维护。

当版本无法确认时，在 ClusterPolicy 的 driver.enabled=true 且 toolkit.enabled=true 的
前提下，默认按旧版 GPU Operator 处理，由 Neutree 管理 client label。客户可以通过移除
`neutree.ai/gpu.deploy.client.managed` Annotation 主动退出 Neutree 管理。

Annotation 的存在表示该节点由 Neutree 管理，Neutree 只会将其值设置为 `true`，不会设置
为 `false`。Label 和 Annotation 的处理规则如下：

| 节点状态 | 处理方式 |
|---|---|
| Label、Annotation 都不存在 | 满足 Neutree 接管条件时，原子设置 Label 和 `Annotation=true` |
| Label 存在、Annotation 不存在 | 视为外部管理，不修改 Label |
| Annotation 存在、Label 存在 | 视为 Neutree 管理，按 Driver Pod 状态更新 Label |
| Annotation 存在、Label 不存在 | 仍视为 Neutree 管理，恢复 Label |
| Annotation 值异常 | 仍视为 Neutree 管理，规范化为 `true` |

如果 GPU Operator 或 Driver Manager 将该 Label 改写为 `paused-*`（例如
`paused-for-driver-upgrade`），说明 GPU Operator 已经开始管理该 Label。Neutree 不与其并行
协调，而是移除 `neutree.ai/gpu.deploy.client.managed` Annotation，释放管理权并保留 Label
当前值，后续由 GPU Operator 独立恢复和维护。

集群删除时，如果 Annotation 存在，只删除 Annotation，保留
`nvidia.com/gpu.deploy.client` 的当前值，避免影响其他可能使用该 Label 的组件。客户通过
移除 Annotation 退出管理时，应保留该 Label；如果同时删除 Label，节点会再次满足首次接管
条件，后续可能被 Neutree 重新接管。

首次写入 Label 和 Annotation 使用同一个 Node Patch，并处理 ResourceVersion 冲突，避免
出现只写入其中一个对象的半托管状态。

推理实例 Pod 继续由原有 owner 自愈。Neutree 只在每次 Kubernetes Endpoint reconcile
时尽力删除当前 Endpoint owner 链下、phase=Failed 且 reason=UnexpectedAdmissionError
的 Pod；其他失败原因、Running Pod、外部 owner Pod 不删除。

## 实现顺序

- 在 Accelerator Package 增加 `DependencyDeployMode` 和
  `PluginDependencyDeployMode`。
- 扩展 plugin registration，支持 Kubernetes/Static Operator factory。
- 在 AcceleratorManager 增加 `OperatorForKubernetesCluster`。
- 在 Kubernetes cluster reconcile 中按 matcher 和 deploy mode 分发。
- 将 Metrics/HAMi 的 legacy 逻辑限定为未被 Operator 接管的 accelerator。
- 保留 Ascend 现有硬件切分配置注入，不在本轮迁移；将与 device plugin 强耦合的
  节点预选、节点不调度、配置和部署逻辑收束到 AcceleratorOperator。
- 实现 NVIDIA `AcceleratorOperator`，集中处理 NodeAgent、exporter、device plugin、
  GPU Operator provider 和恢复逻辑。
- 增加模式、owner、Operator 状态和 legacy/operator 冲突测试。
\n
\n
