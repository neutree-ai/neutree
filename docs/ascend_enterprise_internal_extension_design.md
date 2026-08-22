# Ascend 企业版集成设计

> **文档状态：企业内部扩展草稿，非监控权威。** 本文保留 engine、runtime、license、
> 交付和后续 vNPU 的候选设计；其中 Roadmap 4 的接口、Profile、allocation、NVIDIA
> 迁移及指标示例均不得作为当前实现输入。监控契约以
> [Ascend NPU 监控权威设计](./ascend-npu-monitoring-design.md) 和
> [加速器驱动探测与 Adapter 迁移方案](./nvml-replacement-and-feature-discovery.md) 为准，
> 文档总入口见 [加速器监控设计文档索引](./accelerator-monitoring-design-index.md)。本稿与
> `ascend-enterprise-integration-design.md` 合并定稿前，不作为企业集成执行契约。

## 背景

本文档定义 Ascend 能力如何以企业版能力集成到 Neutree，同时保持社区版只提供通用扩展点，不包含 Ascend 专属实现、常量、指标名、镜像、chart 或运行时逻辑。

本设计按 5 条 roadmap 组织：

1. 企业版集成
2. 静态集群和 Kubernetes 集群支持
3. Engine 支持
4. 监控支持
5. vNPU 支持

权威输入：

- 社区版 Neutree：
  - `cmd/neutree-core/app/builder.go`
  - `internal/accelerator/manager.go`
  - `internal/accelerator/plugin`
  - `internal/engine/registry.go`
  - `cmd/neutree-cli/app/cmd/packageimport/engine.go`
  - `internal/cli/packageimport`
  - `cmd/neutree-node-agent/neutree-node-agent.go`
  - `internal/observability/neutreemetrics`
- 企业版 Neutree：
  - `neutree-enterprise` 仓库的 `cmd/neutree-core/neutree-core.go`
- 已有 Ascend Plugin：
  - `neutree-ascend` 仓库目标分支的 `internal/plugin`
  - `neutree-ascend` 仓库目标分支的 `internal/engine`
  - `neutree-ascend` 仓库目标分支的 `dist/*manifest.yaml`

## 总体原则

- 社区版只提供通用扩展边界，不出现 Ascend 专属符号。
- Ascend accelerator type 由企业版拥有。现有 Ascend Plugin 使用 `npu`，社区版只把它当作 opaque string 透传。
- Ascend accelerator plugin 和 Ascend engine 分开交付。
- Accelerator plugin 走企业版内部集成，注入到 `neutree-core`。
- Ascend engine 不走控制面自动注入，也不新增 `WithEngines(...)`。Engine 通过外置 engine import package 导入。
- `neutree-node-agent` 通过显式 `--accelerator-type` 选择 adapter；没有配置直接启动失败。
- 监控主指标继续使用 vendor-neutral 的 `neutree_accelerator_*`。
- 静态 Ray/SSH 集群和 Kubernetes 集群的整卡 NPU 支持纳入当前版本范围。
- vNPU 是后续独立 Kubernetes-only roadmap，当前版本只保留边界和计数语义，不作为实现或验收目标。

## 当前版本范围

当前版本只收敛以下能力：

- 企业版内部 Ascend accelerator plugin 注册和 vendor gate。
- 静态 Ray/SSH 与 Kubernetes 的整卡 NPU 资源发现、资源转换、资源解析和运行时注入。
- `accelerator.type=npu` 的 endpoint resource 语义，310P/910B 通过 `accelerator.product`、static runtime profile 和 engine version 区分。
- 静态 Ray/SSH 的 310P/910B `neutree-serve:<version>-npu-ascend*` 基础镜像、image labels、cluster version 过滤和离线 image list。
- vLLM-Ascend 通过外置 engine import package 导入，不新增 engine dependency injection。
- `neutree-node-agent` adapter registry、Enterprise node-agent image 选择和 npu-exporter 指标归一。
- license 继续复用底层 `ResourceTypeGPU`，但业务语义是 accelerator unit，且只按物理 GPU/NPU 卡数计数。

当前版本不包含：

- vNPU 实现、vNPU allocation join、vNPU endpoint usage 和 vNPU dashboard。
- static Ray/SSH vNPU。
- 同一静态 Ray/SSH 集群内 310P/910B 混合运行的支持或显式限制。
- 独立 NPU license resource type。
- MindIE 主路径。
- 社区版中的 Ascend 常量、Ascend runtime、Ascend metrics parser 或 Ascend engine 内置定义。

## 当前状态

### 社区版

`cmd/neutree-core/app/builder.go` 已支持通过 `app.Builder` 注入 controller 和 reconcile hooks，但还没有 accelerator plugin 注入入口。企业版 `cmd/neutree-core` 当前基于该 builder 注入 license、签名和 feature hooks。

社区版 accelerator plugin 目前主要由 `internal/accelerator/manager.go` 和 `internal/accelerator/plugin` 管理。内置 NVIDIA/AMD plugin 可以在社区版进程内注册，外部 plugin 可以通过 `/v1/plugin/register` 注册。Enterprise 作为独立 Go module 不能直接 import 社区版 `internal` package，所以需要 public accelerator extension boundary。

Engine 已有独立导入链路。`neutree-cli import engine` 支持 archive 和 manifest 两种输入，最终通过 `/v1/engine/register` 注册 engine metadata。`workspace_controller` 会在 workspace reconcile 时将 registry 中的 engine 同步到 workspace DB。该路径已经满足 Ascend engine 导入，不需要新增 builder dependency injection。

`neutree-node-agent` 当前 accelerator metrics 仍偏 NVIDIA/DCGM：

- `devicesnapshot.FromAcceleratorMetrics` 读取 `UUID`、`gpu`、`modelName` 和 `DCGM_FI_DEV_FB_TOTAL`。
- `normalizer.normalizeAcceleratorSamples` 解析 `DCGM_FI_DEV_GPU_UTIL`、`DCGM_FI_DEV_FB_USED`、`DCGM_FI_DEV_FB_TOTAL`、温度和 PCIe byte counters。
- hardware info 使用 DCGM labels，并有可选 NVML fallback。

这说明 node-agent 需要 adapter registry，不能继续把 DCGM 解析写死在通用路径里。

### 企业版和已有 Ascend Plugin

企业版 `cmd/neutree-core` 当前通过 `app.NewBuilder().WithConfig(c)` 构建 control plane，并注入 license hooks。该位置是企业版内部注册 Ascend accelerator plugin 的自然入口。

已有 Ascend Plugin 已实现以下可复用能力：

- `internal/plugin/plugin.go`
  - 通过 SSH 执行 `npu-smi info -m` 探测 NPU。
  - 生成 static cluster node runtime config。
  - 设置 Docker runtime 为 `ascend`。
  - 注入 `ASCEND_VISIBLE_DEVICES`、`ASCEND_RT_VISIBLE_DEVICES`、`ACCELERATOR_TYPE`、`NPU_CHIP_TYPE`。
  - 提供 container runtime config。
- `internal/plugin/npu_types.go`
  - 定义 310P 和 910B 的 Kubernetes resource name、Ray product name、engine suffix、runtime image suffix、chip type。
- `internal/plugin/plugin_converter.go`
  - Ray 资源转换为 `NPU` 和产品 custom resource。
  - Kubernetes 资源转换为 `huawei.com/Ascend310P` 或 `huawei.com/Ascend910B`。
- `internal/plugin/plugin_parser.go`
  - 从 Kubernetes resource 和 Ray resource 解析为统一 accelerator group。
- `internal/engine` 与 `dist/*manifest.yaml`
  - 已有 vLLM-Ascend engine manifest、schema、template 和 image metadata 雏形。

该已有 plugin 当前是外部 REST plugin 形态。最终企业版主路径应迁移为企业版内部 plugin，保留 REST plugin 作为兼容或实验路径。

## Roadmap 1：企业版集成

### 目标

让 Ascend accelerator plugin 作为企业版能力进入 `neutree-core` 进程，不要求社区版包含 Ascend 代码，也不要求用户额外部署独立 accelerator plugin service。

### 社区版改动

社区版新增 public accelerator extension package，例如：

```text
github.com/neutree-ai/neutree/pkg/accelerator
```

该 package 定义企业版可实现的接口。接口覆盖 manager、orchestrator、metrics planner 需要的能力：

- `Resource() string`
- `GetNodeAccelerator(...)`
- `GetNodeRuntimeConfig(...)`
- `GetContainerRuntimeConfig()`
- `GetResourceConverter()`
- `GetResourceParser()`
- `GetAcceleratorProfile(...)`

社区版 `app.Builder` 增加 accelerator plugin 注入入口：

```go
func (b *Builder) WithAcceleratorPlugins(plugins ...accelerator.Plugin) *Builder
```

`Build()` 在初始化 accelerator manager 前，将这些 plugin 传入 manager 或注册到等价 registry 中。社区版仍保留 `/v1/plugin/register`，但它不是企业版 Ascend 主路径。

社区版不得新增：

- Ascend accelerator type 常量；
- `npu-smi` 调用；
- Ascend runtime config；
- `huawei.com/Ascend*` resource mapping；
- vLLM-Ascend engine schema/template；
- npu-exporter 指标解析；
- vNPU identity 或 allocation join。

### 企业版改动

企业版新增内部 package，例如：

```text
github.com/neutree-ai/neutree-enterprise/pkg/accelerator/ascend
```

该 package 从已有 Ascend Plugin 迁移可复用代码：

- `npu_types.go`
- `plugin.go` 中的 SSH 探测和 runtime config 逻辑；
- `plugin_converter.go`
- `plugin_parser.go`

企业版 `cmd/neutree-core` 注册方式：

```go
builder := app.NewBuilder().
    WithConfig(c)

if normalizeVendor(vendor.Vendor) == "smtx" {
    builder.WithAcceleratorPlugins(ascend.NewPlugin())
}
```

NPU plugin 只在 Enterprise vendor 归一化后为 `smtx` 时注册。现有 license 和 vendor 代码中可能使用 `SMTX` 大写值，比较前必须统一归一化为小写。归一化逻辑需要做成通用工具函数，禁止各调用点手写 `strings.ToLower(...) == "smtx"`。

建议工具函数：

```go
func NormalizeVendor(value string) string {
    return strings.ToLower(strings.TrimSpace(value))
}

func IsSMTXVendor(value string) bool {
    return NormalizeVendor(value) == "smtx"
}
```

企业版所有 vendor gate 都通过该工具函数判断，包括 Ascend plugin 注册、Enterprise node-agent component profile 注册、license/feature gate 和任何后续 NPU feature 开关。vendor 不是 `smtx` 时，企业版仍正常启动，但不注册 Ascend/NPU accelerator plugin，也不暴露相关 runtime、resource conversion、profile 或 metrics exporter 能力。

企业版 license/feature gate 使用同一个 Enterprise-owned accelerator type。现有实现如果继续使用 `npu`，则以下位置必须一致：

- Ascend plugin `Resource()`；
- cluster config/status accelerator type；
- endpoint `resources.accelerator.type`；
- node-agent `--accelerator-type`；
- metrics `accelerator_type` label；
- license feature hook 判断。

资源配额首期沿用现有 license resource type，但代码语义应调整为 accelerator unit，而不是继续把业务概念称为 GPU。当前企业版 license 协议只有 `ResourceTypeGPU = "GPU"` 和 `ResourceTypeWorkspace`，为了兼容已有 license 证书、license server、trial license、API 和用量记录，本轮不新增 `ResourceTypeNPU` 或 `ResourceTypeAccelerator`。

处理方式：

- license wire protocol / persisted resource type 继续使用 `types.ResourceTypeGPU`。
- 企业版业务代码新增或重命名为 accelerator 语义的 helper，例如 `AcquireAcceleratorLicenseHook`、`GetClusterRequiredAcceleratorUnits`、`reconcileAcceleratorLicenseUsage`。
- 上述 helper 内部调用 `AcquireResource` / `ReleaseResource` 时仍传 `types.ResourceTypeGPU`。
- license unit 计算统一从 `cluster.Status.ResourceInfo.Allocatable.AcceleratorGroups` 汇总，不区分 NVIDIA GPU、AMD GPU 或 Ascend NPU。
- Ascend plugin 只要把 NPU 资源解析进 `AcceleratorGroups`，就会消耗同一个 accelerator quota bucket。
- UI/API 如果展示 license 名称，应优先使用 “Accelerator” 或“加速卡/加速器配额”文案；只有底层兼容字段仍叫 `GPU`。

因此，本设计的结论不是“NPU 当作 GPU”，而是“历史上名为 GPU 的 license bucket 在首期作为 accelerator unit bucket 复用”。后续如果产品需要 GPU/NPU 分开售卖，再新增独立 license resource type 和迁移策略。

分层责任：

- license 层不再判断设备 vendor，也不解析 GPU/NPU 型号；它只消费外层已经归一好的 accelerator unit。
- resource parser、cluster resource builder 和静态节点探测必须保证 `AcceleratorGroups[*].Quantity` 表示实际物理 GPU/NPU 卡数。
- `AcceleratorGroups[*].ProductGroups` 只用于型号维度拆分，不能让同一张物理卡在总量中重复出现。
- runtime profile、engine image key 和 product group 都不能直接进入 license unit 计数。
- 如果某个资源解析路径无法证明 `Quantity` 是物理卡数，应在进入 license hook 前失败或标记不可计费状态，不能让 license 层猜测修正。

license helper 的计数实现只需要聚合 `AcceleratorGroups[*].Quantity`：

```go
func GetClusterRequiredAcceleratorUnits(cluster *v1.Cluster) (int, error) {
    var units float64

    groups := cluster.Status.ResourceInfo.Allocatable.AcceleratorGroups
    for _, group := range groups {
        units += group.Quantity
    }

    if units != math.Trunc(units) {
        return 0, fmt.Errorf("accelerator license units must be whole physical devices")
    }

    return int(units), nil
}
```

该 helper 不读取 `ProductGroups`，也不按 vendor/type 分支。`ProductGroups` 可以用于校验 `Quantity` 来源是否合理，但不能作为 license unit 的第二个计数来源。

如果静态 Ray/SSH 运行时为了镜像选择在 status、package 或内部 reconcile context 中出现 `npu-ascend310p`、`npu-ascend910b` 等 runtime profile，license/feature gate 必须先归一到同一个 NPU family，再执行 Ascend NPU feature 检查和 GPU quota 扣减。runtime profile 不新增独立 license 维度。

计数规则：

- 同一物理加速卡只能在 `AcceleratorGroups` 中计入一次。
- runtime profile 和 product group 不能额外增加 license unit。
- 如果 parser 同时产出 family group 和 runtime-profile group，license helper 必须先归一或拒绝该状态，避免重复扣减。

### 交付物

- 社区版 public accelerator extension。
- 社区版 builder plugin 注入入口。
- 企业版 Ascend accelerator plugin package。
- 企业版 `cmd/neutree-core` 通过通用 vendor normalize 工具判断 `smtx`，并在命中时注册 Ascend plugin。
- 企业版 license/feature gate 与 accelerator type 对齐。
- 企业版 license/feature gate 兼容 NPU family 和 310P/910B runtime profile。
- 现有 `ResourceTypeGPU` 作为 accelerator unit quota bucket 复用的行为说明。

### 验收

- 社区版不包含 Ascend 专属符号或实现。
- 企业版启动后，无需外部 REST plugin service 即可获取 Ascend runtime/resource/profile 能力。
- vendor 不是 `smtx` 时，企业版不注册 NPU plugin，相关 Ascend resource conversion 和 profile lookup 不可用。
- Ascend NPU cluster 的 accelerator 数量会计入现有 GPU license usage。
- 移除企业版 `WithAcceleratorPlugins(ascend.NewPlugin())` 后，Ascend runtime/resource conversion 能力消失，NVIDIA/AMD 现有行为不受影响。

## Roadmap 2：静态集群和 Kubernetes 集群支持

### 目标

Ascend 在静态 Ray/SSH 集群和 Kubernetes 集群中都能完成资源发现、资源解析、资源转换、运行时注入和 endpoint 部署。

### 静态 Ray/SSH 集群

企业版 Ascend plugin 负责静态节点探测：

- 通过 SSH 执行 `npu-smi info -m`。
- 节点不是 NPU 节点时，忽略 `npu-smi` command not found 或普通执行失败，返回空 accelerator list。
- SSH 连接失败必须返回错误，避免吞掉节点连通性问题。
- 根据探测结果识别 310P 或 910B。

运行时配置：

- Docker runtime 设置为 `ascend`。
- runtime image suffix 按产品区分：
  - 310P：`npu-ascend310p`
  - 910B：`npu-ascend910b`
- 注入环境变量：
  - `ASCEND_VISIBLE_DEVICES`
  - `ASCEND_RT_VISIBLE_DEVICES`
  - `ACCELERATOR_TYPE=<enterprise-owned-type>`
  - `NPU_CHIP_TYPE=ascend310p|ascend910b`

runtime image suffix 是静态 Ray/SSH 集群的运行时 profile，不等价于对外 accelerator type。对外 accelerator type 仍由企业版 plugin 拥有并作为 opaque string 透传；310P/910B 的差异通过 product、runtime image suffix 和 image label 体现。

静态节点 cluster 基础镜像使用两个明确的 NPU runtime variant：

```text
neutree-serve:<cluster-version>-npu-ascend310p
neutree-serve:<cluster-version>-npu-ascend910b
```

最小实现使用单个参数化 `cluster-image-builder/Dockerfile.npu`，通过 build arg 区分基础镜像，并提供两个 build target：

```text
docker-build-npu-310p
docker-build-npu-910b
```

310P 和 910B 的基础镜像变量需要分开，避免两个 target 共享同一个默认值：

```text
ASCEND_CLUSTER_BASE_IMAGE_310P ?= <310P CANN base image>
ASCEND_CLUSTER_BASE_IMAGE_910B ?= <910B CANN base image>
```

镜像 label 也要带 runtime variant，用于版本过滤和离线包选择：

```text
neutree.ai/accelerator-type=npu
neutree.ai/runtime-profile=npu-ascend310p|npu-ascend910b
neutree.ai/accelerator-product=HUAWEI_Ascend310P|HUAWEI_Ascend910B
```

cluster versions API 不能把 `npu-ascend310p` 和 `npu-ascend910b` 当作 accelerator type 来过滤。若当前 API 只能按 `neutree.ai/accelerator-type` 精确过滤，则需要增加通用的 runtime profile / product 过滤能力，或由企业版调用方在拿到 `accelerator-type=npu` 的候选版本后按 `neutree.ai/runtime-profile` 二次过滤。首期不要求社区版新增 Ascend accelerator type 常量。

Ray resource conversion：

- 使用通用 resource `NPU` 表示数量。
- 使用产品 custom resource 表示型号，例如 `HUAWEI_Ascend310P`、`HUAWEI_Ascend910B`。
- endpoint resource spec 中 `accelerator.product` 有值时，将其转换为对应 Ray product resource。

Ray resource parsing：

- 从 Ray cluster resource 中读取 `NPU`。
- 从产品 custom resource 中恢复 product group。
- 对外聚合到 Enterprise-owned accelerator type。

静态 Ray/SSH cluster image 获取路径需要同步处理：

- 可用版本查询：`GET /clusters/available_versions` 对 SSH cluster 查询 `neutree/neutree-serve`，读取 image labels，并按 `accelerator-type=npu`、runtime profile 和 product 过滤。
- SSH cluster 启动：`cluster.Spec.Version` 继续保存纯版本，例如 `v1.0.1`；runtime config 再追加 `ImageSuffix`，得到 `v1.0.1-npu-ascend910b`。
- SSH cluster 升级 pre-pull：使用 status 或 reconcile context 中解析出的 runtime profile 取 image suffix，不能退回默认 `neutree-serve:<version>`。
- 离线 cluster package：`scripts/builder/build-package.sh` 在 `cluster ssh` 模式下按 accelerator 和 runtime profile 选择 image list。Ascend 需要新增 310P/910B 对应 image list，分别包含对应 `neutree-serve` variant image。推荐路径是 `image-lists/cluster/ssh/npu-ascend310p-images.txt` 和 `image-lists/cluster/ssh/npu-ascend910b-images.txt`。

兼容规则：

- 用户配置不能把 `cluster.Spec.Version` 写成带 suffix 的 tag，否则会被二次拼接。
- 如果用户只配置 NPU family 而未提供 product/runtime profile，静态集群应执行节点探测；无法唯一确定 310P/910B 时返回明确错误。
- license 和 quota 统计按 accelerator family 归并，runtime variant 不新增独立 license resource type。

同一静态 Ray/SSH 集群内 310P 和 910B 混合运行不作为当前版本目标。本版本只要求能为单一 runtime profile 的静态集群选择正确基础镜像、runtime config 和 image list。后续如果要支持或显式禁止混合集群，需要单独设计节点级 runtime profile、按节点 pre-pull、Ray resource 聚合和 endpoint 调度约束。

### Kubernetes 集群

Kubernetes resource conversion：

- `HUAWEI_Ascend310P` 映射到 `huawei.com/Ascend310P`。
- `HUAWEI_Ascend910B` 映射到 `huawei.com/Ascend910B`。
- `accelerator.product` 必填。缺少 product 时返回错误，避免控制面无法确定 Kubernetes resource name。

Kubernetes resource parsing：

- 从 node allocatable/capacity 中解析 `huawei.com/Ascend310P` 和 `huawei.com/Ascend910B`。
- 对外统一聚合到 Enterprise-owned accelerator type。
- product group 保留具体型号。

Kubernetes runtime：

- 依赖 Ascend device plugin 和 Kubernetes resource request/limit 完成设备注入。
- 企业版不在社区版 deployment template 中硬编码 Ascend resource。
- profile 或 engine import package 提供 Ascend 需要的模板内容。

### 交付物

- 静态节点 Ascend 探测和 runtime config。
- 静态节点 `Dockerfile.npu` 和 310P/910B build targets。
- 静态节点 310P/910B cluster image tag、label 和版本过滤规则。
- Ascend 310P/910B 离线 cluster package image lists。
- Ray resource converter/parser。
- Kubernetes resource converter/parser。
- 310P/910B product mapping。
- endpoint resource spec 对 Ascend product 的校验。

### 验收

- 静态 Ray/SSH 集群能识别 NPU 总量和产品型号。
- 静态 Ray/SSH endpoint 能收到正确 Docker runtime、image suffix 和环境变量。
- 静态 Ray/SSH cluster version 能按 NPU runtime profile 找到正确 `neutree-serve` 镜像。
- 离线 cluster package 能分别打包 310P 和 910B 的静态节点基础镜像。
- Kubernetes 集群能解析 `huawei.com/Ascend*` 资源并展示为统一 accelerator group。
- Kubernetes endpoint 使用 Enterprise-owned accelerator type 和 product 后，能生成正确 resource requests/limits。

## Roadmap 3：Engine 支持

### 目标

Ascend engine 不通过控制面自动注入，也不新增 `WithEngines(...)`。vLLM-Ascend 通过外置 engine import package 发布和导入。

### 导入路径

外置 engine import package 由 Enterprise 发布。导入方式复用现有社区版能力：

```bash
neutree-cli import engine --package <vllm-ascend-package-or-manifest>
```

导入链路：

1. `neutree-cli import engine` 读取 archive 或 manifest。
2. package importer 解析 manifest、schema、template 和 image metadata。
3. importer 按需加载和推送 engine image。
4. importer 调用 `/v1/engine/register` 注册 engine metadata。
5. workspace controller 在 reconcile 时将 engine registry 同步到 workspace DB。

现有 workspace engine 同步依赖 workspace reconcile。导入 engine 后，如果没有触发 workspace reconcile，已有 workspace 的 DB 记录可能不会立即出现新版本。首期可以复用现有 reconcile 机制；如果产品要求导入后立即可见，再增加显式 resync 或管理命令。

### Engine package 内容

vLLM-Ascend import package 至少包含：

- engine name：复用 `vllm`；
- Ascend-specific engine version，例如 `v0.18.0-ascend-npu910b`；
- image metadata，image key 使用 Enterprise-owned accelerator type，例如 `npu`；
- values schema；
- Kubernetes deploy template；
- 如需支持静态集群，补充 SSH/Ray 对应模板或现有约定的 image key；
- package manifest。

当前 engine image 选择逻辑按 endpoint resource 中的 accelerator type 匹配 image key：

- Kubernetes 使用 `endpoint.Spec.Resources.GetAcceleratorType()`，并按 `k8s_<type>`、`<type>` 顺序查找 engine image。
- Ray/SSH 使用同一个 accelerator type，并按 `ssh_<type>`、`<type>` 顺序查找 engine image。

因此 Ascend endpoint resource 的 `accelerator.type` 仍建议保持 `npu`。310P/910B 差异不通过 `accelerator.type` 或 image key 区分，而通过不同 engine version 区分。

推荐版本命名：

```text
v0.18.0-ascend-npu310p
v0.18.0-ascend-npu910b
```

推荐 image key：

```text
Images["npu"]
Images["k8s_npu"] // 仅在 Kubernetes 需要独立镜像时使用
Images["ssh_npu"] // 仅在 Ray/SSH 需要独立镜像时使用
```

310P/910B 的镜像差异由 engine version 自身承载。例如 `v0.18.0-ascend-npu310p` 的 `Images["npu"]` 指向 310P 镜像，`v0.18.0-ascend-npu910b` 的 `Images["npu"]` 指向 910B 镜像。这样不需要修改当前 engine image lookup，也不会要求 endpoint resource type 从 `npu` 变成 `npu-ascend310p` 或 `npu-ascend910b`。

如果后续统一为 `family + runtime profile + product` 模型，并且需要同一个 engine version 同时覆盖多个 NPU runtime profile，再单独设计 engine image selection 维度；不在当前版本引入。

已有 Ascend Plugin worktree 中 `dist/vllm-*-manifest.yaml` 和 `internal/engine` 可以作为迁移输入，但最终交付形态是 import package，不是控制面内置 engine。

### Scope

首期主路径是 vLLM-Ascend。

MindIE 不进入本次主路径。后续如果需要支持 MindIE，应作为独立 engine import package 导入，不挂在 accelerator plugin 生命周期上。

### 交付物

- vLLM-Ascend engine import package 构建流程。
- vLLM-Ascend manifest/schema/template。
- vLLM-Ascend engine image 元数据。
- 导入文档和版本命名规范。

### 验收

- 不修改 `app.Builder` 增加 engine DI。
- 不导入 Ascend engine package 时，控制面不会出现 vLLM-Ascend 版本。
- 导入 vLLM-Ascend engine package 后，workspace reconcile 能同步 engine 版本。
- 使用导入后的 vLLM-Ascend 版本能部署 text-generation endpoint。

## Roadmap 4：监控支持

### 目标

支持 Ascend NPU 监控，同时保持 Neutree 对外主指标为 vendor-neutral 的 `neutree_accelerator_*`。

### 社区版改动

`neutree-node-agent` 新增必填参数：

```text
--accelerator-type=<type>
```

规则：

- 参数必填。
- 没有配置直接启动失败。
- `cluster-type` 只表示运行环境：`kubernetes` 或 `ray`。
- `accelerator-type` 选择 metrics/allocation adapter。
- adapter 未注册时启动失败。
- adapter 选择按配置优先，不从 scraped metrics 自动识别。
- 配置的 adapter 和 exporter 指标族不匹配时，报告 scrape/normalization error，不自动切换。

社区版新增 accelerator metrics adapter registry。概念接口：

```go
type AcceleratorMetricsAdapter interface {
    AcceleratorType() string
    Normalize(req AdapterNormalizeRequest) ([]normalizer.Sample, error)
    DeviceSnapshot(req AdapterSnapshotRequest) (*v1.NodeDeviceSnapshot, error)
    HardwareInfos(req AdapterHardwareRequest) ([]model.GPUHardwareInfo, error)
    AllocationProvider(input AdapterAllocationInput) allocation.Provider
    EndpointUsageProvider(input AdapterEndpointUsageInput) EndpointGPUUsageProvider
}
```

社区版只注册 NVIDIA/DCGM adapter，将当前写死的 DCGM 解析迁移到该 adapter 后面。

node-agent 是 Neutree 通用组件，不应作为 `AcceleratorProfile` 的顶层字段。`AcceleratorProfile` 继续只描述 accelerator 相关能力，例如 cluster runtime、engine runtime 和 metrics exporter。Enterprise node-agent 镜像选择应走通用 component profile / image override 机制。

社区版新增通用 component profile，例如：

```go
const ComponentNeutreeNodeAgent = "neutree-node-agent"

type ComponentProfile struct {
    Image string            `json:"image,omitempty"`
    Args  []string          `json:"args,omitempty"`
    Env   map[string]string `json:"env,omitempty"`
}

type ComponentProfileResolver interface {
    ResolveComponentProfile(component string) ComponentProfile
}
```

`neutree-node-agent-enterprise` 进入 OSS 的方式不是给 metrics planner 增加 Enterprise 专用参数，也不是塞进 `AcceleratorProfile`，而是 Enterprise 在构建 control plane 时注入通用 component profile：

```text
Enterprise cmd/neutree-core
  -> vendor == smtx 时注册 ascend.NewPlugin()
  -> vendor == smtx 时注册 ComponentNeutreeNodeAgent image override
  -> OSS metrics/static planner 解析 ComponentNeutreeNodeAgent profile
  -> Kubernetes/static node-agent component image
```

概念注册方式：

```go
builder := app.NewBuilder().
    WithConfig(c)

if normalizeVendor(vendor.Vendor) == "smtx" {
    builder.WithAcceleratorPlugins(ascend.NewPlugin())
    builder.WithComponentProfiles(map[string]componentprofile.ComponentProfile{
        componentprofile.ComponentNeutreeNodeAgent: {
            Image: "neutree/neutree-node-agent-enterprise:<version>",
        },
    })
}
```

现有 accelerator profile 拉取入口仍然保留给 metrics exporter：

- `internal/accelerator/manager.go`：`GetAcceleratorProfile(ctx, acceleratorType)` 根据 accelerator type 找到 plugin，并调用 plugin 的 `GetAcceleratorProfile`。
- `internal/cluster/component/metrics/exporters.go`：Kubernetes metrics planner 通过 `acceleratorMgr.GetAcceleratorProfile` 读取 exporter profile。
- `internal/cluster/staticcluster/helpers.go`：static planner 通过 `AcceleratorProfileProvider.GetAcceleratorProfile` 读取静态节点 runtime profile。

因此 OSS 需要做的是增加通用 component profile resolver，并让 Kubernetes/static 两条 planner 对 `neutree-node-agent` 使用该 resolver。Enterprise 需要做的是注册 component image override；Ascend plugin 的 `GetAcceleratorProfile` 只返回 Ascend runtime/resource/exporter profile。

规则：

- `ComponentProfile.Image` 为空时使用社区版默认 `neutree/neutree-node-agent:<version>`。
- `ComponentProfile.Image` 有值时，Kubernetes DaemonSet 和 static node component 都使用该镜像，并继续经过现有 image prefix / registry rewrite。
- 社区版 planner 负责传入 `--accelerator-type=<selected-type>`，该值来自 cluster/static 配置和 accelerator profile selection，不由 Enterprise profile 自己拼接。
- `ComponentProfile.Args` 对 node-agent 只能追加 edition-owned 参数，不能覆盖社区版基础参数，例如 `--cluster-type`、`--metrics-mode`、`--node`、`--node-ip`、pod-resources socket、Ray dashboard URL。
- `ComponentProfile.Env` 与 planner 生成的基础 env 合并；冲突的保留字应直接报错，避免 component profile 覆盖 node identity 或 cluster runtime 参数。
- static cluster 首期继续复用 metrics exporter runtime 中的 Docker options 来获得 accelerator visibility；如果后续发现 node-agent 和 exporter 的 runtime 需求不同，再把 node-agent component runtime 独立出来。

Kubernetes 当前 `metrics` manifest 中 DaemonSet 名称和 container 名称可以继续保持 `neutree-node-agent`。需要改的是 selected accelerator type 和 image/args/env 的来源：

- selected accelerator type 必须来自 `cluster.Spec.Config.AcceleratorType`。该字段为空时，managed node-agent 规划失败，不再通过遍历 `SupportPlugins()` 自动猜测 NVIDIA 或 Ascend。
- metrics planner 只调用 `GetAcceleratorProfile(ctx, selectedType)`。`SupportPlugins()` 可继续用于外部能力枚举或兼容逻辑，但不能作为 node-agent adapter/image 的选择依据。
- `NeutreeNodeAgentMetricsImage` 从 `ComponentNeutreeNodeAgent` profile 派生。
- manifest args 显式包含 `--accelerator-type=<selected-type>`。
- 组件名、ServiceMonitor scrape job 和 Prometheus labels 不因为企业版镜像而变化。

static cluster 当前 `buildNodeAgentComponent` 也应从 `ComponentNeutreeNodeAgent` profile 派生 image/args/env。组件名继续保持 `neutree-node-agent`，这样 node component lifecycle、health check 和回滚路径不分叉。静态节点如果没有可用 accelerator type，则不能生成 accelerator node-agent 配置；NPU/NVIDIA 都必须显式得到 selected type 后再进入 accelerator profile lookup。

### 企业版改动

企业版发布 `neutree-node-agent-enterprise`。该镜像复用社区版 node-agent package，并在 Enterprise-only code 中注册 Ascend adapter：

```go
func init() {
    neutreemetrics.RegisterAcceleratorAdapter(enterpriseType, ascend.NewAdapter())
}
```

Ascend adapter 负责解析 `npu-exporter` 指标，并映射到 canonical metrics。

内存语义：

- 优先使用 HBM/on-chip memory。
- DDR 只在 HBM 指标不可用时作为 fallback。

利用率语义：

- 优先使用 overall NPU utilization。
- AICore utilization 只作为 fallback。

设备身份：

- `vdie_id` 映射为 device UUID。
- `id` 映射为本地 device index。
- product 固定从 Exporter `model_name` 映射；Ray/Device Plugin product/resource labels 仅用于 adapter 内部调度匹配。

主指标：

- `neutree_accelerator_utilization_ratio`
- `neutree_accelerator_memory_used_bytes`
- `neutree_accelerator_memory_total_bytes`
- `neutree_accelerator_temperature_celsius`
- `neutree_node_accelerator_info`
- `neutree_node_accelerator_total`
- `neutree_node_accelerator_allocated`
- `neutree_node_accelerator_free`
- `neutree_endpoint_replica_accelerator_*`

vendor 通过 `accelerator_type` label 区分。Enterprise 可为 health、interconnect、ECC 等无法映射到 vendor-neutral 语义的数据增加 supplemental metrics。

### Metrics Profile

Ascend accelerator profile 由企业版 plugin 返回：

- npu-exporter component 定义；
- exporter scrape target 和 relabel 配置；
- Kubernetes 和 static cluster 的部署差异。

Enterprise component profile 由企业版 `cmd/neutree-core` 在 vendor 为 `smtx` 时注册：

- `ComponentNeutreeNodeAgent.Image=neutree-node-agent-enterprise:<version>`；
- node-agent 附加 args/env；
- `--accelerator-type=<enterprise-owned-type>` 由社区版 planner 按 selected accelerator type 自动注入。

社区版 metrics planner 只渲染 accelerator profile 和 component profile，不理解 Ascend 内部语义。

如果 Enterprise 没有注册 `ComponentNeutreeNodeAgent` image override，则会落回社区版 node-agent 镜像。该镜像没有注册 Ascend adapter，因此在收到 `--accelerator-type=<enterprise-owned-type>` 后必须启动失败。Enterprise 测试需要覆盖这个失败路径，避免 NPU 集群静默使用 NVIDIA/DCGM 逻辑。

### 交付物

- 社区版 node-agent adapter registry。
- 社区版 NVIDIA/DCGM adapter。
- 企业版 Ascend adapter。
- 企业版 node-agent image。
- Ascend metrics profile。
- npu-exporter 部署配置。
- Ascend dashboard/query 规则。

### 验收

- 未配置 `--accelerator-type` 时 node-agent 启动失败。
- 社区版 NVIDIA metrics 行为保持兼容。
- 企业版 Ascend 集群能上报 canonical accelerator metrics。
- exporter 指标族不匹配时有明确错误，不静默降级到其他 adapter。
- dashboard 可通过 `accelerator_type` label 区分 NVIDIA 和 Ascend。

## Roadmap 5：vNPU 支持（后续独立）

### 目标

支持 Kubernetes Ascend vNPU 场景下的设备身份、资源分配、endpoint usage 和监控归属。根据 [HAMi Ascend NPU 虚拟化调研](./hami-ascend-npu-virtualization-research.md)，vNPU 首期仅支持 Kubernetes，不支持 static Ray/SSH vNPU。该 roadmap 不进入当前版本实现范围，只保留后续设计边界，避免整卡 NPU 实现后难以扩展。

### 支持范围

首期支持：

- Kubernetes Ascend 集群。
- 整卡分配。
- HAMi vNPU template mode，作为默认 vNPU 主路径。
- Ascend `npu-exporter` 物理 NPU 指标。
- Kubernetes pod-resources、HAMi annotations 和 npu-exporter metadata 的 allocation join。

实验/受控试点：

- `hami-vnpu-core` soft slicing mode。该模式依赖 `libvnpu.so`、device-share、共享内存、`npu-smi` 和 ARM/驱动/CANN 版本约束，当前不作为稳定生产承诺。

首期不承诺：

- static Ray/SSH vNPU。
- `hami-vnpu-core` 大规模生产。
- 910B/910C vNPU 层级 exporter 指标。
- WebUI 中完整展示 vNPU topology/template 层级。

### 资源模型

vNPU 需要保留两层身份：

- physical NPU：物理芯片身份，用于 node-level capacity、health、temperature、interconnect。
- virtual NPU：虚拟设备身份，用于 pod/container allocation、endpoint replica usage、租户隔离和容量切分。

Enterprise adapter 需要把 `npu-exporter` 中的 vNPU 相关指标映射到 Neutree 的 device identity：

- `vdie_id` 作为 virtual device identity 的主要来源。
- physical chip `id` 作为本地物理设备 index。
- 当 exporter 能提供 physical-to-virtual 关系时，保留 parent-child mapping。
- 当 exporter 缺少稳定 join key 时，返回 partial allocation，并显式暴露 join failure。

license quota 仍按物理 NPU 计数：

- `AcceleratorGroups[*].Quantity` 必须表示底层物理 NPU 数量。
- HAMi template、vNPU slice、core/memory share 和 virtual device 数量不能额外增加 license unit。
- vNPU adapter 可以输出 virtual device usage 和 supplemental metrics，但不能改变 cluster-level accelerator unit 的物理卡计数语义。

HAMi template mode 还需要保留 template metadata：

- HAMi scheduler 写入的 `hami.io/<CommonWord>-devices-allocated`。
- template mode 注入的 `ASCEND_VNPU_SPECS`。
- `huawei.com/<CommonWord>` annotation 中的 template 信息。

`npu-exporter` 不解析 HAMi annotations、`ASCEND_VNPU_SPECS` 或 template 名称，因此只依赖 exporter 不能展示 “Pod 使用哪个 HAMi template”。

### Kubernetes allocation join

优先级：

1. kubelet pod-resources。
2. HAMi / Ascend device plugin annotations。
3. npu-exporter container/vNPU metrics。

规则：

- pod-resources 能稳定提供 device id 时，以 pod-resources 为准。
- pod-resources 不能 join 到稳定 `vdie_id` 时，fallback 到 HAMi / Ascend device plugin annotations。
- annotations 和 exporter 冲突时，adapter 报告 allocation inconsistency，不静默覆盖。
- template mode 需要解析 `hami.io/<CommonWord>-devices-allocated` 和 `ASCEND_VNPU_SPECS` 才能展示 vNPU/template 归属。
- `hami-vnpu-core` mode 必须要求 Pod 显式申请 memory/core，并带有 `huawei.com/vnpu-mode: hami-core` annotation。

### Static Ray/SSH vNPU

static Ray/SSH vNPU 不在首期范围。原因：

- 当前 HAMi Ascend vNPU 能力依赖 Kubernetes scheduler、device plugin、Pod annotations 和 kubelet Allocate 流程。
- static Ray/SSH 没有等价的 HAMi scheduler allocation metadata。
- 仅通过 Ray actor process environment 和 `npu-smi` 无法稳定表达 template、soft slicing 或 virtual device parent-child 关系。

static Ray/SSH 首期只支持整卡 NPU。后续如果要支持 static vNPU，需要单独设计静态节点上的 vNPU 创建、分配、生命周期和监控数据源。

### 指标语义

主指标继续使用 `neutree_endpoint_replica_accelerator_*` 表示 endpoint replica usage。vNPU 支持新增或扩展 labels 时必须满足：

- 不破坏现有 NVIDIA query。
- `accelerator_type` 仍表示 Enterprise-owned accelerator type。
- physical device 和 virtual device 的 label 命名稳定。
- virtual device label 不用于整卡 NPU 场景。

如果需要 supplemental metrics，应由 Enterprise adapter 输出，例如：

- virtual device health；
- physical-to-virtual mapping；
- allocation join error count；
- vNPU capacity/used ratio。

监控数据源：

- `npu-exporter` 作为物理 NPU 基础指标源。
- 310P vNPU 的 `vnpu_pod_*` 指标可以作为 vNPU 使用量输入。
- 910B/910C 不能默认认为 `npu-exporter` 能提供 vNPU 层级指标。
- `hami-vnpu-core` soft slicing 场景可接入 Ascend device-plugin 内置 `:9395/metrics`，用于 soft-slice workload 指标。

### 交付物

- vNPU identity model。
- Kubernetes vNPU allocation join。
- HAMi template metadata join。
- vNPU endpoint usage metrics。
- join inconsistency/error metrics。
- vNPU dashboard。

### 验收

- Kubernetes vNPU pod 能归属到 endpoint replica。
- physical NPU 和 virtual NPU 不混用 identity。
- template mode Pod 能通过 HAMi annotations 或 `ASCEND_VNPU_SPECS` 还原 template allocation metadata。
- 310P vNPU 场景可关联 `vnpu_pod_*` 指标到 namespace/pod/container。
- join 失败时指标可观测，不产出误导性 allocation。
- 整卡 NPU 场景不受 vNPU labels 影响。

## Roadmap 依赖关系

推荐顺序：

1. 企业版集成：先打通 public accelerator extension 和 enterprise internal plugin。
2. 静态集群和 Kubernetes 集群支持：迁移已有 Ascend Plugin 的资源探测、runtime、parser/converter。
3. Engine 支持：并行准备 vLLM-Ascend import package，但不阻塞 accelerator plugin。
4. 监控支持：在 cluster/runtime 可用后接入 npu-exporter 和 enterprise node-agent。
5. vNPU 支持：后续独立 roadmap，在整卡 NPU metrics/allocation 稳定后扩展 virtual device 语义。

硬依赖：

- Roadmap 2 依赖 Roadmap 1。
- Roadmap 4 依赖 Roadmap 1，并部分依赖 Roadmap 2 的 cluster profile planning。
- Roadmap 5 依赖 Roadmap 4 的 adapter registry 和 allocation provider boundary。

可并行：

- Roadmap 3 可与 Roadmap 1/2 并行，因为 engine 通过 import package 导入，不依赖 control-plane builder 注入。

## 测试策略

### Unit test

社区版：

- public accelerator extension interface 和 manager 注册逻辑。
- `WithAcceleratorPlugins` builder 注入逻辑。
- opaque accelerator type 透传，不依赖 Ascend 常量。
- `--accelerator-type` 必填。
- adapter 缺失时 node-agent 启动失败。
- NVIDIA/DCGM adapter 保持现有 normalization 输出。
- component profile 中 `ComponentNeutreeNodeAgent.Image` 为空时使用社区版默认 node-agent 镜像。
- Kubernetes metrics manifest 使用 component profile 中的 `ComponentNeutreeNodeAgent.Image` 渲染 node-agent DaemonSet 镜像。
- static cluster node component 使用 component profile 中的 `ComponentNeutreeNodeAgent.Image` 渲染 node-agent 镜像。
- Kubernetes 和 static cluster 的 node-agent args 都显式包含 `--accelerator-type=<selected-type>`。
- component profile 中的 node-agent 附加 args/env 不允许覆盖社区版保留参数。
- engine import package 仍可通过 `/v1/engine/register` 注册 engine definitions。

企业版：

- vendor gate：vendor 归一化为 `smtx` 时注册 NPU plugin，非 `smtx` 时不注册。
- vendor normalize 工具函数覆盖 `SMTX`、`smtx`、混合大小写和首尾空白。
- Ascend plugin、Enterprise node-agent component profile、license/feature gate 复用同一个 vendor 判断函数。
- accelerator license helper 使用 accelerator unit 语义命名，但底层继续通过 `ResourceTypeGPU` acquire/release。
- NVIDIA/AMD/Ascend 的 `AcceleratorGroups` 会统一计入 accelerator unit quota。
- Ascend `npu-smi info -m` 解析。
- 310P/910B product mapping。
- static node runtime config。
- static cluster image suffix、runtime profile label 和 cluster version filtering。
- 裸 NPU family 到 310P/910B runtime profile 的探测归一化或拒绝逻辑。
- endpoint `accelerator.type` 保持 `npu`，310P/910B 通过 `accelerator.product`、static runtime profile 和 engine version 区分。
- Ascend 310P/910B 离线 cluster package image list 选择。
- container runtime config。
- Ray resource converter/parser。
- Kubernetes resource converter/parser。
- Ascend accelerator profile。
- vendor 为 `smtx` 时注册 Enterprise node-agent component profile。
- 缺失 Enterprise node-agent component image override 时，node-agent 因 Ascend adapter 未注册而启动失败。
- Ascend metrics adapter memory/utilization fallback。
- Kubernetes allocation join。
- Static Ray/SSH process join。
- vLLM-Ascend engine import package manifest/schema/template 构造。

### DB test

- accelerator type 继续作为 string 透传时，不需要数据库迁移。
- workspace engine sync 覆盖 import package 注册 engine 后 create/update engine DB 记录。
- vLLM-Ascend 版本 merge 不覆盖已有 vLLM versions。
- 重复导入同名 engine 时，merge 行为符合现有 `util.MergeEngine` 语义。

### E2E test

社区版：

- NVIDIA Kubernetes metrics flow 仍能产出 node-agent metrics。
- Static Ray/SSH NVIDIA metrics flow 仍能产出 device snapshot 和 endpoint allocation metrics。
- unknown adapter 部署按预期失败。
- 未配置 accelerator type 的 cluster/static node-agent 规划或启动按预期失败。

企业版：

- 企业版 `neutree-core` 启动后内置 Ascend accelerator plugin。
- Ascend static Ray/SSH cluster 能识别 NPU resource、product group、runtime image suffix 和环境变量。
- Ascend static Ray/SSH cluster 能按 runtime profile 拉取正确 `neutree-serve:<version>-npu-ascend*` 镜像。
- Ascend Kubernetes cluster 能识别 `huawei.com/Ascend*` resource。
- Ascend NPU cluster 的物理 accelerator 数量能计入现有 accelerator unit license usage。
- vLLM-Ascend engine import package 导入后可部署 text-generation endpoint。
- Kubernetes Ascend cluster 能部署 npu-exporter 和 `neutree-node-agent-enterprise`，并上报 canonical accelerator metrics。
- Static Ray/SSH Ascend cluster 能上报 node-level metrics 和 Ray Serve endpoint allocation metrics。
- Kubernetes 和 Static Ray/SSH Ascend cluster 都实际使用 Enterprise node-agent image，而不是社区版默认 image。
- 离线 cluster package 分别使用 310P/910B variant image list，并包含正确的 static cluster base image。

### Manual testing

如果 CI/E2E 环境没有 Ascend NPU 硬件，需要手动验证：

- 真实 `npu-smi info -m` 输出解析。
- Ascend Docker runtime 注入设备。
- vLLM-Ascend 在 310P/910B 上启动和推理。
- 310P/910B static cluster 基础镜像的 CANN/runtime 依赖与真实硬件匹配。
- npu-exporter 在物理 NPU 场景的指标完整性。

### 后续 vNPU testing

vNPU 后续 roadmap 单独补充测试，不作为当前版本门禁：

- Kubernetes vNPU identity、HAMi annotation join 和 allocation join。
- vNPU 不按 virtual device、template 或 slice 数量额外膨胀 license usage。
- Kubernetes vNPU pod/replica 能归属到 endpoint usage。
- npu-exporter 在 vNPU 场景的指标完整性。
- HAMi template mode Pod 是否注入 `ASCEND_VISIBLE_DEVICES` 和 `ASCEND_VNPU_SPECS`。
- `hami-vnpu-core` PoC 是否满足 memory/core request、driver/CANN、device-share、共享内存和 `:9395/metrics` 条件。
- Kubernetes Ascend device plugin annotations 与 pod-resources 的 join 结果。

## 发布与回滚

社区版发布：

- public accelerator extension、builder 注入入口、node-agent adapter registry 和 `--accelerator-type` 必填逻辑需要配套发布。
- 社区版部署模板必须为 NVIDIA 现有路径显式传入 NVIDIA accelerator type。
- 社区版 metrics/static planner 支持从 component profile 选择 node-agent image/args/env，但默认仍使用社区版 node-agent image。
- engine import/register API 不新增控制面注入入口，保持现有行为。
- 回滚时同时回滚 node-agent 必填参数和部署模板变更。

企业版发布：

- Enterprise Ascend accelerator plugin、`neutree-node-agent-enterprise`、npu-exporter profile 和 vLLM-Ascend engine import package 分开交付，但版本需要兼容。
- `cmd/neutree-core` 只在 vendor 为 `smtx` 时注入 Ascend accelerator plugin。
- 发布 Ascend static cluster 基础镜像时，同步发布 310P/910B image labels、cluster version filtering 和离线 package image lists。
- vLLM-Ascend engine definitions 通过外置 import package 发布和导入。
- 回滚 accelerator 能力：移除 `WithAcceleratorPlugins(ascend.NewPlugin())` 或切回不包含 Ascend plugin 的企业版镜像。
- 回滚监控能力：component profile 切回已知可用的 enterprise node-agent image，或停止部署 npu-exporter。
- 回滚 engine 能力：停止导入或导入旧版本 vLLM-Ascend engine package。

## 已确认决策

- Ascend 代码只在企业版展示，不进入社区版实现。
- 社区版不定义 Ascend-specific constants 或 symbols。
- 社区版只把 Enterprise accelerator type 当作 opaque string。
- 企业版主路径采用内部 Ascend accelerator plugin，不依赖独立 external plugin service。
- NPU plugin 仅在 Enterprise vendor 归一化后为 `smtx` 时注册。
- external REST plugin 注册保留为第三方或实验路径。
- `--accelerator-type` 必填；缺少配置直接退出。
- adapter 选择按配置优先；不从 metrics 自动识别 NVIDIA 或 Ascend。
- Ascend engine 通过外置 engine import package 导入，不通过控制面自动注入。
- Engine 不新增 `WithEngines(...)` dependency injection 入口。
- Ascend endpoint resource `accelerator.type` 保持 `npu`；不得使用 `npu-ascend310p` 或 `npu-ascend910b` 作为 endpoint accelerator type 或 engine image key。
- Enterprise node-agent 通过通用 component profile 被 Kubernetes 和 static cluster 选中；社区版不写死企业版镜像，Ascend accelerator profile 不承载 node-agent 镜像选择。
- node-agent 组件名继续保持 `neutree-node-agent`，只切换 image/args/env。
- 静态 Ray/SSH 集群和 Kubernetes 集群的整卡 NPU 支持在当前版本范围。
- 静态 Ray/SSH 集群需要区分 310P/910B runtime profile，并通过 `npu-ascend310p`/`npu-ascend910b` image suffix 选择对应 `neutree-serve` 基础镜像。
- 310P/910B runtime profile 用于 static cluster image suffix、image label 和 offline package image list；engine package 保持 `accelerator.type=npu`，通过不同 engine version 区分 310P/910B。
- 同一静态 Ray/SSH 集群内 310P 和 910B 混合运行不作为当前版本目标；支持或限制混合集群需后续单独设计。
- 主监控指标继续使用 `neutree_accelerator_*`。
- Ascend memory 语义是 HBM/on-chip memory 优先，DDR 仅 fallback。
- Ascend utilization 语义是 overall utilization 优先，AICore utilization 仅 fallback。
- 后续 vNPU allocation 优先 kubelet pod-resources，必要时 fallback 到 HAMi / Ascend device plugin annotations。
- Static Ray/SSH allocation 以 Ray Dashboard Actor `required_resources` 的整卡数和 Actor PID 为调度证据，通过后代 NPU `process_id` 关联 npu-exporter `vdie_id`；`ASCEND_VISIBLE_DEVICES` 与 `ASCEND_RT_VISIBLE_DEVICES` 仅作运行时诊断，不能作为 allocation 依据。
- vNPU 支持作为后续独立 Kubernetes-only roadmap，当前版本只保留 physical device 和 virtual device 两层 identity 边界。
- static Ray/SSH 首期仅支持整卡 NPU，不支持 vNPU。
- Ascend NPU 适用现有 accelerator unit license bucket；底层兼容字段仍是 `ResourceTypeGPU`，不新增 NPU license resource type。
- MindIE 不进入本次主路径；如需支持，应作为独立 engine import package。
