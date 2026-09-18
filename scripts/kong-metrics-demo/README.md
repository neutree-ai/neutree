# Kong 路由指标 Demo

本分支从 `origin/main` 的 `62fe9c7c` 开始。目标是在现有 Kong 上使用真实
`model-aggr`，验证 **插件指标 → vmagent → VictoriaMetrics → Grafana**。
2026-09-18 已获准在 `ssh dev` 的企业版环境部署，并完成真实请求的链路冒烟。
结果、失败与修复、部署范围见 [部署记录](deployment-smoke-20260918.md)。
本轮使用现有容器更新方式，不等同于完整发行安装/升级验收。

## 改动与验收

- `routing.lua`：无上限目标也维护已有 inflight；有上限时仍用同一计数判断容量。
- `metrics.lua`：复用 Kong 自带 Prometheus exporter 注册两类指标。
  - `kong_neutree_route_requests_total`：已选中目标且结束的请求数（包括上游错误）。
  - `kong_neutree_route_inflight`：抓取时直接读取 `neutree_ai_gateway_inflight`，
    不新建一套并发累加/释放逻辑。
- `handler.lua`：worker 初始化注册指标，配置更新刷新目标列表，log 阶段记请求数。
- `status_api.lua`：只在 Kong Status 监听器提供 `/metrics/neutree`。
  自带 `/metrics` 不会主动读取我们的 admission 字典，因此提供这个小的采集入口，
  先刷新本次响应的本地 gauge，再调用原 exporter 输出；不修改 Kong 源码或安装额外 exporter。
- 标签：`endpoint`（包含 workspace 的端点路径）、`virtual_model`、`upstream`、`upstream_model`。
  指标不包含凭据、请求正文或 request ID。

验收目标：

1. 实际 Kong 3.9.1 能注册并输出这些指标。
2. 所有配置目标（含不限流目标）有 inflight 序列，空闲时为 0。
3. 真实请求进行时出现正的 inflight，结束后归零；请求计数增量等于实际完成请求数。
4. vmagent 采集到 VictoriaMetrics 的请求计数与直接读取值一致。
5. 看板能够按虚拟模型对比上游请求数、实际分流比例、当前并发。

## 仓库内的部署链路

| 环节 | 仓库中的接入 |
| --- | --- |
| 自动启用 exporter | `internal/gateway/kong.go` 的 Init 幂等同步全局 `neutree-prometheus` 插件 |
| Docker Kong 监听 | Compose 为 Kong 设置 `KONG_STATUS_LISTEN=0.0.0.0:8100`，不发布宿主机端口 |
| Docker 发现 | Compose 为 vmagent 设置 `NEUTREE_KONG_METRICS_HOST=kong` |
| Helm 发现 | Kong 已有 8100 Status 监听；新增 headless Service，vmagent 通过完整集群 DNS 名分别发现 Pod |
| 统一抓取任务 | `observability/vmagent/prometheus.yml` 的 `neutree-gateway` job，抓取 `/metrics/neutree` |
| 看板自动分发 | `observability/grafana/dashboards/neutree_model_routing_dashboard.json`，由既有 vendir、Grafana provisioning 分发加载 |
| 安装包内容 | `make prepare-build-cli` 同步 Lua、采集配置、看板并重新生成 CLI 内嵌的两个 tar |

vmagent 使用官方支持的 `%{ENV_VAR}` 配置替换机制：
https://docs.victoriametrics.com/vmagent/#how-to-collect-metrics-in-prometheus-format
Docker 和 Helm 各自注入 DNS 名，共用同一份抓取配置。
Helm 的 `global.clusterDomain` 默认 `cluster.local`，自定义集群 DNS 后缀时一并设置。
没有配置指标存储的部署，继续按原规则不启动 vmagent。

部署时应从本分支重新构建控制面/CLI/Chart，按现有升级入口部署；
不能只替换 Lua，也不需要手工向 Kong Admin API 创建 Prometheus 插件或手工追加抓取 job。
企业版通过已有 `scripts/sync-community.sh` 导入本分支对应提交的部署资源，并将 Go 依赖更新到同一提交。
企业版 UI 与中文资源沿用既有构建流程；本次没有 UI 源码修改或新增镜像。
本轮验证了企业版构建和 CLI 渲染，并选择性应用到现有容器；没有运行整套 `launch` 升级或数据库迁移。
企业构建建议传入准确的 Community 提交号；含 `/` 的分支名不能直接用于 `go get`。

## 可重复验证

`verify.py` 仅发送有界的真实推理请求并读取指标，不做部署、重启或资源配置修改。
默认每次 10 个请求，最多并发 2，每个请求最多生成 8 tokens。
访问密钥通过 `ENDPOINT_API_KEY` 环境变量提供，不打印或保存。
用量会由现有平台正常计量；选择对应上游可正常调用的模型。

```bash
python3 scripts/kong-metrics-demo/verify.py \
  --gateway http://172.21.152.146 \
  --metrics http://<Kong容器IP>:8100/metrics/neutree \
  --query-url http://172.21.152.146:8481/select/0/prometheus \
  --model test-model-weighted
```

可逐个选择现有 `test-model`、`test-model-cross`、`test-qwen`、
`test-qwen-limited`、`test-qwen-primary` 等模型。先用并发 1 验证计数，
再对不限流或容量足够的模型验证并发。脚本要求该虚拟模型在测试期间没有其他调用，
否则总量比较不成立。上游失败、未选中目标、没有采到正并发都会明确失败，不能算通过。

单元测试用假的 exporter 验证标签、读取、清理行为，不证明真实 exporter 或采集链已接通。
真实上游和存储链路已做冒烟；跨 worker 的系统性校验、配置重载、流式取消及多 Pod 仍需专门测试。
现有 vmselect 默认 `search.latencyOffset=30s`，脚本最多等待 90 秒核对存储读数。
DNS 刷新使用 vmagent 默认值（本环境为 30 秒）；其 v1.115.0 不支持 `dns_sd_configs.refresh_interval` 字段。

本地验证：

- `make gateway-lua-test`：30 个测试通过。
- `python3 -m unittest discover -s scripts/kong-metrics-demo -v`：3 个测试通过。
- `go test ./internal/gateway -count=1`：通过，覆盖自动启用 exporter 和重复初始化。
- `go test ./cmd/neutree-cli/app/cmd/launch -run TestPrepareCoreWiresKongMetrics -count=1`：
  通过，实际解包 CLI 资源并渲染 Compose，检查监听器、采集地址、Lua 文件，以及未配置远程写入时不启动 vmagent。
- `make prepare-build-cli`、`helm lint`、`helm template`：通过。
  渲染结果检查了自定义集群域名、headless Service 与 Kong Pod selector、Status 端口、采集配置及 Lua/看板 ConfigMap。

在临时副本恢复“不限流就跳过计数”后，Lua 测试变为 29 通过、1 失败，
失败项正是不限流目标的并发计数检查；正式工作树保留该改动。
使用 Go overlay 临时恢复 main 的网关初始化代码后，新测试因缺少 exporter 自动启用而失败，
确认这一部署接线不可省略。两个反向验证均未改动运行环境。

## 后续环境验证与恢复

评审通过后再升级现有环境，先备份部署配置和插件、记录现有版本与存储挂载。
环境验收完成后需要恢复时，回到原版本和原部署配置，删除本次自动创建的
`neutree-prometheus` 插件实例及新增看板（如原来没有）。不删除 volume/PVC，
不清空 VictoriaMetrics；已写入的指标按正常保留周期过期。

## 边界与简化

本次不做日志增强、延迟/Token/成功率指标、未选择目标计数、生产级指标生命周期治理。
请求计数只覆盖 model_routes 已选中目标的请求，不能作为所有入口请求的总量。
重启 Kong 后内存 counter 清零，Grafana 使用 `increase` 处理 counter reset；历史数据留在 VictoriaMetrics。
配置删除后 inflight 序列在下一次抓取移除；历史请求 counter 的序列仍保留在当前实例注册表中。
正式上线需要决定这部分清理策略，不把 demo 视作完整可观测性功能。

消融取舍：取消独立 demo Kong、模拟上游、手工部署封装；不维护第二份 inflight gauge，
不增加定时同步任务，保留直接读取现有计数的采集入口。
