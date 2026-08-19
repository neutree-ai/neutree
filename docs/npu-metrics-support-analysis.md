# Atlas 300I Duo NPU 指标支持分析

> **注意**：本文是历史调研文档，保留原始证据。NPU 监控的**权威设计**见
> [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md)；面向用户的验证状态
> 见 [NPU 指标支持矩阵](./npu-metrics-support-matrix.md)。本文 `v26.0.0` 基线已由
> 2026-08-12 实测的 `v26.1.0` 抓取取代；`910B` 相关结论已按
> [mind-cluster master 源码](https://github.com/Ascend/mind-cluster/tree/master/component/npu-exporter)
> 修正（见 §910B 候选矩阵）。与权威设计冲突处以权威设计为准。

## 范围和证据

本报告评估当前 `neutree-node-agent` 能否通过 NPU Exporter v26.0.0
暴露 Ascend Atlas 300I Duo（Ascend 310P）的指标，以及 Neutree 是否应自行
调用 DCMI。

当前首期产品范围已扩展为 310P 和 910B；本报告中的逐项指标证据仍仅针对已安装的
Atlas 300I Duo / 310P。910B 必须在实现前单独完成 collector availability、单位和
标签兼容矩阵，不能将本报告的 DDR 或不支持 PCIe 结论直接套用。上游文档可以全局
启用指标组，但 Exporter 源码仅在识别到产品支持时注册对应采集器。310P 实际适用的
采集器为 BaseInfo、DDR 和 vNPU；HBM、PCIe、HCCS、SIO、RoCE、Network、Optical、
UB 采集器不会为该卡注册。

当前版本目标是尽可能支持物理整卡清单、物理指标、整卡 allocation 和通用副本级
accelerator 指标；副本级指标仅在设备、container/replica 和内存语义在 310P/910B
真实硬件上验证后发布。无法唯一归因、产品不支持或语义不可靠时不输出该时序，并在
公开 [NPU 指标支持矩阵](./npu-metrics-support-matrix.md)说明原因。vNPU inventory、
allocation、endpoint usage 和 dashboard 均不在范围内，`vnpu_pod_*` 不进入
`neutree_endpoint_replica_accelerator_*` 指标族。

整卡 allocation 是当前版本目标，但按 roadmap 在物理指标和真实设备 ID 映射验证后
实现，不与第一阶段物理指标交付绑定。

310P 和 910B 共享单一企业 `npu` adapter。adapter 不按产品名硬编码内存指标：对同一
`vdie_id`，优先使用完整的 HBM `used+total` 指标对，缺失时使用完整的 DDR `used+total`
指标对；任一产品的实际序列、单位和数值必须通过验证。`model_name` 仅用于 product label
规范化与格式校验，不使用产品白名单：只去除首尾空白，结果必须非空、最多 128 bytes 且
不含控制字符，保留大小写和其它可打印字符；同一 `vdie_id` 的参与样本必须携带相同的
规范化值。缺失、非法或冲突时该设备不生成任何 `neutree_*`，只保留原始序列。
产品变体不形成 `npu310p`、`npu910b` 等新的 adapter type。
产品不支持、Exporter 未暴露或 adapter 验证无效的可选指标一律缺失，不输出 `0`、
`NaN` 或额外的 unknown-status 时间序列。

下述 Neutree 结论均基于当前实现，而非目标设计：

- `cmd/neutree-node-agent/neutree-node-agent.go` 将一个通用
  `accelerator-exporter` 抓取目标接入指标服务。
- `internal/observability/neutreemetrics/normalizer/normalizer.go` 只识别
  DCGM 指标名，并把加速卡类型固定为 `nvidia_gpu`。
- `internal/observability/neutreemetrics/devicesnapshot/device_snapshot.go`
  仅通过 `DCGM_FI_DEV_GPU_UTIL` 发现设备，并生成 NVIDIA 设备快照。
- `internal/observability/neutreemetrics/hardware/hardware_inventory.go`
  仅解析 DCGM/NVML/CUDA 硬件字段。
- `api/v1/accelerator_plugin.go` 与
  `internal/cluster/component/metrics/exporters.go` 已具备通用 Exporter
  Profile/部署能力，但没有厂商指标适配器，也没有 Kubernetes 中 NPU Exporter
  所需的宿主机设备、驱动库挂载和显式权限能力；通用 Socket 挂载属于后续可选能力，
  不进入首期 NPU Profile。

## CPU 兼容性

CPU-only 节点、未配置 accelerator exporter 的节点，以及 exporter 被禁用的集群
必须继续运行 Node Agent 并输出 node-exporter/cAdvisor/cgroup 派生的节点和运行时
指标。`--accelerator-type` 是由 component planner 与 Node Agent 镜像同时下发的 adapter
选择，而不是每个节点硬件存在性的声明：即使统一 DaemonSet 配置为 `npu`，本机没有
NPU Exporter 的 CPU 节点仍只输出通用指标。

“没有本机 Exporter”与“配置了 `npu` 但 Enterprise adapter 未注册”必须区分：前者是
统一 DS 中 CPU 节点的正常状态；后者是明确的组件版本/配置错误，应让 NPU Node Agent
失败，以免静默丢失加速器可观测性。Kubernetes 和静态 Ray/SSH 均应支持 CPU 与 NPU
节点共存，NPU Exporter 仅部署/抓取到匹配 NPU 类型的节点。

因此启动校验规则是：`accelerator-type` 为空时允许 CPU-only；非空且 adapter 未注册
时 fail-fast；非空且 adapter 已注册但本机没有可用 Exporter 时正常运行，只跳过该节点
的 accelerator 样本。

Kubernetes 首期的统一 DaemonSet 只接受“CPU 节点 + 一种加速器 exporter 类型”。规划器
根据 Node 与 `AcceleratorPlugin` 的匹配结果选择全局 `--accelerator-type`；若匹配出两种
或以上类型，必须在规划阶段拒绝配置，而不能按优先级任选一个或静默回退。静态 Ray/SSH
不受此限制，按每个静态 Node 的 `AcceleratorType` 生成本地组件配置。零个匹配则为
CPU-only：不部署 accelerator exporter，也不向 Node Agent 传入 `--accelerator-type`。
“Node 匹配 AcceleratorPlugin”的唯一权威条件是插件返回的
`AcceleratorProfile.MetricsExporter.Runtime.NodeSelector`：同一选择器同时决定 managed
Exporter DaemonSet 的部署范围和上述 Node Agent 类型推导；首期不新增第二套 plugin
selector API。任一声明 `MetricsExporter` 的 Profile 都必须提供非空且有效的 Runtime
`NodeSelector`；缺失是 Profile 配置错误，不能解释为“所有节点”或 CPU-only。

Node 标签变化使一个已运行集群匹配多种类型时，controller 必须先校验再变更资源：保留
最近一次成功下发的 Node Agent/Exporter，metrics component 标记为配置错误，等待管理员
恢复为单一类型。不得因标签漂移删除或重配现有监控组件。

`npu` adapter 对 310P 和 910B 使用产品能力表，不再用 `npu310p`/`npu910b` 区分协议；
但一个 managed Exporter DaemonSet 只有一份 Runtime Profile。两种产品只有在真机验证其
镜像、volume、设备文件、Socket、capability 与 `privileged` 设置完全一致，属于同一个
exporter runtime compatibility group 时，才允许在同一 Kubernetes 集群混部。否则规划阶段
拒绝产品混部。首期 `npu` 只提供一份 Runtime Profile，不引入按产品 selector 的 exporter
variant API；因此仅在该共享兼容组对两种产品都完成真机验证后才同时发布 Kubernetes
支持，否则暂缓 910B Kubernetes 支持。静态节点虽按每个 Node 的 `AcceleratorType` 配置，
但这不是按产品选择 Runtime Profile，受同一份 `npu` Profile 约束。

首期由 Enterprise NPU 集群的 component planner 临时硬编码指定 Enterprise Node Agent
镜像和 `--accelerator-type=npu`；社区版保持当前 OSS 的硬编码默认镜像。后续引入按集群
解析的 release info，使 Enterprise 与 OSS 可以独立定义和发布各自的组件版本。无论版本
来源是当前规划器还是 release info，镜像与 adapter 参数都必须作为同一个 component
revision 变更。

现有 Node Agent 将 accelerator exporter 端口硬编码为 managed 的 `19400`、external 的
`9400`，不能用于 NPU。显式 `accelerator-type` 模式新增
`--accelerator-exporter-port` 和 `--accelerator-exporter-metrics-path`，由 planner 从唯一
选中的 `AcceleratorExporterProfile.Port/MetricsPath` 推导并与镜像、type 原子下发。
Kubernetes 只发现本节点匹配 Exporter Pod 的地址，静态 Ray/SSH 使用 localhost；两个后端
都不得猜测厂商端口或路径。无 `accelerator-type` 的 legacy DCGM 兼容路径保留当前固定端点。
Profile 的 port 必须为正整数，metrics path 必须为合法绝对 HTTP 路径。

Exporter 健康不由 Node Agent 判定：Kubernetes 以 Exporter DaemonSet readiness 和
metrics component 状态为准，静态 Ray/SSH 以 `NodeComponentHealthCheck` 为准。Node
Agent 对已配置 adapter 的 Exporter 做 best-effort 抓取；一次抓取失败只跳过加速器
样本，不能将 NPU 节点的设备语义改写为 CPU，也不能作为 Exporter 健康告警的权威来源。

## 结论

使用 NPU Exporter 作为 NPU 硬件指标采集器，不要在核心
`neutree-node-agent` 中集成 DCMI。

NPU Exporter 已通过 DCMI 动态库调用驱动/HDK 并提供 Prometheus 接口。若在
Node Agent 中重复实现，会让 Agent 与宿主机 ABI、DCMI 库路径和权限、设备文件、
驱动升级生命周期耦合，同时造成重复轮询。DCMI 调用也不能解决端点指标中更困难的
问题，即把硬件进程或容器关联到 Neutree 的 endpoint、replica 和 allocation。

需要实现的是基于 Exporter 的 Ascend 适配器以及 allocation/usage 关联。社区版仅
提供通用 adapter registry 和显式 accelerator-type 透传；企业版 Node Agent 镜像负责
注册和实现企业插件拥有的 `npu` adapter、NPU Exporter 解析以及 Ascend 运行时要求。
CPU-only 模式不进入 adapter 选择。只有当
Exporter 缺少某个 310P 已支持且业务必须使用的指标时，才考虑 DCMI 兜底；此能力
也应封装在 Ascend 专用采集器中，不应进入通用 Node Agent normalizer。

## 对既有 GPU 采集链路的复用分析

现有 GPU/DCGM 流程不能直接满足 NPU 要求，但以下基础设施可复用：

| 既有能力 | 当前实现 | 对 NPU 的结论 |
| --- | --- | --- |
| Exporter Profile | GPU Profile 已声明 image、args、port、env、config file 和 NodeSelector | 可复用为 NPU Profile 的外层契约。 |
| managed Exporter 部署 | metrics component 枚举 Profile 并按 NodeSelector 将匹配 exporter 部署为 DaemonSet | 可复用，但需加入单类型规划、NPU volume/runtime 和 readiness 规则。 |
| 本节点抓取 | Node Agent 在 Kubernetes 中发现本节点带目标标签的 Exporter Pod；静态使用 localhost | 可复用地址发现模型。 |
| 通用 Neutree 指标出口 | normalizer 已输出 `neutree_accelerator_*` 和 node/replica 级指标 | 目标 metric contract 可复用，但每个 NPU 映射需独立证明语义。 |

下列能力不能直接复用，必须扩展或以 NPU adapter 替换：

| 缺口 | 当前 GPU 实现 | NPU 所需处理 |
| --- | --- | --- |
| 指标解析与设备快照 | normalizer、device snapshot、hardware inventory 识别 `DCGM_*` 名称，并硬编码 `accelerator_type=nvidia_gpu` | Enterprise `npu` adapter 解析 `npu_*` 指标、`vdie_id`、产品能力和单位，不能复用 DCGM parser。 |
| 端点 | Node Agent 将 managed/external accelerator exporter 固定为 `19400/9400` 和 `/metrics` | 显式类型由 Profile 下发 port/path；legacy DCGM 才保留固定端点。 |
| Runtime 挂载和权限 | GPU Profile 只使用 `--gpus all`、`SYS_ADMIN` 和 NVIDIA 环境变量；现有 Profile 不表达 host device、驱动库、Socket 或 `privileged` | 采用 `ComponentVolume`、显式 `Privileged` 和 backend transformer；不在 Docker 参数中隐藏挂载。 |
| 容器/副本关联 | GPU Exporter Profile 没有 Docker/containerd Socket；现有 allocation 假定 GPU UUID 与既有分配来源可关联 | NPU 以 PodResources 或 Ray Dashboard Actor PID 到后代进程为归属权威，验证其 `vdie_id` 可唯一关联；Socket 不属于当前 Profile。 |
| 单一 DaemonSet | 当前 metrics component 可返回多个匹配 exporter，但 Node Agent 没有 adapter type 选择 | 规划器先校验零/一/多匹配，再为统一 Node Agent 下发一个显式 type。 |

因此 GPU 流程足以缩短部署和指标出口的建设，但不能作为 NPU 运行时权限、DCMI 访问、
设备身份或副本级语义的依据。

Ray 的共享流程继续负责 Dashboard 的 actor/replica/PID 定位、Endpoint 身份、超时和
通用输出；“进程环境/进程记录中的设备引用如何转换为物理设备”下沉为 adapter 能力。
`npu` adapter 以 Dashboard Backend Actor PID 为根，检查 Exporter `process_id` 是否为
其后代，再与 Exporter 快照的 `vdie_id` 关联。Ascend 可见设备环境变量仅作诊断提示，
不能作为 allocation 事实或逻辑 index 回退，更不能据此输出 allocation 或副本级指标。

Kubernetes 的共享 provider 继续读取并保留 PodResources 中每个 container 的
`ResourceName` 与 device ID；但“该资源名是否属于本 adapter”必须由 adapter 决定。NPU
plugin/profile 以精确允许列表声明可处理的资源名，adapter 只处理命中项，再将其 device ID
映射为 `vdie_id`。共享层不得汇总所有 plugin 的 device ID，也不得出现 `huawei.com/*` 等
厂商常量。

该允许列表属于通用 `AcceleratorProfile.Allocation.KubernetesResourceNames`，不属于
`MetricsExporter`。它描述 PodResources 的调度资源语义，即使未来替换 Exporter 仍有效。
配置声明后必须非空、无重复、逐项精确匹配；初期 NPU 值为 `huawei.com/Ascend310P`，新 Device
Plugin 资源名通过同一配置显式追加，adapter 不以产品名或正则猜测。

> **2026-08-12 设计收敛**：上述 `KubernetesResourceNames` 允许列表**已被权威设计移除**。
> 当前决策是 ResourceName → Neutree accelerator type 由企业 `npu` plugin 的
> `ResourceParser` 在 planner 阶段建立（照 NVIDIA `gpu_parser.go` 模式）；NodeAgent 侧
> 不依赖 ResourceName。见
> [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md) §ResourceName → Neutree type 的映射。

### 推荐目标架构：adapter 聚合并生成通用结果

将当前分散在 exporter parser、device snapshot、allocation provider 和 normalizer 中的
厂商判断收敛为一次 adapter 调用。共享层先在超时边界内采集原始证据，再交给已选择的
adapter：

```text
Exporter scrape + PodResources/Endpoint Pod + Ray Dashboard/Actor/PID + Node context
                                  |
                                  v
                       AcceleratorEvidence
                                  |
                                  v
                           npu adapter
          parse npu_* / resolve vdie_id / resolve allocations / join usage
                                  |
                                  v
                    AcceleratorMetricResult
             DeviceSnapshot + canonical generic metric samples
                                  |
                                  v
          shared validation, common labels, Prometheus serialization
```

接口形态为 `BuildMetrics(ctx, evidence AcceleratorEvidence) (AcceleratorMetricResult, error)`。
`AcceleratorEvidence` 只承载原始 Exporter、调度、工作负载和节点证据；它不预先解释
厂商 ResourceName、环境变量或设备 ID。adapter 生成最终的通用 accelerator/node/replica
指标结果，并在缺失、歧义或未验证时省略样本。共享层仍负责 I/O、超时、CPU-only、Exporter
readiness、允许的 descriptor/标签/单位校验和公共标签注入。

adapter 不直接发送任意 Prometheus 文本：`AcceleratorMetricResult` 只能使用既有通用
metric ID 与受校验标签。这样既能让 Enterprise NPU adapter 拥有全部厂商语义，又不会让
插件绕过 Neutree 的指标契约。现有 GPU/DCGM 逻辑可逐步迁移为同一接口的实现；无类型
legacy DCGM 路径在迁移完成前保持兼容。

`AcceleratorEvidence`、`AcceleratorMetricResult` 和 resolver 接口属于
`internal/observability/...` 的运行期实现模型，不加入 `api/v1`，也不构成外部插件协议。
它们承载瞬时的 scrape、PodResources、Dashboard、PID/进程证据；`api/v1` 仅保留可部署的
Profile、Runtime 和声明式配置。

物理 Exporter 证据与调度证据独立降级：NPU scrape/解析成功但 PodResources 或 Ray
Dashboard 暂时不可用时，adapter 仍输出已验证的物理设备与物理指标；只省略
`neutree_node_accelerator_allocated/free` 和全部副本级样本。不生成 0、unknown 或 NaN，
也不改变 Exporter readiness。只有 allocation 证据完整且确认没有分配时，才显式输出
`allocated=0` 与 `free=total`，以表示真实空闲而非未知。

| 需求 | 决策 | 原因 |
| --- | --- | --- |
| 310P 物理健康、利用率、DDR 内存、温度和功耗 | NPU Exporter | 支持的采集器已通过 DCMI 暴露这些数据。 |
| 节点设备清单和稳定设备标识 | NPU Exporter 适配器 | 文档定义 `vdie_id` 可作为 NPU UUID。 |
| 副本使用量 | 仅限整卡独占的候选范围 | 静态 Ray 已验证 Dashboard Actor PID 到 Exporter `process_info`/`vdie_id`；容器指标和 Socket 不依赖也不在首期范围。 |
| 分配关系和请求内存 | Pod-resources/Device Plugin 或 Ray Dashboard Actor/PID 后代进程 | 这是调度事实，不是 DCMI 事实。 |
| NVIDIA PCIe TX/RX 字节计数的等价能力 | 310P 不支持 | 310P 不注册 PCIe 采集器；上游 PCIe 数据也是带宽/统计值，不是 DCGM 字节计数器。 |
| 通用 Node Agent 实现 | 增加厂商适配器接口 | 当前实现硬编码 DCGM/NVIDIA。 |

## 当前 Node Agent 指标契约

下表覆盖当前 Node Agent 发出的全部加速器指标。“NPU Exporter 匹配”只表示
上游可提供数据，不表示当前代码可原样输出。

| 当前 Neutree 指标 | 当前输入及语义 | 310P NPU Exporter 来源 | 匹配情况和必要转换 |
| --- | --- | --- | --- |
| `neutree_accelerator_utilization_ratio` | `DCGM_FI_DEV_GPU_UTIL`，百分比转为 0..1 | `npu_chip_info_utilization` | 支持。百分比除以 100。310P 不暴露 `overall_utilization`。 |
| `neutree_accelerator_memory_used_bytes` | `DCGM_FI_DEV_FB_USED`，MiB | `npu_chip_info_used_memory` | 支持，表示 DDR 已用内存。MiB 转字节；310P 不注册 HBM 采集器。 |
| `neutree_accelerator_memory_total_bytes` | `DCGM_FI_DEV_FB_TOTAL`，MiB | `npu_chip_info_total_memory` | 支持，表示 DDR 总内存。MiB 转字节。 |
| `neutree_accelerator_temperature_celsius` | `DCGM_FI_DEV_GPU_TEMP`，摄氏度 | `npu_chip_info_temperature` | 支持，无需单位转换。 |
| `neutree_accelerator_pcie_tx_bytes_total` | DCGM 累积字节数 | 310P 无来源 | 不支持。不得把带宽 Gauge 转换为 Counter。 |
| `neutree_accelerator_pcie_rx_bytes_total` | DCGM 累积字节数 | 310P 无来源 | 不支持，原因相同。 |
| `neutree_node_accelerator_info` | 每块已发现 GPU 一个样本 | 带 `vdie_id`、`id`、`model_name` 的基础指标 | 支持，需用 Ascend 发现条件替换 DCGM utilization 条件。 |
| `neutree_node_accelerator_total` | 按产品计数 | 同一设备清单 | 支持。产品取 `model_name`，类型使用企业插件返回的 `npu`。 |
| `neutree_node_accelerator_allocated` | 已分配 UUID 数 | Exporter 无权威来源 | Kubernetes 需 Pod-resources/Device Plugin；静态集群需 Ray 可见设备/进程映射。 |
| `neutree_node_accelerator_free` | 总数减已分配数 | 由前项导出 | 仅在 allocation 关联完成后支持。 |
| `neutree_node_accelerator_hardware_info` | 内存、PCIe bus/gen/width、NUMA 标签 | DDR 总内存和 `pcie_bus_info`；310P 未验证通用 link gen/width/NUMA 来源 | 保留既有 info 时间序列，输出已验证内存和 PCIe bus；不可避免的既有描述性标签取 `unknown`。独立数值指标缺失时不输出。 |
| `neutree_node_accelerator_nvidia_info` | CUDA、NVLink、NVSwitch、驱动标签 | 无 Ascend 对应项 | NPU 不应输出；只有消费者需要时再增加厂商专用信息。 |
| `neutree_endpoint_replica_accelerator_allocation` | Endpoint 分配、vDevice 和申请显存标签 | PodResources/Device Plugin 或静态 Ray 映射 | 当前版本目标。仅在物理 `vdie_id` 可唯一关联 replica 后输出。 |
| `neutree_endpoint_replica_accelerator_memory_allocated_bytes` | 已分配整卡的可用内存容量 | 唯一关联 `vdie_id` 的 `npu_chip_info_total_memory` | 当前版本目标。整卡唯一归属时使用物理 DDR 总容量；不是调度请求值，不能由已用 DDR 推导。Kubernetes 仍需验证 device ID 映射。 |
| `neutree_endpoint_replica_accelerator_memory_used_bytes` | 副本实际使用量 | 静态 Ray 使用 Actor 后代 `process_info` memory；Kubernetes 使用唯一整卡的 `npu_chip_info_used_memory` | 静态 Ray 按 replica 进程 memory 求和；Kubernetes 与既有 GPU 整卡 fallback 一致，只有 PodResources 到 `vdie_id` 唯一整卡归属时使用物理 DDR 已用。共享、vNPU 或映射不唯一时缺失。 |
| `neutree_endpoint_replica_accelerator_utilization_ratio` | 副本实际利用率 | `container_npu_utilization` | 仅当整卡独占，且 PodResources/Ray 归属与 NPU 样本唯一关联经实测验证后输出；否则缺失。 |

普通 CPU/主机内存指标来自 node-exporter，不受本工作影响。Endpoint 的 CPU/主机内存
指标来自 cAdvisor/cgroup，也独立于 NPU Exporter。

## 310P NPU Exporter 指标

### 设备标识和物理状态

所有基础指标均具备物理设备键所需标签：`vdie_id`（映射到
`accelerator_uuid`）、`id`（映射到 `accelerator_index`）和 `model_name`
（映射到 `product`）。完整基础标签集为 `id`、`model_name`、`vdie_id`、
`pcie_bus_info`、`namespace`、`pod_name`、`container_name`；直到 Exporter
通过 CRI/OCI 将进程映射到容器前，容器相关标签为空。

| NPU Exporter 指标 | 单位/含义 | Node Agent 映射 | 处理结论 |
| --- | --- | --- | --- |
| `machine_npu_nums` | 已发现 NPU 数，无标签 | 交叉核验设备数量 | 仅诊断用途；设备清单仍以 `vdie_id` 为键。 |
| `npu_chip_info_name` | 含设备名称标签的 Info Gauge | 产品清单信息 | 可选；标准产品字段使用 `model_name`。 |
| `npu_chip_info_serial_number` | 含序列号标签的 Info Gauge | 设备清单信息标签 | 不要将序列号加到高频使用量时序上。 |
| `npu_chip_info_product_type` | 含产品形态标签的 Info Gauge | 可选 Ascend 信息 | 不能替代 `model_name`。 |
| `npu_chip_info_health_status` | 1 健康，0 不健康 | 无当前 generic 映射 | 当前版本不输出；不能用一次成功抓取替代设备健康。 |
| `npu_chip_info_error_code`、`_1` 至 `_9` | 最多十个故障码样本 | 无当前 generic 映射 | 当前版本不输出；无样本表示没有上报故障码，并非数值 0。 |
| `npu_chip_info_temperature` | 摄氏度 | `neutree_accelerator_temperature_celsius` | 直接映射。 |
| `npu_chip_info_power` | 瓦；推理产品为板卡 MCU 功耗 | 无当前 generic 映射 | 当前版本不输出。 |
| `npu_chip_info_voltage` | 伏特 | 无当前 generic 映射 | 当前版本不输出。 |
| `npu_chip_info_aicore_current_freq` | MHz | 无当前 generic 映射 | 当前版本不输出。 |
| `npu_chip_info_utilization` | AICore 利用率，百分比 | `neutree_accelerator_utilization_ratio` | 310P 的必选来源。 |
| `npu_chip_info_vector_utilization` | Vector Core 利用率，百分比 | Ascend 补充指标 | 310P 可用；不能映射为通用利用率。 |
| `npu_chip_info_overall_utilization` / `npu_chip_info_cube_utilization` | 虽有 descriptor，但 310P 不注册这两个 collector（`supportedOverallUtilDevices`/`supportedCubeDevices` 不含 310P） | 无 | Exporter 对 310P 不输出，不能作 fallback；910B 两者均支持（overall 优先、cube 不纳入通用 util）。 |
| `npu_chip_info_network_status` | 网络健康 descriptor | 无 | 310P 非训练卡，预期无样本。 |
| `npu_chip_info_process_info_num` | 占用设备的进程数 | `neutree_accelerator_processes` | 可选诊断指标。 |
| `npu_chip_info_process_info` | 每进程 NPU 内存，MiB，附 `process_id` | 静态 Ray 进程关联候选数据 | 仅在 PID 和物理 `vdie_id` 均匹配后转为字节。 |

### DDR 和内存语义

本版本 Exporter 中，Atlas 300I Duo / 310P 只产生完整的 DDR 内存指标对。因此同设备
fallback 在该实测样本上选择 DDR，通用 Neutree 内存指标的实际语义为“设备 DDR 内存”。
全局 API 中存在 HBM 指标，但该卡不产生完整 HBM 对，不能将其作为运行时 fallback 期待。

| NPU Exporter 指标 | 单位/含义 | Node Agent 映射 | 处理结论 |
| --- | --- | --- | --- |
| `npu_chip_info_total_memory` | 物理 DDR 总内存，MiB | `neutree_accelerator_memory_total_bytes` | 必需。乘以 `1024 * 1024`。 |
| `npu_chip_info_used_memory` | 物理 DDR 已用内存，MiB | `neutree_accelerator_memory_used_bytes` | 必需。乘以 `1024 * 1024`。 |
| `npu_chip_info_hbm_total_memory` | HBM 总内存，MiB | 310P 无 | 全局文档有定义，但该产品不可用。 |
| `npu_chip_info_hbm_used_memory` | HBM 已用内存，MiB | 310P 无 | 同上。 |
| `npu_chip_info_hbm_temperature` / `npu_chip_info_hbm_utilization` | HBM 诊断数据 | 310P 无 | 不应对外承诺。 |

### 容器、进程和 vNPU 指标

NPU Exporter 可以输出关联到容器的实际使用信息，这很有价值，但它不是调度数据源。
容器标签可能过期、对主机进程为空，也可能指向 Neutree Endpoint 之外的容器。适配器
必须要求精确标识关联；无法唯一关联时，宁可不输出副本指标，也不能将节点级使用量
归因给所有 Endpoint。当前版本将在整卡独占、container/replica 精确关联且内存语义
经真实 310P/910B 工作负载验证时，逐项放行通用副本级指标；否则保持缺失。

| NPU Exporter 指标 | 单位/标签 | Node Agent 映射 | 前置条件 |
| --- | --- | --- | --- |
| `npu_container_info` | 容器标识，包括 `container_id`、`namespace`、`pod_name`、`vdie_id` | 可选关联辅助数据 | 该上游指标需要 Exporter 访问并解析 CRI/OCI Socket；不是 Neutree allocation 或 replica 归属的权威来源。 |
| `container_npu_total_memory` | 整卡 DDR 总内存，MiB | 副本 total 的验证辅助 | 不是 allocation；不直接发布为副本分配量。 |
| `container_npu_used_memory` | 整卡 DDR 已用内存，MiB | 副本内存使用量候选 | 仅当整卡独占且 container/replica 精确关联经实测验证后，MiB 转字节发布。 |
| `container_npu_utilization` | 整卡 AICore 利用率，百分比 | 副本利用率候选 | 仅当整卡独占且 container/replica 精确关联经实测验证后，百分比转 ratio 发布。 |
| `vnpu_pod_total_memory` | vNPU 配额内存，KiB | 不消费 | 首期明确不支持 vNPU。 |
| `vnpu_pod_used_memory` | vNPU 已用内存，KiB | 不消费 | 首期明确不支持 vNPU。 |
| `vnpu_pod_aicore_utilization` | vNPU AICore 利用率，百分比 | 不消费 | 首期明确不支持 vNPU。 |

vNPU 标签包括 `v_dev_id`、`is_virtual`、`aicore_count`、NPU `id`、
`model_name` 以及 namespace、Pod、容器身份。首期不解析或输出这些标签；后续若
纳入 vNPU，`v_dev_id` 应填入 `vdevice_index`，不能替代物理设备 UUID。

### 310P 明确不可用的指标族

在 v26.0.0 中，以下上游指标族不应成为 Atlas 300I Duo 的输入：
`npu_chip_info_hbm_*`、`npu_chip_info_pcie_*`、`npu_chip_info_hccs_*`、
`npu_chip_info_network_*`、`npu_chip_roce_*`、`npu_chip_optical_*`、
`npu_chip_info_sio_*`、`npu_chip_info_ub_*`。它们可能被其他 Ascend 产品形态
支持，但对 310P 生成零值时序或把缺失当作设备故障都是错误行为。

### 910B 实现候选矩阵（待真机验证）

以下内容来自 MindCluster NPU Exporter 源码的 collector 注册条件（`IsSupported`
产品表）和指标 API，属于 adapter 实现候选，不是对外支持承诺。每项仍需在 910B
真机上验证 DCMI 返回值、单位、标签、部署挂载和工作负载语义后，才可将状态写入
公开支持矩阵。

> 2026-08-12 已按 [mind-cluster master 源码](https://github.com/Ascend/mind-cluster/tree/master/component/npu-exporter)
> 核对产品表，修正了此前 `branch_v26.0.0` 推断的几处结论。权威设计见
> [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md)。

| 能力 | v26 源码候选 | 通用 Neutree 映射 | 处理原则 |
| --- | --- | --- | --- |
| 设备标识 | 基础指标的 `vdie_id`、`id`、`model_name` | UUID、index、product | 与 310P 共用 identity adapter。 |
| 通用利用率 | `npu_chip_info_overall_utilization`；基础 utilization 可作 fallback | `neutree_accelerator_utilization_ratio` | 优先 overall，百分比转 ratio；**910B 支持 cube utilization（`supportedCubeDevices` 含 910B），但 cube 不代表整卡利用率，不纳入通用 util**；无有效样本则缺失。 |
| 内存 | `npu_chip_info_hbm_used_memory`、`npu_chip_info_hbm_total_memory`，单位 MB | `neutree_accelerator_memory_used_bytes`、`_total_bytes` | HBM complete pair 优先；910B 被 `notSupportedDdrDevices` 排除，**DDR 不作 fallback**；需验证单位与可调度内存容量的语义一致。 |
| 基础健康 | 温度、功耗、健康、频率、错误码等 BaseInfo 指标 | 仅温度映射到既有通用指标 | power/temp/health/process_info 是通用物理指标，310P 与 910B 均支持；当前版本不为功耗、健康、频率、错误码新增 descriptor。 |
| PCIe | `npu_chip_info_pcie_{rx,tx}_{p,np,cpl}_bw`，单位 MB/ms，含 min/avg/max 标签 | 新增 910B 专属 PCIe bandwidth 指标 | **910B 被 `supportedPcieDevices` 支持**。不映射到 `*_pcie_{rx,tx}_bytes_total` byte counter（310P 无源）；首期新增 910B 专属带宽 Gauge，经 E2E 验证后发布。 |
| HCCS | `npu_chip_info_hccs_bandwidth_info_total_{tx,rx}` 等，单位 GB/s | 无现有 generic 映射 | 910B 被 `supportedHccsDevices` 支持；可作为产品专用补充指标候选；采集失败的 `-1` 必须过滤为缺失。 |
| DDR | 910B 被 DDR collector 排除 | 无 | 不以 DDR 作 fallback。 |
| vNPU | **vNPU collector 仅对 310P 支持（`supportedVnpuDevices` 仅 `Ascend310P`）** | 无 | 910B 的 template vNPU 不产生 `vnpu_pod_*`，当前版本不实现。 |
| 容器/副本 | `container_npu_*` 依赖容器映射及产品 collector 行为 | 副本指标候选 | 先以 PodResources/Ray 建立归属；仅在 die 独占和 NPU 样本唯一关联的真机验证后逐项发布。910B 与 310P 同走 socket 路径。 |

产品支持矩阵记录“已验证的原始指标和单位”，但不以硬编码产品名直接生成指标。运行时只
对同一设备实际存在、完整且单位和数值均通过校验的指标族归一；其余保持缺失。

## Neutree 需要的改动

1. 引入按加速器类型选择的指标适配器。NVIDIA 适配器保留现有 DCGM 行为；Ascend
   适配器识别 `npu_chip_info_*`、`container_npu_*`、`vnpu_pod_*`。所选类型应
   由 component profile 与 Node Agent 镜像原子下发，而不是从抓取结果猜测；本机仅在
   发现到匹配 Exporter 后处理加速器指标。
2. 让 Plugin/资源发现链路透传企业插件拥有的 `npu` 类型。当前 API 常量和 Node
   Agent 输出仅支持 NVIDIA、AMD，且 normalizer 将所有已发现设备固定为 NVIDIA；
   社区版不增加 Ascend 专属常量。
3. 用适配器自己的发现条件替换 NVIDIA 专用 Gate：Ascend 只要存在带非空 `vdie_id`
   的有效基础指标，即可发现设备。所有关联都必须同时保留 `vdie_id`、index 和 product。
   本机未发现匹配 Exporter 时跳过 accelerator processing，保持现有 CPU/运行时指标。
4. 复用既有通用利用率、内存、温度、inventory、整卡容量和经验证副本级指标族；
   当前版本不新增健康、功耗、频率或互连 descriptor，绝不把 PCIe 带宽样本伪装成
   字节计数器。
5. 实现 Ascend Kubernetes 整卡 allocation provider。首期只有 kubelet pod-resources 的
   设备 ID 可以稳定且唯一关联 `vdie_id` 时才输出 allocation 和副本指标；否则保持缺失。
   Ascend Device Plugin/HAMi allocation metadata fallback 在 Kubernetes 真机对照后单独决定，
   不能以逻辑 index 或环境变量猜测。对整卡独占场景，验证调度归属与 NPU 样本的唯一关联后
   输出通用副本级指标。
6. 实现静态 Ray/SSH 的整卡 Actor PID 后代进程到 `vdie_id` 的 replica 映射，并从
   `required_resources` 读取整卡数。多进程共享或无法唯一关联时，只保留物理设备级指标；
   验证通过时输出通用副本级指标。
7. 在 Kubernetes 部署 NPU Exporter 前扩展 exporter runtime profile。当前 managed
   exporter builder 只 materialize ConfigMap 和 capability，未提供宿主设备、
   DCMI/驱动库或可选 CRI/OCI Socket 挂载字段。静态 Docker 有通用
   `DockerRunOptions` 逃生口，Kubernetes 没有。

## 部署和运行约束

- NPU 首期使用 `managed` Exporter 模式。企业 `npu` Profile 是 Exporter 镜像、参数、
  `ComponentVolume`、runtime 要求和 readiness 契约的唯一输入；310P 和 910B 真实
  节点 E2E 验证通过前，不放行 Kubernetes managed 部署。
- NPU Exporter 会周期调用驱动接口。升级 NPU 驱动前，应停止业务工作负载和 Exporter。
- DCMI 动态库及父目录有属主/权限约束。这进一步说明 DCMI 应留在厂商 Exporter，
  而不是分发到 Node Agent 容器。
- Exporter 可用镜像或宿主机二进制运行，只提供 HTTP 服务；Node Agent 仅需访问
  其 Prometheus endpoint。

### CRI/OCI Socket 的实际作用

Socket 不用于 DCMI 或物理卡的 BaseInfo/DDR 采集。上游 NPU Exporter 先通过
`devmanager.AutoInit` 初始化物理设备访问；随后无条件创建 container devices parser。
该 parser 通过 Docker/containerd/CRI Socket 按 container ID 读取 OCI Spec，从
`Linux.Resources.Devices` 和进程环境中找 Ascend 设备，再组装容器名、namespace、Pod
和设备的关系。因此 Socket 服务的是 `npu_container_info` 与 `container_npu_*` 一类
容器元数据/指标，而不是 `npu_chip_info_*` 物理指标。

当前上游 v26 没有 `containerMode=none`；`docker`、`containerd`、`isula` 三种模式都
尝试连接运行时。parser 初始化失败时主程序只记录错误，仍继续启动物理 collector，所以
socket-free 运行能否稳定产出物理指标必须以目标 310P 实测确认。若不能稳定工作，不能为
了通过试验重新挂载 Socket；应维护无容器模式的 Exporter 变体或暂缓当前 managed NPU
Profile。Neutree 的 allocation/replica 归属本来就使用 PodResources 或 Ray Dashboard，
不依赖这个容器 parser。
- 上游默认启用 `npu`、`ddr`、`vnpu` 等指标组；首期共享 Profile 启用 `npu`、`ddr`、`hbm`，
  并显式关闭 `vnpu` 及其他未验证组。v26 会跳过当前产品不支持但配置为 `ON` 的 collector，
  因而 310P 跳过 `hbm`、910B 跳过 `ddr`。Exporter 默认五秒更新一次，可配置为一至六十秒；
  Prometheus 从缓存读取，短生命周期容器可能没有指标。
- Exporter 部署健康由 Profile 显式 `Readiness` 声明，映射为 DaemonSet HTTP readiness 或
  静态 component health check；NPU 默认复用 `/metrics`、端口 `8082`、初始宽限 15 秒。
  设备健康由 NPU Exporter 的硬件指标表达。现有
  `neutree_metrics_scrape_up{target="accelerator-exporter"}` 可以保留为 Node Agent
  抓取诊断，但不得作为 Exporter readiness 或告警的权威来源。

## 已确认的数据模型：通用 Component Volumes

`AcceleratorExporterProfile.ConfigFiles` 只能生成 ConfigMap 挂载，不能表达 NPU
Exporter 运行所需的宿主机驱动目录、DCMI 动态库、设备文件或 CRI/OCI Socket。不要
让 Kubernetes 复用 `DockerRunOptions` 解析任意 `--volume`/`--device` 字符串：这会
把容器运行时语法和不受约束的宿主机访问带入 API。

建议在 `api/v1` 定义可被所有 Neutree-managed component 复用的厂商无关
`ComponentVolume`/`ComponentVolumeMount`。它描述“哪个已验证的宿主机路径，以
何种类型和访问模式挂载到容器”，不在 API 中出现 Ascend、DCMI 或某个具体 `/dev`
设备名称。它与 Kubernetes volume 模型一致，也允许未来一个 volume 被多个容器挂载；
当前 Exporter 只有一个容器，仍保持最小实现。现有 `NodeComponentVolume` 迁移到该
通用模型属于后续重构，不在本次 NPU 接入范围；`ModelCache` 也保持独立，因为其
NFS/PVC 存储语义并不具备 Docker 等价物。

```go
type ComponentVolume struct {
	Name   string                `json:"name"`
	Source ComponentVolumeSource `json:"source"`
}

type ComponentVolumeSource struct {
	// HostPath is the only profile-defined source. ConfigMap files continue to
	// be materialized through ConfigFiles.
	HostPath *ComponentHostPathVolumeSource `json:"host_path,omitempty"`
}

type ComponentHostPathType string

const (
	ComponentHostPathDirectory  ComponentHostPathType = "directory"
	ComponentHostPathFile       ComponentHostPathType = "file"
	ComponentHostPathSocket     ComponentHostPathType = "socket"
	ComponentHostPathCharDevice ComponentHostPathType = "char_device"
)

type ComponentHostPathVolumeSource struct {
	// Path is the absolute path on a matching node.
	Path string `json:"path"`
	// Type maps to Kubernetes HostPath type and validates the expected node object.
	Type ComponentHostPathType `json:"type"`
}

type ComponentVolumeMount struct {
	// Name references one item in Volumes.
	Name string `json:"name"`
	// MountPath is the absolute path in the exporter container.
	MountPath string `json:"mount_path"`
	// ReadOnly defaults to true. False must be explicitly requested for a device
	// or a socket that requires read-write access.
	ReadOnly *bool `json:"read_only,omitempty"`
}

type AcceleratorExporterRuntimeProfile struct {
	// Static Docker only. Kubernetes managed exporters always use Pod networking.
	HostNetwork      bool                             `json:"host_network,omitempty"`
	HostPID          bool                             `json:"host_pid,omitempty"`
	// Privileged defaults to false and must be explicitly justified by the
	// plugin-owned runtime profile.
	Privileged       bool                             `json:"privileged,omitempty"`
	Capabilities     *AcceleratorExporterCapabilities `json:"capabilities,omitempty"`
	NodeSelector     map[string]string                `json:"node_selector,omitempty"`
	Volumes          []ComponentVolume                `json:"volumes,omitempty"`
	VolumeMounts     []ComponentVolumeMount           `json:"volume_mounts,omitempty"`
	DockerRunOptions []string                         `json:"docker_run_options,omitempty"`
}
```

### 后端投影规则

| Volume source 类型 | Kubernetes managed exporter | 静态 Docker exporter |
| --- | --- | --- |
| `directory` | `HostPathVolumeSource`，`type: Directory` | `--volume host:container:ro/rw` |
| `file` | `HostPathVolumeSource`，`type: File` | `--volume host:container:ro/rw` |
| `socket` | `HostPathVolumeSource`，`type: Socket` | `--volume host:container:ro/rw` |
| `char_device` | `HostPathVolumeSource`，`type: CharDevice` | `--device host:container`；若 Docker 不能保留所需访问模式则拒绝部署 |
| `Capabilities`、`HostPID`、`Privileged` | 原有 `securityContext.capabilities`、`hostPID`、`privileged` | 映射到对应 Docker 参数；`Privileged` 仅显式为 `true` 时生成 `--privileged` |
| `DockerRunOptions` | 不读取 | 仅保留为历史兼容 escape hatch；不可成为 NPU Profile 的必需条件 |

Volume 从 Profile 到 manifest 的转换必须是结构化的。Kubernetes builder 生成
`corev1.Volume`/`corev1.VolumeMount`，静态 builder 生成 Docker 参数；两个后端都不得
自行猜测路径，也不得从环境变量或镜像标签构造挂载。Profile 定义的 `Volumes` 仅允许
一个 `host_path` source；现有 `ConfigFiles` 继续生成内部 ConfigMap volume，两个集合
的名称必须全局唯一。

首期 Enterprise `npu` Profile 不声明 Docker 或 containerd Socket volume，与既有 GPU
采集流程保持一致。Neutree 的 allocation/replica 归属来自 PodResources 或 Ray
Dashboard/进程证据，而不是 Exporter 的容器元数据。`socket` source 保留给后续单独安全
评审、且确有必要的 `container_npu_*` 增强；它不进入当前 Runtime Profile。

网络按后端投影：静态 Docker NPU Exporter 使用 `HostNetwork=true`，Node Agent 通过
localhost 访问；Kubernetes managed Exporter 固定使用 Pod 网络，Node Agent 发现同一
Node 上 Exporter Pod 的 Pod IP。共享 Profile args 不得将 Exporter 固定绑定到
`127.0.0.1`，否则 Kubernetes 无法访问；静态也不以 Docker `--publish` 作为连接契约。

### 必须的校验和安全边界

1. `Name` 必须唯一且为合法 Kubernetes volume 名；`HostPath.Path`、`MountPath` 必须是
   clean 的绝对路径，拒绝空值、`..`、重复的容器挂载路径和容器根目录 `/`。
2. `Type` 必须显式给出，Profile/manifest 不允许使用 Kubernetes
   `DirectoryOrCreate`，防止拼写错误在宿主机创建意外目录。
3. 默认只读；非只读必须在插件 Profile 中显式声明并通过单元测试。驱动库应只读，
   字符设备和需要 connect 的 Socket 按上游 Exporter 实测权限声明。
4. 社区版只校验结构、路径安全和后端映射；具体允许哪些宿主路径、需要哪些设备和
   capability，由企业 `npu` 插件 Profile 决定。这样 OSS 不引入厂商路径白名单或
   Ascend 常量。
5. Kubernetes 必须让 `HostPath` 类型与 `Kind` 对应，启动前检查目标节点对象类型；
   缺失或类型不符应使该 Exporter Pod 失败并通过 metrics component 状态上报，而不是
   降级为没有设备指标的正常运行。
6. Plugin Profile 不得用 `DockerRunOptions` 声明 `--volume`、`-v`、`--mount` 或
   `--device`；这些参数必须由结构化 `ComponentVolume` transformer 生成。历史
   `DockerRunOptions` 仅临时保留给非挂载 Docker 参数，检测到重复或挂载参数即拒绝计划。
7. `Privileged` 默认关闭。只有插件 Profile 能显式开启，且必须由供应商部署要求和
   310P/910B 真机验证记录原因；不得把该权限隐藏在 `DockerRunOptions` 中。Enterprise
   `npu` Profile 首期可显式设为 `true`：官方 Exporter 部署需要宿主设备访问，且当前
   310P probe 在此配置下通过。Profile 必须记录镜像 digest、mount、无 Socket 边界和
   理由；后续最小权限优化不阻塞本期交付。

首期 Enterprise NPU Profile 固定使用已验证的不可变 digest；`v26.0.0` tag 仅用于可读
说明，不能作为运行时解析依据。后续只能由 Release Info 选择新的 digest，并为该 digest
重新记录硬件、驱动和 E2E 证据。

### 企业 `npu` Profile 的实例形状

> **2026-08-12 设计收敛**：本 Profile 实例基于 v26.0.0 实测。权威设计的 Profile 已更新为：
> **image 仅用 tag（`npu-exporter:v26.1.0`，不承诺 digest）**；`-containerMode` 按后端区分
> （静态 Docker 用 `docker`、Kubernetes 用 `containerd`，见权威设计 §数据模型与 Profile）。
> 下例仅保留 v26.0.0 历史结构说明，不作为部署清单。

```yaml
metrics_exporter:
  name: npu-exporter
  image: swr.cn-south-1.myhuaweicloud.com/ascendhub/npu-exporter@sha256:298d31a8ddb472587c3669d9c8b2c4499eed7383f91ebe042ac29cc3502c65b6
  args: ["-ip=0.0.0.0", "-port=8082", "-updateTime=5", "-containerMode=docker"]
  port: 8082
  metrics_path: /metrics
  config_files:
    - path: /usr/local/metricConfiguration.json
      content: |-
        [
          {"metricsGroup": "ddr", "state": "ON"},
          {"metricsGroup": "hccs", "state": "OFF"},
          {"metricsGroup": "npu", "state": "ON"},
          {"metricsGroup": "network", "state": "OFF"},
          {"metricsGroup": "network-npu", "state": "OFF"},
          {"metricsGroup": "pcie", "state": "OFF"},
          {"metricsGroup": "roce", "state": "OFF"},
          {"metricsGroup": "sio", "state": "OFF"},
          {"metricsGroup": "vnpu", "state": "OFF"},
          {"metricsGroup": "version", "state": "OFF"},
          {"metricsGroup": "optical", "state": "OFF"},
          {"metricsGroup": "hbm", "state": "ON"},
          {"metricsGroup": "ub", "state": "OFF"},
          {"metricsGroup": "optical-npu", "state": "OFF"}
        ]
  runtime:
    host_network: true # Static Docker only; Kubernetes uses Pod networking.
    privileged: true
    volumes:
      - name: ascend-driver
        host_path:
          path: /usr/local/Ascend/driver
          type: directory
      - name: ascend-dcmi
        host_path:
          path: /usr/local/dcmi
          type: directory
      - name: host-sys
        host_path:
          path: /sys
          type: directory
    volume_mounts:
      - name: ascend-driver
        mount_path: /usr/local/Ascend/driver
        read_only: true
      - name: ascend-dcmi
        mount_path: /usr/local/dcmi
        read_only: true
      - name: host-sys
        mount_path: /sys
        read_only: true
```

一台卡有多个设备节点时，企业 Profile/部署 planner 必须生成完整且确定的 volume
清单，或提供经过验证的设备目录 volume 模型；不能硬编码单个设备节点。310P 和 910B
的路径、设备数量、capability 和 `privileged` 需求都应通过各自的 hardware fixture 和
真实节点 E2E 确认。`socket` source 不在当前 `npu` Profile 中。

上游镜像默认开启全部内建指标组。首期 Profile 通过 `ConfigFiles` 完整覆盖
`/usr/local/metricConfiguration.json`：统一开启 `npu`、`ddr`、`hbm`，并显式关闭其余全部
内建组，尤其是 `vnpu`。v26.0.0 在 collector 初始化时会跳过不支持当前产品的 `ON` 项，
因此 310P 不会采集 HBM、910B 不会采集 DDR；这允许同一配置服务两个产品，但不替代各自的
硬件 E2E。这样 vmagent 不会直接采集原始 `vnpu_*`，而非只依赖 Node Agent adapter 不做转换；
其它未验证的互连/网络 collector 也保持关闭。此配置依赖当前 v26.0.0 的“文件存在即完整替换”
实现；更新 Exporter digest 时必须复验该语义。

vmagent 对上述已启用 collector 的原始 NPU Exporter 指标不做 metric relabel 或 drop，
包括带 `process_id` 的 `npu_chip_info_process_info`。这些是客户可用的厂商诊断数据；
Node Agent 只消费其中生成通用 `neutree_*` 指标所需的样本，既不代理也不改写原始序列。

在 310P 静态节点的真机试验中，早期只开启 `ddr`、`npu` 的配置以只读 bind mount 覆盖镜像
文件后，Exporter 在 8 秒内就绪，持续返回四个设备身份、四个利用率样本和五个进程样本，
`vnpu` 样本数为零。启动日志逐项确认仅 `ddr`、`npu` 为 ON。共享配置将额外把 `hbm` 设为
`ON`；其在 310P 上的安全跳过来自 v26 源码 capability，尚未作为该历史真机结果的一部分。
因此静态 `NodeComponentHealthCheck` 的首次探测宽限必须不少于 15 秒，以涵盖无 Socket 时的
CRI client 超时和首次 DCMI 采集。
该镜像的 `-ip` 参数为必填：省略时会以 `listen ip is invalid` 退出。共享 `npu`
Profile 必须携带唯一的 `-ip=0.0.0.0`，两个 backend 都不覆盖它；缺失、重复或非
wildcard 值是 Profile 配置错误。Ray Head vmagent 按既有 GPU 的 file-SD 模式从每个
NPU Node IP 抓取原始诊断指标；310P 真机已验证该地址的 `:8082/metrics` 返回 HTTP
200。Kubernetes 使用同一参数监听 Exporter Pod IP。Profile 同时固定上游默认
`-containerMode=docker`；在无 Socket 边界下该可选 parser 会按预期报错，但 310P 已验证
物理设备与进程采集不受影响。

## 实机调研与验证

本章是当前版本的前置调研，不预设任何候选指标最终可发布。以 Atlas 300I Duo / 310P
为首个 fixture；910B 使用同一流程独立复测。每项调研都保存原始 Exporter 抓取结果、
节点/工作负载状态、组件镜像 digest、驱动/CANN 版本和可复现命令，并据此更新
[`NPU 指标支持矩阵`](./npu-metrics-support-matrix.md)。调研前，依赖未证实关联的
allocation 和副本级时间序列一律不输出。

### 已采集的 310P 基线

采集时间：2026-07-22。目标节点为 openEuler 22.03 LTS-SP4，内核
`5.10.0-324.0.0.225.oe2203sp4.aarch64`，`npu-smi`/HDK 版本为 `25.5.2`，Docker
版本为 `26.1.3`。`npu-smi info` 识别出两张 310P3 板卡、共四个 device（逻辑设备
0--3），均为健康状态且采集时无 NPU 进程。宿主机已存在字符设备
`/dev/davinci0`--`/dev/davinci3`、`/dev/davinci_manager`、`/dev/devmm_svm` 和
`/dev/hisi_hdc`，以及 `/usr/local/Ascend/driver`、`/run/containerd/containerd.sock`
和 `/run/docker.sock`。采集时未运行任何 Docker 容器，尚未部署 NPU Exporter。

这只是 Runtime 输入清单的起点：尚未证明所有设备都是必需挂载，也尚未定位 DCMI
动态库及其权限链路。Docker/containerd Socket 已明确不进入当前 Profile。后续启动试验
以逐项最小化为原则，不能直接将上述所有对象写入最终 Profile。

### Socket-free 物理采集试验结果

采集时间：2026-07-22。以镜像
`swr.cn-south-1.myhuaweicloud.com/ascendhub/npu-exporter:v26.0.0`
（digest `sha256:298d31a8ddb472587c3669d9c8b2c4499eed7383f91ebe042ac29cc3502c65b6`）运行
短时 probe。probe 使用 `--privileged`、`--network host`，只读挂载
`/usr/local/Ascend/driver`、`/usr/local/dcmi`、`/sys`；未挂载 Docker、containerd 或
CRI Socket。`--network host` 仅用于绕过该测试节点 Docker legacy iptables 缺少 `DOCKER`
NAT 链导致的端口发布失败，不构成 Kubernetes Runtime Profile 的要求。
probe 的 `-ip=127.0.0.1` 仅用于这次静态手工试验的暴露面收敛；共享 Profile 不采用该
绑定，以支持 Kubernetes 的 Pod IP 抓取。

Exporter 按预期记录 container parser 初始化失败，但继续运行并监听 `127.0.0.1:8082`。
`/metrics` 稳定返回 `machine_npu_nums 4`、四个非空 `vdie_id`、310P3 产品名、PCIe bus
ID、温度、DDR 总/已用内存、AICore 利用率以及健康/功耗/电压/频率/进程信息。DDR 与
`npu-smi info` 同时采集的四个 device 值一致；`npu_container_*` 时间序列数量为零，符合
无 Socket 预期。这证明 HDK 25.5.2 的 310P3 可在当前安全边界内进行物理采集，不能据此
推导容器级或副本级支持。

本次成功 probe 使用 `Privileged=true`。因此初始 Enterprise NPU Runtime Profile 可将
该值作为显式默认配置，而不是将权限藏在 Docker 参数中；理由为官方 Exporter 的宿主设备
访问要求和上述实测基线。该结论仅证明该配置有效，尚未证明非特权不可行，后续可作为
最小权限优化单独测试，不能改变当前无 Socket 约束。

### Kubernetes Managed Exporter 与 PodResources 试验结果

采集时间：2026-07-23。目标 Kubernetes 集群的 `npu` 节点即上述 310P3 真实节点，具有
`accelerator=huawei-Ascend310P` 标签和 `huawei.com/Ascend310P=4` allocatable 资源，运行
Ascend Device Plugin。三个已存在的 vLLM Pod 分别请求 `1`、`2`、`1` 张整卡。直接调用
Node Agent 已挂载的 kubelet PodResources v1 socket，得到唯一的实际分配：`qwen-1 ->
Ascend310P-0`、`qwen-2 -> Ascend310P-1, Ascend310P-3`、`qwen-3 -> Ascend310P-2`。这与
Device Plugin 写入的 Pod annotations 一致，但 PodResources 是本期 allocation 的唯一权威
来源，annotation 不参与 fallback。

同一节点以普通 Pod 网络运行官方 Exporter digest，使用 `Privileged=true`、只读 HostPath
挂载 `/usr/local/Ascend/driver`、`/usr/local/dcmi`、`/sys`，不挂载 Docker、containerd 或
CRI socket。显式 `command: ["/usr/local/bin/npu-exporter"]` 和共享参数
`-ip=0.0.0.0 -port=8082 -updateTime=5 -containerMode=docker` 下，Exporter 在约 7 秒后就绪。
Pod IP 的 `:8082/metrics` 可由同一集群的 vmagent Pod 直接读取，返回
`machine_npu_nums 4` 及 index `0`--`3` 的非空 `vdie_id`。Device Plugin 的
`Ascend310P-<index>` 因此可经 NPU adapter 的通用末段 index 解析后唯一连接到 Exporter 的
`id=<index> -> vdie_id`；这证明 Kubernetes 整卡 allocation 和副本级关联的数据路径可行。
`vnpu_*` 和 `npu_container_*` 均为零，符合禁用 vNPU 和 socket-free 的当前边界。

该试验还发现一个必须在 Roadmap 1 解决的模型缺口。该镜像没有 OCI `ENTRYPOINT`，只定义
`CMD ["/bin/sh", "-c", "/usr/local/bin/npu-exporter"]`；Kubernetes 的 `args` 会替换
`CMD`，所以现有仅含 `Args` 的 `AcceleratorExporterProfile` 会尝试执行
`-ip=0.0.0.0` 并启动失败。Profile/transformer 必须新增 `Command`，并将上述二进制路径
显式渲染到 Kubernetes `container.command`。同时，当前 renderer 只投影 capabilities、
ConfigMap 文件和 selector，尚未投影 `Privileged` 与 typed HostPath
`ComponentVolume`/`ComponentVolumeMount`；没有这三项，当前生产 renderer 不能部署该
已验证的 NPU Profile。此为确定的实现前置条件，不是硬件或 PodResources 可行性风险。

### Ray Serve allocation 与进程关联试验结果

采集时间：2026-07-23。目标节点已运行 Ray、Ray Dashboard、Ray Serve 和三个实际 Backend
Actor。Ray 资源账本中的 NPU 请求为 `2 + 1 + 1`，合计四张整卡。以同一 socket-free
Exporter probe 采集时，`npu_chip_info_process_info` 覆盖逻辑设备 `0`--`3` 的五个进程
记录。通过进程树验证，设备 `0`、`1` 的 NPU 进程均为两卡 Backend Actor 的后代，设备
`2`、`3` 分别为两个一卡 Backend Actor 的后代；Exporter 的 `process_id` 与
`vdie_id` 因而可精确连接到 Ray Actor。

这验证了静态 Ray 的整卡分配关联路径：Dashboard Actor PID 为根，适配器只接受其后代
NPU 进程，并以该进程的 `vdie_id` 识别物理设备。该环境中的两卡 Backend Actor 虽请求
两张卡，却观测到 `ASCEND_VISIBLE_DEVICES=0,1,2,3`；因此可见设备环境变量只能作为诊断
信息，绝不能用于 `allocated/free` 或副本归属。四卡均被上述唯一关联覆盖，故静态 Ray
的 allocation 语义已具备实现和 Agent E2E 的证据基础。

进一步的可行性试验验证了三个实现关键点。第一，Ray Dashboard 的 Backend Actor
`required_resources` 明确返回 `HUAWEI_Ascend310P` 和 `NPU`，三个 Actor 的资源数为
`2 + 1 + 1`；NPU adapter 因而可直接从 Actor 资源解析整卡数，不需要复用 GPU 的
`num_gpus` 字段。两个资源键在每个 Actor 上数值相等，且与实际关联的物理设备数相符；
它们是同一调度约束的别名而非两个可累加的资源。adapter 以 Profile 指定的 canonical
键（310P 为 `NPU`）取值，并要求每个配置的 alias 均存在且与其相等；缺失、非整数或不一致时
省略 allocation 和全部副本样本，绝不相加或猜测。第二，两卡 Actor 的后代进程中，一个
Worker 同时使用两张卡，且其中
一张卡还有第二个后代进程；Exporter 对这两张卡报告的进程内存分别为
`20351.22 + 101.23 MiB` 和 `20351.23 MiB`。因此副本内存必须按
`Actor -> 后代 process_id -> vdie_id` 分组求和，而不能假设一张卡或一个副本只有一个
进程。对应物理 DDR 已用内存为 `22139 MiB` 与 `21469 MiB`，并不等于进程和；因此
`memory_used_bytes` 明确采用进程内存和，物理 DDR 已用仅作为设备级指标。该路径可
无歧义产生整卡独占副本的 `memory_used_bytes` 候选值。相应的
`memory_allocated_bytes` 使用同一 `vdie_id` 的物理 DDR 总容量，不把 Ray 资源数量或
物理 DDR 已用值误作显存申请量。

第三，向该两卡 Ray Serve Endpoint 发送受控推理负载期间，属于该 Actor 的两个
`vdie_id` 的物理利用率从 `0` 升至 `7--10%`，另两个属于其它 Backend Actor 的设备保持
`0`。这验证了将物理 `utilization_ratio` 归属给唯一整卡副本的可行性。该结论不表示
Node Agent 已交付：adapter 仍需验证单位转换、重启时的缺失语义和真实 Agent E2E；不满足
唯一归属或整卡独占时，副本内存和利用率都必须缺失。

同一 Profile 的 Exporter 重启后，四个 `vdie_id`、逻辑设备 index、产品名和 PCIe bus
标签保持不变，且进程指标在约 8 秒后恢复。这证明静态 310P 的本地 Exporter 重启不会
破坏当前设备关联；宿主机重启/驱动重装属于组件故障恢复验证，不是本期静态方案可行性的
前置条件。

| 调研问题 | 在目标环境收集的证据 | 结论如何影响设计 |
| --- | --- | --- |
| Exporter 是否能在目标驱动上稳定运行 | 已验证 v26.0.0 digest 在 HDK 25.5.2 的 310P3 上以 socket-free 方式返回四个设备的物理指标；保留启动日志和 `/metrics` 原文 | 物理采集可使用端口 `8082` 与 `/metrics`；仍需将最小 mount/`privileged` 结论固化为 Profile 并接入 Node Agent。 |
| Runtime Profile 的最小权限和挂载是什么 | 实际的设备文件、驱动目录及对象类型；逐步移除 mount/capability/`privileged` 的启动试验 | 填充当前 `ComponentVolume`、capabilities 与 `Privileged`；未证明的权限不进入 Profile。 |
| 物理设备身份是否可稳定关联 | `npu-smi`/驱动设备清单、Exporter `vdie_id`/index/product、Node Agent device snapshot | 决定 `neutree_node_accelerator_info/total` 的 device ID 和产品标签能否发布。 |
| 物理指标的数值语义是否正确 | 已在两卡 Ray Backend 的受控推理负载下观察到仅其两个 `vdie_id` 的利用率从 `0` 升至 `7--10%`；DDR/HBM、温度与单位仍按产品分别验证 | 决定每项既有通用指标是否映射；不完整、非法或单位不明的样本保持缺失。 |
| 整卡 allocation 的权威来源是什么 | 静态 Ray 已验证 Dashboard Actor PID 到后代 NPU 进程、`vdie_id` 的 `2+1+1` 整卡关联；Kubernetes 已实测 PodResources `Ascend310P-0/1/2/3` 与 Device Plugin 及四个 Exporter index 一致 | NPU adapter 只处理匹配的 ResourceName，并将带非空前缀、末段十进制 index 的 Device ID 解析为 index，再唯一连接到 `vdie_id`；不匹配即不输出 `allocated/free`。 |
| 容器/副本可否唯一关联整卡样本 | 静态 Ray 已验证 Actor/PID/进程到 `vdie_id` 的唯一关联；Kubernetes 已实测 1/2/1 整卡 PodResources 分配且无重叠 | 只有调度归属与 NPU 样本唯一关联且整卡独占时，才输出副本 allocation、内存和利用率。 |
| CPU 兼容和故障语义是否保持 | CPU-only 节点、Exporter 未就绪/停止、可选字段缺失的抓取与 Node Agent 输出 | 验证 CPU 节点仍输出通用指标；NPU 指标不输出而非输出 0、NaN 或 unknown。 |
| 310P 与 910B 是否可共用 Runtime Profile | 两种产品的镜像、设备/驱动 mount、capability 和 `privileged` 实测差异；910B 在共享 `npu`/`ddr`/`hbm` 配置下安全跳过 DDR 且 readiness 成功；HBM total/used、overall utilization 的指标名、单位和标签；socket-free、Pod 网络、启动和重启行为 | 全部一致才同一 runtime compatibility group；不同则暂缓 910B 发布，不增加 variant API。 |

调研完成后按以下顺序落结论：先更新 Profile 的已验证运行时输入，再将支持矩阵相应
行改为“支持”或“不支持”并附证据，最后才将通过的映射加入公开的通用指标契约。单次
抓取成功、源码存在 collector 或供应商文档列出指标，都不是支持结论。

## 交付计划和验收

### Roadmap 1：通用契约和 Enterprise 组件

新增 adapter registry、启动校验、`ComponentVolume`/`ComponentVolumeMount` 以及
Enterprise Node Agent 镜像的 planner 手工选择。镜像与 `--accelerator-type=npu`
必须作为同一 component revision 下发；不迁移既有 `NodeComponentVolume`。

### Roadmap 2：静态 Ray/SSH 物理指标

先在静态 Ray/SSH 实现单一 `npu` adapter，验证 310P、910B 的 UUID/index/product、
内存、利用率、温度、健康、功耗和节点清单。310P 不输出 PCIe Counter；910B 以其
单独完成的 capability matrix 为准。

### Roadmap 3：Kubernetes Managed Exporter

通过 Enterprise Profile 的 `Command`、结构化 volume、`Privileged` runtime 要求和
readiness 部署 NPU Exporter。310P 的 Pod 网络、HostPath、socket-free 物理采集和
vmagent Pod-IP 抓取已通过真实节点 E2E；实现上述 renderer 投影后可放行 Kubernetes
310P 物理指标。910B 仍须以同一流程独立通过；CPU 节点继续由同一 Node Agent DaemonSet
输出通用指标。

### Roadmap 4：整卡 allocation 与副本级指标

在设备 ID 映射完成验证后，交付 Kubernetes 的 PodResources/Ascend Device Plugin 整卡
关联，以及静态 Ray/SSH 的整卡关联。对整卡独占的真实 310P/910B 工作负载，逐项验证
并发布 `neutree_endpoint_replica_accelerator_allocation`、
`memory_allocated_bytes`、`memory_used_bytes`、`utilization_ratio`；不支持或无法
证明的指标保持缺失，并输出公开支持矩阵。vNPU 仍不实现。

| 验证类型 | 必需覆盖 |
| --- | --- |
| Unit test | 基于 fixture 的适配器测试，覆盖既有通用物理整卡映射及单位转换；310P 不支持的 PCIe/HBM 不得生成通用指标；健康、功耗、频率和互连原始指标不得新增 descriptor；副本级样本必须要求唯一 device/container/replica 关联和经验证的整卡独占语义；`vnpu_pod_*` 不得生成副本指标；CPU-only 与未配置 exporter 必须正常启动且不产生加速器样本。 |
| DB test | 不适用：适配器读取 Prometheus 文本和运行时 API，不涉及持久化改动。 |
| E2E test | 先在静态 Ray/SSH 的 Atlas 300I Duo 与 910B 节点验证物理指标、`accelerator_type="npu"`、设备清单及抓取失败行为；Kubernetes 在宿主挂载契约实现后复测同一物理指标和整卡 allocation；在整卡独占工作负载上验证各副本级候选指标，未通过语义验证的指标不发布；同时验证 CPU-only 集群不部署 NPU Exporter 仍可输出节点/运行时指标；不执行 vNPU 验证。 |

物理 310P 驱动/DCMI 路径和故障注入需要人工硬件验证，因为标准 E2E 环境无法模拟。

## 来源

- [MindCluster v26.0.0 NPU Exporter 安装说明](https://gitcode.com/Ascend/mind-cluster/blob/branch_v26.0.0/docs/zh/scheduling/installation_guide/03_installation/manual_installation/03_npu_exporter.md)
- [MindCluster v26.0.0 Prometheus 指标 API](https://gitcode.com/Ascend/mind-cluster/blob/branch_v26.0.0/docs/zh/scheduling/api/npu_exporter/01_prometheus_metrics_api.md)
- [MindCluster v26.0.0 NPU Exporter 源码](https://gitcode.com/Ascend/mind-cluster/tree/branch_v26.0.0/component/npu-exporter)
- `cmd/neutree-node-agent/neutree-node-agent.go`
- `internal/observability/neutreemetrics/normalizer/normalizer.go`
- `internal/observability/neutreemetrics/devicesnapshot/device_snapshot.go`
- `internal/observability/neutreemetrics/hardware/hardware_inventory.go`
- `internal/cluster/component/metrics/exporters.go`
