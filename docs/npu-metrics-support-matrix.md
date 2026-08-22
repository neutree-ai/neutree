# NPU 指标支持矩阵

> 文档总入口：[加速器监控设计文档索引](./accelerator-monitoring-design-index.md)。

本矩阵是面向用户的版本化支持记录。“支持”只能来自对应产品和运行环境的真机 E2E；
“不支持”来自真机 E2E，或固定版本官方源码对该产品的显式硬排除，并必须记录 Exporter、
驱动或关联语义的实际原因。其它源码或文档候选仍使用“待验证”，不能当作对外支持承诺。

当前设计和数据模型见
[Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md)；版本化实机证据见
[NPU 指标支持分析](./npu-metrics-support-analysis.md)。

## 状态定义

| 状态 | 含义 |
| --- | --- |
| 待验证 | 尚未完成该产品、运行环境和指标组合的真机 E2E。 |
| 支持 | E2E 已验证指标值、单位、标签和关联语义。 |
| 不支持 | E2E 已确认无法安全发布，或固定版本官方源码显式排除该产品，并记录原因。 |
| 非当前范围 | 当前版本明确不实现，例如 vNPU。 |

## 当前版本

发布版本：待定

| 产品 | 运行环境 | 通用 Neutree 指标/能力 | 状态 | E2E 证据或原因 |
| --- | --- | --- | --- | --- |
| Atlas 300I Duo / 310P | 静态 Ray/SSH | 通用物理设备身份、利用率、内存、温度；健康、功耗保留为原始 Exporter 诊断指标 | 待验证 | 仓库中的 v26.1.0 fixture 只固定 parser、标签和单位证据；静态 Docker 的 exporter endpoint、最小只读 mount、readiness 与 Enterprise NodeAgent adapter 仍需在固定 Docker/驱动/镜像 digest 上完成真机 E2E。旧版无 socket probe 只作历史观察，不是当前发布证据。 |
| Atlas 300I Duo / 310P | Kubernetes | 通用物理设备身份、利用率、内存、温度；健康、功耗保留为原始 Exporter 诊断指标 | 待验证 | v26.1.0 目标 Profile 已声明 driver、DCMI、sysfs 和 containerd runtime mount；仍需在固定驱动和镜像 digest 上完成 managed Exporter、readiness、Pod-IP 抓取及 Enterprise NodeAgent adapter 真机 E2E。 |
| Atlas 300I Duo / 310P | 所有 | `neutree_node_accelerator_npu_info`：`product=Ascend310P`、驱动版本、`hccs_capable="0"` | 待验证 | fixture 已有 `node_base_info.driverVersion`、`npu_chip_info_name.name` 和 310P-only `product_type` 交叉校验；目标值仍必须来自 Adapter 驱动探测，descriptor 和真机 E2E 尚未完成。 |
| Atlas 300I Duo / 310P | 所有 | HCCS 链路计数和带宽动态指标 | 不支持 | 固定 v26.1.0 `supportedHccsDevices` 只包含 910B/910A3，明确排除 310P；当前 Profile 也关闭 `hccs`，Neutree 不提供对应 descriptor。 |
| Atlas 300I Duo / 310P | 静态 Ray/SSH | 整卡 allocation、`neutree_node_accelerator_allocated/free` | 待验证 | 2026-07-23 已通过运行中 Ray Serve 的 `2+1+1` Actor PID -> 后代 NPU 进程 -> `vdie_id` 关联验证；待 Enterprise Node Agent adapter 真机 E2E。可见设备环境变量会暴露全部四卡，不能作为分配来源。 |
| Atlas 300I Duo / 310P | Kubernetes | 整卡 allocation、`neutree_node_accelerator_allocated/free` | 待验证 | 整卡 Device Plugin ref 必须由企业 Adapter 解析为 logic ID，经 DCMI 转换并唯一命中本周期驱动快照 UUID；HAMi 路径使用对应 Pod annotation 的 vdie UUID。任一记录缺失、重复或歧义时，本周期整个 Kubernetes allocation 路径不输出。 |
| Atlas 300I Duo / 310P | 静态 Ray/SSH | 整卡独占副本的 allocation、allocated/used memory、利用率；非切分共享卡副本的 fraction allocation、allocated/process memory | 待验证 | 2026-07-23 已验证 Dashboard `required_resources` 的 `2+1+1` NPU 资源及 Actor/PID -> `process_info`/`vdie_id` 关联。整卡/die 唯一归属时用 die total 作为 `memory_allocated_bytes` 并发布物理 utilization；非切分共享卡使用 `MemoryMiB=round(device.MemoryMiB × gpuQuantity)`、`CoreUnits=round(100 × gpuQuantity)`，前者继续生成 `_memory_allocated_bytes`，后者进入 allocation/resource view。共享 Actor 不复制整卡 total/100 core units，也不发布物理 utilization。待 Adapter E2E 验证单位、唯一性和重启缺失语义。 |
| Atlas 300I Duo / 310P | Kubernetes | 通用副本级 allocation、内存、利用率 | 待验证 | 整卡/die 唯一归属时，`memory_allocated_bytes` 取 die total，`memory_used_bytes` 取 `process_info` 按 Pod 聚合，utilization 取容器级证据；软切分共享/template 只发布逐 Pod HAMi annotation 配额，used/util 缺失。 |
| Atlas 300I Duo / 310P | 所有 | vNPU inventory、实际 usage、dashboard | 非当前范围 | 当前版本不发布 vNPU inventory 或实际 used/util；Kubernetes 软切分/template 仅保留经 annotation 验证的 allocation quota。Profile 关闭 `vnpu` collector，原始 `vnpu_*` 不作为通用契约。 |
| Ascend 910B | 静态 Ray/SSH | 物理设备、整卡 allocation、通用副本级指标 | 待验证 | 先完成 910B capability matrix，再执行真机 E2E。 |
| Ascend 910B | Kubernetes | 物理设备、整卡 allocation、通用副本级指标 | 待验证 | 先完成 managed Exporter 与 capability matrix 真机 E2E。 |
| Ascend 910B | 所有 | `neutree_node_accelerator_npu_info`：`product=Ascend910B`、驱动版本、`hccs_capable="1"` | 待验证 | 固定源码只证明驱动版本 API、芯片 identity 和 HCCS collector 产品门禁存在；Adapter descriptor、固定镜像 fixture 和 910B 真机 E2E 尚未完成。`product_type` 上游明确不支持，不能作为型号来源。 |
| Ascend 910B | 所有 | HCCS 链路计数和带宽动态指标 | 非当前范围 | 上游 collector 是候选，但当前 Profile `hccs=OFF`，Neutree 无对应 descriptor、固定 digest fixture 和 910B E2E。`hccs_capable="1"` 不代表已发布动态遥测。 |

## 更新规则

1. 每一行至少记录产品、运行环境、Node Agent/Exporter 版本、测试时间和 E2E 用例或
   可复现命令。
2. “支持”需要验证数值、单位、标签、缺失语义以及 CPU-only 节点兼容性；副本级指标
   还需要验证整卡独占和唯一关联。
3. “不支持”需要说明是硬件产品能力、Exporter collector、驱动/DCMI、部署 runtime，
   还是无法证明的 allocation/container 语义造成；若依据官方源码硬排除，必须固定 tag
   和 commit，并在升级 Exporter 时重新核对。
4. 产品未暴露或语义无效的可选数值指标不生成零值或 unknown 时序；它们保持缺失。
5. 每个“支持”结论必须固定 Exporter image digest；同名 tag 或新 digest 都需要重新
   执行对应硬件 E2E，不能继承原有结论。
