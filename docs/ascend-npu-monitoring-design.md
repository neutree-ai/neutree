# Ascend NPU 监控权威设计

## 文档状态与阅读指引

本文是 Neutree 对 Ascend 物理 NPU 监控、整卡/die 分配和条件化副本指标的**权威设计**。它基于 2026-08-12 在真实 310P3 集群（HDK 25.5.2、NPU Exporter v26.1.0、HAMi hami-core 软切分）上的抓取证据，合并了此前三份文档的结论。

文档关系：

- [NPU 指标支持分析](./npu-metrics-support-analysis.md) 与 [NPU 监控详细设计](./npu-monitoring-design.md) 保留为历史调研与设计草稿；与本文冲突处以本文为准。
- [NPU 指标支持矩阵](./npu-metrics-support-matrix.md) 是面向用户的验证状态记录，独立维护，本文引用。
- `docs/adr/0001-0003` 为独立审计轨迹，本文决策章节内嵌摘要并链接。

读者入口：

- **评审**：读 §2（结论）、§3（决策）、§4（架构）、§5（指标契约）。
- **实现**：读 §4-§11。
- **验证状态**：去 [支持矩阵](./npu-metrics-support-matrix.md)。

## 范围

> **当前版本能力边界（2026-08-12 实测收敛）**：副本级指标**只提供分配量**（die total 或 HAMi annotation 配额），**不提供切分/虚拟化场景（软切分、template vNPU）的实际资源使用率**（exporter `vnpu_pod_*`=0、容器归属丢失）；**仅整卡/die 独占分配时提供实际资源使用率**。

首期包含：

- Neutree 管理 NPU Exporter（v26.1.0），保留其已启用的原始厂商指标供 vmagent 和客户诊断。
- Enterprise NodeAgent `npu` adapter 将已验证的物理指标转换为既有 `neutree_*` 通用指标。
- 静态 Ray/SSH 与 Kubernetes 的 die 级 inventory、allocation、free，以及满足证据条件的副本指标。
- **K8s 整卡直用 / die 独占**副本指标（exporter 精确归属，含 `memory_used`/`utilization`）。
- **K8s 软切分共享 / template vNPU** 副本的 `memory_allocated_bytes`（经 HAMi annotation 配额，不含实际占用）。
- **静态整卡独占 / 非切分共享卡**副本指标（`process_info` 按进程归属，`memory_allocated` 用 die total）。
- **910B 专属 PCIe bandwidth 指标**（源码 `supportedPcieDevices` 含 910B；310P 无此来源，不输出）。
- CPU-only 节点与没有可用 Exporter 的节点的兼容行为。

首期不包含：

- K8s 软切分共享 / template vNPU 副本的 `memory_used_bytes`、`utilization_ratio`（exporter 与 HAMi :9395 均无容器级切片/模板使用量来源，实测 `vnpu_pod_*`=0）。
- 静态非切分共享卡的 `utilization_ratio`（die 物理 util 无法唯一归属给任一共享者）。
- vNPU template-mode inventory、replica usage、dashboard（310P/910B 的 `vnpu_pod_*` 均不纳入通用契约）。
- 新增健康、功耗、频率、互连或厂商专属的 `neutree_*` descriptor。
- PCIe 累积字节 Counter（310P 无来源；910B 仅出 bandwidth Gauge，不伪装 counter）。
- 多种加速器类型同时由一个 Kubernetes NodeAgent DaemonSet 处理。

## 实机调研证据（2026-08-12）

环境：单 worker 节点 `npu`（K8s node，IP 172.20.128.86），openEuler 22.03 LTS-SP4，HDK/驱动 25.5.2，containerd 2.3.3。物理拓扑：2 张 Atlas 300I Duo 卡（`machine_card_nums 2`）× 2 die = 4 个 NPU（`machine_npu_nums 4`）。NPU Exporter tag `v26.1.0`（`npu_exporter_version_info{exporterVersion="v26.1.0_linux-aarch64"}`，Profile 仅用 tag 不承诺 digest）。Exporter 部署时**挂载了容器 runtime socket**，因此 `npu_container_info`、`container_npu_*`、`npu_chip_info_process_info` 的容器标签可用。

5 个 qwen25-0p5b-chat-npu 服务 Pod 全部调度在节点 `npu` 上，全部标注 `huawei.com/vnpu-mode: hami-core`（HAMi 软切分）。`kubectl get pods -o wide` 展示的 `172.31.239.x` 为 Calico Pod IP，非节点 IP。

### 物理拓扑与分配

| die (exporter id) | vdie_id | 卡 / bus | total (MB) | 分配形态 |
|---|---|---|---|---|
| 0 | D0B86E64-20C0EE8D-...45003019 | 卡 A / 81:00.0 | 44278 | die 独占（pod wft4t） |
| 1 | D0B86E64-2120E58D-...44003019 | 卡 A / 81:00.0 | 43693 | die 独占（pod wx9mt） |
| 2 | D0B86E64-20A0E48D-...8F003019 | 卡 B / 82:00.0 | 44278 | die 独占（pod 6g574） |
| 3 | C6E96E64-20A08C46-...89003039 | 卡 B / 82:00.0 | 43693 | **软切分共享（pod zkds2 + pqr5v）** |

注意：同一张卡的两个 die 物理 DDR total 容量不同（44278 vs 43693），是 300I Duo 硬件特性，不是误差。

### 软切分共享的 annotation 证据

`hami.io/Ascend310P-devices-allocated` 与 `huawei.com/Ascend310P` 是 HAMi 调度的分配事实，是软切分配额的权威来源：

- pod `zkds2`：`Ascend310P-0`，vdie `C6E96E64-...89003039`，8192MB / 50 core
- pod `pqr5v`：`Ascend310P-1`，vdie `C6E96E64-...89003039`，8192MB / 50 core

两个 pod 的 `devices-allocated` 指向**同一个 vdie**，各自配额 8192MB/50core。`Ascend310P-<n>` 是 Device Plugin 逻辑分配编号，不是 die 物理 index。

### Exporter 在 die 独占 vs 软切分共享的行为差异（核心证据）

**die 独占**（pod wft4t/wx9mt/6g574）：exporter 的 `npu_container_info` 每个 die 一个 container、`container_npu_*` 等于该 die 物理值、`npu_chip_info_process_info` 带 `pod_name` 标签且按 pod 可聚合。

**同 die 软切分共享**（pod zkds2+pqr5v）：exporter 对同一 vdie 的 `npu_container_info` **只保留一个 container 归属**（抓取中表现为 pqr5v），另一 pod（zkds2）的容器归属**缺失**。同时 `container_npu_used_memory`/`npu_chip_info_used_memory` 报的是**整 die 物理 used**，不含切片信息。`npu-smi info` 与 exporter 对同一 die 的 used 一致（如 die3 6385/43693），软切分的 8192MB 配额对两者都不可见。

**进程 memory 语义**（以 die 独占样本为准）：

| die | pod | 进程数 | 进程 memory 和 (MB) | die used (MB) | 差值 |
|---|---|---|---|---|---|
| 0 | wft4t | 1 | 2759.2 | 4527 | 1768 |
| 1 | wx9mt | 1 | 2759.2 | 3830 | 1071 |
| 2 | 6g574 | 1 | 2759.2 | 4669 | 1910 |
| 3 | pqr5v | 2 | 5518.4 | 6385 | 868 |

进程 memory 和 < die used：die used 含驱动、框架、缓存等非该 replica 独占的开销。因此副本 `memory_used_bytes` 采用进程 memory 和，不用 die 物理 used，与静态 Ray 路径语义统一。

### 整卡直用实测样本（K8s 非虚拟化，pod `-full-`）

pod 名 `qwen25-0p5b-chat-npu-full-*`（直接整卡申请，不 template、不 hami-core）。3 个 pod 独占 die 0/1/3，die 2 空闲：

| pod | die(id) | container used | container total | process_info 和 | chip used |
|---|---|---|---|---|---|
| 6x7lr | 0 | 1874 | 44278 | 101.2 (pid 1699008) | 1874 |
| 52vc7 | 1 | 1167 | 43693 | 101.2 (pid 1699015) | 1167 |
| zwbnc | 3 | 1023 | 43693 | 431.2 (pid 1698999) | 1023 |
| (空闲) | 2 | — | 44278 | 无进程 | 1968 |

**验证结论**：

1. **`machine_npu_nums 4` 正确**——整卡直用无 template 拆分，逻辑设备数 = 物理 die 数 = 4（对照 template 模式下为 5）。
2. **`container_npu_*` = die 物理值**（整卡独占时完全成立）：container used 1874/1167/1023 分别等于 chip used。
3. **`npu_container_info` 每 die 一个 container**，归属无歧义（3 pod → 3 个独立 vdie）。
4. **进程 memory 和 < die used 再次印证**：die 3 进程 431 MB vs die used 1023；die 0 进程 101 MB vs die used 1874。`process_info` 按 pod 聚合才反映真实副本使用量。
5. **整卡直用路径下 exporter 完整可用**：container 归属、进程归属、物理指标全部正常。这直接验证了"**仅整卡/die 独占分配时才能获取实际资源使用率**"的边界。

**整卡直用 vs template 抓取对照**：

| 项 | 整卡直用（-full-） | template（-template-test-） |
|---|---|---|
| `machine_npu_nums` | 4（物理数）✅ | 5（逻辑设备数） |
| `container_npu_*` / `npu_container_info` / `process_info` | ✅ 完整可用 | ❌ 空 |
| 副本 memory_used / utilization | ✅ 可提供 | ❌ 不可提供 |

### HAMi :9395 证据

`curl 172.31.239.147:9395/metrics` 只输出 **host 级** `hami_host_gpu_memory_used_bytes` / `hami_host_gpu_utilization_ratio`，标签为 `device_index`/`device_type`/`device_uuid`，**没有** namespace/pod/container 标签，也**没有** `hami_vgpu_*` 容器级序列。`device_uuid` 与 exporter `vdie_id` 一致，`hami_host_gpu_memory_used_bytes`（bytes）与 `npu_chip_info_used_memory`（MiB）为同一物理量（1,912,602,624 bytes = 1824 MiB）。因此 HAMi :9395 对软切分也**给不出容器级切片使用量**，只能作节点级整卡指标的备用/交叉校验源。

## 结论总览（决策表）

| 决策 | 理由 | 被否方案 |
|---|---|---|
| **副本级指标当前只提供分配量，不提供实际使用率**：template/vNPU 的实际资源使用率无法获取（实测 exporter `vnpu_pod_*`=0、容器归属丢失）；**仅 die 独占整卡分配时才能提供实际资源使用率**（`container_npu_*`/`process_info` 可用） | exporter 对 HAMi 的软切分与 template 虚拟化层均不可见；分配量是 HAMi annotation 的调度事实 | 从 exporter 拿模板/切片使用量 |
| 用 NPU Exporter 采集物理/die 指标，不集成 DCMI | 避免 Agent 与宿主机 ABI/库路径/权限/驱动生命周期耦合；DCMI 也解决不了进程/容器→endpoint 归属 | Node Agent 内集成 DCMI |
| 软切分共享与 template vNPU 的 `memory_allocated` 用 HAMi annotation 配额 | exporter 与 :9395 均无容器级切片/模板使用量；annotation 的 vdie+memory+core 是调度分配事实 | exporter 整卡 total（虚报）；:9395 host 级（无 pod 粒度） |
| die 独占副本 `memory_used` 用 exporter `process_info` 按 pod 聚合 | `process_info` 带 pod_name，与静态 Ray 语义统一；不虚报（进程和 < die used） | die 物理 used fallback（虚报 868~1910MB） |
| **软切分共享 / template vNPU 不支持实际资源使用采集**：`memory_used`/`utilization_ratio` 均不输出，只给 HAMi annotation 分配量 | exporter 对 HAMi 软切分/template 虚拟化层不可见（实测 `vnpu_pod_*`=0、容器归属丢失）；切片/模板级使用量两个源都无 | 从 exporter `vnpu_pod_*` 或 `container_npu_*` 取切片使用量（实测均不可用） |
| allocation 权威来源 = PodResources / Ray Dashboard / HAMi annotation | 都是调度事实，不是 DCMI 事实 | exporter 容器元数据（软切分共享丢归属） |
| 缺失时不输出 0/NaN/unknown | 避免把未知当故障 | 输出零值/未知序列 |
| 310P 结论不外推 910B | 910B collector capability 与 310P 不同（HBM vs DDR、overall util） | 直接套用 310P 映射 |
| `Privileged=true` 首期显式默认 | 官方 Exporter 需要宿主设备访问，310P probe 已验证 | 非特权首期 |

## 架构决策（内嵌 ADR 摘要）

- **[ADR-0001](./adr/0001-enterprise-owned-accelerator-metrics-adapters.md) 企业拥有 adapter**：OSS 只提供厂商无关 adapter registry + 显式 accelerator-type 透传；`npu` adapter 的注册、Exporter 解析、Ascend 运行时要求全在企业 Node Agent 镜像。CPU-only 无 adapter 可启动；配置了类型但 adapter 未注册则 fail-fast。
- **[ADR-0002](./adr/0002-structured-component-volumes.md) 结构化挂载**：用 backend-neutral `ComponentVolume`/`VolumeMount` 表达宿主机驱动、设备、权限；禁止把 `--volume`/`--device` 藏进 `DockerRunOptions`。新增 `Command`（K8s `args` 会替换镜像 CMD）、`Privileged`（默认关，NPU 首期显式 true）。**本次更新**：首期 NPU Profile 增加容器 runtime socket volume（实测证明它是容器归属前提）。
- **[ADR-0003](./adr/0003-adapter-owned-accelerator-metric-aggregation.md) adapter 聚合**：共享层只采原始 `AcceleratorEvidence`，adapter 做全部厂商语义（含 HAMi annotation 解析），返回受限 `AcceleratorMetricResult`。物理证据与调度证据独立降级。
- **新增决策（本次）**：单 `npu` adapter 解析双数据源（npu-exporter :8082 + HAMi annotation），HAMi :9395 仅作节点级备用/交叉校验源，不进入 adapter 的副本指标路径。

## 总体架构与所有权边界

```text
                 ┌────────────────────────────────────────────────┐
                 │  Kubernetes 集群（节点 npu，172.20.128.86）       │
                 │                                                │
  vmagent ◄─────  npu-exporter :8082  (Neutree-managed, 含 CRI socket)
  (原样保留)      │   物理/die 指标 + die独占容器归属 + process_info
                 │                                                │
                 │  HAMi device-plugin :9395  (HAMi 自带, Neutree 只抓取)
                 │   host 级整卡指标 → 仅节点级备用/交叉校验
                 │                                                │
                 │  HAMi scheduler → Pod annotations（分配事实）
                 │   vdie UUID + memory 配额 + core + vnpu-mode
                 └────────────────────────────────────────────────┘
                                     │
                                     v
                     AcceleratorEvidence（原始 Exporter 文本 + PodResources + Pod annotations）
                                     │
                                     v
                          Enterprise NPU adapter
       解析 npu_chip_info_* / container_npu_* / process_info + HAMi annotation 配额
                                     │
                                     v
                       AcceleratorMetricResult（仅既有通用 metric ID + 校验标签）
                                     │
                                     v
              共享层：descriptor/单位/标签校验、公共标签、Prometheus 序列化
```

共享层负责 I/O、超时、公共标签、Prometheus 序列化、CPU 兼容、descriptor/单位校验及 Exporter target 发现。它不解释厂商资源名、device ID、环境变量或指标名。

### Adapter 接口设计

adapter 是"厂商 exporter 原始输出 → Neutree 通用指标"的唯一转换点。当前 normalizer 的 `normalizeAcceleratorSamples` 等函数硬编码 DCGM→`neutree_*` switch；adapter 设计把这一步抽成按 `accelerator_type` 选择的实现。

**接口**：

```go
// Accelerator 是 NodeAgent 内按 accelerator_type 注册的厂商适配器。
// 只有已注册的 adapter 才会被 NodeAgent 选择；无类型走 legacy DCGM 兼容路径。
type Accelerator interface {
    // Type 返回本 adapter 处理的 accelerator_type（如 "npu"）。
    // 注册后必须与 planner 下发的 --accelerator-type 一致。
    Type() string

    // BuildMetrics 将原始证据转换为 Neutree 通用加速器指标。
    // 结果只能使用既有通用 metric ID 与受校验标签。
    BuildMetrics(ctx context.Context, evidence AcceleratorEvidence) (AcceleratorMetricResult, error)
}
```

**`AcceleratorEvidence`（瞬时输入，不解释厂商语义）**：

`AcceleratorEvidence` 是共享层在一次 scrape 周期内收集到的**原始、未解释**证据集合——它只负责"采集"，不负责"理解"。三个核心语义：

- **不解释厂商语义**：只携带原始字符串/原始记录（Exporter 文本、PodResources 原始分配、HAMi annotation 原文），不解析 `npu_chip_info_*` 是什么、`Ascend310P-0` 是什么、配额是多少。这些全部留给 adapter。
- **不做归属判断**：不回答"这个 vdie 属于哪个 endpoint"。PodResources/Ray/HAMi 证据只是并列摆着，归属是 adapter 在 `BuildMetrics` 里做的。
- **不预先解释 device ID**：`Ascend310P-<n>`、`vdie_id`、`super_device_id` 的映射关系，evidence 不碰。

它承载四类证据：① exporter 原始文本（`ExporterText`）；② kubelet PodResources 原始分配（复用现有 `allocation.PodResourceLister` 返回的 `model.PodResource`，含 `ResourceName + DeviceIDs` 原始形态）；③ HAMi annotation 原文 + endpoint Pod 元数据；④ 静态 Ray 的 Actor/PID/进程证据。

**与现有 `allocation.Provider` 的对照**：现有 `Provider.Allocations(ctx, *NodeDeviceSnapshot)` 输入**已解释**的设备快照、返回已归属分配，是"解释后"路径；`AcceleratorEvidence` 是**解释前**路径——共享层只采集原始证据，adapter 理解。物理证据（`ExporterText`）与调度证据（PodResources/Ray/annotation）**独立存在、独立降级**，一方失败不影响另一方。

**生命周期**：每次 scrape 周期的瞬时值，非持久状态；属于 `internal/observability/...`，不进 `api/v1`，不是外部插件协议。

```go
type AcceleratorEvidence struct {
    // AcceleratorType 是 planner 下发的显式类型，adapter 用它自检是否被正确选择。
    AcceleratorType string

    // ExporterText 是 accelerator exporter 抓取的原始 Prometheus 文本。
    // adapter 自行解析厂商指标名（如 npu_chip_info_*、container_npu_*）。
    ExporterText string

    // ExporterUp 表示本节点 accelerator exporter 是否抓取成功。
    // adapter 不判定 readiness，只据此跳过无样本的解析。
    ExporterUp bool

    // PodResources 是 kubelet PodResources 的原始分配记录（ResourceName + device ID）。
    // 复用现有 allocation.PodResourceLister 返回的 model.PodResource（含 ResourceName + DeviceIDs）。
    // Kubernetes 路径提供；静态 Ray 路径为空。
    PodResources []model.PodResource

    // EndpointPods 是本节点 endpoint replica 的 Pod 元数据（用于关联 PodResources）。
    EndpointPods []EndpointPod

    // RayEvidence 是静态 Ray 路径的 Dashboard actor/PID/process 证据。
    RayEvidence *RayEvidence

    // NodeAnnotations 是 HAMi 调度的分配事实（vdie UUID、memory 配额、core、vnpu-mode）。
    // Kubernetes + HAMi 路径提供；静态路径为空。
    NodeAnnotations map[string]string
}

type RayEvidence struct {
    Actors       []RayActor     // Dashboard Backend Actor（含 required_resources）
    ActorProcesses map[int]ProcessInfo // Actor PID -> 后代 NPU 进程
}
// RayActor / ProcessInfo 复用现有 RayServeAllocationProvider 的输入模型
// （dashboard.RayActor、allocation.ProcessInfo），不另起炉灶。
```

**`AcceleratorMetricResult`（受限输出）**：

```go
type AcceleratorMetricResult struct {
    // DeviceSnapshots 是发现/归属后的设备快照（供 inventory/allocation 消费）。
    DeviceSnapshots []v1.DeviceAllocation

    // Samples 是 adapter 生成的通用 neutre_* 样本。
    // 只允许既有 metric ID + 受校验标签；缺失、歧义、未验证时省略样本。
    Samples []Sample
}
```

**`BuildMetrics` 内的处理流程**（`npu` adapter）：

1. **解析物理证据**：`promtext.ParseVector(evidence.ExporterText)` → `npu_chip_info_*`/`container_npu_*`/`process_info`，构造 die 级设备快照（`vdie_id`/`id`/`model_name`/`product`）。
2. **内存指标族 fallback**：同一 `vdie_id` 上完整 HBM `used+total` 对优先，完整 DDR 对 fallback（310P 用 DDR、910B 用 HBM）。
3. **util fallback**：`overall_utilization` 优先，fallback 基础 `utilization`（910B）；cube 不纳入。
4. **副本归属分流**（按分配形态）：
   - Kubernetes：PodResources + HAMi annotation → 判定 die 独占 / 软切分共享 / template → 按文档分流表输出。
   - 静态 Ray：RayEvidence Actor PID → 后代进程 → `vdie_id`。
5. **生成受限样本**：只输出通用 metric ID，缺失序列不输出。

**normalizer 迁移路径**（GPU 侧渐进式）：

- 现有 `normalizeAcceleratorSamples`/`normalizeNodeGPUSamples`/`normalizeGPUHardwareInfoSamples`/`normalizeEndpointAllocationSamples` 保持 DCGM 行为，作为 legacy 路径。
- 新增 `internal/observability/neutreemetrics/adapter/` 包：`Accelerator` 接口 + registry + `AcceleratorEvidence`/`AcceleratorMetricResult`。
- NodeAgent 装配：`--accelerator-type` 非空时从 registry 取 adapter，`BuildMetrics` 结果送入现有 normalizer 的通用样本出口（公共标签、descriptor 校验、序列化）；为空时走 legacy DCGM 路径。
- 后续把 NVIDIA DCGM 逻辑也迁入 `nvidia` adapter（不在本次范围）。

### 企业版注册机制（镜像 core 注入模式）

NodeAgent 侧的 adapter 注册**完全镜像 core 的 accelerator plugin 注入模式**（`internal/accelerator/plugin/` 的包级 registry + `init()` 注册），企业版通过**独立 NodeAgent 镜像**内置 `npu` adapter。

**core 注入模式（参考模板，已存在）**：

```go
// internal/accelerator/plugin/plugin.go（controller 进程）
var plugins = make(map[string]AcceleratorPlugin)
func registerAcceleratorPlugin(p AcceleratorPlugin) { plugins[p.Resource()] = p }
func GetLocalAcceleratorPlugins() map[string]AcceleratorPlugin { return plugins }

// internal/accelerator/plugin/gpu.go（OSS 内置）
func init() { registerAcceleratorPlugin(&GPUAcceleratorPlugin{...}) }

// internal/accelerator/manager.go:90（启动自动加载）
for _, p := range plugin.GetLocalAcceleratorPlugins() { ... }
```

**NodeAgent 侧 adapter 注入（本次新增，同构）**：

```go
// internal/observability/neutreemetrics/adapter/adapter.go（node-agent 进程）
var adapters = make(map[string]Accelerator)
func Register(a Accelerator) { adapters[a.Type()] = a }
func GetLocalAccelerators() map[string]Accelerator { return adapters }

// internal/observability/neutreemetrics/adapter/nvidia.go（OSS 内置）
func init() { Register(&nvidiaAccelerator{}) }

// npu.go（企业 NodeAgent 镜像源码树，OSS 不含）
func init() { Register(&npuAccelerator{}) }

// cmd/neutree-node-agent/main.go（装配）
registry := adapter.GetLocalAccelerators()
server, _ := neutreemetrics.NewServer(config.WithAccelerators(registry))
```

**企业版提供独立 NodeAgent 镜像**：企业 fork 仓库在 `adapter/npu.go` 加 `init() { Register(&npuAccelerator{}) }`，构建出带 `npu` adapter 的镜像。OSS 镜像不含 `npu.go`，`GetLocalAccelerators()` 只有 nvidia。`--accelerator-type` 由 planner 与镜像**原子下发**，NodeAgent 从本地 registry 取对应 adapter；未注册则 fail-fast。

**与 core 的对齐点**：

| core（controller） | node-agent（镜像） | 一致 |
|---|---|---|
| `plugin.go` 包级 registry | `adapter.go` 包级 registry | ✅ 同构 |
| `init()` 注册 GPU/AMD | `init()` 注册 nvidia/npu | ✅ 同构 |
| `GetLocalAcceleratorPlugins()` | `GetLocalAccelerators()` | ✅ 同构 |
| manager 启动自动加载 | NodeAgent main 装配 registry | ✅ 同构 |
| 企业 fork 加 plugin | 企业 fork 加 `npu.go` | ✅ 同构 |

**接口边界**：`AcceleratorEvidence`/`AcceleratorMetricResult`/`Accelerator` 属于 `internal/observability/...`，是瞬时 Node Agent 实现数据，不进入 `api/v1`，也不构成外部插件协议。`api/v1` 仅保留可部署的 Profile/Runtime 与声明式配置。

## 指标契约

`accelerator_uuid` 统一取 exporter `vdie_id`（die 级）。label 结构完全复用现有 `endpointAcceleratorLabelNames`（`cluster_type/endpoint/instance_id/replica/node/accelerator_type/accelerator_uuid/accelerator_index/vdevice_index/product`），**不为 HAMi 增加 core/vnpu-mode 标签**。`accelerator_type` = 企业插件返回的 `npu`；`product` = 规范化后的 `model_name`（310P3-Ascend-V1）。

### 物理 / die 级指标

| 通用指标 | 310P 来源 | 910B 来源 | 单位转换 | 缺失语义 |
|---|---|---|---|---|
| `neutree_accelerator_utilization_ratio` | `npu_chip_info_utilization`（唯一） | `npu_chip_info_overall_utilization`（优先，÷100）→ fallback `npu_chip_info_utilization`；**cube 不纳入** | % ÷ 100 | 无有效样本则缺失 |
| `neutree_accelerator_memory_used_bytes` | `npu_chip_info_used_memory`（DDR） | `npu_chip_info_hbm_used_memory`（HBM） | MiB × 2^20 | 同 die 两族均不完整则缺失 |
| `neutree_accelerator_memory_total_bytes` | `npu_chip_info_total_memory`（DDR） | `npu_chip_info_hbm_total_memory`（HBM） | MiB × 2^20 | 同上 |
| `neutree_accelerator_temperature_celsius` | `npu_chip_info_temperature` | 同左 | 直出 | 缺失 |
| `neutree_node_accelerator_info` | `vdie_id`/`id`/`model_name`/`product_type` | `vdie_id`/`id`/`model_name`（**无 product_type**） | — | 每 die 一条 |
| `neutree_node_accelerator_total` | product = `model_name` | 同左 | — | 按 product 计数，**按 `vdie_id` 去重**（template 下 `machine_npu_nums` 是逻辑设备数 5，物理 die 仅 4，不能用 `machine_npu_nums`） |
| `neutree_node_accelerator_allocated/free` | PodResources / Ray / annotation 的 die 级唯一集合 | 同左 | — | 仅在调度证据完整时输出 |

**310P vs 910B 核心差异**（源码 `IsSupported` 判定）：

| Collector | 310P | 910B | 依据 |
|---|---|---|---|
| DDR | ✅ | ❌ | `notSupportedDdrDevices` 含 910B |
| HBM | ❌ | ✅ | `supportedHbmDevices` 含 910B |
| overall / cube utilization | ❌ | ✅ | `supportedOverallUtilDevices`/`supportedCubeDevices` |
| vnpu | ✅（唯一） | ❌ | `supportedVnpuDevices` 仅 310P |
| product_type 标签 | ✅ | ❌ | `setProductType` 仅 310P |
| PCIe / HCCS | ❌ | ✅ | `supportedPcieDevices`/`supportedHccsDevices` 含 910B |
| network / roce / optical | ❌ | ✅（默认训练卡） | `IsTrainingCard()`：310* 恒 false |
| power / temp / health / process_info / container_npu_* | ✅ | ✅ | 通用物理指标 |
| sio / ub | ❌ | ❌ | 仅 910A3/A5 |

**首期新增 910B 专属 PCIe 指标**：源码 `supportedPcieDevices` 含 910B，首期新增 910B 的 PCIe bandwidth 指标（不入通用 `neutree_accelerator_pcie_*_bytes_total` byte counter，避免 bandwidth gauge 伪装 counter）；310P 无 PCIe 数据源，明确不输出。

**310P 明确不输出**：PCIe bytes counter、health/power/freq/互连（保留为原始诊断，不加 descriptor）、`vnpu_pod_*`。910B 的 vNPU 同样不输出（`supportedVnpuDevices` 仅 310P）。

### 副本级指标（按分配形态分流）

> **权威边界（2026-08-12 实测收敛）**：当前 Neutree 只能提供**分配量**（die total 或 HAMi annotation 配额）。**切分/虚拟化场景（软切分、template）的实际资源使用率无法获取**（exporter `vnpu_pod_*` 不产生、容器归属丢失）；**整卡/die 独占分配时才能获取实际资源使用率**（`container_npu_*` / `process_info` 可用）。

| 分配形态 | `_allocation` | `_memory_allocated_bytes` | `_memory_used_bytes` | `_utilization_ratio` |
|---|---|---|---|---|
| **K8s 整卡直用**（非虚拟化，pod 直接整卡） | PodResources → vdie 唯一 | die total memory | `process_info` 按 pod 聚合 | `container_npu_utilization` |
| **K8s 同 die 软切分共享**（hami-core） | HAMi annotation 关联 | **HAMi 配额（8192MB）** | **不输出**（无源） | **不输出**（无源） |
| **K8s template vNPU**（die 拆多模板） | HAMi annotation 关联 | **HAMi 配额（6144MB）** | **不输出**（实测 `vnpu_pod_*`=0） | **不输出**（实测 `vnpu_pod_*`=0） |
| **静态整卡独占** | Ray PID → vdie 唯一 | die total memory | 进程 memory 和 | die util |
| **静态非切分共享卡**（多进程共享物理卡） | Ray PID → 多进程归属 | die total memory | 各 Actor 进程 memory 和 | **不输出**（物理 util 无法唯一归属） |
| 共享 / 无法唯一归属 | 缺失 | 缺失 | 缺失 | 缺失 |

### template-mode vNPU 路径

**template-mode 的识别**：Pod 申请 `huawei.com/Ascend310P: "1"` + `huawei.com/Ascend310P-memory: "4096"`，且**不**设置 `huawei.com/vnpu-mode: hami-core`。device-plugin 在 Allocate 阶段注入 `ASCEND_VISIBLE_DEVICES` + `ASCEND_VNPU_SPECS`。实测 annotation 示例：`hami.io/Ascend310P-devices-allocated: ";<vdie>,Ascend310P,6144,0:;"`（6144MB 配额、0 core）。

**exporter 的 `vnpu_pod_*` 在 template mode 下实测不可用**（2026-08-12）：

- 源码层面：`VnpuCollector` 的展开函数 `GetChipListWithVNPU` **定义了但未被调用**；`getNPUChipList` 只把 `VDevInfos` 挂到 chip 上不展开。
- 实测层面：集群切到 template mode 后（5 个模板、die 3 拆 2 份），exporter 的 vnpu collector 已启用（`metricsGroup [vnpu] is on`）、`VnpuCollector` 已注册，但每次采集仅 10-30µs 空转，**`vnpu_pod_*` 始终为 0**。根因是 DCMI `GetVirtualDeviceInfo` 返回的 `VDevActivityInfo` 为空——**HAMi 的 template 虚拟设备对 exporter 的 DCMI 视野不可见**。

因此 **`vnpu_pod_*` 不是 Neutree 副本级指标的有效来源**。template-mode 副本级指标与 hami-core 软切分一致：**只提供 HAMi annotation 分配量（memory_allocated_bytes），不提供 memory_used / utilization_ratio**。上游 `vnpu_pod_*` 的三个 descriptor（`aicore_utilization`/`total_memory`/`used_memory`，标签含 `v_dev_id`/`is_virtual`/`aicore_count`）仅作原始诊断保留给 vmagent，不进入通用指标契约。

**物理/die 级指标不受影响**：即使 Pod 走 template vNPU，`npu_chip_info_*` 仍按物理 die 输出，die 级 inventory/allocated 仍有效。**范围边界**：若未来 exporter 上游修复 vnpu 展开，`v_dev_id` 应填 `vdevice_index`，不能替代物理 `vdie_id`（`accelerator_uuid`）。

**310P vs 910B**：exporter 的 vnpu collector 源码仅对 `Ascend310P` 返回 supported；910B/910C 的 template vNPU 不产生 `vnpu_pod_*`。

**同一次抓取的其它观察**：
- `machine_npu_nums` 从 4 变 **5**：根因已由源码确认（`getNPUChipList` → `dmgr.GetDeviceList()` 返回逻辑设备数，`len(chips)` 直接作为 `machine_npu_nums`）。template 虚拟化下 die 3 被切成 2 个模板（ncjqr + dw555），DCMI 把它枚举成 2 个逻辑设备，但两个逻辑设备 `GetPhysicIDFromLogicID` 返回同一 PhyId=3。因此 `machine_npu_nums` 语义是**逻辑设备数（含 template 拆分），不是物理 die 数**；`id` 标签用 `PhyId` 仍是 4 个唯一值（0-3）。**Neutree 的 `neutree_node_accelerator_total` 必须按 `vdie_id` 去重或按 `id` 唯一集合计数，不能直接用 `machine_npu_nums`。**
- `vnpu_pod_*` 为 0 的更深层根因：`GetChipListWithVNPU`（负责把活动虚拟设备展开成独立 chip）在 master 源码中**定义了但没有被调用**；`getNPUChipList` 只把 `VDevInfos` 挂到 chip 上不展开，且实测 `VDevActivityInfo` 为空。因此 exporter 在 template mode 下既不展开 vnpu 序列，`machine_npu_nums` 也不等于物理数。
- `container_npu_*` / `npu_container_info` / `process_info` 容器标签**全空**（template 虚拟设备不绑定物理 `/dev/davinci*`，导致 exporter 容器 parser 解析不到物理设备归属）。
- 物理 die 指标（util/mem/temp/power/health）仍完整正常（id 0-3 各 14 个样本）。

### HAMi :9395 的定位

`hami_host_*` 只作**节点级整卡指标**的备用/交叉校验源（如 Exporter scrape 失败时用 `hami_host_gpu_memory_used_bytes` 交叉核对整卡 used）。不进入副本级指标路径。

## 数据模型与 Profile

目标态在现有 `AcceleratorExporterProfile` 基础上增加 `Command`、`Readiness`、`Privileged`、`ComponentVolume`/`VolumeMount`（`api/v1/accelerator_plugin.go` 当前**均未实现**，仅 `Args/Port/MetricsPath/Env/ConfigFiles/Runtime`）。**不新增 `Allocation.KubernetesResourceNames`**——ResourceName→type 映射由企业 `npu` plugin 的 `ResourceParser` 建立（见 §ResourceName → Neutree type 的映射）。权威 Profile 实例（路径以 310P/910B 官方镜像和驱动布局验证后为准）：

```yaml
metrics_exporter:
  name: npu-exporter
  image: swr.cn-south-1.myhuaweicloud.com/ascendhub/npu-exporter:v26.1.0   # 仅 tag，不承诺 digest
  command: ["/usr/local/bin/npu-exporter"]
  args: ["-ip=0.0.0.0", "-port=8082", "-updateTime=5", "-containerMode=<docker|containerd>"]
  port: 8082
  metrics_path: /metrics
  readiness:
    http_path: /metrics
    initial_delay_seconds: 15
    period_seconds: 5
    timeout_seconds: 5
    failure_threshold: 3
  runtime:
    privileged: true
    volumes:
      - {name: ascend-driver, host_path: {path: /usr/local/Ascend/driver, type: directory}}
      - {name: ascend-dcmi, host_path: {path: /usr/local/dcmi, type: directory}}
      - {name: host-sys, host_path: {path: /sys, type: directory}}
      # 容器 runtime socket volume（容器归属/process_info 的 pod_name 标签前提）
      - {name: container-socket, host_path: {path: <docker|containerd sock>, type: socket}}
    volume_mounts:
      - {name: container-socket, mount_path: <socket mount path>, read_only: false}
```

**`containerMode` 与 socket 按后端区分**：

| 后端 | `-containerMode` | socket volume 路径 |
|---|---|---|
| 静态 Ray/SSH（Docker） | `docker` | `/run/docker.sock` |
| Kubernetes（containerd） | `containerd` | `/run/containerd/containerd.sock` |

容器归属/`process_info.pod_name` 以对应 runtime socket 为前提；Profile 按后端实例化时填充对应值与 socket 挂载。

### npu exporter 采集规则

NPU Exporter 的采集由**两层配置**决定：`-args`（采集参数）与 `metricConfiguration.json`（指标组开关）。

**采集参数（Profile `args`）**：

| 参数 | 值 | 语义 |
|---|---|---|
| `-ip` | `0.0.0.0` | 监听地址。**必填**，省略时以 `listen ip is invalid` 退出；wildcard 使 K8s Pod IP 与静态节点 IP 均可访问 |
| `-port` | `8082` | 指标端口（对应 Profile `port`） |
| `-updateTime` | `5` | 采集周期（秒，可 1-60）；Prometheus 从缓存读取，短生命周期容器可能无指标 |
| `-containerMode` | `<docker\|containerd>` | 按后端区分（见上表）；容器 parser 失败仅记录日志，物理 collector 继续 |

**指标组开关（`metricConfiguration.json`，经 Profile `config_files` 下发）**——统一开启 `npu`/`ddr`/`hbm`，显式关闭其余全部内建组：

```json
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
```

**采集规则要点**：

- **产品自动跳过**：v26 在 collector 初始化时跳过当前产品不支持的 `ON` 项——310P 不采集 HBM（无 HBM collector 数据），910B 不采集 DDR（`notSupportedDdrDevices` 含 910B）。同一配置服务两个产品，但各自仍需硬件 E2E。
- **vnpu 组显式 OFF**：避免模板/软切分场景产生误导性空序列（实测 `vnpu_pod_*`=0）；即便设为 ON，HAMi 虚拟设备对 DCMI 也不可见。
- **未验证的互连/网络组保持 OFF**：network/pcie/roce/sio/optical/ub 等不进入首期，只保留为原始诊断（若产品支持）。
- **依赖 v26 "文件存在即完整替换" 语义**：更新 exporter tag 时必须复验该配置行为。
- **vmagent 对原始指标不做 relabel/drop**：包括带 `process_id` 的 `npu_chip_info_process_info`，原样保留为厂商诊断数据；NodeAgent adapter 只消费生成通用 `neutree_*` 所需的样本。

### Neutree 抓取链路（vmagent + NodeAgent）

**vmagent 采集 rules（Kubernetes）**：由 `planAcceleratorExporters` → 模板渲染进 vmagent ConfigMap，使用 `kubernetes_sd_configs` + Pod label 自动发现，不手写 rule：

```yaml
- job_name: '{{ .JobName }}'            # 如 npu-npu-exporter
  {{ if .HasCustomMetricsPath }}metrics_path: {{ .MetricsPath }}{{ end }}
  kubernetes_sd_configs:
  - role: pod
    selectors:
    - role: pod
      label: app={{ .AppLabel }}        # 靠 exporter Pod label 发现
  relabel_configs:
  - source_labels: [__meta_kubernetes_pod_ip]
    replacement: $1:{{ .Port }}         # 端口来自 Profile
  - target_label: accelerator_type
    replacement: {{ .AcceleratorType }}  # 打上 npu 标签
```

**NodeAgent 发现 exporter 地址**：当前 `ScrapeTargetProvider` 两种实现——

- **Kubernetes**：`KubernetesScrapeTargetProvider.Targets` 用 `client.MatchingFields{"spec.nodeName": p.NodeName}` **只列本节点 Pod**，按 label（`app` 或 `neutree.ai/metrics-target=accelerator-exporter`）匹配 exporter Pod，取其 PodIP。
- **静态**：`StaticScrapeTargetProvider.Targets` 直接用 `127.0.0.1`。

**端口来源（当前 vs 目标）**：

| 现状 | 目标（NPU 接入） |
|---|---|
| 硬编码 `managedAcceleratorExporterPort=19400`、`external=9400` | 显式 accelerator-type 模式由 planner 从 Profile 推导 `--accelerator-exporter-port`/`--accelerator-exporter-metrics-path`，与镜像/type 原子下发（NPU 用 8082） |

**CPU 节点不访问的隔离链**（三层，已实现）：

1. **exporter 不部署到 CPU 节点**：`selectClusterAcceleratorExporter` → `acceleratorExporterMatchesAnyNode` 只留匹配节点；DaemonSet + `NodeSelector` 只调度 NPU 节点。
2. **NodeAgent 只列本节点 Pod**：`spec.nodeName` 过滤 → CPU 节点无 exporter Pod → 0 target。
3. **无 target → 跳过 accelerator 样本**：CPU 节点只输出 node/runtime 指标（CPU-only 兼容）。

**静态 Ray file-SD（设计新增，代码待实现）**：Ray Head vmagent 按 file-SD 从每个 NPU 节点 IP 抓取原始指标（与 GPU 路径一致）；静态 exporter 用 host network，NodeAgent 通过 localhost 抓取。Kubernetes 用 Pod 网络 + Pod-IP 抓取。

### ResourceName → Neutree type 的映射

**不引入 `allocation.kubernetes_resource_names`**（已移除）。K8s ResourceName → Neutree accelerator type 的关联**完全照 NVIDIA 现有模式**，由 `npu` plugin（企业版注入 core）的 `ResourceParser` 在 planner 阶段建立。

**NVIDIA 现有映射（参考模板，两套独立机制）**：

| 阶段 | 机制 | 用什么识别 | 产出 |
|---|---|---|---|
| **静态节点发现** | plugin `Handle().GetNodeAccelerator` + `lspci` | PCI vendor ID（NVIDIA `10de:`） | accelerator_type = `nvidia_gpu` |
| **K8s 资源解析** | `ResourceParser.ParseFromKubernetes` | `resource["nvidia.com/gpu"]` 硬编码在 parser 常量 | type + product（来自 node label `nvidia.com/gpu.product`） |
| **NodeAgent 采集** | exporter 指标 | `model_name`/label | product（如 A100） |

**NPU 的映射（同构）**：

| 阶段 | 机制 | NPU 用什么 | 产出 |
|---|---|---|---|
| **静态节点发现** | `npu` plugin `GetNodeAccelerator` | Ascend PCI vendor ID `19e5:` 或 `npu-smi` 检测 | accelerator_type = `npu` |
| **K8s 资源解析** | `npu` plugin `ResourceParser.ParseFromKubernetes` | `resource["huawei.com/Ascend310P"]` 声明在 parser | type = `npu` |
| **NodeAgent 采集** | NPU exporter | `model_name`（`310P3-Ascend-V1`） | product |

**关键边界**：ResourceName → type 的映射**只在 planner 部署阶段用一次**（识别节点是 NPU、决定部署什么 exporter / 下发什么 `--accelerator-type`）。**NodeAgent 侧不依赖 ResourceName**——product 直接从 exporter `model_name` 拿；分配归属（虚拟化走 HAMi annotation、非虚拟化走 UUID/进程关联）与 ResourceName 无关。

## Allocation 数据流

### Kubernetes

NodeAgent 读取 kubelet PodResources 和 Pod annotations。adapter 处理命中项：

**形态 A：非虚拟化整卡直用**（pod 直接 `huawei.com/Ascend310P: "1"`，不 template、不 hami-core）。Device Plugin 分配**物理 die**，exporter 在挂 socket 时 `container_npu_*`/`npu_container_info`/`process_info` 有完整容器归属（与 hami-core 时期单 pod 独占 die 的行为一致）。这是最基础的 die 独占形态：副本 `memory_allocated`=该 die total、`memory_used`=`process_info` 按 pod 聚合、`utilization`=`container_npu_utilization`。PodResources 的 device ID（`Ascend310P-0` 等）→ `vdie_id` 唯一映射。

**形态 B：同 die 软切分共享**（`huawei.com/vnpu-mode: hami-core`）。某 vdie 的 `devices-allocated` 指向 ≥2 个 endpoint replica（如 zkds2+pqr5v 共享 `C6E96E64-...89003039`）→ 副本 `memory_allocated_bytes` 取各自 HAMi annotation 的 memory 配额（8192MB），`memory_used_bytes`/`utilization_ratio` 缺失。

**形态 C：template vNPU**（`huawei.com/Ascend310P` + `-memory` 申请，无 `vnpu-mode`）。同 die 拆多模板（如 die 3 拆 2 个 6144MB 模板），副本 `memory_allocated_bytes` 取 HAMi annotation 配额，`memory_used`/`utilization` 缺失（实测 `vnpu_pod_*`=0）。

**Device ID 语义**：`Ascend310P-<n>` 是 Device Plugin 逻辑编号，**不是** die 物理 index；物理 die 以 annotation 的 vdie UUID 为准。die 独占判定：PodResources/annotation 中某 vdie 只属于一个 endpoint replica → 副本指标按 die 独占路径输出。

### 静态 Ray/SSH

adapter 以 Ray Dashboard Backend Actor PID 为根，读取 Actor `required_resources` 的 canonical `NPU` 值；只接受 Actor 后代的 NPU `process_id`，并将其 `vdie_id` 归属给 replica。`memory_used_bytes` 是每个 replica 所有关联 process memory 的和。`ASCEND_VISIBLE_DEVICES` 仅作诊断，不作 allocation 依据。

**静态集群的两种分配形态**：

- **整卡/die 独占**（单 Actor 独占一个 die）：`process_info` 单进程归属该 vdie，副本 `memory_allocated`=die total、`memory_used`=进程 memory、`utilization`=die util。
- **非切分方式共享卡**（多 Actor/进程共享一张物理卡，无 HAMi 虚拟化，直接绑物理 device）：exporter `process_info` 出现**多个 `process_id` 落在同一 vdie**（如之前实机验证中两卡 Actor 的 Worker 同时使用两张卡、且一张卡有多个后代进程）。此时**每个进程的 memory 可分别聚合到各自 Actor**，但**整卡 die 物理 used 无法唯一归属给任一共享者**。`memory_allocated_bytes` 仍可给（该 die total），`memory_used_bytes` 只能给"该 Actor 关联进程的 memory 和"（不是 die used），`utilization_ratio` 无法唯一归属（die 物理 util 是共享的）→ **共享卡时 utilization_ratio 缺失**。

**静态集群与 K8s 的分配模型差异**：

- 静态 Ray/SSH **没有 HAMi scheduler/device-plugin/annotation 机制**。集群 accelerator 类型按**集群级**检测（`detectClusterAcceleratorType`：优先 `Spec.Config.AcceleratorType`，其次缓存状态，最后对每个节点 `GetNodeAcceleratorType` 检测；CPU-only 节点返回空类型被**跳过**，可与 accelerator 节点共存；非空类型不一致则报错拒绝混合）。
- 因此静态节点**没有 K8s 的软切分/template annotation 配额维度**；它的"共享"是**非切分的多进程共享物理卡**，归属靠 `process_id` → vdie 关联，而非 HAMi annotation。
- exporter 地址：静态节点用 localhost 抓取（Node Agent 同机），Ray Head vmagent 按 file-SD 从每个 NPU 节点 IP 抓取原始指标。静态 exporter 使用 host network。
- CPU-only 节点在静态集群内与 NPU 节点共存时，仅输出 node/runtime 指标，不部署/不抓 accelerator exporter。

**静态 vs K8s 副本级指标对照**：

| 分配形态 | memory_allocated | memory_used | utilization |
|---|---|---|---|
| K8s 整卡直用 / die 独占 | die total | `process_info` 按 pod 聚合 | `container_npu_utilization` |
| K8s 软切分共享 / template | HAMi 配额 | 缺失 | 缺失 |
| 静态整卡独占 | die total | 进程 memory 和 | die util |
| 静态非切分共享卡 | die total | 各 Actor 进程 memory 和 | **缺失**（物理 util 无法唯一归属） |

## Exporter 运行边界

- socket-free 是**可选的**安全边界（v26.0.0 已验证）；v26.1.0 挂 socket 可获得容器归属。首期 Profile 挂 containerd socket，使 `npu_container_info`/`process_info.pod_name` 可用。v26.1.0 的 socket-free 行为需重验。
- `-containerMode` 按后端设置：静态 Docker 用 `docker`、Kubernetes 用 `containerd`，与挂载的 runtime socket 对应；挂 socket 时容器 parser 成功，不挂时记录初始化失败但物理 collector 继续。
- vNPU collector 对 hami-core 软切分**零样本**是必然结果（软切分不创建虚拟设备），即使配置为 ON。
- collector capability：共享配置开启 `npu`/`ddr`/`hbm`，关闭 `vnpu` 及未验证组；310P 安全跳过 HBM，910B 安全跳过 DDR。
- Exporter 健康由 Profile 声明的 readiness 表示，非 NodeAgent scrape 状态。

## 兼容性、运维和安全

- 未指定类型时保留 legacy DCGM 自动兼容；NPU 必须由静态节点 `AcceleratorType` 或 Kubernetes Plugin/Node label 显式选择。
- 社区版维持硬编码 NodeAgent image；Enterprise 版本手动选择 Enterprise image + `--accelerator-type=npu` 原子下发。
- `Privileged=true` 首期显式，理由是官方 Exporter 访问宿主驱动/DCMI 的真实硬件验证。
- 310P 和 910B 只有在镜像、mount、设备、权限和 capability 验证为同一 runtime compatibility group 后才能共享 managed Profile；否则拒绝混合并延后 910B Kubernetes 发布。
- Exporter 重启后物理样本短暂缺失时不产生伪值。

### CPU/混合节点路径

**Kubernetes**：统一 NodeAgent DaemonSet + 单一 accelerator exporter 类型。planner 依据 Node label 与 `MetricsExporter.Runtime.NodeSelector` 的匹配选择零个或一个 accelerator type：

- **零匹配** → CPU-only：不部署 accelerator exporter，NodeAgent 无 `--accelerator-type`，仅输出 node/runtime 指标（node-exporter/cAdvisor/cgroup 派生）。Node label 不命中 `NodeSelector` 的 CPU 节点天然被 DaemonSet 排除。
- **恰一个匹配 NPU Profile** → 下发 Enterprise NodeAgent image + `--accelerator-type=npu`，仅匹配 NodeSelector 的节点部署 Exporter（DaemonSet + NodeSelector）。CPU 节点在同一 DS 中仅输出通用指标。
- **多类型匹配** → 规划前拒绝，保留上次成功部署，metrics component 报配置错误，等管理员恢复单类型。不得按优先级任选或静默回退。

**静态 Ray/SSH**：无统一 DS。按每个静态节点的 `AcceleratorType` 生成本地组件配置（`detectClusterAcceleratorType` 对每个节点 `GetNodeAcceleratorType`；空类型节点跳过、非空类型不一致则拒绝混合）。CPU-only 节点与 NPU 节点天然共存，CPU 节点不部署/不抓 accelerator exporter。

**"没有本机 Exporter"与"配置了 npu 但 adapter 未注册"必须区分**：

- 前者是统一 DS 中 CPU 节点的正常状态 → 仅跳过该节点 accelerator 样本。
- 后者是明确组件版本/配置错误 → NPU Node Agent fail-fast，避免静默丢失加速器可观测性。

**启动校验规则**：`accelerator-type` 为空允许 CPU-only；非空且 adapter 未注册 fail-fast；非空且 adapter 已注册但本机无 Exporter 时正常运行，仅跳过该节点 accelerator 样本（不切换为 CPU 类型）。

### 故障降级路径

**设计原则**：物理证据与调度证据**独立降级**；Exporter 物理指标可用而 PodResources/Ray/HAMi 不可用时，仍输出物理指标；调度证据不可用时不输出 allocated/free 与副本指标。任何缺失都用"序列缺失"表达，不输出 0/NaN/unknown，不改变 Exporter readiness。

| 故障场景 | 判定 | 降级行为 |
|---|---|---|
| **Exporter 不可达/未就绪** | NodeAgent 抓取失败 / readiness probe 失败 | 跳过该节点 accelerator 样本，仍输出 node/runtime 指标；不切换为 CPU 类型；`neutree_metrics_scrape_up` 保留为抓取诊断，不作为告警权威 |
| **Exporter 解析成功但某指标缺失** | 同 die 两族内存均不完整、util 无有效样本等 | 仅缺失该序列，不输出 0/NaN |
| **PodResources socket 不可用**（K8s） | kubelet PodResources 读取失败 | 不输出 allocated/free 与副本指标；物理指标照常输出 |
| **HAMi annotation 缺失** | 该 pod 无 `devices-allocated` / 非 HAMi 调度的 NPU pod | 无法判定分配形态 → 副本 allocation/memory_allocated 缺失 |
| **HAMi :9395 不可达** | :9395 抓取失败 | 节点级整卡备用源缺失，不影响 exporter 主路径；只影响交叉校验 |
| **同 die 多 pod 共享但配额缺失** | annotation 有 vdie 但无 memory 配额字段 | `memory_allocated_bytes` 缺失（不能猜配额） |
| **`machine_npu_nums` 与 `id` 集合不一致** | template 虚拟化下逻辑设备数 > 物理 die 数 | `neutree_node_accelerator_total` 按 `vdie_id` 去重，不直接用 `machine_npu_nums` |
| **adapter 未注册**（配置错误） | `--accelerator-type=npu` 但镜像无 `npu` adapter | **fail-fast**（启动失败），避免静默丢失加速器可观测性 |
| **多类型匹配**（K8s） | Node label 匹配 >1 个 accelerator Profile | 规划前拒绝，保留上次成功部署，metrics component 报配置错误 |
| **Exporter 重启** | 物理样本短暂缺失 | 缺失期间不产生伪值；恢复后正常 scrape 重建序列 |

**物理 vs 调度证据独立降级的细化**：

- Exporter scrape 成功但 PodResources / Ray Dashboard / HAMi 全部不可用 → 只输出 `neutree_accelerator_*` 物理指标，不输出 `neutree_node_accelerator_allocated/free` 与全部副本样本。
- 调度证据完整且确认**零分配**时 → 显式输出 `allocated=0`、`free=total`（表示真实空闲，不是未知）。
- 调度证据可用但某 vdie 归属不唯一（同 die 多 pod 且无法区分）→ 该 vdie 相关副本样本缺失。

## 当前状态 vs 目标态

| 目标能力 | 代码现状（2026-08-12） |
|---|---|
| adapter 接口（`Accelerator`/`BuildMetrics`）与 registry | **未实现**：`AcceleratorEvidence`/`AcceleratorMetricResult`/`BuildMetrics` 不存在（见 §总体架构）；normalizer 仅 `accelerator_type` label，无 adapter 目录 |
| `npu` adapter（双源解析：exporter + HAMi annotation） | 未实现 |
| `AcceleratorExporterProfile.Command` | 未实现（Profile 无该字段） |
| `Readiness` | 未实现 |
| `Privileged` / `ComponentVolume`/`VolumeMount`（含 socket） | 未实现（Runtime 只有 HostNetwork/HostPID/Capabilities/NodeSelector/DockerRunOptions） |
| `npu` plugin `ResourceParser.ParseFromKubernetes`（ResourceName→type） | 未实现：GPU/AMD 已有 parser（`gpu_parser.go`/`amd_gpu_parser.go` 硬编码 `nvidia.com/gpu`/`amd.com/gpu`），企业 `npu` plugin 需照此声明 `huawei.com/Ascend310P` |
| HAMi annotation 解析（`devices-allocated` 配额） | 未实现 |
| :9395 抓取 | 未实现 |
| normalizer 迁移（legacy DCGM 保留 + adapter 接入） | 未开始：`normalizeAcceleratorSamples` 等仍硬编码 DCGM→`neutree_*` switch |
| adapter 注册机制（镜像 core 的 plugin 注入模式） | 未实现：core 已有 `plugin.go` 包级 registry + `init()` 注册（GPU/AMD），NodeAgent 侧无对应 adapter registry |
| 抓取链路（NodeAgent 端口 Profile 下发 + 静态 file-SD） | 部分实现：vmagent `kubernetes_sd_configs` + NodeAgent `spec.nodeName` 过滤 + CPU 三层隔离已存在；**端口硬编码 `19400/9400`**、静态 file-SD 未实现 |

## 验证、发布和 Roadmap

### Roadmap 1：通用契约和 Enterprise 组件

adapter registry（镜像 core `plugin.go` 注入模式）、`Accelerator`/`BuildMetrics` 接口、`AcceleratorEvidence`/`AcceleratorMetricResult` 类型、启动校验（adapter 未注册 fail-fast）、`ComponentVolume`/`VolumeMount`（含 socket）、planner 手工选 Enterprise NodeAgent 镜像 + `--accelerator-type=npu` 原子下发。**企业版提供独立 NodeAgent 镜像**（在 `adapter/npu.go` 加 `init() { Register(&npuAccelerator{}) }` 构建）；OSS 镜像仅内置 nvidia。**把 NVIDIA DCGM 逻辑迁成 `nvidia` adapter 作为参考实现**（替换 legacy normalizer 硬编码路径，验证接口设计）。不迁移既有 `NodeComponentVolume`。

### Roadmap 2：静态 Ray/SSH 物理指标

企业 NodeAgent 镜像的 `npu` adapter 解析静态 Ray/SSH 物理指标，验证 310P/910B 的 UUID/index/product、内存、利用率、温度。310P 输出 DDR 内存、基础 util、无 PCIe；910B 输出 HBM 内存、overall util（cube 不纳入）、PCIe bandwidth 专属指标。910B 以其单独 capability matrix 为准。**抓取链路**：Ray Head vmagent 按 file-SD 从每个 NPU 节点 IP 抓取原始指标；静态 exporter 用 host network，NodeAgent 通过 localhost 抓取。

### Roadmap 3：Kubernetes Managed Exporter

通过 Profile 的 `Command`、结构化 volume、`Privileged`、readiness 部署 NPU Exporter，**含 CRI socket volume**。310P 的 Pod 网络、HostPath、socket、vmagent Pod-IP 抓取经真实节点 E2E 后放行。v26.1.0 的 socket-free 行为重验作为前置。**抓取链路落地**：NodeAgent 端口从硬编码 `19400/9400` 改为 Profile 下发 `--accelerator-exporter-port`/`--accelerator-exporter-metrics-path`（NPU 8082）；vmagent 保留 `kubernetes_sd_configs` + Pod-IP 抓取。

### Roadmap 4：整卡/die allocation 与副本级指标

交付 Kubernetes 的整卡直用 / die 独占副本指标（`process_info` 按 pod 聚合）与软切分共享 / template 的 `memory_allocated_bytes`（HAMi annotation 配额）；交付静态整卡独占 / 非切分共享卡副本指标（进程归属、die total 分配量）。软切分/template 的 `memory_used`/`utilization`、静态非切分共享卡的 `utilization` 明确不承诺，输出公开支持矩阵。HAMi :9395 作节点级备用源接入。

### 验证矩阵

| 验证类型 | 必需覆盖 |
|---|---|
| Unit test | 基于 fixture 的 adapter 测试：K8s 整卡直用 / die 独占 vs 软切分共享 vs template 分流；静态非切分共享卡的多进程归属；`process_info` 按 pod/进程聚合；HAMi annotation 配额解析；两源单位差异（MiB vs bytes）；310P DDR vs 910B HBM 指标族 fallback；910B overall util 优先 + cube 排除；910B PCIe bandwidth；CPU-only 启动；**adapter registry 注册/查找 + 未注册 fail-fast**；**nvidia adapter 迁移后现有 DCGM 断言全绿**（`normalizer_test.go` 迁移） |
| E2E test | 310P static Ray/SSH 物理指标 + 整卡独占/非切分共享卡副本指标；310P Kubernetes 整卡直用 + 软切分共享 + template `memory_allocated`；910B static 物理指标（HBM、overall util、PCIe）；CPU-only 集群无 Exporter 仍输出节点指标；v26.1.0 socket-free 行为复测；**企业 NodeAgent 镜像含 npu adapter 而 OSS 镜像不含** |
| Manual | 驱动/DCMI 版本组合、最小权限试验、节点重启/驱动重装恢复 |

## 来源

- 本次实机抓取：`/Users/huangwei/.kube/npu`（2026-08-12），节点 `npu`，5 个 qwen pod，HAMi hami-core；template mode 与整卡直用（`-full-`）抓取
- core 注入参考：`internal/accelerator/plugin/plugin.go`、`gpu.go`、`amd_gpu.go`、`internal/accelerator/manager.go`、`cmd/neutree-core/app/builder.go`
- NodeAgent 装配参考：`cmd/neutree-node-agent/neutree-node-agent.go`（options provider 分支）、`internal/observability/neutreemetrics/hami/hami_provider.go`、`allocation/allocation_provider.go`
- [NPU 指标支持分析](./npu-metrics-support-analysis.md)
- [NPU 监控详细设计](./npu-monitoring-design.md)
- [NPU 指标支持矩阵](./npu-metrics-support-matrix.md)
- [HAMi Ascend NPU 虚拟化调研](./hami-ascend-npu-virtualization-research.md)
- ADR [0001](./adr/0001-enterprise-owned-accelerator-metrics-adapters.md) / [0002](./adr/0002-structured-component-volumes.md) / [0003](./adr/0003-adapter-owned-accelerator-metric-aggregation.md)
- `api/v1/accelerator_plugin.go`、`internal/observability/neutreemetrics/collector.go`、`normalizer/normalizer.go`
- [MindCluster master NPU Exporter 源码](https://github.com/Ascend/mind-cluster/tree/master/component/npu-exporter)——2026-08-12 分析 `collector/metrics/*.go` 的 `IsSupported` 产品表（`supportedHbmDevices`/`notSupportedDdrDevices`/`supportedOverallUtilDevices`/`supportedCubeDevices`/`supportedVnpuDevices`/`supportedPcieDevices`/`supportedHccsDevices`/`supportedSioDevices`）、`setProductType`（仅 310P）、`IsTrainingCard`（310* 恒非训练卡）、`GetChipListWithVNPU`（vnpu 仅 310P）
- [MindCluster v26.0.0 NPU Exporter 安装说明](https://gitcode.com/Ascend/mind-cluster/blob/branch_v26.0.0/docs/zh/scheduling/installation_guide/03_installation/manual_installation/03_npu_exporter.md)
- [MindCluster v26.0.0 Prometheus 指标 API](https://gitcode.com/Ascend/mind-cluster/blob/branch_v26.0.0/docs/zh/scheduling/api/npu_exporter/01_prometheus_metrics_api.md)
