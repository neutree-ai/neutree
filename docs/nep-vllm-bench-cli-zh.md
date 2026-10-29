# NEP: 通过 CLI 快速 Benchmark 托管推理实例

> **单文件兼容格式**：当前仓库没有确认的 NEP 目录、编号或 `nep.yaml`
> 体系；本次按用户指定路径原地维护等价元数据，不新增 sidecar 文件，也不
> 代表仓库已经采纳统一 NEP 制度。

## 元数据

| 字段 | 值 |
|---|---|
| ID | `draft-vllm-bench-cli` |
| 标题 | 通过 CLI 快速 Benchmark 托管推理实例 |
| 状态 | `provisional` |
| 组件 | `neutree-cli`、`pkg/client`、Kubernetes runtime、static Ray runtime |
| 阶段 | `experimental` |
| Feature gate | 无；命令位于 `exp` 命名空间，按调用启用 |
| 禁用支持 | 支持；不调用命令即可禁用，不写入控制面状态 |
| 新增平台指标 | 无 |
| 目标版本、跟踪 Issue、Owner、Reviewers、Approvers | 尚未指定，需在评审流程中确认 |
| 权威实现参考 | `cmd/neutree-cli/app/cmd/bench/`、`internal/cli/bench/` |

## 设计批准与发布准入检查

草案默认不勾选。检查项记录事实、证据和责任方，不等同于批准或发布授权。

### Design Approval（设计批准）

- [ ] 问题、目标/非目标、用户行为、方案和替代方案已收敛。证据：本文第
  “动机”“提议”“替代方案”章节；责任方：提案评审人（待指定）。
- [ ] API、凭据边界、资源竞争、兼容性和清理生命周期的阻塞决策已解决。
  证据：本文“安全与隔离”“PRR”“开放问题”；责任方：CLI/runtime 维护者
  （待指定）。
- [ ] 测试计划、GPU/静态集群前置设施、验收和回退路径已明确。证据：本文
  “测试计划”“验收准入”“基础设施需求”；责任方：测试负责人（待指定）。
- [ ] PRR 已逐项分析，未验证项有责任方和验证计划。证据：本文“Production
  Readiness Review”；责任方：提案评审人（待指定）。
- [ ] 已取得真实设计批准并同步状态。当前未取得批准，状态保持 `provisional`；
  证据待由 RFC/NEP 评审渠道补充。该项阻塞提案状态晋级和发布准入，不阻塞本地
  草案评审。

### Release Readiness（发布准入）

- [ ] 实现与批准设计一致，重大偏差已重新确认。当前有实现参考，但尚无批准设计；
  责任方：实现负责人（待指定）。
- [ ] 目标版本及阶段验收条件已满足。目标版本尚未指定，不能勾选。
- [ ] 适用的 Unit/DB/集成/E2E 测试已执行并记录。已有 Unit、静态检查和真实手工
  验证记录；正式发布前仍需按目标环境复核。
- [ ] 启停、升级/降级、存量数据和混跑路径已验证。无持久化迁移；CLI/API 版本偏差
  仍需按支持矩阵验证。
- [ ] PRR 待验证项已用实际证据关闭，排障和回退手段可用。集中监控和资源隔离尚未
  提供，不能勾选。
- [ ] 用户文档、部署说明和发布说明已就绪。当前仅有 RFC/CLI help，需由文档负责人
  确认是否足够。
- [ ] 无发布阻塞项，残余风险和不适用项已有责任方确认。尚未完成。

## 摘要

本提案增加实验性命令 `neutree-cli exp bench ENDPOINT`，让操作者
通过 Endpoint 名称和工作负载参数快速运行可重复的 vLLM `random` benchmark。CLI
自动读取 Endpoint/Cluster、获取目标 Cluster 凭据、选择一个推理副本、解析 route
和 tokenizer，在选定推理服务副本容器内运行 `vllm bench serve`，并把日志、结果和
manifest 保存到运行 CLI 的主机。

首期明确选择“不新增 benchmark 运行时依赖”：benchmark client 直接复用推理镜像
中已安装的 vLLM 和已挂载的 tokenizer。请求仍经过正常 Router 或 Ray Serve route，
因此结果表示 Endpoint route，而不是单副本直连性能。该选择会让 benchmark 与线上
推理共享资源，是提案必须由评审确认的主要代价。

CLI 只承担中立编排，不将 vLLM 的 tokenizer、请求和结果版本语义移植进 Go。
Endpoint 的 engine 决定 runtime 内采用的 benchmark 行为；首期只实现 vLLM，其他
engine 返回 unsupported-engine 错误。后续新增 engine 时保持 CLI 参数和产物契约不变。

## 动机

### 当前行为与证据

在该能力之前，操作者需要手工定位 inference Pod 或 Ray Backend、取得 kubeconfig
或 SSH 信息、推导服务 URL 和 tokenizer 路径、拼接 vLLM 命令并拷贝结果。Kubernetes
和 static Ray Cluster 的步骤不同，难以保证参数、路由和产物一致。

现有实现参考已将职责拆分到：

- `cmd/neutree-cli/app/cmd/bench/bench.go`：命令、flag 和输入校验。
- `internal/cli/bench/options.go`：正整数列表解析、去重排序和矩阵展开。
- `internal/cli/bench/kubernetes.go`：Pod、容器、tokenizer 和 Router 解析。
- `internal/cli/bench/static.go`：Ray Backend/Actor/Node/Docker 解析。
- `internal/cli/bench/run.go`：case 生命周期、manifest、日志和结果回传。
- `pkg/client/cluster.go`：只读取目标 Cluster 凭据。

### 目标

| 目标 | 可验收结果 | 验证方式 |
|---|---|---|
| 快速启动一次 Endpoint benchmark | 一条 CLI 命令可以解析目标并启动 case | CLI 单测、目标环境手工/E2E |
| 参数可重复 | input/output/concurrency 矩阵按稳定顺序执行，参数映射到 vLLM | `options_test.go`、`command_test.go` |
| 不增加 runtime 依赖 | 不创建 benchmark image、sidecar、Job 或包安装流程；直接使用副本容器内已有 `vllm` | 镜像 preflight、部署验证 |
| 结果可审计 | 每个 run 有 manifest、命令、stdout、stderr 和 vLLM result | `run_test.go`、真实矩阵验证 |
| 不泄露凭据 | API key、kubeconfig、SSH key 不进入命令、日志、manifest 或 result | 产物断言、代码审查 |

### 非目标

- 首期不支持 vLLM 之外的引擎、非 `random` 数据集或自动调参。
- 不新增控制面 benchmark 资源、数据库表、UI、历史结果存储或调度器。
- 不提供绕过 Router/Ray Serve 的单副本直连 benchmark。
- 不提供 benchmark client 的独立资源配额或隔离机制。

## 提议

### 用户体验

前置条件：Endpoint 为 Running、引擎为 vLLM、任务为 text-generation；目标 runtime
镜像已包含 `vllm bench serve` 和 tokenizer；操作者使用默认 Admin API key。

```bash
neutree-cli exp bench ENDPOINT \
  --workspace default \
  --input-lens 16384 \
  --output-lens 2048 \
  --concurrencies 1,2,4 \
  --num-prompts 20 \
  --request-rate inf \
  --output-dir ./results
```

Endpoint 的 `spec.engine.engine` 是唯一 engine 来源，CLI 不接受 `--engine`。
首期只为 `vllm` Endpoint 在选定 runtime 内运行 vLLM benchmark；其他 engine 在
读取 Endpoint 后返回 unsupported-engine 错误。CLI 保持中立的参数、case、产物和
生命周期职责，engine 专属行为由 runtime 负责。

`--input-lens`、`--output-lens`、`--concurrencies` 支持逗号分隔或重复传入正整数。
CLI 去重、排序后执行笛卡尔积，默认值分别为 `16384`、`2048`、`2`、`20`、`inf`。
`--timeout` 限制整个 CLI 操作；`--output-dir` 是运行 CLI 的主机目录。

CLI 使用 klog 在标准错误输出记录带时间戳的 Endpoint/Cluster/runtime 解析、preflight 和每个 case 的生命周期。case
开始时输出编号与 input/output/concurrency、prompt 数和 request rate，随后输出脱敏后的远端
`vllm bench serve` 命令展示；URI 中的 userinfo、query 和 fragment 不会进入控制台，完整
POSIX quote 后命令保存在 `0600` 的 `command.sh`。完成时输出本地 `result.json` 路径，以及
可用的 completed/failed、耗时、吞吐、TTFT 摘要。若安装的 vLLM result schema 没有这些已知
字段，则显示 `summary=unavailable`，不影响 case 成功状态。输出不包含 API key、kubeconfig
或 SSH 私钥。

成功时在标准输出打印本地结果目录；失败时保留已写入的 manifest 和日志，并停止后续 case。该命令会
产生真实推理流量，CLI help 和本文均明确这一点。

### 组件影响

| 组件 | 当前职责与证据 | 提议变更 | 不受影响的边界 |
|---|---|---|---|
| `neutree-cli` | Cobra command 体系 | 增加 `exp bench ENDPOINT` 和通用 benchmark flags | 既有 CRUD、launch、import 命令不变 |
| `pkg/client` | 现有 generic client 与受保护 credentials API | 增加按 workspace/name 读取单个 Cluster 凭据的方法 | 不新增 API endpoint、schema 或 RLS |
| `internal/cli/bench` | 新增 runtime resolver/runner | 实现矩阵、Pod/Backend 选择、vLLM 命令和产物收集 | 不修改 controller、gateway、Endpoint 路由 |
| Kubernetes runtime | 已有 inference Pod、Router、kubeconfig | 在选中 Pod 的服务容器内 exec | 不新增 Pod/Job/sidecar |
| Static Ray runtime | 已有 Ray Serve、SSH、Docker | 在选中 Backend 容器内执行 | 不改 Ray deployment 或镜像 |
| PostgreSQL/PostgREST/UI | 本功能不需要持久化资源 | 无变更 | 数据库、RLS、UI、dashboard 均不变 |

### 约束、风险与缓解

| 风险/约束 | 触发条件与影响 | 缓解/恢复措施 | 剩余风险 |
|---|---|---|---|
| 副本资源竞争 | benchmark 与线上请求共享 CPU、内存和 GPU | 明确 live traffic；串行 case；操作者控制并发和 prompt 数 | 首期没有独立 quota/isolation |
| runtime 缺少 vLLM 或 Python | 自定义镜像不包含 `vllm bench` 或模型发现所需运行时 | `vllm bench serve --help` 与模型发现 preflight；失败不运行 case | 版本兼容由 runtime 决定，尚无完整矩阵 |
| detailed result 含敏感文本 | `result.json` 可能保存 generated texts、route 和路径 | 本地 `0700/0600` 权限；不上传；文档要求操作者管理留存 | CLI 不自动脱敏或过期清理 |
| route 结果不是单副本结果 | Router/Ray Serve 可能将请求调度到其他副本 | manifest 记录 measurement scope=`endpoint-route`；文档明确语义 | 不适合作为单副本容量结论 |
| 远端清理不完整 | CLI/SSH/Pod exec 被强制终止 | 信号或超时时请求监督进程终止本次进程组；无法确认时保留目录并告警 | CLI 被硬杀、网络断开或 preflight 辅助命令异常时需人工检查 |

## 设计细节

### 架构与职责

```text
CLI host
  -> Endpoint + Cluster API
  -> GetClusterCredentials(目标 Cluster)
  -> 选定一个 inference replica container
  -> 容器内 vllm bench serve
  -> 正常 Endpoint route
  -> stream logs/result.json 到 --output-dir
```

#### Benchmark client 放置取舍

首期直接在选定的推理服务副本容器内运行：

- Kubernetes 通过 Pod exec 进入选中的 `vllm` 容器。
- Static Cluster 通过 SSH 加 `docker exec` 进入承载 Ray Serve Backend Actor 的容器。

这样复用镜像中已有的 `vllm bench serve`、`python3`、tokenizer 和集群网络，不新增
benchmark image、Python 包、sidecar、Job、PVC 或权限对象。代价是共享资源和生命周期；
启动 benchmark 不会触发独立调度或隔离。

#### Kubernetes

1. 解码目标 Cluster kubeconfig。
2. 在 Cluster namespace 中按 `app=inference,endpoint=<endpoint>` 列出 Running Pod。
3. 按名称选择首个可用 Pod；优先 `vllm` 容器，否则仅一个常规容器时使用该容器。
4. 从 `vllm serve <model-path>` 参数取得 tokenizer。
5. 通过 Kubernetes exec 执行命令，请求走：
   `http://router-service.<namespace>.svc.cluster.local:8000/<workspace>/<endpoint>`。

#### Static SSH Cluster

1. 查询 `<workspace>_<endpoint>` Ray Serve application 的 `Backend`。
2. 按 Node、Replica、Actor 标识排序选择首个 replica。
3. 通过 Ray Actor State 获取 PID/Node，读取 `/proc/<pid>/cgroup` 并解析唯一 Docker ID。
4. `docker inspect` 校验 ID，再通过 SSH + `docker exec` 执行。
5. 从 Ray Serve `model.path` 获取 tokenizer；请求走
   `<cluster-dashboard-host>:8000/<workspace>/<endpoint>`。

### API、数据模型与配置

本提案不新增控制面 API、数据库字段、CRD、RLS、UI 配置或持久化资源。

| 输入 | 默认值/校验 | 作用 |
|---|---|---|
| `ENDPOINT` | 必填位置参数 | 目标 Endpoint 名称 |
| `--workspace` | `default`；非空 | 资源隔离范围 |
| `--input-lens` | `16384`；正整数列表 | `--random-input-len` |
| `--output-lens` | `2048`；正整数列表 | `--random-output-len` |
| `--concurrencies` | `2`；正整数列表 | `--max-concurrency` |
| `--num-prompts` | `20`；大于 0 | `--num-prompts` |
| `--request-rate` | `inf`；或正有限数 | `--request-rate` |
| `--output-dir` | `.`；CLI host 路径 | 本地产物根目录 |
| `--timeout` | `0`；非负 duration | CLI 总超时 |

`Client.GetClusterCredentials` 复用现有 `/api/v1/credentials/clusters` 权限边界，
只按 workspace/name 读取目标 Cluster。没有新增 server-side state。

### 生命周期与失败处理

这是一次性、串行的 CLI 操作，不创建异步控制面资源。

| 操作/事件 | 持久化/状态变化 | 执行方 | 失败、重试与恢复 |
|---|---|---|---|
| 参数与 Endpoint 校验 | 无 | CLI | 失败即返回，不读取凭据或远程执行 |
| 凭据与 runtime 解析 | 无；凭据仅内存/临时 key | CLI/API/Cluster | API、kubeconfig、Ray、SSH 或 Docker 错误直接返回 |
| Preflight 与 `/v1/models` | 写入 preflight logs 和 manifest | 目标容器 + CLI | 失败不运行 case，保留日志 |
| Case 启动 | manifest 增加进行中 case | CLI + runtime | 每个 case 串行；无自动重试，避免改变负载语义 |
| Case 成功 | 本地写入 `result.json`，manifest 标记 `Succeeded` | vLLM + CLI | 结果必须非空；否则视为失败 |
| Case 取消 | `SIGINT`/`SIGTERM` 时 manifest 标记 `Canceled`，保留 stdout/stderr | CLI + runtime | 独立 exec 请求监督进程终止该 case 的进程组；确认后再清理，未确认则保留目录并告警 |
| Case 失败 | manifest 标记 `Failed`，保留 stdout/stderr | CLI | 停止后续 case；结果回传未完成时保留远端目录和本地 `.partial` 文件 |
| CLI 正常退出 | 删除远端临时目录 | CLI | cleanup best effort；本地结果保留 |

### 运行依赖与支持环境

- Neutree API：Endpoint/Cluster generic read 和受保护 Cluster credentials API。
- Kubernetes：目标 kubeconfig、Pod exec 权限、目标 Pod 内 `sh`、`vllm`、`python3`。
- Static：Ray Dashboard/State API、SSH 凭据、目标节点上的 Docker、容器内 `sh`、
  `vllm`、`python3`。
- vLLM：运行时需提供 `vllm bench serve`；首期不声明固定最低版本，以 preflight 作为
  兼容性门槛。
- 本地 CLI host：Go CLI、网络访问 Neutree API；静态模式需要 `ssh` 客户端。

未知的 vLLM 版本、custom image 或 Cluster 组合不自动承诺兼容，需通过目标环境验证。

## 安全与隔离

- 默认 Admin API key 是文档前置条件；CLI 不做特殊 Admin role 分支，权限由既有
  credentials API 决定。
- workspace、Endpoint name 和 Cluster name 在 API 查询和 runtime 选择中保持绑定；
  Kubernetes 使用 Endpoint label selector，Static 使用 workspace+endpoint 的 Ray app 名。
- kubeconfig、SSH private key 只在内存或临时受限文件中使用，不写入任何产物。
- shell 参数全部校验或 quote；Docker ID、SSH user/host 也做格式校验。
- 结果可能含内部地址、路径、vLLM 参数和 generated text；文件权限为目录 `0700`、
  文件 `0600`，CLI 不上传或自动清理本地结果。
- 无数据库写入，因此不涉及新增 RLS；无新的网络监听端口。

## 测试计划

### Prerequisite Testing Updates

Unit test 不需要新增基础设施；现有 fake runner、fake dashboard、HTTP test server
覆盖命令和凭据边界。E2E 需要 profile-driven Kubernetes GPU Endpoint 和 static SSH
GPU Endpoint；当前环境缺少统一自动化 profile，因此真实路径使用手工验证记录。

### Unit test

现有覆盖包括：

- `cmd/neutree-cli/app/cmd/bench/bench_test.go`：flags、engine 和早期校验。
- `internal/cli/bench/options_test.go`：正整数解析、去重、矩阵顺序和非法输入。
- `command_test.go` / `model_test.go`：vLLM 命令 quote、模型字段兼容。
- `kubernetes_test.go` / `static_test.go` / `resolver_test.go`：Pod、容器、Ray、Actor、
  Node、cgroup Docker ID 和 tokenizer 解析。
- `run_test.go` / `runner_test.go`：manifest、日志、结果流、失败和 cleanup。
- `pkg/client/cluster_test.go`：单 Cluster credentials 查询和 Authorization header。

本次开发已执行相关包 `go test`、`go test -race ./internal/cli/bench`、`go vet`、
目标包 `golangci-lint`，bench 包覆盖率为 `80.4%`。pre-commit 的 Linux full lint
在未修改的 `origin/main` 上也会复现既有 API/mock typecheck 错误，不能作为本功能的
绿色发布证据。

### DB test

不适用：没有 migration、DB function、RLS policy 或持久化资源。若未来增加历史结果
存储，必须新增 DB test 和回退设计，不沿用本 RFC 的无状态结论。

### E2E test

正式 E2E 需要：

1. Kubernetes vLLM Endpoint：断言 Router route、Pod exec、tokenizer、manifest 和 result。
2. Static SSH vLLM Endpoint：断言 Ray Backend/Actor/PID/Docker 映射、SSH exec 和 Serve route。
3. 失败场景：缺少凭据、无可用 Pod/Backend、无 tokenizer、preflight 失败、result 缺失。

当前开发验证已在真实环境完成两条手工矩阵：Kubernetes `qwen3.8-27b` 和 static SSH
`qwen2.5` 均使用 `16K/2K`、20 prompts、并发 `1,2,4`，每组 20/20 成功并收集完整
manifest/result/log。该记录证明路径可行，但不能替代正式 profile-driven E2E。

### Other Integration Coverage

Kubernetes API exec、Ray Dashboard/State API、SSH 和 Docker exec 是跨组件集成边界；
resolver 单测使用 fake API，真实组合由上节手工矩阵覆盖。没有额外独立 integration
suite；如进入发布阶段，应将上述两条路径纳入自动化环境。

人工测试在当前环境是必要的，因为没有可运行两种底座的统一 E2E profile；步骤为执行
小矩阵、检查 route/manifest/result/log、确认无凭据泄露，并在测试后清理远端临时目录。

## 验收与成熟度准入

当前阶段为 `experimental`，不自动晋级 alpha/beta/GA。进入下一阶段至少需要：

| 阶段 | 准入/退出条件 | 证据 | 责任方 |
|---|---|---|---|
| `experimental` 评审 | CLI 契约、容器内执行取舍、资源竞争和失败策略获评审确认 | 本文设计批准记录 | 提案评审人（待指定） |
| 实验性可用 | Kubernetes/static 各一条自动或可复现验证；产物无凭据；preflight 失败可诊断 | Unit + E2E/手工记录 | CLI/runtime 维护者（待指定） |
| 后续推广评估 | 明确 vLLM 兼容矩阵、资源竞争运行手册和 case 数上限 | 发布准入检查 | 发布负责人（待指定） |

## 兼容性、升级与降级

### 既有行为与数据

- 未调用 `exp bench` 时，既有 CLI 和推理服务行为不变。
- 不新增控制面字段、数据库数据或运行时部署对象，因此无存量数据迁移。
- 旧 CLI 不识别该命令，但可以继续管理现有资源；新 CLI 依赖既有 Endpoint、Cluster
  和 credentials API 形状。
- 结果 JSON 由 vLLM 原样生成，不承诺跨 vLLM 版本字段完全稳定；manifest 是 Neutree
  自有的最小运行记录。

### 发布、禁用与恢复

发布只需分发包含该命令的 CLI binary；不需要先升级 control plane 或 engine image。
禁用方式是不调用命令或回退 CLI binary；已有 case 不会被控制面追踪。正常取消会请求
监督进程终止远端 case；CLI 被硬杀或无法访问集群时不保证终止，操作者需检查残留进程
和临时目录。

若结果传输失败，本地已写入日志和 manifest 保留；重新执行会创建新的 run 目录，不
覆盖旧结果。没有数据库回滚或数据恢复步骤。

### Version Skew

- 新 CLI + 旧 control plane：只要既有 generic resource 和 credentials API 兼容，原则上
  可工作；需在支持矩阵中验证。
- 旧 CLI + 新 control plane：不调用新命令，既有 API 行为不受影响。
- 新 CLI + 缺少 `vllm bench` 的 runtime：preflight 失败，不安装依赖、不改变镜像。
- 不同 vLLM 版本的 `result.json` 字段可能变化；消费方必须按实际版本解析。

## Production Readiness Review（PRR）

### Feature Enablement and Disablement

| 问题 | 结论 | 证据或验证计划 | 责任方 |
|---|---|---|---|
| 如何启用/禁用？ | 通过 CLI 调用启用；不调用或使用旧 CLI 即禁用；适用于 K8s/static 两种路径。 | `bench.go` 命令注册；手工执行记录 | CLI 维护者（待指定） |
| 是否改变默认行为？ | 不改变；`exp bench` 是新增 opt-in 命令，Endpoint 默认流量不变。 | 根命令注册差异、既有命令回归测试 | CLI 维护者（待指定） |
| 禁用后在途请求和持久化状态如何处理？ | 无控制面持久化；运行中的 vLLM 进程由 CLI context/连接控制，产物保留。 | `run.go` cleanup 与 manifest 行为 | CLI 维护者（待指定） |
| 能否再次启用？ | 可以；每次生成唯一 run 目录，不复用旧远端目录。 | `createOutputDirectory`、矩阵手工验证 | CLI 维护者（待指定） |

### Rollout, Upgrade and Rollback Planning

| 问题 | 结论 | 证据或验证计划 | 责任方 |
|---|---|---|---|
| 部署顺序与灰度 | 先分发 CLI，再按 Endpoint/Cluster 显式调用；无服务端灰度。 | CLI binary 发布流程 | 发布负责人（待指定） |
| 发布/回退失败影响 | 不写控制面状态；主要风险是目标副本资源竞争和远端残留。 | 资源竞争说明、cleanup 测试 | runtime 维护者（待指定） |
| 暂停/回退阈值 | 首期无平台级自动阈值；操作者根据 stderr、失败数、TTFT/E2E 结果停止。 | 结果 JSON/日志文件契约 | 运行操作者（待指定） |
| 升级/降级/再次升级 | CLI 无持久化迁移；vLLM runtime 版本兼容需 preflight 和目标矩阵验证。 | 版本偏差计划 | 发布负责人（待指定） |
| 是否废弃字段/API | 不新增或废弃 API/字段。 | `pkg/client/cluster.go`、无 migration | API 维护者（待指定） |

### Monitoring Requirements

| 问题 | 结论 | 证据或验证计划 | 责任方 |
|---|---|---|---|
| 如何确认使用与正常？ | CLI 输出实时阶段日志、case 参数/命令/结果目录；查看 manifest case 状态、stdout/stderr 和 result。 | `run.go`、文件契约 | 运行操作者（待指定） |
| SLI/SLO 和告警 | 首期不新增平台 SLI/SLO 或告警；benchmark 指标只属于单次结果。 | 无 metrics 变更；需在推广前评审 | 运维负责人（待指定） |
| 使用哪些日志/指标？ | CLI 标准错误输出的 klog 阶段日志与 case 摘要；本地 preflight/case logs、vLLM result JSON；无集中采集。 | `run.go`、文件表 | CLI 维护者（待指定） |
| 缺失观测能力 | 无集中 dashboard/alert，无法跨 run 聚合；推广前需决定是否增加结果上传或报告层。 | 开放决策 | 产品/运维评审（待指定） |
| 标签基数和敏感信息 | 不新增平台 labels；详细 result 可能含 generated text，保持本地权限和 retention。 | 产物权限与安全章节 | 运行操作者（待指定） |

### Dependencies

| 问题 | 结论 | 证据或验证计划 | 责任方 |
|---|---|---|---|
| 真实依赖 | Neutree API、credentials API、Kubernetes/Ray API、SSH/Docker、runtime 内 `vllm`、`python3`。 | 依赖章节与 resolver/runner 代码 | CLI/runtime 维护者（待指定） |
| 依赖不可用影响 | 解析/preflight 失败时不开始 case；已有线上流量不被 CLI 主动改写。 | failure handling、手工错误场景 | CLI 维护者（待指定） |
| 超时/重试/恢复 | 使用总 context 和 SSH keepalive；case 不自动重试，避免额外负载；依赖恢复后重新启动新 run。 | `Options.Timeout`、`runner.go` | CLI 维护者（待指定） |

### Scalability

| 问题 | 结论 | 证据或验证计划 | 责任方 |
|---|---|---|---|
| API 调用规模 | 每次 run 读取 Endpoint/Cluster/凭据，并做一次 runtime discovery；case 串行。 | `run.go`、resolver 代码 | CLI 维护者（待指定） |
| 新增资源上限 | 不新增 Neutree 资源；case 数由输入矩阵决定，当前没有显式最大 case 数。 | `expandCases` | 评审需决定是否增加矩阵上限 |
| 存储增长 | 结果只写 CLI host，逐请求 detailed JSON 可能很大；无自动 retention。 | result 文件表 | 运行操作者（待指定） |
| 对既有 SLO 影响 | benchmark 与推理副本共享资源，可能增加 TTFT/E2E；不承诺线上 SLO 不受影响。 | placement tradeoff；需压测观察 | 运行操作者（待指定） |
| 资源热点 | 目标副本 CPU、内存、GPU、网络和容器临时盘；无新增控制面资源。 | 运行模型 | runtime 维护者（待指定） |
| 资源耗尽与背压 | `--concurrencies`、`--num-prompts`、`--timeout` 由操作者控制；当前无服务端 quota 或 case 上限。 | flags/validation | 评审需决定上限与运行手册 |

### Troubleshooting

| 问题 | 结论 | 证据或验证计划 | 责任方 |
|---|---|---|---|
| API/Cluster 故障 | 先看 CLI error；检查 Endpoint/Cluster 状态和凭据权限，不修改资源。 | `manifest`/error handling | 运行操作者（待指定） |
| runtime 选择失败 | 查看 preflight 前的 CLI error；K8s 检查 Pod/container args，static 检查 Ray Actor/PID/cgroup/Docker。 | resolver 日志和单测 | runtime 维护者（待指定） |
| vLLM case 失败 | 查看 case `stdout.log`、`stderr.log`、`command.sh` 和 manifest error；确认资源竞争与模型 route。 | 文件契约 | 运行操作者（待指定） |
| result 缺失 | 检查 vLLM 是否退出成功、容器临时目录和 exec 连接；失败时重新启动新 run。 | `runCase` 非空检查 | CLI 维护者（待指定） |
| 不满足预期延迟 | 先比较 `result.json` 的 completed/failed、TTFT/TPOT/ITL/E2E 和 concurrency，再停止 live traffic；不把 route 结果当单副本结论。 | result schema | 运行操作者（待指定） |

## 缺点

- 共享推理容器资源会污染线上延迟与吞吐，首期无法隔离。
- 需要 runtime 镜像预装 vLLM bench、`python3` 和 tokenizer；不支持自动补依赖。
- 结果保留在 CLI host，缺少集中历史、跨 run 对比、统一 retention 和告警。
- detailed JSON 可能很大且含生成文本，操作者必须自行管理安全与存储。
- 没有显式 case 数上限或服务端 quota，过大的矩阵可能造成长时间 live traffic。

## 替代方案

替代方案来自四个契约维度：执行归属、client 位置、测量对象、结果归属。

| 替代方案 | 取舍与不采用原因 | 重新考虑条件 |
|---|---|---|
| 人工运行 vLLM bench | 无需代码，但重复集群差异处理且产物不一致 | CLI 维护成本超过人工流程时复评 |
| 单独 benchmark Pod/Job/sidecar/image | 隔离更好，但需要镜像、tokenizer 分发、生命周期和权限 | 资源竞争成为不可接受风险时复评 |
| 在 Cluster 外运行 client | 不占副本资源，但引入外部网络/gateway 偏差 | 需要测量外部调用路径时另建模式 |
| 直连单副本 | 可测副本本身，但不代表生产 route | 需要单副本容量分析时另建 RFC |
| 控制面 benchmark 资源 | 可集中调度/留存，但首期引入持久化和 UI 复杂度 | 需要历史平台能力时拆分后续提案 |
| 不做增强 | 无开发和资源风险，但保留人工、不可比、易错流程 | 如果需求优先级下降，维持现状 |

## 实现历史

| 日期 | 事实 | 证据 |
|---|---|---|
| 2026-10-21 | 完成 `exp bench` CLI、Kubernetes/static runtime 解析、结果收集与单测 | `cmd/neutree-cli/app/cmd/bench/`、`internal/cli/bench/`、`pkg/client/cluster.go` |
| 2026-10-21 | Kubernetes `qwen3.8-27b` 运行 `16K/2K`、20 prompts、并发 `1,2,4`，每组 20/20 成功 | 本地 benchmark 归档与 manifest/result |
| 2026-10-21 | Static SSH `qwen2.5` 运行相同矩阵，每组 20/20 成功 | 本地 benchmark 归档与 manifest/result |
| 2026-10-22 | RFC 补充产物契约、容器内运行取舍、PRR 和发布准入 | 本文修订 |

## 开放问题与决策

| 问题/决策 | 状态 | 负责人或确认渠道 | 阻塞影响 | 下一步/结论 |
|---|---|---|---|---|
| 是否增加单次 run 的 case 数上限？ | 待确认 | CLI/runtime 评审 | 影响资源保护和可发布性 | 结合目标环境和最大可接受 live traffic 确定 |
| 是否需要集中上传 benchmark result/metrics？ | 延后 | 产品/运维评审 | 不阻塞首期 CLI | 首期保留本地文件，后续单独提案 |
| vLLM 兼容版本矩阵如何维护？ | 待确认 | engine/runtime 维护者 | 影响支持范围声明 | 先以 preflight 为门槛，发布前补版本验证 |
| 是否提供 benchmark client 资源隔离？ | 延后 | runtime 维护者 | 当前共享资源风险需明确接受 | 资源竞争不可接受时设计独立 Job/image |
| NEP owner/reviewer/target release | 待确认 | 项目评审流程 | 阻塞正式批准和发布准入 | 评审时补齐元数据 |

## References

- `cmd/neutree-cli/app/cmd/bench/bench.go`
- `internal/cli/bench/options.go`
- `internal/cli/bench/command.go`
- `internal/cli/bench/model.go`
- `internal/cli/bench/kubernetes.go`
- `internal/cli/bench/static.go`
- `internal/cli/bench/runner.go`
- `internal/cli/bench/run.go`
- `pkg/client/cluster.go`
- `contributing/testing.md`
- `docs/vllm-bench-cli-design.md`

## 基础设施需求

本提案不新增生产部署制品、控制面服务或数据库设施。正式 E2E 需要：

- 一个可运行 vLLM Endpoint 的 Kubernetes GPU profile；
- 一个可运行 vLLM Endpoint 的 static SSH/Ray GPU profile；
- 可复现的 kubeconfig、SSH、Ray Dashboard/State API 测试凭据；
- 测试后清理远端临时目录和本地详细结果的运行手册。

这些环境准备和资源费用需要由 E2E/平台负责人确认，不由本文自动授权创建。
