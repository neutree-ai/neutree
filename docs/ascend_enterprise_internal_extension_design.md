# Ascend 企业版集成设计

## 背景

本文档定义 Ascend 能力如何以企业版能力集成到 Neutree，同时保持社区版只提供通用扩展点，不包含 Ascend 专属实现、常量、指标名、镜像、chart 或运行时逻辑。

本设计按 3 条 roadmap 组织：

1. 企业版集成
2. 静态集群和 Kubernetes 集群支持
3. Engine 支持

权威输入：

- 社区版 Neutree：
  - `cmd/neutree-core/app/builder.go`
  - `internal/accelerator/manager.go`
  - `internal/accelerator/plugin`
  - `internal/engine/registry.go`
  - `cmd/neutree-cli/app/cmd/packageimport/engine.go`
  - `internal/cli/packageimport`
- 企业版 Neutree：
  - `neutree-enterprise/cmd/neutree-core/neutree-core.go`
- 已有 Ascend Plugin：
  - `neutree-ascend/internal/plugin`
  - `neutree-ascend/internal/engine`
  - `neutree-ascend/dist/*manifest.yaml`

## 总体原则

- 社区版只提供通用扩展边界，不出现 Ascend 专属符号。
- Ascend accelerator type 由企业版拥有。现有 Ascend Plugin 使用 `npu`，社区版只把它当作 opaque string 透传。
- Ascend accelerator plugin 和 Ascend engine 分开交付。
- Accelerator plugin 走企业版内部集成，注入到 `neutree-core`。
- Ascend engine 不走控制面自动注入，也不新增 `WithEngines(...)`。Engine 通过外置 engine import package 导入。
- 静态 Ray/SSH 集群和 Kubernetes 集群的整卡 NPU 支持纳入当前版本范围。
- 监控、node-agent 适配和 vNPU 不属于本设计或当前版本范围；如需支持，另行立项和设计。

## 当前版本范围

当前版本只收敛以下能力：

- 企业版内部 Ascend accelerator plugin 注册和 vendor gate。
- 静态 Ray/SSH 与 Kubernetes 的整卡 NPU 资源发现、资源转换、资源解析和运行时注入。
- `accelerator.type=npu` 的 endpoint resource 语义，310P/910B 通过 `accelerator.product`、static runtime profile 和 engine version 区分。
- 静态 Ray/SSH 的 310P/910B `neutree-serve:<version>-npu-ascend*` 基础镜像、image labels、cluster version 过滤和离线 image list。
- vLLM-Ascend 通过外置 engine import package 导入，不新增 engine dependency injection。
- license 继续复用底层 `ResourceTypeGPU`，但业务语义是 accelerator unit，且只按物理 GPU/NPU 卡数计数。

当前版本不包含：

- 监控、node-agent adapter、npu-exporter、dashboard 和 vNPU。
- 同一静态 Ray/SSH 集群内 310P/910B 混合运行的支持或显式限制。
- 独立 NPU license resource type。
- MindIE 主路径。
- 社区版中的 Ascend 常量、Ascend runtime、Ascend metrics parser 或 Ascend engine 内置定义。

## 当前状态

### 社区版

`cmd/neutree-core/app/builder.go` 已支持通过 `app.Builder` 注入 controller 和 reconcile hooks，但还没有 accelerator plugin 注入入口。企业版 `cmd/neutree-core` 当前基于该 builder 注入 license、签名和 feature hooks。

社区版 accelerator plugin 目前主要由 `internal/accelerator/manager.go` 和 `internal/accelerator/plugin` 管理。内置 NVIDIA/AMD plugin 可以在社区版进程内注册，外部 plugin 可以通过 `/v1/plugin/register` 注册。Enterprise 作为独立 Go module 不能直接 import 社区版 `internal` package，所以需要 public accelerator extension boundary。

Engine 已有独立导入链路。`neutree-cli import engine` 支持 archive 和 manifest 两种输入，最终通过 `/v1/engine/register` 注册 engine metadata。`workspace_controller` 会在 workspace reconcile 时将 registry 中的 engine 同步到 workspace DB。该路径已经满足 Ascend engine 导入，不需要新增 builder dependency injection。

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

### 设计约束与 Roadmap 边界

- Roadmap 1 交付进程内 extension contract、Enterprise 注册、feature gate 和 accelerator quota 语义；它不验收 SSH/Kubernetes 集群发现、runtime image、endpoint 部署或 engine import。这些运行时路径分别由 Roadmap 2-3 验收。
- 社区版只认识 plugin resource 的 opaque string，不增加 `npu`、Ascend 型号、`npu-smi`、Huawei resource name 或 Ascend runtime 常量。
- 对外 canonical accelerator family 是 Enterprise-owned 的 `npu`。`npu-ascend310p`、`npu-ascend910b` 是 legacy/runtime profile：feature gate 必须兼容它们，但它们不能成为 `AcceleratorGroups` 的独立计数键，也不能作为 manager 注册的第二个 plugin resource。
- 现有 Enterprise ADR 中 variant-as-type 的历史输入继续兼容；后续 Roadmap 按 `family=npu + runtime profile + product` 收敛。Roadmap 1 不迁移已有 cluster 数据，也不改变 API schema。
- `pkg/accelerator` 位于社区 L1，只能依赖 `api/v1` 和 Kubernetes 公共类型；不得依赖 `internal/`。这使独立 Go module 的 Enterprise 能直接实现 contract。

### 当前实现校正

当前 `options.Config()` 已在 `app.Builder` 之前创建 `accelerator.NewManager(e)`，因此仅在 `Builder.Build()` 前向全局 registry 写入 plugin 不足以保证 controller 使用同一个 manager，且会重复挂载 `/v1/plugin/register`。Roadmap 1 必须将 manager 的唯一创建点移到 builder 构建期：`options.Config()` 只准备 `GinEngine`，`Builder.Build()` 在 controller factory 执行前创建 manager 并写回 `CoreConfig.AcceleratorManager`。

### 社区版改动

社区版新增 public accelerator extension package：

```text
github.com/neutree-ai/neutree/pkg/accelerator
```

该 package 提升现有 plugin contract，而不是新增 Enterprise DTO/adapter 层。它定义以下通用接口和 generic plugin type 常量：

```go
type Plugin interface {
    Resource() string
    Type() string
    Handle() PluginHandle
}

type PluginHandle interface {
    GetNodeAccelerator(context.Context, *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error)
    GetNodeRuntimeConfig(context.Context, *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error)
    DetectStaticNodeAccelerator(context.Context, *v1.DetectStaticNodeAcceleratorRequest) (*v1.DetectStaticNodeAcceleratorResponse, error)
    GetContainerRuntimeConfig() (v1.RuntimeConfig, error)
    GetAcceleratorProfile(context.Context) (*v1.AcceleratorProfile, error)
    GetResourceConverter() ResourceConverter
    GetResourceParser() ResourceParser
    Ping(context.Context) error
}

type ResourceConverter interface {
    ConvertToRay(*v1.ResourceSpec) (*v1.RayResourceSpec, error)
    ConvertToKubernetes(*v1.ResourceSpec) (*v1.KubernetesResourceSpec, error)
}

type ResourceParser interface {
    ParseFromRay(map[string]float64) (*v1.ResourceInfo, error)
    ParseFromKubernetes(map[corev1.ResourceName]resource.Quantity, map[string]string) (*v1.ResourceInfo, error)
}
```

`internal/accelerator/plugin` 和 `internal/accelerator/resourceparser` 通过 type alias 使用该 contract，保留 NVIDIA/AMD 的现有实现和测试入口。`internal/accelerator/manager` 对外继续暴露既有 manager 行为；此迁移不改变 API request/response schema。

社区版 `app.Builder` 增加 accelerator plugin 注入入口：

```go
func (b *Builder) WithAcceleratorPlugins(plugins ...accelerator.Plugin) *Builder
```

`WithAcceleratorPlugins` 复制输入 slice，避免调用方后续修改影响 builder。`Build()` 在创建任一 controller 前调用 `accelerator.NewManager(b.config.GinEngine, b.acceleratorPlugins...)`，并将返回实例写入 `CoreConfig.AcceleratorManager`；`NewManager` 改为返回错误，以便 builder 将非法注入转化为启动失败。

manager 初始化顺序固定为：

1. 加载现有本地 NVIDIA/AMD plugins。
2. 校验并加载 builder 注入的 internal plugins。
3. 在同一个 `GinEngine` 上注册一次 `/v1/plugin/register`。
4. 将该 manager 注入所有 controller factory，并由 `App.Run()` 启动它。

注入 validation 必须拒绝 nil plugin、空 `Resource()`、与本地 plugin 重复的 resource、以及同一 builder 调用中的重复 resource。外部 REST plugin 的注册、heartbeat 和健康移除逻辑保持不变；它不能覆盖任一 internal plugin。社区版仍保留 `/v1/plugin/register`，但它不是企业版 Ascend 主路径。

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

旧 plugin server 的 `Run()` 和 `Register()` 不迁移到主路径。新 `ascend.Plugin` 直接实现 public `accelerator.Plugin`：`Type()` 返回 generic internal plugin type，`Handle()` 返回自身，`DetectStaticNodeAccelerator()` 复用 node detection 结果并填充标准 response，`Ping()` 对进程内 plugin 返回 nil。`GetAcceleratorProfile()` 返回 `AcceleratorType=npu`、通用 Ascend cluster/engine runtime；`MetricsExporter` 保持 nil，本设计不定义监控能力。

`npu_types.go`、converter 和 parser 保留在 Enterprise package。parser 只能生成一个 `AcceleratorGroups["npu"]` family group；310P/910B 数量仅出现在该 group 的 `ProductGroups`。converter 只在 `accelerator.type=npu` 时工作，并继续要求 Kubernetes 请求带可识别 product。这样 converter/parser 已随进程内 plugin 可用，但真实集群路径仍由 Roadmap 2 验收。

Enterprise 在组装层集中处理 vendor gate：

```go
builder := app.NewBuilder().
    WithConfig(c)

featureResolver := hook.NoopAcceleratorFeatureResolver{}
if vendor.IsSMTX(vendor.Vendor) {
    builder.WithAcceleratorPlugins(ascend.NewPlugin())
    featureResolver = ascend.NewFeatureResolver()
}

builder.WithAfterReconcileHook(
    "cluster",
    hook.CheckClusterFeatureHook(objectStorage, licenseClient, featureResolver),
)
```

`pkg/vendor` 提供 `NormalizeVendor(value string) string` 和 `IsSMTX(value string) bool`，统一执行 trim/lowercase。所有 vendor gate 使用这些 helper，包括 Ascend plugin 注册、license feature 集合选择和后续 NPU feature 开关；license certificate 的 vendor matching 同样使用 canonical value 比较。禁止在调用点手写 `strings.ToLower(...) == "smtx"`。

建议工具函数：

```go
func NormalizeVendor(value string) string {
    return strings.ToLower(strings.TrimSpace(value))
}

func IsSMTX(value string) bool {
    return NormalizeVendor(value) == "smtx"
}
```

vendor 不是 `smtx` 时，企业版仍正常启动，但不注册 NPU plugin，不注入 Ascend feature resolver，也不暴露相关 runtime、resource conversion 或 profile 能力。

企业版 license/feature gate 使用同一个 Enterprise-owned accelerator type。现有实现如果继续使用 `npu`，则以下位置必须一致：

- Ascend plugin `Resource()`；
- cluster config/status accelerator type；
- endpoint `resources.accelerator.type`；
- license feature hook 判断。

资源配额首期沿用现有 license resource type，但代码语义应调整为 accelerator unit，而不是继续把业务概念称为 GPU。当前企业版 license 协议只有 `ResourceTypeGPU = "GPU"` 和 `ResourceTypeWorkspace`，为了兼容已有 license 证书、license server、trial license、API 和用量记录，本轮不新增 `ResourceTypeNPU` 或 `ResourceTypeAccelerator`。

处理方式：

- license wire protocol / persisted resource type 继续使用 `types.ResourceTypeGPU`。
- 企业版业务代码新增 `AcquireAcceleratorLicenseHook` 和 `GetClusterRequiredAcceleratorUnits`；为避免破坏现有 Enterprise package consumer，旧 GPU 命名 helper 可作为薄兼容 wrapper 保留一个版本。
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

    if cluster == nil || cluster.Status == nil || cluster.Status.ResourceInfo == nil || cluster.Status.ResourceInfo.Allocatable == nil {
        return 0, nil
    }

    groups := cluster.Status.ResourceInfo.Allocatable.AcceleratorGroups
    for _, group := range groups {
        if group == nil || group.Quantity < 0 || math.IsNaN(group.Quantity) || math.IsInf(group.Quantity, 0) || group.Quantity != math.Trunc(group.Quantity) {
            return 0, fmt.Errorf("accelerator quantity must be a non-negative whole physical-device count")
        }
        units += group.Quantity
    }

    if math.IsNaN(units) || math.IsInf(units, 0) || units != math.Trunc(units) {
        return 0, fmt.Errorf("accelerator license units must be whole physical devices")
    }

    return int(units), nil
}
```

该 helper 不读取 `ProductGroups`，也不按 vendor/type 分支。`ProductGroups` 可以用于校验 `Quantity` 来源是否合理，但不能作为 license unit 的第二个计数来源。

`CheckClusterFeatureHook` 不再直接比较 `"npu"`，而是接收 Enterprise 组装层注入的 `AcceleratorFeatureResolver`。Ascend resolver 仅将 trim/lowercase 后的 `npu`、`npu-ascend310p`、`npu-ascend910b` 映射为 `AscendNPUFeature`；其他输入返回空 feature 集。quota hook 不依赖该 resolver，也不识别 vendor、型号、product 或 runtime profile。

计数规则：

- 同一物理加速卡只能在 `AcceleratorGroups` 中计入一次。
- runtime profile 和 product group 不能额外增加 license unit。
- 如果 parser 同时产出 family group 和 runtime-profile group，license helper 必须先归一或拒绝该状态，避免重复扣减。

### 影响面与测试版本矩阵

| Surface | 结论 | 证据和设计动作 |
| --- | --- | --- |
| Backend / controller | 适用 | public contract、manager 创建时序和 Enterprise license hook 都改变；无 DB migration。 |
| `neutree-enterprise` | 适用 | 独立 module 实现 Ascend plugin、vendor helper 和 feature resolver；以 `COMMUNITY_VERSION=<sha> make sync-community` 验证依赖同步。 |
| OSS UI/UX、Enterprise UI/UX、i18n、vendor branding | N/A | 不新增 API field、status、用户可见文案或页面；UI 无 consumer contract 变更。 |
| API/UI contract | N/A | `accelerator.type` 已是 opaque string，Roadmap 1 不修改 `api/v1` schema。 |
| DB | N/A | license wire/persisted resource type 继续使用 `ResourceTypeGPU`，不迁移数据。 |
| E2E / manual verification | N/A | 本 Roadmap 按已确认范围只执行 unit test；真实 NPU 环境、集群部署和手工 E2E 转交 Roadmap 2。 |

| Test Version Field | Roadmap 1 约定 |
| --- | --- |
| Code under test | community `chore-ascend-enterprise-roadmap-1` 与对应 Enterprise branch；实现开始前记录 SHA，PR 创建后回填 PR URL。 |
| Deployed artifacts | `N/A`；本 Roadmap 不部署 `neutree-core`、chart 或 engine。 |
| Edition | `both`；community 验证 generic contract，Enterprise 验证 SMTX composition 和 license semantics。 |
| Environment / lease | `N/A`；只需 Go unit-test toolchain，不申请 E2E lease。 |
| Rerun rule | community contract、Enterprise plugin/vendor/license 任一代码变更后，重跑受影响的 `go test` package；仅 docs 或 `.agent-notes` 更新无需重跑。 |

### 验证设计

- 社区版 unit test：外部 fake plugin 可实现 public contract；builder/manager 注入后所有 converter、parser、profile lookup 走同一 manager；nil、空 resource 和重复 resource 启动失败；NVIDIA/AMD 和 REST registration 不回归。
- Enterprise unit test：vendor normalization 覆盖大小写和空白；SMTX 才生成 plugin/resolver；Ascend plugin 完整实现 contract；310P/910B parser 只生成 `npu` family group；feature resolver 覆盖 family 与两个 legacy profile。
- license unit test：NVIDIA、AMD、NPU physical quantity 汇总一次；`ProductGroups` 不影响配额；nil status 返回零；负数、NaN、Inf 与小数数量返回错误；底层请求仍使用 `ResourceTypeGPU`。
- TestRail：`C2735296`，记录社区 contract/manager 与 Enterprise plugin/license 的定向 unit-test evidence。
- 验收不以现有 `go test ./...` 作为唯一证据：当前仓库缺少嵌入式 `cmd/neutree-cli/app/cmd/launch/manifests/neutree-core.tar`，全量命令在任务开始前已失败。实现阶段使用受影响 package 的定向 unit test，并记录该基线限制。

### 交付物

- 社区版 `pkg/accelerator` public contract、internal alias 和 builder 注入入口。
- 单一 manager 创建时序、internal plugin validation 及对现有 REST registration 的兼容。
- Enterprise `pkg/accelerator/ascend` in-process plugin package，不依赖独立 REST service。
- Enterprise vendor normalize helper、SMTX composition、feature resolver 与 accelerator-unit license helper。
- `ResourceTypeGPU` 作为 accelerator quota bucket 的兼容说明、非法计数量拒绝规则及 unit-test matrix。

### 验收

- 社区版不包含 Ascend 专属符号或实现。
- 企业版启动后，无需外部 REST plugin service 即可获取 Ascend runtime/resource/profile 能力。
- vendor 不是 `smtx` 时，企业版不注册 NPU plugin，相关 Ascend resource conversion 和 profile lookup 不可用。
- Ascend NPU cluster 的物理 accelerator 数量会计入现有 `GPU` license usage；product group 和 runtime profile 不会重复计数，非法计数会被拒绝。
- 移除企业版 `WithAcceleratorPlugins(ascend.NewPlugin())` 后，Ascend runtime/resource conversion 能力消失，NVIDIA/AMD 现有行为不受影响。
- Roadmap 1 验收只依赖定向 unit test；静态 Ray/SSH 与 Kubernetes NPU 集群 E2E 不作为本 Roadmap 的通过条件。

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

## Roadmap 依赖关系

推荐顺序：

1. 企业版集成：先打通 public accelerator extension 和 enterprise internal plugin。
2. 静态集群和 Kubernetes 集群支持：迁移已有 Ascend Plugin 的资源探测、runtime、parser/converter。
3. Engine 支持：并行准备 vLLM-Ascend import package，但不阻塞 accelerator plugin。

硬依赖：

- Roadmap 2 依赖 Roadmap 1。

可并行：

- Roadmap 3 可与 Roadmap 1/2 并行，因为 engine 通过 import package 导入，不依赖 control-plane builder 注入。

## 测试策略

### Unit test

社区版：

- public accelerator extension interface 和 manager 注册逻辑。
- `WithAcceleratorPlugins` builder 注入逻辑。
- opaque accelerator type 透传，不依赖 Ascend 常量。
- engine import package 仍可通过 `/v1/engine/register` 注册 engine definitions。

企业版：

- vendor gate：vendor 归一化为 `smtx` 时注册 NPU plugin，非 `smtx` 时不注册。
- vendor normalize 工具函数覆盖 `SMTX`、`smtx`、混合大小写和首尾空白。
- Ascend plugin 和 license/feature gate 复用同一个 vendor 判断函数。
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
- vLLM-Ascend engine import package manifest/schema/template 构造。

### DB test

- accelerator type 继续作为 string 透传时，不需要数据库迁移。
- workspace engine sync 覆盖 import package 注册 engine 后 create/update engine DB 记录。
- vLLM-Ascend 版本 merge 不覆盖已有 vLLM versions。
- 重复导入同名 engine 时，merge 行为符合现有 `util.MergeEngine` 语义。

### E2E test

社区版：

- NVIDIA/AMD 既有 accelerator plugin 行为保持兼容。

企业版：

- 企业版 `neutree-core` 启动后内置 Ascend accelerator plugin。
- Ascend static Ray/SSH cluster 能识别 NPU resource、product group、runtime image suffix 和环境变量。
- Ascend static Ray/SSH cluster 能按 runtime profile 拉取正确 `neutree-serve:<version>-npu-ascend*` 镜像。
- Ascend Kubernetes cluster 能识别 `huawei.com/Ascend*` resource。
- Ascend NPU cluster 的物理 accelerator 数量能计入现有 accelerator unit license usage。
- vLLM-Ascend engine import package 导入后可部署 text-generation endpoint。
- 离线 cluster package 分别使用 310P/910B variant image list，并包含正确的 static cluster base image。

### Manual testing

如果 CI/E2E 环境没有 Ascend NPU 硬件，需要手动验证：

- 真实 `npu-smi info -m` 输出解析。
- Ascend Docker runtime 注入设备。
- vLLM-Ascend 在 310P/910B 上启动和推理。
- 310P/910B static cluster 基础镜像的 CANN/runtime 依赖与真实硬件匹配。

## 发布与回滚

社区版发布：

- public accelerator extension 和 builder 注入入口需要配套发布。
- engine import/register API 不新增控制面注入入口，保持现有行为。

企业版发布：

- Enterprise Ascend accelerator plugin 和 vLLM-Ascend engine import package 分开交付，但版本需要兼容。
- `cmd/neutree-core` 只在 vendor 为 `smtx` 时注入 Ascend accelerator plugin。
- 发布 Ascend static cluster 基础镜像时，同步发布 310P/910B image labels、cluster version filtering 和离线 package image lists。
- vLLM-Ascend engine definitions 通过外置 import package 发布和导入。
- 回滚 accelerator 能力：移除 `WithAcceleratorPlugins(ascend.NewPlugin())` 或切回不包含 Ascend plugin 的企业版镜像。
- 回滚 engine 能力：停止导入或导入旧版本 vLLM-Ascend engine package。

## 已确认决策

- Ascend 代码只在企业版展示，不进入社区版实现。
- 社区版不定义 Ascend-specific constants 或 symbols。
- 社区版只把 Enterprise accelerator type 当作 opaque string。
- 企业版主路径采用内部 Ascend accelerator plugin，不依赖独立 external plugin service。
- NPU plugin 仅在 Enterprise vendor 归一化后为 `smtx` 时注册。
- external REST plugin 注册保留为第三方或实验路径。
- Ascend engine 通过外置 engine import package 导入，不通过控制面自动注入。
- Engine 不新增 `WithEngines(...)` dependency injection 入口。
- Ascend endpoint resource `accelerator.type` 保持 `npu`；不得使用 `npu-ascend310p` 或 `npu-ascend910b` 作为 endpoint accelerator type 或 engine image key。
- 静态 Ray/SSH 集群和 Kubernetes 集群的整卡 NPU 支持在当前版本范围。
- 静态 Ray/SSH 集群需要区分 310P/910B runtime profile，并通过 `npu-ascend310p`/`npu-ascend910b` image suffix 选择对应 `neutree-serve` 基础镜像。
- 310P/910B runtime profile 用于 static cluster image suffix、image label 和 offline package image list；engine package 保持 `accelerator.type=npu`，通过不同 engine version 区分 310P/910B。
- 同一静态 Ray/SSH 集群内 310P 和 910B 混合运行不作为当前版本目标；支持或限制混合集群需后续单独设计。
- Ascend NPU 适用现有 accelerator unit license bucket；底层兼容字段仍是 `ResourceTypeGPU`，不新增 NPU license resource type。
- MindIE 不进入本次主路径；如需支持，应作为独立 engine import package。
