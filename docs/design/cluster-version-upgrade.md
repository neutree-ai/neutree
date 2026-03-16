# 集群版本升级设计文档

## 1. 背景

Neutree v1.0.1 版本对集群基础镜像进行了两项重要改进：

1. **升级 Ray 版本**：为适配 vLLM 版本升级的要求，将 Ray 运行时从旧版本升级到 Ray 2.53.0，以支持新版 vLLM 推理引擎的依赖。
2. **集群镜像轻量化**：对 `neutree-serve` 集群镜像进行了瘦身优化，移除了不必要的组件和依赖，减小镜像体积，加快部署速度。

然而，当前 Neutree 仅支持集群的创建和配置更新（如节点增减、参数变更），缺少对集群 serving 版本的升级能力。已创建的 v1.0.0 集群无法升级到 v1.0.1 及后续版本，用户只能通过删除重建的方式更新集群版本，这会导致服务中断和 Endpoint 丢失。

因此，需要为 Neutree 增加集群版本原地升级能力，支持用户在不销毁集群的前提下，将 SSH 和 Kubernetes 两种类型的集群平滑升级到新版本。

## 2. 目标

1. **支持集群原地版本升级**：用户通过修改 `spec.version` 即可触发升级，无需删除重建集群，保留现有 Endpoint 配置
2. **最小化业务中断时间**：SSH 集群通过阻塞式预拉取镜像（集群镜像 + 引擎镜像），将中断窗口压缩到集群重建的纯操作耗时；Kubernetes 集群利用 Rolling Update 实现零中断或近零中断
3. **升级状态可观测**：新增 `Upgrading` 阶段，与配置变更（`Updating`）明确区分，用户可实时感知升级进度
4. **支持手动回滚**：升级失败或不符合预期时，用户将 `spec.version` 改回原版本即可触发自动回滚，所有中间状态均可恢复
5. **提供版本查询能力**：提供 API 从镜像仓库查询可用的升级版本，辅助用户决策

## 3. User Story

**作为** Neutree 平台用户，

**我希望** 能够将已有集群从当前版本升级到更高版本，

**以便** 在不销毁集群、不丢失 Endpoint 配置的前提下，获得新版本的功能改进和性能优化。

### 验收标准

1. 用户可以查询指定集群的可用升级版本列表
2. 用户可以通过修改集群的 `spec.version` 字段触发版本升级
3. 升级过程中集群状态显示为 `Upgrading`，与配置变更（`Updating`）明确区分
4. SSH 集群升级前自动预拉取新版本镜像（包括集群镜像和引擎镜像），最大限度减少业务中断时间
5. Kubernetes 集群利用 Deployment 滚动更新机制，在 replicas > 1 时实现零中断或近零中断升级
6. 升级失败时，用户可以通过将 `spec.version` 改回原版本触发自动回滚，集群恢复到升级前状态
7. 升级完成后，集群状态自动恢复为 `Running`，`status.version` 更新为新版本

## 4. 整体设计

### 4.1 版本字段定义

| 字段 | 位置 | 说明 |
|------|------|------|
| `spec.version` | `ClusterSpec.Version` | 期望版本（用户设置） |
| `status.version` | `ClusterStatus.Version` | 实际运行版本（系统检测） |

### 4.2 集群阶段（Phase）

新增 `Upgrading` 阶段，`DetermineClusterPhase()` 判断优先级：

| 优先级 | 条件 | Phase |
|--------|------|-------|
| 1 | 资源就绪 | Running |
| 2 | 未初始化 | Initializing |
| 3 | `status.version != spec.version`（两者非空） | **Upgrading** |
| 4 | `observedSpecHash != currentHash` | Updating |
| 5 | 兜底 | Failed |

### 4.3 版本写入与读取

**SSH 集群**

- **写入**：`spec.version` 注入到 Docker 镜像 tag（`neutree-serve:<version>`）和 Ray 节点 label（`neutree.ai/neutree-serving-version`）
- **读取**：通过 Ray Dashboard `ListNodes()` 获取所有 ALIVE 节点的版本 label，取最高版本写入 `status.version`

**Kubernetes 集群**

- **写入**：`spec.version` 注入到 Router Deployment 的镜像 tag（`router:<version>`）和 label
- **读取**：从 Router Deployment 的 label 读取版本写入 `status.version`

### 4.4 SSH 集群升级流程

**为什么需要重建升级？**

SSH 类型的集群以 Ray 作为底座，Head 和 Worker 节点通过 Ray 集群协议通信。Ray 不支持跨版本兼容，也就是同一集群内不能混用不同 Ray 版本。由于集群版本升级涉及 Ray 版本变更（如 v1.0.0 → v1.0.1 包含 Ray 版本升级），无法对单个节点进行滚动替换，必须将整个集群先停止（`downCluster`），再以新版本统一拉起（`upCluster`），确保所有节点运行相同版本的 Ray。

```
prePullImages（阻塞：集群镜像 + 引擎镜像，所有节点并发拉取）  ← 业务正常运行
  ↓
downCluster（force stop all workers + ray down）               ← ⚠️ 业务中断开始
  ↓
upCluster(restart=true)（新版本镜像 ray up）
  ↓
reconcileWorkerNode（新版本镜像启动所有 worker）
  ↓
checkAndUpdateStatus（从 Ray Dashboard 读取 status.version）   ← ✅ 集群就绪
  ↓
Endpoint Reconcile → 推理实例重建（Ray Serve Application 重新部署） ← ⚠️ 业务中断持续
  ↓
推理实例就绪（模型加载完成，开始接受请求）                         ← ✅ 业务中断结束
```

**业务中断区间**：`downCluster` → 推理实例就绪。集群升级完成后，Endpoint 控制器会检测到推理实例不存在并触发重建，推理实例需要重新部署 Ray Serve Application 并加载模型，这一阶段的耗时取决于模型大小和加载速度。通过阻塞式预拉取镜像（集群镜像 + 引擎镜像），将集群自身的中断时间压缩到 down + up + startWorkers 的纯操作耗时，但完整的业务恢复还需加上推理实例重建时间。

### 4.5 Kubernetes 集群升级流程

```
reconcile → Router Deployment 更新镜像 + label
  ↓
K8s Rolling Update（旧 Pod 逐步替换为新 Pod）  ← ⚠️ 短暂中断或零中断
  ↓
getDeployedVersion → 写入 status.version
```

依赖 Deployment 的 Rolling Update 策略，replicas > 1 且 `maxUnavailable=0` 时可实现零中断。

### 4.6 手动回滚

用户通过将 `spec.version` 改回原版本触发回滚。所有升级中间状态均可自动恢复：

| 失败步骤 | 失败时集群状态 | 回滚恢复方式 |
|----------|--------------|-------------|
| prePullImages | Head=v1, Workers=v1 | 所有节点仍为 v1，自动恢复 |
| downCluster | 部分/全部节点停止，都是 v1 | reconcileHeadNode + reconcileWorkerNode 自动恢复 |
| upCluster(v2) | Head 未启动，Workers 已停止 | upCluster(v1) + reconcileWorkerNode 自动恢复 |
| reconcileWorkerNode | Head=v2, Workers 部分/未启动 | reconcileHeadNode 版本检查 → down+up(v1) + startWorkers(v1) 自动恢复 |

回滚状态流转：**Upgrading → Running**

关键机制：`reconcileHeadNode` 在检测到 Head 实际版本与 `spec.version` 不一致时，自动执行 `downCluster` + `upCluster` 重建集群。

### 4.7 可用升级版本查询 API

```
GET /clusters/:workspace/:name/available_upgrade_versions
```

从 Image Registry 查询镜像 tags，过滤出 semver 大于当前 `spec.version` 的版本，升序返回：

```json
{
  "current_version": "v1.0.0",
  "available_versions": ["v1.0.1", "v1.1.0"]
}
```

查询镜像根据集群类型区分：SSH → `neutree/neutree-serve`，K8s → `neutree/router`。

## 5. UX 设计

前端代码仓库：[neutree-ai/ui](https://github.com/neutree-ai/ui)

### 5.1 类型与状态补充

**ClusterPhase 枚举补充**（`src/domains/cluster/types.ts`）：

```typescript
enum ClusterPhase {
  PENDING = "Pending",
  RUNNING = "Running",
  PAUSED = "Paused",
  FAILED = "Failed",
  DELETED = "Deleted",
  INITIALIZING = "Initializing",
  UPDATING = "Updating",
  UPGRADING = "Upgrading",   // 新增
  DELETING = "Deleting",
}
```

**ClusterStatus 组件补充**（`src/domains/cluster/components/ClusterStatus.tsx`）：

为 `Upgrading` 阶段添加样式映射，建议使用蓝色（`bg-blue-100 text-blue-800`）区分于 Updating 的黄色。

### 5.2 集群详情页 — 升级入口

**位置**：集群详情页（`src/pages/clusters/show.tsx`）的右上角操作菜单（三点按钮），通过 `ShowPage` 的 `extraActions` prop 注入升级操作项。

**交互流程**：

1. 用户点击三点菜单，显示操作列表：**Upgrade** | Edit | Delete
2. Upgrade 按钮默认显示，无前置条件。可用版本由 `available_upgrade_versions` API 返回，无可用版本时在 Dialog 中提示
3. 点击 Upgrade 弹出升级对话框（Dialog）

**升级对话框（Upgrade Dialog）**：

```
┌─────────────────────────────────────────┐
│  Upgrade Cluster                     [×]│
│─────────────────────────────────────────│
│                                         │
│  Current Version:  v1.0.0               │
│                                         │
│  Target Version:   [v1.0.1        ▾]    │
│                                         │
│  ⚠ SSH clusters will experience         │
│    downtime during upgrade.             │
│                                         │
│              [Cancel]  [Upgrade]         │
└─────────────────────────────────────────┘
```

- **Current Version**：显示 `status.version`（当前运行版本）
- **Target Version**：下拉选择框，选项来自 `GET /clusters/:workspace/:name/available_upgrade_versions` 接口返回的 `available_versions`，默认选中最新版本
- **警告提示**：SSH 类型集群显示中断提示；K8s 类型集群显示滚动更新提示
- **Upgrade 按钮**：确认后通过 `useUpdate()` 更新集群的 `spec.version` 字段触发升级

**实现参考**：

新建组件 `src/domains/cluster/components/ClusterUpgradeAction.tsx`，参考 `EndpointPauseAction` 模式：

- 使用 `useCustom()` 调用 `available_upgrade_versions` API 获取可用版本
- 使用 `useUpdate()` 更新 `spec.version` 触发升级
- 使用 `useInvalidate()` 刷新详情页数据
- 使用 `sonner` toast 显示成功/失败通知

在 `show.tsx` 中注入：

```tsx
<ShowPage
  record={record}
  extraActions={(record) => (
    <ClusterUpgradeAction cluster={record as Cluster} />
  )}
>
```

### 5.3 集群列表页 — 版本列

**位置**：集群列表页（`src/pages/clusters/list.tsx`），在 Status 列之后添加 Version 列。

```tsx
<Table.Column
  header={t("common.fields.version")}
  accessorKey="status.version"
  id="version"
  enableHiding
  cell={({ row }) => {
    const { spec, status } = row.original;
    // 显示 status.version（实际版本），如果正在升级则显示升级箭头
    // e.g. "v1.0.0" 或 "v1.0.0 → v1.0.1"
  }}
/>
```

**显示逻辑**：
- 正常状态：显示 `status.version`（如 `v1.0.0`）
- 升级中（`status.phase === "Upgrading"`）：显示 `status.version → spec.version`（如 `v1.0.0 → v1.0.1`）

### 5.4 集群详情页 — 版本信息展示

在详情页 Basic 标签的基本信息卡片中，Status 行下方添加 Version 行：

```tsx
<ShowPage.Row title={t("common.fields.version")}>
  {record.status?.version ?? "-"}
  {record.status?.phase === "Upgrading" && (
    <span className="text-muted-foreground"> → {record.spec.version}</span>
  )}
</ShowPage.Row>
```

### 5.5 国际化（i18n）

在 `src/locales/en-US.json` 和 `src/locales/zh-CN.json` 中添加：

```json
{
  "clusters.actions.upgrade": "Upgrade",
  "clusters.actions.upgradeTitle": "Upgrade Cluster",
  "clusters.fields.currentVersion": "Current Version",
  "clusters.fields.targetVersion": "Target Version",
  "clusters.messages.upgradeSuccess": "Cluster upgrade initiated successfully",
  "clusters.messages.upgradeFailed": "Failed to initiate cluster upgrade",
  "clusters.messages.noUpgradeVersions": "No upgrade versions available",
  "clusters.messages.upgradeWarningSSH": "SSH clusters will experience downtime during upgrade. All running endpoints will be temporarily interrupted.",
  "clusters.messages.upgradeWarningK8s": "Kubernetes clusters use rolling updates with zero downtime.",
  "status.phases.cluster.Upgrading": "Upgrading"
}
```
