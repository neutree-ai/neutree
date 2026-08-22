# 加速器监控设计文档索引

## 文档定位

本文是 NVIDIA GPU 与 Ascend NPU 监控设计的统一导航页，不定义新的接口或指标契约。
实现、评审和验证应从这里选择对应的权威文档，不能把历史分析、调研稿或支持矩阵当作
实现输入。

## 推荐阅读顺序

1. [加速器驱动探测与 Feature Discovery 识别逻辑复用方案](./nvml-replacement-and-feature-discovery.md)：先了解跨厂商 Adapter 边界、NVIDIA 当前 legacy 路径和迁移目标。
2. [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md)：再了解 Ascend 产品、Profile、allocation、指标和降级语义。
3. [NPU 指标支持矩阵](./npu-metrics-support-matrix.md)：查看经过真机 E2E 后可以对外声明的能力；“待验证”不是支持承诺。
4. 按需阅读 ADR、版本化证据和专项调研，不从这些材料反向恢复已废弃方案。

## 权威边界

| 问题 | 权威来源 |
|---|---|
| NodeAgent Adapter、Feature Discovery、NVIDIA legacy 迁移、跨厂商所有权 | [加速器驱动探测与 Adapter 迁移方案](./nvml-replacement-and-feature-discovery.md) |
| Ascend 310P/910B 产品能力、NPU Exporter Profile、allocation 和指标语义 | [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md) |
| NPU 驱动版本、型号、`hccs_capable` 静态能力与动态遥测边界 | [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md#npu-专有-info-与-hccs-能力) 与 [NPU 指标支持矩阵](./npu-metrics-support-matrix.md) |
| 面向用户的产品/环境支持状态 | [NPU 指标支持矩阵](./npu-metrics-support-matrix.md) |
| 已接受架构决策及其审计轨迹 | [ADR 0001](./adr/0001-enterprise-owned-accelerator-metrics-adapters.md)、[ADR 0002](./adr/0002-structured-component-volumes.md)、[ADR 0003](./adr/0003-adapter-owned-accelerator-metric-aggregation.md) |
| 310P 实机和上游源码证据 | [NPU 指标支持分析](./npu-metrics-support-analysis.md) 与 [脱敏 fixture](./fixtures/npu-exporter-v26.1.0-310p.prom) |

跨厂商边界与 Ascend 产品语义同时涉及同一流程时，两份权威设计必须同时满足。ADR 是
决策审计记录，不替代完整执行契约。ADR 0002 已于 2026-08-19 同步 runtime 边界；
ADR 0001/0003 已于 2026-08-20 同步当前 Exporter/discovery 和厂商 info 契约，并将
ADR 0003 修订为“一个 Adapter
注册对象 + Kubernetes/Static 两个集群能力接口”，allocation 故障原子性和分配形态的
详细契约以权威设计为准。发现实质冲突时必须先修订或 supersede ADR，
不得用旧前提覆盖权威设计。

## 主设计章节导航

### 通用边界与 NVIDIA 迁移

- [文档状态与当前/目标对照](./nvml-replacement-and-feature-discovery.md#文档状态)
- [所有权边界](./nvml-replacement-and-feature-discovery.md#所有权边界)
- [目标数据流](./nvml-replacement-and-feature-discovery.md#目标数据流)
- [NVIDIA 当前兼容路径](./nvml-replacement-and-feature-discovery.md#nvidia-当前兼容路径)
- [NVIDIA Adapter 目标态](./nvml-replacement-and-feature-discovery.md#nvidia-adapter-目标态)
- [生命周期和故障边界](./nvml-replacement-and-feature-discovery.md#生命周期和故障边界)
- [迁移计划与退出条件](./nvml-replacement-and-feature-discovery.md#迁移计划与退出条件)
- [验证矩阵](./nvml-replacement-and-feature-discovery.md#验证矩阵)

### Ascend NPU 权威设计

- [范围](./ascend-npu-monitoring-design.md#范围)
- [实机调研证据](./ascend-npu-monitoring-design.md#实机调研证据2026-08-12fixture-于-2026-08-19-复核)
- [结论总览](./ascend-npu-monitoring-design.md#结论总览决策表)
- [总体架构与所有权边界](./ascend-npu-monitoring-design.md#总体架构与所有权边界)
- [指标契约](./ascend-npu-monitoring-design.md#指标契约)
- [NPU 专有 info 与 HCCS 边界](./ascend-npu-monitoring-design.md#npu-专有-info-与-hccs-能力)
- [数据模型与 Profile](./ascend-npu-monitoring-design.md#数据模型与-profile)
- [Allocation 数据流](./ascend-npu-monitoring-design.md#allocation-数据流)
- [Adapter 与 Exporter 运行边界](./ascend-npu-monitoring-design.md#adapter-与-exporter-运行边界)
- [当前状态 vs 目标态](./ascend-npu-monitoring-design.md#当前状态-vs-目标态)
- [验证、发布和 Roadmap](./ascend-npu-monitoring-design.md#验证发布和-roadmap)

## 文档目录

### 当前规范

| 文档 | 状态 | 负责内容 |
|---|---|---|
| [加速器驱动探测与 Feature Discovery 识别逻辑复用方案](./nvml-replacement-and-feature-discovery.md) | 目标架构与迁移契约 | 单 Adapter registry、Kubernetes/Static 能力接口和 NVIDIA/NPU 共用边界；NVIDIA NVML/DCGM、allocation、target discovery 和 legacy 退出条件 |
| [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md) | Ascend 唯一权威设计 | NPU driver/DCMI、型号/专有 info、HCCS 边界、Exporter、Profile、310P/910B、HAMi、Kubernetes/Ray allocation、指标与 Roadmap |
| [NPU 指标支持矩阵](./npu-metrics-support-matrix.md) | 对外支持状态账本 | 产品与运行环境的 E2E 结论及固定源码硬排除；只记录验证结果，不定义设计 |

### 架构决策

| 文档 | 状态 | 负责内容 |
|---|---|---|
| [ADR 0001：Enterprise-owned accelerator metrics adapters](./adr/0001-enterprise-owned-accelerator-metrics-adapters.md) | Accepted，2026-08-20 已修订 | OSS/Enterprise Adapter 所有权、显式类型选择、通用/厂商 info 契约和 driver/exporter 降级边界 |
| [ADR 0002：Structured component volumes](./adr/0002-structured-component-volumes.md) | Accepted，2026-08-19 已修订 | 跨 Kubernetes/Docker 的结构化 volume、runtime mount、command 和 privilege |
| [ADR 0003：Adapter-owned metric aggregation](./adr/0003-adapter-owned-accelerator-metric-aggregation.md) | Accepted，2026-08-20 已修订 | 一个基础 Adapter 注册对象、两个集群能力接口、强类型 evidence、厂商聚合和显式白名单输出；详细 allocation 服从权威设计 |

### 证据与专项调研

| 文档或资产 | 状态 | 用途 |
|---|---|---|
| [NPU 指标支持分析](./npu-metrics-support-analysis.md) | 历史、非规范 | 固定版本的 310P 实机与源码证据 |
| [NPU Exporter v26.1.0 310P fixture](./fixtures/npu-exporter-v26.1.0-310p.prom) | 脱敏测试证据 | Parser、label 和单位转换的 golden 输入 |
| [HAMi Ascend NPU 虚拟化调研](./hami-ascend-npu-virtualization-research.md) | 专项调研、非规范 | 整卡、template 和 hami-core 行为背景；发布语义以权威设计为准 |

### 草稿与已替代文档

| 文档 | 状态 | 使用限制 |
|---|---|---|
| [NPU 监控详细设计](./npu-monitoring-design.md) | 已被 Ascend 权威设计替代 | 仅用于追溯旧方案，不作为实现、Profile 或验收输入 |
| [Accelerator Domain Interface Redesign](./accelerator-domain-interface-redesign.md) | Draft for review | 更宽的 accelerator domain 重构候选；未接受前不改变本轮 Adapter 契约 |
| [Ascend 企业版集成设计](./ascend-enterprise-integration-design.md) | 企业集成草稿 | 覆盖 engine、runtime、license 和交付；监控部分服从 Ascend 权威设计 |
| [Ascend 企业版内部扩展设计](./ascend_enterprise_internal_extension_design.md) | 企业集成草稿 | 与上一份存在范围重叠；合并定稿前不得作为第二份监控权威设计 |

## 图示与资产

| 资产 | 内容 |
|---|---|
| [加速器 Adapter 统一目标与 NVIDIA 迁移（SVG）](./images/accelerator-adapter-architecture.svg) / [PNG](./images/accelerator-adapter-architecture.png) | NVIDIA/NPU 目标 Adapter、raw vmagent 旁路、NVIDIA legacy 和退出门 |
| [Ascend NPU 监控流程（SVG）](./images/ascend-npu-monitoring-flow.svg) / [PNG](./images/ascend-npu-monitoring-flow.png) | Ascend driver/Exporter/evidence、Kubernetes/Ray allocation 和降级路径 |

## 维护规则

1. 修改跨厂商 Adapter 边界或 NVIDIA 迁移时，同时更新通用方案及其架构图。
2. 修改 Ascend 产品、Profile、allocation 或指标语义时，同时更新权威设计及 Ascend 流程图。
3. 新的实机观察先进入版本化 evidence/fixture；只有完成设计评审后才能改变权威契约。
4. 支持矩阵只有在固定镜像 digest、驱动、产品和运行环境完成 E2E 后才能改为“支持”。
5. Accepted ADR 与当前设计发生实质冲突时，先修订或 supersede ADR，再进入实现。
6. 历史、调研和草稿文档不得使用“当前权威设计”表述，也不得复制可执行 Profile 形成第二真相。
