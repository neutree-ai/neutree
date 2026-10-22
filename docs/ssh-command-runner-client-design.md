# NEU-787：Native SSH 标准输出与错误输出分离

## 1. 状态与决策

- 状态：最小修复设计，2026-10-21 按讨论收敛；本文不代表实现或验证已完成。
- 目标：解决 shell 启动脚本向 stderr 输出异常、真实业务命令成功时，合并输出造成的业务解析失败。
- 决策：继续使用系统 OpenSSH，仅在 SSH 子进程执行边界分离 stdout / stderr。
- 本文替代此前 GoClient 默认实现及 SSHCommandRunner 接口化方案；两者不再属于本票实施内容。

## 2. 根因与适用边界

现有 `pkg/command/executor.go` 的 `OSExecutor.Execute` 调用 CombinedOutput；`pkg/command_runner/ssh_command_runner.go` 将合并结果作为 Run 的成功返回值。accelerator、Docker 状态、文件 hash 等解析器因此可能把 shell 的 stderr 当成业务数据。

```text
修改前：业务 stdout + 启动 stderr → CombinedOutput → Run → 业务解析失败
修改后：业务 stdout → Run → 业务解析
        启动 stderr → 独立诊断，不进入成功返回值
```

| 远端行为 | 本次预期 |
| --- | --- |
| stderr 有噪声，业务命令成功退出 | 返回原始 stdout，stderr 非空不自动判失败 |
| stderr 报错，命令非零退出 | 返回执行错误，保留底层 cause 和 stderr，不把失败伪装成成功 |
| stdout 本身包含欢迎语或报错 | 原样返回，仍可能解析失败；不删除首行或猜测业务数据 |
| 启动脚本提前退出，业务命令未执行 | 不保证恢复执行；退出 0 也不等于业务后置条件已经满足 |

实际问题是否符合第一行，仍应由复现中独立收集的双流与退出状态确认。

## 3. 范围与非目标

纳入：SSH Run 与现有 uptime 预检的双流收集、失败诊断保留、测试注入，以及现有调用者回归。

不做：

- 不将 SSHCommandRunner 改为接口，不引入 Native / GoClient 双实现或连接池。
- 不变更 OpenSSH 的密钥、agent、用户配置、HostKey、代理或 ControlMaster 策略。
- 不修改 `OSExecutor.Execute` 的合并输出语义及 command.Executor 接口。
- 不修改 scp 上传、文件安装、临时密钥生命周期、accelerator 解析器和 manager 聚合规则。
- 不修改 `ray up/down` 的命令、参数或输出收集，不修改 Kubernetes exec、CLI 流式 SSH 或运维脚本。
- 不引入 stdout 过滤、通用结果分帧、失败重试或执行方式 fallback。
- 不移除 uptime 预检，不在本票重新设计连接/远端失败分类；现有预检误分类问题单独跟踪。

## 4. 实现设计

### 4.1 独立双流执行能力

在 `pkg/command_runner` 定义专用于 SSH 子进程的结果与回调，不改变旧 ProcessExecute：

```go
type SSHProcessOutput struct {
    Stdout string
    Stderr string
}

type SSHProcessExecute func(context.Context, string, []string) (SSHProcessOutput, error)
```

默认实现仍启动系统 ssh：

1. 使用 exec.CommandContext，保持调用者取消边界。
2. Stdout、Stderr 分别设置 writer，调用 Run；不使用 CombinedOutput 或通用 `2>/dev/null`。
3. stdout 原样收集；stderr 使用 16 KiB 有界缓冲，超限时保留尾部并标记截断，避免启动噪声挤掉命令最后输出的关键诊断，例如 Docker 的 No such object。
4. 保留进程原始错误，context 取消时同时保留 context cause；不将本地 ssh 的 255 解释为可信远端退出状态。
5. 设置有界 WaitDelay，避免取消后仍被后代进程持有的输出管道无限拖住；不承诺取消已终止所有远端副作用。

SSH Run 显式添加 `-T`，禁止伪终端；即使用户配置 RequestTTY=yes/force，也不把终端合流引入解析路径。用户命令主动 `2>&1` 的内容仍属于 stdout，本修复不能逆向分离。

### 4.2 Runner 与构造入口

保留具体 SSHCommandRunner、既有 Run/Files/CloseControlMaster 契约和原构造参数。构造函数末尾增加可选 `SSHCommandRunnerOption`，通过 `WithSSHProcessExecute` 注入双流执行器；未设置时使用上述真实进程实现。

- 所有既有普通构造调用保持可编译，默认自动使用双流执行；不根据旧 ProcessExecute 是否为 mock 选择行为。
- 新选项意味着构造函数的精确函数类型改变；若有函数值赋值，必须相应适配，本仓调用点需审计。
- 旧 ProcessExecute 继续用于 scp 和关闭 ControlMaster，不作为已分离输出的替代品。
- CommonArgs、StaticNode 工厂及直接创建 runner 的插件增加可选双流依赖传递点，仅供显式注入；不新增公共业务 runner 抽象或全局可变开关。
- Ray 的本地 executor 仍模拟/执行 ray subprocess，不能通过 SSH 测试注入替换它。

### 4.3 成功与失败结果

- withOutput=true 且执行成功：只返回 Stdout，保持字节内容，不 TrimSpace 或删行。
- withOutput=false：仍执行并检查错误，成功时不返回输出。
- stderr 非空且退出 0：成功，不进入解析输入；本票不为每次启动噪声新增告警刷屏。
- 非零退出/进程启动失败/取消：返回错误，保留 errors.Is/As 链和有界 stderr。exitOnFail=true 不再丢失底层 cause 或诊断；不把完整 argv、命令环境或私钥加入新的错误文本。
- 不把错误场景的 stdout 当作 stderr 拼回解析输入；Docker inspect 的“no such object”等 stderr 诊断仍在错误中可见。

### 4.4 uptime 预检

保留先 uptime、再真实命令的顺序及 ConnectTimeout=10。两次都使用同一个双流执行器，预检成功时不消费其 stdout/stderr；失败诊断使用 stderr 和 cause。

现有预检的任何非零错误仍包装 ErrConnectionFailed，包括某些 shell 启动失败；不能将其描述为本票已经实现精确连接分类。真实业务命令失败不新增该 sentinel。

## 5. 调用路径与影响

| 路径 | 处理与影响 |
| --- | --- |
| NVIDIA / AMD / 企业 Ascend | 原构造调用默认生效，不改解析器或探测协议；社区测试显式注入双流回调。企业构造参数保持可用，不要求先迁移公共 runner 接口 |
| StaticNode / Docker / 文件 hash | 继续走现有 runner，业务成功输出不再混入 stderr；临时密钥、ControlMaster 与文件上传逻辑不变 |
| Ray worker 命令 | Docker runner 自动受益；仅测试依赖透传调整，不改 worker 命令或关闭语义 |
| 模型 NFS | 继续使用现有 Docker runner，无新连接所有权规则 |
| ray up/down / Kubernetes / scp | 不替换执行机制，不宣称获得本次 SSH Run 的双流语义 |

没有 API/JSON/DB/UI 变更，没有新的认证、信任材料或依赖包。回滚程序即可恢复旧行为，也会重新暴露输出污染问题；没有数据迁移或失败后自动回滚远端副作用。

## 6. 验证计划

### Unit test

- 先增加回归测试，确认旧实现无法满足双流契约，再实现修复。
- 使用真实本地子进程验证 stdout/stderr 独立、stderr 无换行、仅 stderr、非零退出、原始 cause、取消及 stderr 截断；覆盖超限启动噪声后关键末尾诊断仍可见，不只验证 argv。
- 使用受控 ssh 替身进程验证默认构造路径确实不调用旧 CombinedOutput 回调，分别覆盖 uptime 和真实命令；替身测试不宣称是真实 SSH E2E。
- 验证 Run 的 withOutput/exitOnFail、空命令、环境变量、关机、转发及 `-T` 行为；失败不重放，不丢 stderr/cause。
- stdout 噪声继续保留，不能把通用过滤当作测试通过条件。
- 保留文件 hash/安装、Docker inspect 不存在对象、社区 accelerator 设备探测、StaticNode 和 Ray 测试回归；新执行逻辑覆盖率至少 80%，检查 race。

### DB test

没有数据库或资源协议修改，不新增 DB test。

### E2E test

优先复用现有 SSH 集群与故障场景，在隔离节点/用户的 shell 启动阶段注入 stderr 噪声，确认设备、容器、hash/配置检查不再因该噪声解析失败；对照 stdout 噪声与启动失败，不要求这些场景成功。不能改共享环境全局 bashrc。

需要真实环境的验证单独记录是否具备条件及实际结果；本地替身不替代远端结论。若没有可用 E2E 环境，明确列出未验证项，才安排相应人工验证；有 E2E 覆盖时不要求重复人工测试。

## 7. 决策记录

| 状态 | 决策 | 理由 / 后续 |
| --- | --- | --- |
| confirmed | 保留 Native SSH，分离双流 | 当前直接根因为诊断输出混入成功解析输入，无需同时迁移认证和连接机制 |
| confirmed | 不做 runner 接口化 / GoClient 默认切换 | 与本次最小修复解耦，按独立需求重新评审 |
| confirmed | ray up/down、scp 和全局 OSExecutor 不改 | 控制兼容性和回归范围 |
| confirmed | 失败保留诊断，不按噪声文本过滤 | 不隐藏真实执行失败，不删合法 stdout |
| deferred | uptime 误分类、stdout 分帧及任意损坏启动脚本恢复 | 不由本次 stderr 分离方案承诺解决 |
