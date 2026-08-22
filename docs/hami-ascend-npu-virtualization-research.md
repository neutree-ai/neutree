# HAMi Ascend NPU 虚拟化调研

更新时间：2026-07-14

## 结论摘要

HAMi 对 Ascend NPU 的支持已经覆盖设备发现、调度、整卡分配、vNPU template 硬切分、`hami-vnpu-core` 软切分和部分监控能力。当前可按两个成熟度层级理解：

| 能力 | 当前阶段 | 集成建议 |
| --- | --- | --- |
| 整卡分配 | 相对成熟 | 可作为基础能力接入 |
| vNPU template mode | 默认主路径，基本可用 | 可做受控生产试点，接受固定模板粒度 |
| `hami-vnpu-core` mode | 技术预览 / Beta 集成 | 仅建议小规模 PoC 或受控试点 |
| Ascend device-plugin 内置监控 | 主要服务 `hami-vnpu-core` | 用于软切分 workload 指标 |
| Ascend `npu-exporter` | 物理 NPU 监控成熟度更高 | 可作为不虚拟化或 310P vNPU 的基础指标源 |
| HAMi template 语义监控 | 不完整 | 需要 join HAMi annotation 或 scheduler 分配数据 |

首版集成建议：

1. 默认支持整卡和 vNPU template mode。
2. 将 `hami-vnpu-core` 标记为实验能力或受控开关。
3. 监控侧优先接入 Ascend `npu-exporter` 的物理 NPU 指标；软切分场景再补 HAMi Ascend device-plugin 的 `:9395/metrics`。
4. 如果需要展示 “Pod 使用哪个 HAMi template”，需要从 HAMi Pod annotations 或 scheduler 分配信息补充 metadata，不能只依赖 `npu-exporter`。

## 组件关系

相关组件：

- HAMi scheduler：调度扩展器，负责根据节点设备信息和 Pod 资源请求选择节点、物理设备 UUID 和切分规格。
- `Project-HAMi/ascend-device-plugin`：负责 Ascend 设备发现、向 kubelet 注册扩展资源、向 HAMi scheduler 上报设备 annotation，并在 kubelet `Allocate` 阶段注入运行时环境。
- `Project-HAMi/hami-vnpu-core`：软切分运行时组件，基于 `libvnpu.so` 拦截和共享内存/令牌调度做资源限制。
- Ascend `npu-exporter`：华为 Ascend 侧 Prometheus exporter，主要导出物理 NPU、容器和 310P vNPU 指标。

HAMi Ascend device-plugin README 明确列出两种模式：

- 基于模板的硬切分：vNPU template mode。
- 基于运行时拦截的软切分：`hami-vnpu-core` mode。

参考：

- <https://github.com/Project-HAMi/ascend-device-plugin>
- <https://github.com/Project-HAMi/HAMi>
- <https://github.com/Project-HAMi/hami-vnpu-core>
- <https://github.com/Ascend/mind-cluster/tree/master/component/npu-exporter>

## 功能列表

### 整卡分配

Pod 只申请 `resourceName`，不申请 memory 时，按整卡分配。例如：

```yaml
resources:
  limits:
    huawei.com/Ascend910B: "1"
```

该路径不依赖 `hami-vnpu-core`，也不需要 template 对齐。

### vNPU template 硬切分

Pod 申请 `resourceName + resourceMemoryName`，不设置 `huawei.com/vnpu-mode: hami-core` 时，走默认 template mode。

```yaml
resources:
  limits:
    huawei.com/Ascend310P: "1"
    huawei.com/Ascend310P-memory: "4096"
```

行为特征：

- 申请显存会向上匹配最小可用 template。
- 不是任意显存精度切分。
- device-plugin 在 `Allocate` 阶段注入 `ASCEND_VISIBLE_DEVICES` 和 `ASCEND_VNPU_SPECS`。
- 传统 template vNPU/整卡模式不会启动 Ascend device-plugin 内置的 `hami-vnpu-core` 监控 exporter。

### `hami-vnpu-core` 软切分

Pod 显式设置：

```yaml
metadata:
  annotations:
    huawei.com/vnpu-mode: hami-core
```

并申请 memory/core：

```yaml
resources:
  limits:
    huawei.com/Ascend910B3: "1"
    huawei.com/Ascend910B3-memory: "28672"
    huawei.com/Ascend910B3-core: "40"
```

行为特征：

- 基于 `libvnpu.so` 拦截 NPU RTS API。
- 基于共享内存和 limiter 做 memory/core 控制。
- 当前 README 标注仅支持 ARM 平台。
- 当前仅支持 HAMi scheduler。
- Ascend 驱动要求 `>= 25.5`。
- 节点需要启用 `device-share`，当前 device-plugin 会在节点级自动执行 `npu-smi set -t device-share ...`。

当前成熟度判断为技术预览 / Beta 集成，原因是语义和兼容性仍在收敛：

- `hami-vnpu-core` 仓库创建于 2026-02，当前没有 release。
- HAMi v2.10 Roadmap 仍包含 `Heterogeneous Ascend Mode(vNPU and HAMi-core) Support`、`HAMi-core mode for vnpu doesn't need to align device memory to template` 等待办项。
- 软切分是否必须申请 `*-core`、未标注 Pod 如何跟随节点模式、ServiceMonitor 标签保留等仍有 open issue/PR。

相关 issue/PR 状态已于 2026-07-14 复核：

| 项目 | 状态 | 链接 |
| --- | --- | --- |
| 要求 soft slicing 显式申请 core | open | <https://github.com/Project-HAMi/HAMi/pull/2024> |
| template 与 hami-core 混部调度兼容 | open | <https://github.com/Project-HAMi/HAMi/pull/2035> |
| annotation-less Pod 跟随节点模式 | open | <https://github.com/Project-HAMi/ascend-device-plugin/pull/106> |
| soft slicing 资源语义文档澄清 | open | <https://github.com/Project-HAMi/ascend-device-plugin/pull/102> |
| vnpu-monitor 加入 Helm chart | merged | <https://github.com/Project-HAMi/ascend-device-plugin/pull/108> |

## 支持硬件范围

HAMi chart 主线 Ascend 配置中已经包含多种 Ascend vNPU 配置：

| commonWord | resourceName | template mode | `resourceCoreName` |
| --- | --- | --- | --- |
| `Ascend910A` | `huawei.com/Ascend910A` | 支持 | chart 中有 |
| `Ascend910B2` | `huawei.com/Ascend910B2` | 支持 | chart 中有 |
| `Ascend910B3` | `huawei.com/Ascend910B3` | 支持 | chart 中有 |
| `Ascend910B4-1` | `huawei.com/Ascend910B4-1` | 支持 | chart 中有 |
| `Ascend910B4` | `huawei.com/Ascend910B4` | 支持 | chart 中有 |
| `Ascend310P` | `huawei.com/Ascend310P` | 支持 | chart 中有 |
| `Ascend910C` | `huawei.com/Ascend910C` | 支持 | chart 中有 |

参考：

- <https://github.com/Project-HAMi/HAMi/blob/master/charts/hami/templates/scheduler/device-configmap.yaml>

需要注意 standalone `ascend-device-plugin` 配置和 HAMi chart 配置存在差异：`ascend-device-plugin/main/ascend-device-configmap.yaml` 中 310P 段落能看到 `resourceName` 和 `resourceMemoryName`，而 HAMi chart 中还包含 `resourceCoreName: huawei.com/Ascend310P-core`。如果要使用 `hami-core`，应优先核对实际部署的 ConfigMap 是否包含对应 `*-core` 资源。

### 310P 软切分状态

社区 issue 和 PR 已经出现 310P / Atlas 300I DOU 的 `hami-core` 验证和问题反馈，因此不能判断为完全不支持。

证据：

- `ascend-device-plugin#101` 直接以 `Ascend310P` 讨论 soft slicing memory/core 语义：<https://github.com/Project-HAMi/ascend-device-plugin/issues/101>
- `HAMi#2027` 讨论 `huawei.com/Ascend310P-core` 资源表达：<https://github.com/Project-HAMi/HAMi/issues/2027>
- `ascend-device-plugin#107` 描述在 Ascend 310P aarch64 集群做过 hami-core workload 和多卡 Pod 验证：<https://github.com/Project-HAMi/ascend-device-plugin/pull/107>
- `ascend-device-plugin#64` 是 Atlas 300I DOU 软切分运行时报错反馈，涉及 `NPU_GLOBAL_SHM_PATH` 等运行时依赖：<https://github.com/Project-HAMi/ascend-device-plugin/issues/64>

判断：

- 310P template mode 可以作为默认可用能力验证。
- 310P `hami-core` 有社区验证和配置路径，但仍建议按 PoC/试点处理。
- 对 910B/910C 的 `hami-core` 和 exporter vNPU 指标，不能直接套用 310P 结论，需要实机验证。

## vNPU template mode 与 `hami-core` 差异

| 维度 | vNPU template mode | `hami-vnpu-core` mode |
| --- | --- | --- |
| 默认路径 | 是，不加 `vnpu-mode` 即走该路径 | 否，需要显式 annotation |
| 切分粒度 | 固定模板 | memory/core 更细粒度 |
| 运行时依赖 | Ascend vNPU template 和 `ASCEND_VNPU_SPECS` | `libvnpu.so`、共享内存、device-share、`npu-smi` |
| 调度资源 | `resourceName` + `resourceMemoryName` | `resourceName` + `resourceMemoryName` + `resourceCoreName` |
| 多卡部分切分 | template mode 对多设备部分显存有限制 | 支持同 Pod 多虚拟设备，但仍需验证框架行为 |
| 平台限制 | 依赖 Ascend template 支持 | README 标注当前仅 ARM |
| 监控 | 不启动 HAMi 内置 soft-slice exporter | 启动 `:9395/metrics` |
| 成熟度 | 默认主路径，较成熟 | 技术预览 / Beta |

## HAMi scheduler 与硬切分协同

template mode 的关键点是：scheduler 负责“选哪张卡、切多大规格”，device-plugin 负责“按 scheduler 写入的结果注入容器运行时环境”。scheduler 不直接创建 vNPU。

流程：

1. Ascend device-plugin 发现设备。
2. device-plugin 向 kubelet 注册扩展资源，例如 `huawei.com/Ascend310P`。
3. device-plugin 向 Node annotations 写入 HAMi 设备信息：
   - `hami.io/node-register-<CommonWord>`
   - `hami.io/node-handshake-<CommonWord>`
   - `hami-vnpu-core`
4. HAMi scheduler Filter 阶段读取 Node annotations 和 Pod 资源请求。
5. scheduler 根据 template、显存、AI Core、设备 count、健康状态、拓扑等做 Fit。
6. scheduler 选出节点和物理设备 UUID，并 patch Pod annotations：
   - `hami.io/vgpu-node`
   - `hami.io/<CommonWord>-devices-to-allocate`
   - `hami.io/<CommonWord>-devices-allocated`
   - `huawei.com/<CommonWord>`，其中硬切分包含 template 名称。
7. Bind 阶段设置：
   - `hami.io/bind-phase=allocating`
   - `hami.io/bind-time`
   - Node lock：`hami.io/mutex.lock`
8. kubelet 调用 Ascend device-plugin `Allocate`。
9. device-plugin 读取 scheduler 写入的 annotations，注入：
   - `ASCEND_VISIBLE_DEVICES`
   - `ASCEND_VNPU_SPECS`
10. device-plugin 消费 `devices-to-allocate`，全部完成后设置 `hami.io/bind-phase=success` 并释放 Node lock。

相关源码：

- HAMi Ascend device：<https://github.com/Project-HAMi/HAMi/blob/master/pkg/device/ascend/device.go>
- HAMi Ascend vNPU config：<https://github.com/Project-HAMi/HAMi/blob/master/pkg/device/ascend/vnpu.go>
- HAMi scheduler Filter/Bind：<https://github.com/Project-HAMi/HAMi/blob/master/pkg/scheduler/scheduler.go>
- HAMi scheduler scoring：<https://github.com/Project-HAMi/HAMi/blob/master/pkg/scheduler/score.go>
- Ascend device-plugin register：<https://github.com/Project-HAMi/ascend-device-plugin/blob/main/internal/server/register.go>
- Ascend device-plugin Allocate：<https://github.com/Project-HAMi/ascend-device-plugin/blob/main/internal/server/allocate.go>

## 集成方式

### HAMi chart 集成

推荐路径是在部署 HAMi 时启用 Ascend：

```yaml
devices:
  ascend:
    enabled: true
    runtimeClassName: ascend
    hamiVnpuCore: false
```

说明：

- `hamiVnpuCore: false`：默认 template mode。
- `runtimeClassName` 非空时，HAMi admission 可为申请 Ascend 资源的 Pod 注入 RuntimeClass。
- 如果启用 `hamiVnpuCore: true`，所有节点默认声明软切分能力；也可以通过 `hami-device-node-config` 做节点级覆盖。

### standalone ascend-device-plugin 集成

可单独部署：

```bash
kubectl label node <ascend-node> ascend=on
kubectl apply -f https://raw.githubusercontent.com/Project-HAMi/ascend-device-plugin/main/ascend-runtimeclass.yaml
kubectl apply -f https://raw.githubusercontent.com/Project-HAMi/ascend-device-plugin/main/ascend-device-configmap.yaml
kubectl apply -f https://raw.githubusercontent.com/Project-HAMi/ascend-device-plugin/main/ascend-device-plugin.yaml
```

该方式需要特别核对：

- ConfigMap 中的 `resourceName`、`resourceMemoryName`、`resourceCoreName` 是否与 HAMi scheduler 配置一致。
- `hamiVnpuCore` 全局开关与节点级覆盖是否符合预期。
- `npu-smi` 路径是否在插件预期路径中，或已通过 hostPath 挂载。

### Pod 示例

整卡：

```yaml
resources:
  limits:
    huawei.com/Ascend910B3: "1"
```

template mode：

```yaml
resources:
  limits:
    huawei.com/Ascend310P: "1"
    huawei.com/Ascend310P-memory: "4096"
```

`hami-core` mode：

```yaml
metadata:
  annotations:
    huawei.com/vnpu-mode: hami-core
spec:
  containers:
    - resources:
        limits:
          huawei.com/Ascend310P: "1"
          huawei.com/Ascend310P-memory: "4096"
          huawei.com/Ascend310P-core: "50"
```

## 监控能力

### HAMi Ascend device-plugin 内置 exporter

`hami-vnpu-core` 节点会启动内置 exporter：

```text
:9395/metrics
```

指标包括：

- `hami_host_gpu_memory_used_bytes`
- `hami_host_gpu_utilization_ratio`
- `hami_vgpu_memory_used_bytes`
- `hami_vgpu_memory_limit_bytes`
- `hami_container_device_utilization_ratio`

限制：

- 主要服务 `hami-vnpu-core` 软切分。
- README 明确说明传统 template vNPU/整卡模式不会启动该 exporter。
- `ascend-device-plugin#107` 仍在修复 ServiceMonitor 标签保留问题，生产接入时需要验证 workload namespace/pod label 是否正确。

### Ascend `npu-exporter`

Ascend `npu-exporter` 是独立于 HAMi 的物理 NPU exporter。它不做虚拟化时即可支持基础监控：

- NPU 数量、型号、健康状态、错误码。
- 温度、功耗、电压。
- AI Core / Vector / Cube / Overall 利用率。
- DDR / HBM 总量、已用量、ECC。
- PCIe / HCCS / RoCE / Network / Optical 等链路指标。
- 容器维度 NPU 利用率和内存，前提是 container runtime socket 和环境解析可用。

`npu-exporter` 源码中 `vnpu` metrics group 默认开启，且 `VnpuCollector` 注册：

- `vnpu_pod_aicore_utilization`
- `vnpu_pod_total_memory`
- `vnpu_pod_used_memory`

但该 vNPU collector 源码只对 `Ascend310P` 返回 supported；测试也显示 `Ascend910`、`Ascend910B`、`Ascend910A3`、`Ascend910A5` 为 false。因此：

- 310P 底层 vNPU 指标可以使用 `npu-exporter`。
- 910B/910C 的 HAMi template vNPU 不能默认认为 `npu-exporter` 可展示 vNPU 层级。
- `npu-exporter` 不解析 HAMi annotation、`ASCEND_VNPU_SPECS` 或 template 名称。

如果需要展示：

```text
Pod -> Ascend310P -> vir02 -> used/total memory
```

需要把 `npu-exporter` 的 `v_dev_id`、pod/container labels 与 HAMi scheduler/device-plugin annotations 做 join。

已有 Neutree 侧指标适配分析见：

- [npu-metrics-support-analysis.md](./npu-metrics-support-analysis.md)

## 稳定性和成熟度判断

| 维度 | 判断 |
| --- | --- |
| HAMi 主仓 release | v2.9.0 是当前最新 release，发布于 2026-05-19 |
| ascend-device-plugin release | GitHub releases 当前为空 |
| hami-vnpu-core release | GitHub releases 当前为空 |
| template mode | 默认路径，配置和调度链路较清晰 |
| hami-core mode | 代码和文档快速变化，核心语义仍有 open PR |
| 监控 | soft-slice 内置监控可用但仍有标签修复；`npu-exporter` 更适合作为物理 NPU 基础指标源 |
| WebUI | HAMi Roadmap 中 Ascend WebUI 支持仍为未完成项 |

## 对 Neutree 的建议

### 首期支持范围

建议首期定义为：

- Kubernetes Ascend 集群。
- 整卡和 template mode。
- Ascend `npu-exporter` 物理 NPU 指标。
- Pod 资源请求和 HAMi annotations 作为 allocation metadata 的补充来源。

暂不把以下能力作为稳定承诺：

- `hami-vnpu-core` 大规模生产。
- 910B/910C vNPU exporter 层级指标。
- HAMi template 名称的自动展示，除非实现 annotation join。
- WebUI 中完整展示 vNPU topology/template 层级。

### 推荐实现边界

Neutree 侧不应把 Ascend `npu-exporter` 当成 DCGM drop-in replacement。建议按已有 [npu-metrics-support-analysis.md](./npu-metrics-support-analysis.md) 的方向引入 Ascend/NPU adapter：

- 物理设备 identity：优先使用 `vdie_id`，device index 使用 `id`。
- memory：HBM used/total 优先，DDR fallback。
- utilization：overall utilization 优先，AI Core utilization fallback。
- endpoint allocation：Kubernetes 优先 kubelet pod-resources；必要时 fallback 到 HAMi annotations。
- vNPU/template 展示：需要额外 join HAMi metadata。

### 验证清单

引入 HAMi Ascend 集成前，应至少验证：

1. Node 上是否出现 `hami.io/node-register-<CommonWord>`。
2. kubelet 是否注册 `huawei.com/Ascend*` 扩展资源。
3. template mode Pod 是否注入 `ASCEND_VISIBLE_DEVICES` 和 `ASCEND_VNPU_SPECS`。
4. `hami-core` Pod 是否同时申请 memory/core，且 `npu-smi`、`device-share`、共享内存路径可用。
5. `hami.io/<CommonWord>-devices-allocated` 是否能稳定解析到 Pod/container/device。
6. Ascend `npu-exporter` 是否能导出物理指标。
7. 310P vNPU 场景下 `vnpu_pod_*` 是否能关联到 namespace/pod/container。
8. Prometheus/Grafana 侧是否保留真实 workload labels。

## 风险

- `hami-core` 仍在快速演进，open PR 可能改变 admission、调度和 device-plugin allocation 行为。
- standalone `ascend-device-plugin` ConfigMap 与 HAMi chart 内置 ConfigMap 可能不一致。
- template mode 是固定模板对齐，不是任意显存/算力精度；用户请求和实际可用资源可能不同。
- `npu-exporter` 的 vNPU metrics 当前源码只支持 310P，不能覆盖所有 HAMi Ascend template 设备。
- 监控指标能表达“使用量”，但不天然表达“调度分配量”和 “HAMi template 名称”。
- 软切分依赖 `libvnpu.so`、`npu-smi`、driver/CANN、共享内存和模型框架 hook，故障面明显大于 template mode。

## 参考资料

- HAMi：<https://github.com/Project-HAMi/HAMi>
- HAMi releases：<https://github.com/Project-HAMi/HAMi/releases>
- HAMi v2.10 Roadmap：<https://github.com/Project-HAMi/HAMi/issues/1889>
- Ascend device-plugin：<https://github.com/Project-HAMi/ascend-device-plugin>
- Ascend device-plugin README：<https://github.com/Project-HAMi/ascend-device-plugin/blob/main/README_cn.md>
- Ascend device ConfigMap：<https://github.com/Project-HAMi/ascend-device-plugin/blob/main/ascend-device-configmap.yaml>
- hami-vnpu-core：<https://github.com/Project-HAMi/hami-vnpu-core>
- Ascend npu-exporter：<https://github.com/Ascend/mind-cluster/tree/master/component/npu-exporter>
- Ascend npu-exporter metrics config：<https://github.com/Ascend/mind-cluster/blob/master/component/npu-exporter/build/metricConfiguration.json>
- Ascend npu-exporter vNPU collector：<https://github.com/Ascend/mind-cluster/blob/master/component/npu-exporter/collector/metrics/collector_for_vnpu.go>
- HAMi Ascend scheduler implementation：<https://github.com/Project-HAMi/HAMi/blob/master/pkg/device/ascend/device.go>
- HAMi scheduler Filter/Bind：<https://github.com/Project-HAMi/HAMi/blob/master/pkg/scheduler/scheduler.go>
