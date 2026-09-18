# 企业版部署与链路冒烟：2026-09-18

## 结论

`ssh dev`（172.21.152.146）的 **Kong → vmagent → VictoriaMetrics → Grafana** 已实测跑通。
可开始系统测试；这是一轮有限的开发环境冒烟，不是完整可观测性功能或发行升级验收。

看板：<http://172.21.152.146:3030/d/neutree-model-routing>
选择外部端点 `/workspace/default/external-endpoint/model-aggr`。

## 运行版本和部署范围

- Enterprise 分支：`demo/kong-routing-metrics`，提交 `9a19d15`，基于企业路由分支 `dca644f`。
- Community 运行时代码：`103445afb7d4`；之后的 `7e8ebd7f` 仅调整冒烟脚本等待时间。
- Core：`v1.2.1-demo-103445af-enterprise`，Vendor `SMTX`。
  不只检查版本字符串：`go version -m` 确认依赖提交，运行中的 `/proc/1/exe` SHA256 与构建二进制一致。
- Kong 3.9.1，两个 worker；vmagent v1.115.0；Grafana 11.5.3。
- API/前端沿用当前企业版构建；检查到现有 UI bundle 包含中文“虚拟模型”资源。

通过 Git 获取源码，使用企业版 Makefile 构建 Core 和携带企业部署资源的 CLI。
CLI `launch neutree-core --dry-run` 的真实渲染结果包含 license-server、Status 监听器和 vmagent 地址。
按原环境容器替换方式更新 Core，并将生成产物中的 Lua、共享字典/监听器配置、采集任务及看板应用到现有部署。
Kong 和 vmagent 按现有 Compose 项目选择性重建；Core 自动创建 Prometheus 插件，没有手工调用 Admin API 创建插件。

**没有执行全量 CLI launch、数据库迁移或完整镜像发布。** Core 二进制保存在当前容器可写层，
普通 restart 保留它，但重建 Core 容器会恢复镜像内的旧二进制。正式交付需构建并升级企业镜像。
API、Grafana、vmstorage 的启动时间和存储挂载保持不变。

## 实测证据

| 检查 | 结果 |
| --- | --- |
| Kong `/metrics/neutree` | HTTP 200，18 条 inflight 序列（其中 model-aggr 17 条） |
| vmagent 目标 | `neutree-gateway` 为 up，lastError 为空 |
| Prometheus 插件 | 企业 Core 自动创建 `neutree-prometheus`，enabled=true |
| model-aggr | spec 与部署前一致，状态 Running |
| 有并发上限的 `test-qwen-limited` | 两轮共 8 次 HTTP 200，qwen-internal 计数为 8；复测 4 次增量精确为 4 |
| 加权 `test-model-weighted` | 6 次 HTTP 200，smartp1=2、smartx2=4；这里只验证计数与分流展示，不验证小样本权重收敛 |
| 并发 | 测试中读到正值；存储中加权两路峰值分别为 1 和 2；两种虚拟模型最终均为 0 |
| 跨链路一致性 | Kong 原始计数、VictoriaMetrics 查询、Grafana datasource proxy 查询逐上游一致 |
| Grafana 浏览器 | 实际看到请求曲线、并发曲线、分流比例和虚拟模型选择器 |
| 访问日志 | Kong 日志确认上述 14 次请求均为 HTTP 200 |

为了测试创建了短期 API key；结束时按正常删除流程撤销，并确认网关返回 401，随后删除临时凭据文件。
测试没有修改 model-aggr 路由、权重或上游配置。

## 遇到的失败和修复

1. **vmagent 配置版本不兼容。** `dns_sd_configs.refresh_interval` 使 v1.115.0 严格解析失败并重启。
   先恢复原采集配置，在仓库删除该字段，使用默认 DNS 刷新周期，再应用构建产物。
   用同一镜像的 `-promscrape.config.dryRun` 验证：旧配置退出 255，修正配置退出 0。
2. **企业构建掩盖依赖更新失败。** `go get ...@demo/kong-routing-metrics` 因分支名含 `/` 报错，
   原 Makefile 继续执行 tidy 和构建，产生新版本标识配旧依赖的二进制，未自动启用指标插件。
   改用准确提交号，并给 sync-community 的 shell recipe 增加 `set -e`。
   反向测试确认无效版本立即失败，尚未进入资产同步；重新构建后核对二进制依赖，插件自动创建成功。
3. **现有部署配置漂移。** 原运行容器有 inflight 共享字典，但旧 Compose 没有；
   初次选择性重建后指标入口 503。补入新 CLI 渲染结果中的共享字典及 worker 配置后恢复。
   同时重建 Kong 时 Core 也曾因网关尚未就绪而启动重试，随后正常运行。
4. **验证等待时间不足。** 首轮两个模型都通过 HTTP 和原始计数校验，但 30 秒存储等待失败。
   核对 vmselect 默认 `search.latencyOffset=30s`，并观察到之后查询读数一致。
   将脚本等待改为 90 秒，未修改服务查询设置。Qwen 复测通过，存储可见等待 32.87 秒；
   加权首轮随后逐上游核对得到 2/4，与 Kong 一致，没有把原脚本失败标成通过。

## 继续测试与恢复

尚未覆盖：Kubernetes/Headless Service 的真实部署、多 Kong Pod 汇总、流式结束/取消、
上游错误、路由热更新及删除、长时间运行。当前环境可继续系统测试。

远端证据及恢复备份：`/root/workspace/.neutree-test/kong-metrics-24993297/`。
其中 `deploy-before.tgz` 保存原 Compose/Lua/采集配置，`neutree-core.before` 保存原 Core；
`final-smoke.json`、`*-smoke*.log`、`config-check-*.log` 保存检查结果。
目录还含受保护的部署配置，不应上传仓库或公开分享。

恢复时需同时恢复 Core、Compose、Lua 和采集配置，重建相关服务，并移除本轮新增的看板/插件。
不要删除 volume/PVC 或清空 VictoriaMetrics。此次新增指标按存储保留周期正常过期。
