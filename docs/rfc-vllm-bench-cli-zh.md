# RFC: 通过 CLI 快速压测托管推理实例

## 状态

- 状态：草案。
- 提议命令：`neutree-cli exp bench ENDPOINT`。
- 权威输入：已确认的产品约束，以及
  `cmd/neutree-cli/app/cmd/bench/` 和 `internal/cli/bench/` 的实现契约。

## 摘要

本 RFC 提议增加一个实验性 CLI 命令，用于快速压测 Neutree 管理的
vLLM Endpoint。操作者只需要提供 Endpoint 和工作负载参数；CLI 从 Endpoint
读取 engine，自动解析 Cluster、选择一个推理副本容器、获取模型与 tokenizer 信息、执行
`vllm bench serve`，并将结果保存到运行 CLI 的主机。

benchmark client 在一个选定的推理副本容器内运行，但请求仍走正常的
Router 或 Ray Serve Endpoint 路径。因此它衡量的是 Endpoint route，不是
绕过调度层的单副本直连性能。

## 背景与问题

当前运行一次可比较的压测需要人工完成下列工作：定位副本、获取 kubeconfig
或 SSH 信息、确定服务路由和 tokenizer 路径、拼装 vLLM 命令、收集结果。
Kubernetes 和静态 Ray Cluster 的操作方式不同，容易造成命令和结果不可比。

本提议把与 engine 无关的基础设施编排收敛进 CLI，同时保持线上请求实际经过的正常服务
路径；engine 专属的 benchmark 行为保留在选定的 runtime 内。

## 目标与非目标

首期目标：

- 支持 Running 状态的 vLLM 文本生成 Endpoint。
- 仅支持 `random` 数据集。
- 支持输入长度、输出长度和并发度矩阵。
- 支持 Kubernetes 与静态 SSH Cluster。
- 将日志、结果和 manifest 保存在 CLI host 本地。

首期非目标：

- 其他推理引擎、其他数据集、自动调参、历史结果存储、图表或 UI。
- 新建控制面 API 资源、数据库表、调度器或 benchmark 专用镜像。
- 绕过 Router 或 Ray Serve 的单副本直连压测。

## 提议

### CLI 契约

#### 新增命令

新增命令位于实验性命令组：

```text
neutree-cli
└── exp
    └── bench ENDPOINT
```

最小调用形式为：

```bash
neutree-cli exp bench ENDPOINT
```

Endpoint 的 `spec.engine.engine` 是 engine 的唯一事实来源，CLI 不提供
`--engine`。命令要求 Endpoint 已处于 `Running`、任务类型为 `text-generation`；
首期仅为 `vllm` Endpoint 调用其 runtime 内的 benchmark 行为，其他 engine 返回
unsupported-engine 错误。默认 Admin API key 是读取目标 Cluster kubeconfig 或 SSH
凭据的前置条件；CLI 不额外实现 Admin role 校验。

CLI 负责中立的参数校验、case 矩阵、产物、取消和生命周期记录；选定的 runtime
负责 engine 专属的 benchmark 行为。首期该行为为 runtime 内的 `vllm bench serve`；
后续 engine 可以接入同一 CLI 契约，而不改变命令参数。

#### Bench 参数

| 参数 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `ENDPOINT` | 是 | — | 目标 Endpoint 名称，位置参数 |
| `--workspace` / `-w` | 否 | `default` | Endpoint 所属 workspace，不能为空 |
| `--input-lens` | 否 | `16384` | 输入 token 长度；支持逗号分隔或重复传入的正整数 |
| `--output-lens` | 否 | `2048` | 输出 token 长度；支持逗号分隔或重复传入的正整数 |
| `--concurrencies` | 否 | `2` | 最大请求并发；支持逗号分隔或重复传入的正整数 |
| `--num-prompts` | 否 | `20` | 每个 case 的 prompt 数，必须大于 0 |
| `--request-rate` | 否 | `inf` | 请求速率；可为 `inf` 或大于 0 的有限数 |
| `--output-dir` / `-o` | 否 | `.` | CLI host 上的本地产物目录 |
| `--timeout` | 否 | `0` | 整个 CLI 操作的超时时间；`0` 表示不设置，且不传给 vLLM |

列表参数会先去重、排序，再按 input/output/concurrency 做笛卡尔积并串行执行。
例如 `--input-lens 4096,8192 --concurrencies 1,2` 会生成四个 benchmark case。

CLI 根命令还提供连接参数：

| 参数 | 默认来源 | 说明 |
|---|---|---|
| `--server-url` | `NEUTREE_SERVER_URL` | Neutree API 地址 |
| `--api-key` | `NEUTREE_API_KEY` | API key；文档前置条件为默认 Admin API key |
| `--insecure` | `false` | 跳过 TLS 证书校验，仅用于受控测试环境 |

#### 完整示例

使用环境变量连接控制面，并测试三个并发组合：

```bash
export NEUTREE_SERVER_URL=https://neutree.example.com
export NEUTREE_API_KEY=<admin-api-key>

neutree-cli exp bench qwen3.8-27b \
  --workspace default \
  --input-lens 16384 \
  --output-lens 2048 \
  --concurrencies 1,2,4 \
  --num-prompts 20 \
  --request-rate inf \
  --output-dir ./results \
  --timeout 2h
```

也可以通过 flag 提供连接参数：

```bash
neutree-cli \
  --server-url https://neutree.example.com \
  --api-key <admin-api-key> \
  exp bench qwen3.8-27b \
  --concurrencies 1,2,4
```

CLI 使用 klog 在标准错误输出记录带时间戳的解析、preflight 和每个 case 的进度。例如：

```text
I1023 15:00:00.123456 12345 run.go:103] benchmark starting: endpoint=default/qwen3.8-27b cases=3
I1023 15:00:00.223456 12345 run.go:118] benchmark endpoint resolving
I1023 15:00:00.323456 12345 run.go:142] benchmark cluster resolved: type=kubernetes
I1023 15:00:00.423456 12345 run.go:168] benchmark target resolved: platform=kubernetes runtime=pod/qwen3-0:vllm
I1023 15:00:00.523456 12345 run.go:183] benchmark artifacts: ./results/neutree-bench-<random>
I1023 15:00:01.123456 12345 run.go:223] benchmark preflight completed: model=qwen3.8-27b
I1023 15:00:01.223456 12345 run.go:239] benchmark case 1/3 started: input_len=16384 output_len=2048 concurrency=1 num_prompts=20 request_rate=inf
I1023 15:00:01.323456 12345 run.go:249] benchmark case 1/3 command: umask 077 && mkdir -p -- '<runtime temporary directory>' && vllm bench serve ...
I1023 15:00:13.663456 12345 run.go:293] benchmark case 1/3 completed: result=./results/neutree-bench-<random>/input-16384-output-2048-concurrency-1/result.json completed=20 failed=0 duration_s=12.34 request_per_s=1.62 output_tokens_per_s=3210.00 p50_ttft_ms=45.00 p99_ttft_ms=93.00
I1023 15:00:40.000000 12345 run.go:302] benchmark completed: cases=3 result_dir=./results/neutree-bench-<random>
```

最终结果目录仍输出到标准输出：

```text
benchmark results: ./results/neutree-bench-<random>
```

每个 case 会输出实际远端 `vllm bench serve` 命令的脱敏展示，和该 case 的 input、output、
concurrency、prompt 数、request rate。常规的本地路径和内部 route 保持原样；URI 中的
userinfo、query 和 fragment 会从控制台展示移除。受 `0600` 权限保护的 `command.sh` 仍保存
完整的 POSIX quote 后命令。完成后 CLI 输出本地 `result.json` 路径以及可用的 completed/failed、
耗时、吞吐、TTFT 摘要；旧版或未来 vLLM 未提供这些已知字段时显示 `summary=unavailable`，
但不会影响已完成 case 的成功状态。控制台不输出 API key、kubeconfig 或 SSH 私钥。结果目录
中每个并发组合对应一个独立 case；目录结构和文件内容见“结果与文件契约”。

### 核心取舍：benchmark 运行位置

选择直接在选定的推理服务副本容器内运行 `vllm bench serve`：

- Kubernetes：通过 Pod exec 进入选中的 `vllm` 容器。
- 静态 SSH Cluster：通过 SSH 加 `docker exec` 进入承载选中 Ray Serve
  Backend Actor 的容器。

这样可以直接复用推理镜像中已经安装的 vLLM、已挂载的 tokenizer 以及集群内网络
路径，不需要新增 benchmark image、Python 包安装、sidecar、Job 或额外运行时依赖。
CLI 会先执行 `vllm bench serve --help`，提前发现不包含 benchmark 能力的自定义镜像。

代价是 benchmark client 与推理服务共享同一容器的 CPU、内存、文件系统和
生命周期，可能与线上请求竞争资源。首期不引入额外隔离或配额，因此此命令是
操作者显式触发的 live-traffic 操作。

### 执行模型

```text
CLI host
  -> Endpoint / Cluster API
  -> 仅获取目标 Cluster 的凭据
  -> 选定一个推理副本容器
  -> 在容器内运行 vllm bench serve
  -> 通过正常 Endpoint route 发送请求
  -> 将结果流式写入 CLI host 的 --output-dir
```

CLI 先读取 Endpoint 和它引用的 Cluster，并通过
`Client.GetClusterCredentials(workspace, cluster)` 仅读取匹配 Cluster 的凭据。
既有 credentials API 的授权结果仍是唯一权限边界。

| Cluster 类型 | client 运行位置选择 | 请求 route | 命令传输 |
|---|---|---|---|
| Kubernetes | 按名称排序后的首个可用 Running inference Pod；优先 `vllm` 容器，否则唯一常规容器 | `router-service.<namespace>.svc.cluster.local:8000/<workspace>/<endpoint>` | Kubernetes Pod exec |
| 静态 SSH | 按 Node、Replica、Actor 标识排序后的首个 Ray Serve `Backend`；由 Actor PID 映射到唯一 Docker 容器 | `<cluster-dashboard-host>:8000/<workspace>/<endpoint>` | SSH + `docker exec` |

Kubernetes 的 tokenizer 路径从容器 `vllm serve <model-path>` 启动参数获取；
静态集群从已部署 Ray Serve 应用的 `model.path` 获取。无法确定 tokenizer 时，
case 在执行前失败。

选定副本只承载 benchmark client。请求仍可能被 Router 或 Ray Serve 调度到
其他健康副本，这正是 Endpoint route 测量语义的一部分。

### exp bench 到 vLLM 命令的转换

每个 case 在目标容器中执行等价命令：

```bash
vllm bench serve \
  --backend openai-chat \
  --base-url <Endpoint route> \
  --endpoint /v1/chat/completions \
  --model <路由发现的模型名> \
  --dataset-name random \
  --random-input-len <input length> \
  --random-output-len <output length> \
  --num-prompts <num prompts> \
  --max-concurrency <concurrency> \
  --request-rate <request rate> \
  --save-result --save-detailed \
  --result-dir <容器内临时目录> \
  --result-filename result.json \
  --metric-percentiles 50,90,95,99 \
  --percentile-metrics ttft,tpot,itl,e2el \
  --tokenizer <容器内已挂载 tokenizer 路径> \
  --disable-tqdm
```

在执行 case 前，CLI 从选定运行时访问 `<Endpoint route>/v1/models`，按
`id`、`name`、`model` 的顺序取第一项可用字段，显式传给 `--model`。这是为了
兼容只返回 `name` 的 router，并避免 vLLM 自动模型发现时无法保存
`result.json`。

`--tokenizer` 不是模型权重复制或下载。它是容器已挂载的 tokenizer 路径，供
vLLM bench 按真实模型 token 语义生成指定长度的 random prompt。

## 结果与文件契约

每次调用在 CLI host 的 `--output-dir` 下创建一个目录：

```text
neutree-bench-<random>/
  manifest.json
  preflight.stdout.log
  preflight.stderr.log
  input-<input>-output-<output>-concurrency-<concurrency>/
    command.sh
    stdout.log
    stderr.log
    result.json
```

新创建的 run/case 目录权限为 `0700`，文件权限为 `0600`。目标容器使用
`/tmp/neutree-bench-*` 临时目录；CLI 通过 Pod exec 或 SSH/Docker exec 流式
收集 stdout、stderr 和 `cat result.json`，内部不使用 `scp`。传输后远端临时
目录会尽力清理。

| 文件 | 写入方与时机 | 内容 |
|---|---|---|
| `manifest.json` | CLI；执行前及每次 case 状态变化后更新 | 格式版本、创建时间、测量范围、Endpoint 的 workspace/name/Cluster、目标平台和运行时 ID、route、模型、tokenizer、模型选择方式；每个 case 的输入/输出/并发、状态、起止时间与错误。整个 run 失败时包含顶层 `error`。 |
| `preflight.stdout.log` | 目标容器；每次调用一次 | `vllm bench serve --help` 输出、原始 `/v1/models` 响应以及最终选中的模型名。 |
| `preflight.stderr.log` | 目标容器；每次调用一次 | preflight 和模型发现的标准错误输出。 |
| `command.sh` | CLI；每个 case 执行前 | 当前 case 的完整 POSIX shell quote 后的远端 `vllm bench serve` 命令，包含容器临时结果目录；不包含 API key、kubeconfig 或 SSH 私钥。 |
| `stdout.log` | vLLM bench；每个 case 一次 | vLLM 原始标准输出，包括有效参数、运行状态、写入 stdout 的 warning，以及成功完成时的 benchmark 汇总。 |
| `stderr.log` | vLLM bench；每个 case 一次 | vLLM 原始标准错误输出，包括诊断信息与失败时的 traceback。 |
| `result.json` | vLLM bench 生成，CLI 流式回写；每个成功 case 一次 | 未修改的 `--save-result --save-detailed` JSON：配置与模型标识、完成/失败请求数、token 总数和吞吐、TTFT/TPOT/ITL/E2EL 聚合与百分位指标，以及逐请求长度、时延、生成文本和错误数组。 |
| `result.json.partial` | CLI；结果回传未完成时 | 已回传的部分数据，不作为完整结果；完整远端目录保留待检查。 |

case 失败时仍保留 manifest 和日志；只有 vLLM 成功完成且 CLI 成功流式读取到非空
远端文件时才会出现 `result.json`。其详细字段由实际安装的 vLLM 版本定义，CLI
不对该 JSON schema 做归一化。

## 安全与运维边界

- 操作者通过全局 `--server-url`、`--api-key` 或等价环境变量提供连接信息。
  默认 Admin API key 是读取 Cluster 凭据的前置条件，但 CLI 不实现额外角色校验。
- 只读取 Endpoint 所引用 Cluster 的凭据。kubeconfig 和 SSH 私钥只保留在内存
  或受限的临时 SSH key 文件中。
- 凭据不会写入 command、manifest、日志或 result 文件。
- 产物可能包含内部 route、运行时和 tokenizer 路径、vLLM 配置和详细结果中的
  生成文本。CLI 不上传也不自动清理 CLI host 上的产物，操作者负责目录访问权限和
  保留策略。
- 用户输入的命令值会先校验或做 POSIX shell quote。
- benchmark 产生真实推理流量，并与选定副本共享资源；首期不新增配额、资源隔离、
  控制面 metrics、dashboard、alert 或网络端点。

若校验、运行时选择、preflight、模型发现、case 或结果回传失败，CLI 保留已有
manifest 和日志，并停止后续 case。`SIGINT`、`SIGTERM` 和超时会取消正在运行的 case；
CLI 通过独立 exec 请求容器内监督进程终止本次 benchmark 的进程组，确认退出后才清理
远端目录。收到信号时 case 状态为 `Canceled`；若无法确认退出或结果回传失败，则保留
远端目录并输出位置供排查。`Ctrl+Z` 仍使用 shell 默认的挂起语义。

CLI 被强制杀死、本机崩溃或网络不可用时无法保证远端进程结束；preflight 的辅助命令
不受 case 监督进程管理。以上情况需要操作者检查运行时并定向清理。

## 替代方案及来源

替代方案来自四个必须明确的契约维度：谁负责执行（人工、CLI、控制面），client
在哪里运行（集群外或推理容器内），测量 Endpoint route 还是单副本，以及结果是
本地文件还是控制面持久化状态。Kubernetes Router 路径和
`neutree-cli exp bench` 的产品方向选择了“CLI 负责、运行时容器内执行、Endpoint
route、本地产物”的组合。

| 替代方案 | 不采用原因 |
|---|---|
| 人工运行 vLLM bench | 重复集群差异处理，命令与产物不一致。 |
| 单独 benchmark Pod、Job、sidecar 或 image | 需要额外镜像或包、tokenizer 分发、生命周期管理和 Cluster 权限，违背首期不新增依赖的目标。 |
| 在目标 Cluster 外运行 client | 引入外部 gateway 与网络影响，不再代表集群内服务路径。 |
| 直连单个副本 | 绕过 Router 或 Ray Serve，改变结果的测量语义。 |
| 新增控制面 benchmark 资源 | 在基础 CLI 流程验证前引入持久化、调度、保留和 UI 成本。 |
| 将 vLLM bench 完整移植为通用 Go client | 会把 vLLM 的 tokenizer、请求和结果版本语义耦合进中立 CLI；首期保留 runtime 内的 vLLM 行为，后续 engine 通过同一 CLI 契约接入。 |

## 兼容性、发布与回滚

该命令是 `exp` 下的增量能力，不改变现有 CLI、Endpoint schema、路由或 engine
image。操作者按调用选择启用。回滚方式是在后续版本移除实验性命令；不会留下
控制面持久化状态。

## 验证与验收

### Unit Test

- CLI 参数与早期校验。
- 矩阵展开、命令转换、模型发现和结果流式回传。
- Kubernetes Pod 选择，以及静态 Ray Actor、节点和 Docker 容器解析。

### DB Test

不适用。本 RFC 不引入 schema、migration、DB function 或 RLS 变更。

### E2E Test

分别对 Kubernetes 和静态 SSH vLLM Endpoint 运行一个小型 random case，验证
route、manifest、日志和 `result.json` 出现在 CLI host。

### Manual Testing

只有在可用 E2E 环境无法提供 vLLM GPU Endpoint 时，才需要人工验证对应底座。

## 需要评审确认的决策

1. 在 `exp` 下提供 vLLM-only 的 `neutree-cli exp bench` 命令。
2. 首期测量 Endpoint route，而不是单副本直连。
3. 直接在选定推理副本容器内运行 vLLM bench，不新增 benchmark 依赖或资源隔离。
4. 将结果保存在 CLI host，并以 random 数据集矩阵作为首期契约。

## 决策记录

| 状态 | 决策 | 解决或回访条件 |
|---|---|---|
| 提议 | 在 `exp bench` 下发布该命令 | RFC 评审确认首期契约；跨引擎稳定后再考虑转正。 |
| 已确认 | vLLM bench 直接运行在选定推理副本容器内 | 需要工作负载隔离或 benchmark image 时重新评估。 |
| 已确认 | 显式传递 model 与 tokenizer | vLLM 不再需要本地 tokenizer 时重新评估。 |
| 延后 | 其他引擎、数据集、并行 case 与历史存储 | CLI 工作流被采用后重新评估。 |

## 评审证据

| 决策领域 | 实现证据 |
|---|---|
| CLI 契约与矩阵校验 | `cmd/neutree-cli/app/cmd/bench/bench.go`、`internal/cli/bench/options.go` |
| 容器内执行与 Kubernetes / 静态解析 | `internal/cli/bench/kubernetes.go`、`internal/cli/bench/static.go`、`internal/cli/bench/runner.go` |
| 模型发现、vLLM 命令与产物回传 | `internal/cli/bench/model.go`、`internal/cli/bench/command.go`、`internal/cli/bench/run.go` |
| Cluster 凭据最小读取范围 | `pkg/client/cluster.go` |
| 单元测试 | `cmd/neutree-cli/app/cmd/bench/bench_test.go`、`internal/cli/bench/*_test.go`、`pkg/client/cluster_test.go` |
