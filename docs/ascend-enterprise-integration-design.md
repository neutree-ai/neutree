# Ascend 企业版集成设计

> **文档状态：企业集成草稿，非监控权威。** 本文讨论 engine、runtime、license 与交付集成；
> 监控接口、Profile、allocation 和指标语义以
> [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md) 为准，跨厂商 Adapter 与
> NVIDIA 迁移以
> [加速器驱动探测与 Adapter 迁移方案](./nvml-replacement-and-feature-discovery.md) 为准。
> 文档总入口见 [加速器监控设计文档索引](./accelerator-monitoring-design-index.md)。本稿与
> `ascend_enterprise_internal_extension_design.md` 合并定稿前，不作为企业集成执行契约。

## 概述

本设计定义 Ascend NPU 以企业版能力接入 Neutree 的总体方向。社区版只提供通用 accelerator 扩展能力；Ascend 的设备适配、运行时、镜像和 engine 资产由企业版交付和维护。

## 现状

当前 Ascend plugin 以独立组件交付。集成 Ascend 能力时，需要单独上传离线物料并部署该组件，安装和运维流程较为复杂。

## 用户故事

- 作为 SMTX 企业版管理员，我希望 control plane 自动提供 Ascend 能力，不需要额外安装插件或上传离线镜像包。
- 作为静态集群管理员，我希望 Neutree 能识别 Ascend 产品并选择匹配的运行时。
- 作为 Kubernetes 管理员，我希望 Ascend 资源在 Neutree 中以统一 accelerator 语义展示和使用。
- 作为推理用户，我希望选择 `npu`、accelerator 和已导入的 vLLM-Ascend version 即可创建 Endpoint。

## 设计目标

- 让 SMTX 企业版具备原生的 Ascend NPU 集成能力，不依赖独立的 accelerator plugin service。
- 让静态 Ray/SSH 与 Kubernetes 两种集群形态都能使用整卡 Ascend NPU。
- 保持社区版对厂商实现无感，避免 Ascend 专属代码、常量和物料进入开源主线。
- 复用现有 engine import、资源模型和 license 能力，减少新增概念与兼容成本。

## 基础设计

整体设计由社区版提供通用能力、企业版提供 Ascend 适配两部分组成：

```text
社区版 accelerator 扩展能力
        |
        v
企业版 Ascend plugin 与运行时资产
        |
        +--> 静态 Ray/SSH 集群
        +--> Kubernetes 集群
        |
        v
vLLM-Ascend engine package 导入与 Endpoint 使用
```

- 社区版提供通用 accelerator 扩展接口、集群资源模型和 engine import 能力，不感知 Ascend 厂商实现。
- 企业版 control plane 在 SMTX 场景组合 Ascend plugin，将 Ascend 适配能力纳入既有的集群和 Endpoint 流程。
- 静态 Ray/SSH 与 Kubernetes 分别沿用既有资源和部署路径；企业版为两者提供 Ascend 所需的产品、运行时和交付物。
- vLLM-Ascend 作为企业版外置 package 发布和导入，避免将特定 engine 固化到 control plane。
- License 继续复用现有 accelerator 配额模型，使物理 GPU 和 NPU 以一致的资源单位纳入管理。

## 详细设计

### vLLM-Ascend Engine 集成

#### Engine 版本隔离

早期方案在同一个 engine version 内，按 accelerator key 选择目标镜像。例如，`nvidia_gpu` 选择 `vllm/vllm-openai:v0.24.0`，`amd_gpu` 选择 `vllm/vllm-openai-rocm:v0.24.0`。该方式无法清楚表达 CUDA、ROCm 或不同 NPU 设备所需的独立镜像，且用户无法从 engine version 直接识别运行时。

新方案将厂商和运行时变体纳入 engine version。每个 engine version 对应确定的目标镜像和运行时；`accelerator.type` 只负责资源调度，不再用于在同一 engine version 内选择厂商镜像。用户创建 Endpoint 时直接选择可见的 engine version。

版本命名规则为 `<baseline-version>-<accelerator-default-key>[-<hardware>]`。没有硬件专属变体时省略 `-<hardware>`；预发布 baseline 使用标准连字符形式，例如镜像 tag 的 `v0.22.1rc1` 对应 engine version 的 `v0.22.1-rc1`。

| 上游镜像 | Neutree 镜像 | Engine version |
| --- | --- | --- |
| `vllm/vllm-openai-rocm:v0.24.0` | `neutree/engine-vllm-openai-rocm:v0.24.0` | `v0.24.0-rocm` |
| `vllm-ascend:v0.22.1rc1-a3` | `neutree/engine-vllm-ascend:v0.22.1rc1-a3` | `v0.22.1-rc1-ascend-a3` |

`nvidia_gpu` 默认不带后缀。同一个 vLLM baseline 的所有 engine variant 共用 engine schema 和静态集群 wrapper；variant 只定义目标镜像、运行时和 Kubernetes template 等加速器差异，避免重复维护相同 baseline 的用户参数和静态集群启动逻辑。

#### Engine 版本导入

`nvidia_gpu` 保持为默认内置集成 engine，Ascend 通过独立 engine package 导入新增。

#### 引擎部署集成

##### Kubernetes 集群

vLLM-Ascend deployment template 与其他加速器 template 保持同一结构和参数约定，并增加以下处理。

Ascend Docker runtime 安装后默认不会成为节点的 default runtime，因此 template 必须显式设置：

```yaml
spec:
  runtimeClassName: ascend
```

SKS 和安装 NPU Operator 的集群自动创建该 RuntimeClass。其他 Kubernetes 环境由用户在部署 Ascend Endpoint 前创建 `ascend` RuntimeClass；用户指南提供以下 YAML，并要求 `handler` 与节点已安装的 Ascend runtime handler 一致：

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: ascend
handler: ascend
```

缺少该 RuntimeClass 时，Endpoint 部署将失败。

template 预留通用 annotations 渲染配置，并合并到 Endpoint Pod metadata。后续 vNPU core 支持通过该配置注入所需 annotation。

##### 静态 Ray/SSH 集群

vLLM-Ascend 静态集群的运行方式和已有的 Ray wrapper 保持一致，并增加以下处理。

`torch_npu` 运行时强依赖 NPU 驱动。以 `cluster-image-builder/serve/vllm/<baseline>/app.py` 为边界，模块顶层、`app_builder` 和 `Controller` 只处理部署编排与请求转发，不能 import、初始化或间接强制依赖 vLLM 与 `torch_npu`；否则控制面或非 NPU 环境会在启动时触发驱动依赖错误。

Ray 场景下，`ASCEND_RT_VISIBLE_DEVICES` 的顺序必须保持升序。顺序不稳定会导致 CANN 无法正确识别已分配的卡。

### 通用 Accelerator 集成

#### 公开接口与注入

开源版在 `pkg/accelerator` 提供厂商无关的公开扩展点。企业版实现该接口并在进程内注入。

接口由 `Plugin` 标识资源类型：

```go
type Plugin interface {
    Resource() string
    Type() string
    Handle() PluginHandle
}
```

`app.Builder` 提供插件注入入口：

```go
func (b *Builder) WithAcceleratorPlugins(plugins ...accelerator.Plugin) *Builder
```

企业版在组装 control plane 时按 vendor gate 注入 Ascend plugin：

```go
builder := app.NewBuilder().WithConfig(c)
if vendor.IsSMTX(vendor.Vendor) {
    builder.WithAcceleratorPlugins(ascend.NewPlugin())
}
```

仅当 `vendor.IsSMTX(...)` 为真时注册 Ascend plugin；非 SMTX 部署不暴露 NPU 插件，也不支持 NPU 能力。

### Ascend Plugin 集群适配

企业版集成 `ascend.Plugin`，并以 `npu` 作为唯一公共 accelerator family。

#### 静态集群探测

静态节点通过 `npu-smi` 探测设备，并记录 `npu` family、device ID 和产品。当前版本仅支持 310P、910B 系列；不在支持系列的产品 fallback 至 CPU 处理。

静态节点探测结果决定 runtime profile。`Cluster.Spec.Version` 只保存纯版本；image suffix 只在运行时镜像解析、升级预拉取和离线包选择时使用。

| 产品 | Runtime profile | Cluster image suffix |
| --- | --- | --- |
| `HUAWEI_Ascend310P3` | `npu-ascend310p` | `-npu-ascend310p` |
| `HUAWEI_Ascend910B*` | `npu-ascend910b` | `-npu-ascend910b` |

#### 静态集群资源转换

Ray 集群启动时，根据 `npu-smi info -m` 的探测结果进行名称规范化，并注册为 Ray custom resource。名称规范化由 Neutree 处理，使加速卡型号名称在 Ray 和 Neutree 中保持一致。

转换规则为：

```text
<Chip Name> => HUAWEI_<Chip Name>
```

例如，`Ascend310P3` 转换为 `HUAWEI_Ascend310P3`。

Neutree 只处理 Neutree Endpoint 到 Ray Resource 的特殊转换：

| Neutree Endpoint 资源 | 生成的 Ray Resource |
| --- | --- |
| `type=npu`，`product=HUAWEI_Ascend310P`，`gpu=<count>` | `NPU=<count>`，`HUAWEI_Ascend310P=<count>` |
| `type=npu`，`product=HUAWEI_Ascend910B2`，`gpu=<count>` | `NPU=<count>`，`HUAWEI_Ascend910B2=<count>` |

#### Kubernetes 资源转换

Kubernetes 资源名称不由 Neutree 控制，因此采用兼容方式处理。`huawei.com/Ascend910*` 和 `huawei.com/Ascend310P*` 都认为是支持的产品型号。

Neutree 与 Kubernetes 固定遵循同一双向规则：

```text
huawei.com/<chip name> <=> HUAWEI_<chip name>
```

Endpoint 按该规则生成 Pod request/limit；Kubernetes 资源发现按相反方向生成 Neutree product。转换不修改 `<chip name>`，因此通用 `Ascend910` 和 HAMi 暴露的具体型号均可保留。下表的 `<gpu>` 等于 Endpoint 的 `resources.gpu` 字段。

| Kubernetes resource | Neutree product | Endpoint 生成的 request/limit |
| --- | --- | --- |
| `huawei.com/Ascend310P` | `HUAWEI_Ascend310P` | `huawei.com/Ascend310P=<gpu>` |
| `huawei.com/Ascend910` | `HUAWEI_Ascend910` | `huawei.com/Ascend910=<gpu>` |
| `huawei.com/Ascend910B2` | `HUAWEI_Ascend910B2` | `huawei.com/Ascend910B2=<gpu>` |

### License 计数

License 计数复用现有 GPU license resource type 作为 accelerator quota bucket，计算配额时只汇总 `AcceleratorGroups[*].Quantity` 的物理卡数。

## 设计确认项

1. Ascend 以企业版能力交付；社区版只提供通用 accelerator 扩展接口。
2. `npu` 是唯一公共 accelerator family；310P、910B 作为产品和运行时差异处理。
3. 当前版本支持静态 Ray/SSH 与 Kubernetes 的整卡 Ascend NPU。
4. vLLM-Ascend 通过独立 engine package 导入；`nvidia_gpu` 保持默认内置集成 engine。
5. License 首期复用现有 GPU license resource type 作为 accelerator quota bucket。

## 输入

- 未入库产品需求附件 `Ascend NPU 支持.docx`：仅作为本草稿的历史输入，待迁移到受版本控制的需求系统；在此之前不能作为可复核的发布证据或唯一需求来源。
