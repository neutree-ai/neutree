# NPU 监控详细设计

> **文档状态：历史草稿，已被替代。** 当前实现和评审只使用
> [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md)；文档总入口见
> [加速器监控设计文档索引](./accelerator-monitoring-design-index.md)。本文只用于追溯旧方案，
> 不得作为 API、Profile、指标、部署或验收输入。

## 背景

本设计定义 Neutree 对 Ascend 物理 NPU 的监控、整卡分配和条件化副本指标的实现边界。
首期逻辑加速器类型为 Enterprise plugin 提供的 `npu`，覆盖 Atlas 300I Duo / 310P 和
910B；每个产品必须独立通过硬件验证，310P 的结论不能外推到 910B。

本文是已被替代的 NPU 监控历史草稿。以下链接仅用于追溯文档演进：

- [NPU 指标支持分析](./npu-metrics-support-analysis.md) 保留硬件调研、原始指标和实测证据。
- [NPU 指标支持矩阵](./npu-metrics-support-matrix.md) 是面向用户的已验证支持声明。
- [ADR 0001](./adr/0001-enterprise-owned-accelerator-metrics-adapters.md)、[ADR 0002](./adr/0002-structured-component-volumes.md) 和 [ADR 0003](./adr/0003-adapter-owned-accelerator-metric-aggregation.md) 记录已作出的架构决策。
- `ascend_enterprise_internal_extension_design.md` 覆盖更宽的企业版、引擎和后续 vNPU 议题；它与本文都不是监控权威，任何监控冲突均以 [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md) 为准。

## 范围

首期包含：

- Neutree 管理 NPU Exporter，保留其已启用的原始厂商指标供 vmagent 和客户诊断。
- Enterprise NodeAgent NPU adapter 将已验证的物理指标转换为既有 `neutree_*` 通用指标。
- 静态 Ray/SSH 与 Kubernetes 的整卡 inventory、allocation、free 以及满足证据条件的副本指标。
- CPU-only 节点与没有可用 Exporter 的节点的兼容行为。

首期不包含：

- vNPU inventory、allocation、replica usage、dashboard 或 HAMi template 语义。
- 新增健康、功耗、频率、互连或厂商专属的 `neutree_*` descriptor。
- 310P 不具备来源的 PCIe 累积字节 Counter。
- 通过 Docker/containerd/CRI socket 解析容器归属。
- 多种加速器类型同时由一个 Kubernetes NodeAgent DaemonSet 处理。

## 当前状态

社区版 metrics component 已能从 `AcceleratorProfile.MetricsExporter` 生成 managed
Exporter DaemonSet，并通过 NodeAgent 抓取 Exporter、生成 GPU/DCGM 通用指标。相关入口为：

- `api/v1/accelerator_plugin.go` 的 `AcceleratorExporterProfile`；
- `internal/cluster/component/metrics/exporters.go` 的 `buildAcceleratorExporter`；
- `internal/cluster/component/metrics/manifests.go` 的 Exporter DaemonSet 模板；
- `cmd/neutree-node-agent/neutree-node-agent.go` 的 Profile、PodResources 和 allocation provider 装配；
- `internal/observability/neutreemetrics/server.go` 的抓取和 normalizer 调用路径。

当前实现仍假设 DCGM/NVIDIA：device snapshot 和 normalizer 按 `DCGM_*` 指标构造设备，
`Server.Config` 中也明确保留了引入 accelerator adapter 的 TODO。现有 exporter profile
尚无 `Command`、结构化 HostPath volume、`Privileged` 或 readiness 表达；Kubernetes renderer
也尚未投影这些字段。因此本文是目标设计，不表示社区版已经支持 NPU。

## 总体架构

```text
                    NPU Exporter /metrics
                         |             \
             原始厂商指标 |              \ NodeAgent 本地抓取
                         v               v
                 vmagent 原样 remote write   原始调度与设备证据
                                                   |
Exporter scrape + PodResources / Ray Dashboard + Actor PID/process + Node context
                                                   |
                                                   v
                                          AcceleratorEvidence
                                                   |
                                                   v
                                      Enterprise NPU adapter
                           解析 npu_*、解析设备引用、关联 vdie_id、验证语义
                                                   |
                                                   v
                                      AcceleratorMetricResult
                                                   |
                                                   v
                            共享标签、descriptor 和单位校验 -> neutree_* 指标
```

vmagent 和 NodeAgent 有不同职责。vmagent 不改写、不丢弃 NPU Exporter 已启用 collector
的原始时间序列，包括 `process_id` 标签；NodeAgent 只消费其中一部分，输出可跨加速器使用的
`neutree_*` 指标。原始厂商标签不是 Neutree 的公共指标契约。

## 所有权和接口

共享层负责 I/O、超时、公共标签、Prometheus 序列化、CPU 兼容、descriptor/单位校验及
Exporter target 发现。它不解释厂商资源名、device ID、环境变量或指标名。

Enterprise adapter 的内部接口为：

```go
BuildMetrics(ctx, evidence AcceleratorEvidence) (AcceleratorMetricResult, error)
```

`AcceleratorEvidence` 是 `internal/observability/...` 的瞬时输入，包含 Exporter 文本、
节点上下文、PodResources、Endpoint Pod、Ray Dashboard、Actor/PID 和进程证据。
`AcceleratorMetricResult` 只能包含既有通用 metric ID 和经过校验的标签。两者不进入
`api/v1`，也不成为外部 plugin 协议。

一个 NPU adapter 拥有：

- `npu_chip_info_*` 的解析、单位转换和按设备指标族 fallback；
- `vdie_id`、逻辑 index、`model_name`、PCIe bus 的物理设备身份；
- 资源名匹配和 Kubernetes Device ID 末段 index 到物理设备的解析；
- Ray actor 后代 NPU process 与 `vdie_id` 的关联；
- 对整卡独占、唯一关联和缺失样本的判定。

共享 allocation provider 保留 PodResources 的每个 `ResourceName` 和 device ID，但不写入
`huawei.com/*` 常量，也不合并其它 Device Plugin 的设备 ID。NPU plugin/profile 以精确允许
列表声明可处理的 ResourceName；当前环境的 `huawei.com/Ascend310P` 是其中一项，后续名称
例如 `huawei.com/npu` 必须显式追加，不能由 NodeAgent 或 adapter 猜测。

## Exporter Profile 和运行时模型

### Profile

NPU Profile 是镜像、启动语义、配置文件、端口、挂载、权限、placement 和 readiness 的唯一
声明来源。它固定使用已验证的 NPU Exporter digest，并由当前 Enterprise 版本手动指定；
Release Info 以后可按集群独立解析社区版和 Enterprise 组件版本。

目标模型在现有 `AcceleratorExporterProfile` 基础上增加：

```go
type AcceleratorExporterProfile struct {
    Name        string
    Image       string
    Command     []string // Kubernetes container.command / Docker command
    Args        []string
    Port        int
    MetricsPath string
    Env         map[string]string
    ConfigFiles []AcceleratorExporterConfigFile
    Runtime     *AcceleratorExporterRuntimeProfile
    Readiness   *AcceleratorExporterReadiness
}

type AcceleratorExporterRuntimeProfile struct {
    HostNetwork      bool // Static Docker only
    HostPID          bool
    Privileged       bool
    Capabilities     *AcceleratorExporterCapabilities
    NodeSelector     map[string]string
    Volumes          []ComponentVolume
    VolumeMounts     []ComponentVolumeMount
    DockerRunOptions []string // compatibility only, cannot express mounts/devices
}

type AcceleratorExporterReadiness struct {
    HTTPPath            string // absolute path; empty defaults to MetricsPath
    Port                int    // zero defaults to the exporter Port
    InitialDelaySeconds int    // NPU default: 15
    PeriodSeconds       int    // default: 5
    TimeoutSeconds      int    // default: 5
    FailureThreshold    int    // default: 3
}

type AcceleratorProfile struct {
    AcceleratorType string
    EngineRuntime   *RuntimeConfig
    MetricsExporter *AcceleratorExporterProfile
    Allocation      *AcceleratorAllocationProfile
}

type AcceleratorAllocationProfile struct {
    // KubernetesResourceNames is the exact allow-list of PodResources names
    // that the plugin's adapter may resolve. It is unrelated to exporter startup.
    KubernetesResourceNames []string
}
```

`ComponentVolume`/`ComponentVolumeMount` 是 backend-neutral 的公共模型。首期仅允许受校验的
HostPath source：`directory`、`file`、`socket`、`char_device`。ConfigMap 文件继续由
`ConfigFiles` 生成，ModelCache 保持其独立语义。Profile 定义的 volume 名称与 ConfigMap
volume 名称不得冲突。`Allocation` 是通用调度关联模型，不属于 `MetricsExporter`：它可省略；
一旦声明，`KubernetesResourceNames` 必须非空、无重复且逐项为精确名称。初期 Enterprise NPU plugin
配置 `huawei.com/Ascend310P`；新 Device Plugin 名称（例如 `huawei.com/npu`）必须通过同一
字段显式追加。空列表表示该 profile 不支持 Kubernetes allocation 关联，不允许由 adapter
推断资源名。

NPU Profile 使用：

```yaml
metrics_exporter:
  name: npu-exporter
  image: swr.cn-south-1.myhuaweicloud.com/ascendhub/npu-exporter@sha256:298d31a8ddb472587c3669d9c8b2c4499eed7383f91ebe042ac29cc3502c65b6
  command: ["/usr/local/bin/npu-exporter"]
  args: ["-ip=0.0.0.0", "-port=8082", "-updateTime=5", "-containerMode=docker"]
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
allocation:
  kubernetes_resource_names: ["huawei.com/Ascend310P"]
```

`Command` 不可省略。已验证镜像没有 OCI `ENTRYPOINT`，只通过 image `CMD` 启动二进制；
Kubernetes 写入 `args` 会替换该 `CMD`，若不显式 `Command`，kubelet 将尝试执行
`-ip=0.0.0.0` 并失败。

### Backend transformer

Kubernetes transformer 将 `Command`/`Args` 投影到 `container.command`/`container.args`，
将 typed HostPath 投影为 `corev1.Volume`/`VolumeMount`，将 `Privileged`、capability、
HostPID 投影到 Pod security context。managed Exporter 使用 Pod 网络，不投影 static 的
`HostNetwork`；Exporter 监听 `0.0.0.0:8082`，供本节点 NodeAgent 和 vmagent 经 Pod IP
抓取。`Readiness` 投影为 HTTP readiness probe；`HTTPPath` 或 `Port` 为空时分别使用
Profile 的 `MetricsPath` 或 `Port`。静态 Docker transformer 将同一字段投影为
`NodeComponentHealthCheck`，而不是让 NodeAgent 的一次 scrape 决定组件健康。

静态 Docker transformer 将同一模型投影为 command、`--volume` 或 `--device`、
`--privileged`、capability、HostPID 和 host network。静态 NodeAgent 通过 `localhost:8082`
抓取；Ray Head vmagent 按现有 GPU file-SD 方式从每个 NPU Node IP 抓取原始指标。

任何 Profile-defined `--volume`、`--mount`、`--device` 均禁止放进 `DockerRunOptions`。
Kubernetes 不解析该兼容字段。

### Exporter 运行边界

首期 Profile 只读挂载驱动、DCMI 和 `/sys`，没有 Docker、containerd 或 CRI socket。使用
`-containerMode=docker` 时，上游可选 container parser 会记录初始化失败；物理 NPU
collector 继续工作，这是已在 310P 上验证的行为。vNPU、网络和其它未验证 collector 在
`metricConfiguration.json` 中显式关闭。初期所有 NPU 产品共用一份 collector 配置：开启
`npu`、`ddr`、`hbm`，关闭 `vnpu`、网络和其它未验证 collector。上游按产品 capability
安全跳过不支持但被设为 `ON` 的 collector；这不构成 910B 的发布承诺，910B 仍须完成真机
运行时与指标语义验证。

### 310P 与 910B 的 collector capability

MindCluster v26 源码在初始化时调用每个 collector 的 `IsSupported`；不支持的 collector
不会进入采集链，即使配置文件将其设为 `ON`。下表是源码 capability，不是 Neutree 对外的
硬件支持承诺：

| Collector 或指标族 | 310P | 910B | 首期 Profile/adapter 处理 |
| --- | --- | --- | --- |
| `ddr`：`npu_chip_info_{total,used}_memory` | 支持 | 不支持，安全跳过 | 共享配置开启；910B 自动跳过。 |
| `hbm`：`npu_chip_info_hbm_{total,used}_memory` | 不支持，安全跳过 | 支持 | 共享配置开启；310P 自动跳过，910B 的对外支持仍待真机确认。 |
| `npu_chip_info_utilization` | 支持 | 支持 | 作为通用 utilization fallback。 |
| `npu_chip_info_overall_utilization`、`cube_utilization` | 未列为支持设备 | 910B 支持 | overall 优先于基础 utilization；cube 不代表整卡利用率，不参与 fallback。 |
| `hccs` | 不支持 | 支持 | 初期均关闭，仅作为后续原始诊断候选。 |
| `pcie` bandwidth collector | 不支持 | 支持 | 初期均关闭；即使启用也不映射为 Neutree PCIe byte counter。 |
| `vnpu` | 支持 | 不支持 | 两者均显式关闭，vNPU 不在当前范围。 |
| `sio`、`ub` | 不支持 | 不支持 | 显式关闭。 |
| network、RoCE、optical 等 | 取决于训练卡/具体板型 | 取决于训练卡/具体板型 | 未经目标硬件 E2E 前显式关闭。 |

310P 专用的 `npu+ddr` 配置不能满足 910B；但 `npu+ddr+hbm` 共享配置可由上游 capability
过滤安全复用。adapter 不按产品名硬编码 DDR 或 HBM：它以同一 `vdie_id` 上完整的 HBM
`used+total` 对为第一优先级，完整的 DDR `used+total` 对为第二优先级，避免混用两个指标族；
两者均不完整则不输出通用内存指标。`model_name` 只用于规范化 product label 和验证格式。
仅当镜像、挂载、设备文件、权限或网络等运行时契约不同，才需要引入产品 Profile variant。

Exporter 的健康状态由 Profile 声明的 Kubernetes readiness 或静态 component health check
表示，而非 NodeAgent scrape 状态。310P 实测约 7--8 秒后可提供指标，因此 NPU Profile 的
初始宽限固定为 15 秒。`neutree_metrics_scrape_up` 仅保留为 NodeAgent 抓取诊断。

## 指标契约

| 指标族 | 数据源和语义 | 首期规则 |
| --- | --- | --- |
| 物理设备 inventory/total | `vdie_id`、`id`、`model_name`、PCIe bus | `vdie_id` 为 canonical UUID。每个 `vdie_id` 的参与样本必须携带相同的 `model_name`：仅去除首尾空白，结果非空、最多 128 bytes、且不含控制字符；保留大小写和其它可打印字符，再作为 `product` label。缺失、非法或冲突时不输出该设备的 `neutree_*`。缺失的其它既有描述性 label 才用 `unknown`。 |
| 设备 utilization | `npu_chip_info_overall_utilization` 或 `npu_chip_info_utilization`，百分比 | 优先 overall，fallback 到基础 utilization，除以 100 输出 ratio；cube 不参与。 |
| 设备 memory used/total | 同一 `vdie_id` 上完整的 `npu_chip_info_hbm_{used,total}_memory` 或 `npu_chip_info_{used,total}_memory`，均为 MiB | 先 HBM、后 DDR，used/total 必须来自同一指标族，乘以 MiB 转 bytes；两族均不完整、单位或数值无效则缺失。 |
| 设备 temperature | `npu_chip_info_temperature` | 直接输出 Celsius。 |
| PCIe bytes counter | 无 310P 对等来源 | 不输出，禁止用 bandwidth gauge 伪造 counter。 |
| health/power/frequency/interconnect | 厂商原始指标 | 原样保留给 vmagent，不增加通用 descriptor。 |
| node allocated/free | 调度 allocation 的唯一 `vdie_id` 集合 | 仅在完整且无歧义的调度关联存在时输出；证据成功且没有分配时显式输出 `allocated=0`、`free=total`，证据不可用或不唯一时两者均缺失。 |
| replica allocation | PodResources 或 Ray allocation | 仅整卡独占、唯一映射时输出。 |
| replica memory allocated | 该整卡经同一指标族 fallback 选出的 memory total | 先 HBM、后 DDR；不是调度 request，也不是 used memory。 |
| replica utilization | 物理 device utilization | 仅在整卡独占且唯一归属时输出。 |
| replica memory used | 静态 Ray 使用 replica 关联的进程 memory 之和；Kubernetes 使用唯一整卡经同一指标族 fallback 选出的物理内存已用 | Kubernetes 仅在 PodResources 证明整卡唯一归属时，先 HBM、后 DDR；共享、vNPU 或映射不唯一时缺失。 |

物理指标与 allocation 证据独立降级。Exporter 可用而 PodResources/Ray 不可用时，adapter
仍输出物理指标，但不输出 allocated/free 或任何 replica 样本。不得用 `0`、`NaN` 或
`unknown` 伪造缺失数值。只有 allocation 证据成功并确认零分配时，才以
`allocated=0`、`free=total` 表示真实空闲。

## Allocation 数据流

### Kubernetes

NodeAgent 挂载 kubelet PodResources socket 并读取其 v1 `List` API。NPU plugin/profile 以
精确允许列表声明 adapter 可处理的 ResourceName；只有命中项才继续处理。Device ID 必须具有
非空前缀和最末段十进制 index，例如
`Ascend310P-0` 或 `Ascend910B-0`，再以 Exporter 唯一的 `id=<index>` 取得 `vdie_id`。
缺少前缀、末段非整数、index 不存在或匹配不唯一时不猜测。真实 310P Kubernetes 集群已验证：
三个 vLLM Pod 的分配为 `0`、`1/3`、`2`，与四个 Exporter physical index 一一对应且无重叠。

Pod annotation 可用于调试对照，但不是 fallback。HAMi/Device Plugin allocation metadata
fallback、逻辑 index 猜测和可见设备环境变量不进入首期路径。ResourceName 不匹配、ID
格式不合法、index 不存在或映射不唯一时，adapter 省略 allocation/free 与全部 replica
指标。

PodResources 提供的是调度归属，不提供 NPU process 到 Pod 的归属。无 socket 的 Exporter
输出中 `container_name`、`namespace`、`pod_name` 为空，因此首期不尝试把 process memory
精确归属到 Kubernetes Pod。与现有 GPU 的整卡唯一 allocation fallback 保持一致：当一个
`vdie_id` 只属于一个整卡 Endpoint replica 时，adapter 将该设备的
由同一指标族 fallback 选出的 physical memory used（先 HBM、后 DDR）作为 replica
`memory_used_bytes`，其明确语义是“唯一整卡的物理内存已用”。同一 `vdie_id` 被多个 replica 共享、映射不唯一或属于 vNPU 时，
`memory_used_bytes` 缺失。未来若引入经验证的 PID-to-Pod 证据，可增加比物理 fallback
更精确的 process-memory 语义，但不得改变当前序列的含义而不作版本化处理。

### 静态 Ray/SSH

adapter 以 Ray Dashboard Backend Actor 的 PID 为根，读取 Actor `required_resources` 的
canonical `NPU` 值；所有配置 alias 必须同时存在、为相同正整数，且绝不求和。它只接受
Actor 后代的 NPU `process_id`，并将其 `vdie_id` 归属给 replica。`ASCEND_VISIBLE_DEVICES`
和 `ASCEND_RT_VISIBLE_DEVICES` 仅用于运行时诊断，不能作为 allocation 依据。

静态 Ray 已验证 2+1+1 的整卡资源账本、后代多进程归属和受控推理下的设备 utilization。
`memory_used_bytes` 是每个 replica 所有关联 process memory 的和；物理 `used_memory`
不作为 replica 内存，因为其中可能包含非该 replica 的使用。

## Kubernetes 统一 DaemonSet 和 CPU 兼容

Kubernetes 集群的 NodeAgent 是一个统一 DaemonSet。planner 依据 Node label 与
`MetricsExporter.Runtime.NodeSelector` 选择零个或一个 accelerator type：

- 没有匹配 Profile 时，下发无 `--accelerator-type` 的 CPU-compatible NodeAgent，不部署 NPU Exporter。
- 恰有一个匹配 NPU Profile 时，下发 Enterprise NodeAgent image、`--accelerator-type=npu` 和对应 Exporter endpoint。
- 同时匹配多个类型时，在变更前拒绝规划，保留上次成功部署并报告配置错误。

配置了 `npu` 但 Enterprise image 中没有 adapter 时，NodeAgent 启动失败。adapter 已注册但
本机 Exporter 不可达时，NodeAgent 继续输出 node/runtime 指标，只省略 accelerator 序列；
它不切换为 CPU 类型。

## 兼容性、运维和安全

- 未指定类型时保留 legacy DCGM 自动兼容；NPU 必须由静态节点 `AcceleratorType` 或 Kubernetes Plugin/Node label 显式选择。
- 社区版维持当前硬编码 NodeAgent image；初期 Enterprise 版本手动选择 Enterprise image。后续 Release Info 按集群管理组件版本。
- 初期使用 `Privileged=true`，理由是官方 Exporter 访问宿主驱动/DCMI 的真实硬件验证；最小权限优化另行进行。
- 310P 和 910B 只有在镜像、mount、设备、权限和 capability 被验证为同一 runtime compatibility group 后才能共享一个 managed Profile；否则拒绝混合并延后 910B Kubernetes 发布。
- Exporter restart 后 physical samples 短暂缺失时不产生伪值；恢复后由正常 scrape 重新产生时间序列。

## 验证、发布和回滚

### Unit test

- NPU adapter 对 310P/910B fixture 的指标解析、单位、product、`vdie_id` 和同设备指标族 fallback。
- 资源名过滤、Device ID 末段 index 映射、唯一性和失败时的缺失语义。
- static Ray alias 一致性、Actor 后代多进程 memory 聚合和环境变量拒绝作为 allocation 依据。
- Profile validation、Command/Args、volume 类型/名称、privileged、readiness 默认值及两个 backend transformer。
- `Allocation.KubernetesResourceNames` 在声明 `Allocation` 时的必填、去重和精确匹配；非允许资源、非法 Device ID 末段或非唯一 index 均不产生 allocation 结果。
- CPU-only、未配置 exporter、adapter 未注册、scrape/PodResources/Dashboard 独立失败场景。

### DB test

不适用。该能力只读取 Prometheus 文本和运行时 API，不改变持久化模型。

### E2E test

- 310P static Ray/SSH：Exporter 启动、原始 vmagent file-SD 抓取、物理通用指标、2+1+1 allocation 和整卡 replica 指标。
- 310P Kubernetes：DaemonSet Profile transformer、Pod IP vmagent 抓取、PodResources 到 `vdie_id`、allocated/free，以及唯一整卡的 allocation、physical-memory fallback 和 utilization。
- 910B：用同一矩阵独立验证，不把 310P 结果视为通过；共享 `npu/ddr/hbm` 配置下 DDR collector 必须被安全跳过且不影响 readiness，HBM total/used 与 overall utilization 的指标名、单位、标签和同设备 fallback 必须通过验证，并验证 socket-free、Pod 网络、启动和重启的运行时契约与 310P 一致。
- CPU-only 集群：不部署 NPU Exporter，NodeAgent 仍产出 node/runtime 指标。

### Manual testing

仅硬件依赖场景需要人工验证：驱动/DCMI 版本组合、最小权限试验和节点重启/驱动重装恢复。
PID-to-Pod 证据是未来精确 Kubernetes process-memory 语义的前置条件，不阻塞首期整卡
physical-memory fallback。

发布以 Profile/Enterprise NodeAgent image 同一 component revision 原子下发。回滚时移除或
切换 NPU Profile，恢复已知可用的 NodeAgent image；原始 vmagent 数据和既有 CPU/GPU
链路保持独立，不因 NPU adapter 回滚而改变。

## 已确认决策

- NPU 首期使用 Neutree-managed Exporter，镜像 digest 固定；不使用 DCMI Go/Python 直连。
- 原始已启用 NPU Exporter 指标由 vmagent 全量保留；NodeAgent 只生成部分 `neutree_*`。首期仅启用 `npu`、`ddr`、`hbm`；`vnpu`、HCCS、PCIe、网络等未验证 collector 保持关闭，后续必须有对应硬件 E2E 才能开启。
- socket-free 是首期安全边界，`-containerMode=docker` 保持上游默认。
- 静态使用 host network；Kubernetes 使用 Pod network，不额外部署中央 vmagent。
- allocation 权威来源是 Kubernetes PodResources 或静态 Ray Dashboard/PID/process，而不是 Exporter container metadata。
- Kubernetes 的唯一整卡 replica `memory_used_bytes` 使用同一设备的物理内存指标族 fallback（先 HBM、后 DDR）；静态 Ray 使用 Actor 后代 process memory 和。共享或 vNPU 均不输出该序列。
- `model_name` 是产品标签；Ray/Device Plugin resource alias 不是产品标签。
- 缺少独立数值或副本证据时不输出时间序列；既有硬件 info 的描述性 label 才可用 `unknown`。
- allocation 证据完整且确认零分配时显式输出 `neutree_node_accelerator_allocated=0` 和 `free=total`；证据不可用、映射不唯一或解析失败时两条序列均缺失。
- vNPU 不属于当前版本目标。
- 初期不引入 310P/910B 产品 Profile variant。910B 必须在同一 `npu` runtime profile 下独立通过硬件 E2E；若镜像、挂载、设备、权限或网络契约不兼容，则暂缓 910B 发布，不以 variant 绕过该门槛。
- `model_name` 仅用于规范化 product label 和格式校验；通用指标按同一 `vdie_id` 的完整指标族 fallback 生成。格式、单位或数值校验失败时不输出 `neutree_*`，但保留原始 Exporter 序列。
- `model_name` 不使用产品白名单。规范化仅去除首尾空白，并要求非空、最多 128 bytes、无控制字符，保留大小写和其它可打印字符。未知产品只要同一 `vdie_id` 的参与样本具有一致且有效的规范化值，即可走通用 fallback；缺失、非法或冲突时该设备的全部 `neutree_*` 缺失。
- 910B E2E 的发布验收包括：共享 `npu/ddr/hbm` 配置的安全跳过、HBM/overall utilization 的指标契约和同设备 fallback，以及 socket-free Kubernetes 运行时与 310P 的一致性。
