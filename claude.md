# Neutree

Neutree 是一个开源的 LLM 基础设施管理平台，提供多集群推理工作负载管理、OpenAI 兼容 API 网关和生产级可观测性。

## Ignore

- bin/, out/, vendor/, tmp/, devtools/
- .vscode/, .idea/
- **/mock/, **/mocks/
- **/*_mock.go
- deploy/docker/neutree-core/gateway/
- deploy/docker/neutree-core/vmagent/
- deploy/docker/obs-stack/grafana/dashboards/
- deploy/chart/neutree/gateway/
- deploy/chart/neutree/vmagent/
- deploy/chart/neutree/grafana-dashboards/
- scripts/dashboard/ray-upstream/
- scripts/dashboard/vllm-upstream/
- scripts/dashboard/output/
- cluster-image-builder/downloader/
- scripts/builder/dist/
- __pycache__/
- *.out, *.tar

## 技术栈

- **语言**: Go 1.23
- **HTTP 框架**: Gin
- **数据库**: PostgreSQL 15 + PostgREST
- **认证**: Supabase GoTrue (JWT/OAuth)
- **CLI**: Cobra
- **容器编排**: Kubernetes 1.32 + controller-runtime
- **集群管理**: Ray (静态节点模式)
- **API 网关**: Kong
- **可观测性**: VictoriaMetrics, Grafana, Vector

## 项目结构

```
cmd/
  neutree-api/     # REST API 服务 (Gin)
  neutree-core/    # 控制平面控制器
  neutree-cli/     # 本地部署 CLI 工具
api/v1/            # 核心 API 类型定义
controllers/       # 11 个 Kubernetes 风格控制器
internal/
  cluster/         # 集群协调 (K8s & SSH/Ray)
  orchestrator/    # 工作负载编排
  gateway/         # Kong 网关集成
  engine/          # 推理引擎管理
  auth/            # 认证授权
  routes/          # API 路由处理
pkg/
  storage/         # PostgreSQL/PostgREST 存储层
  model_registry/  # HuggingFace & 文件模型注册
  command_runner/  # SSH, Docker, K8s 命令执行
db/
  migrations/      # 数据库迁移文件
deploy/
  chart/           # Helm chart
  docker/          # Docker compose
```

## 常用命令

```bash
# 构建
make build              # 构建所有二进制文件
make docker-build       # 构建多架构 Docker 镜像

# 测试
make test               # 运行单元测试
make db-test            # 运行数据库集成测试

# 代码质量
make lint               # golangci-lint 检查
make mockgen            # 生成 mock 文件 (使用 mockery)

# 发布
make release            # 发布二进制和 Helm chart
```

## 架构模式

### 控制器协调模式 (Kubernetes Reconciliation)
- 控制器持续协调期望状态与实际状态
- Spec/Status 分离: 用户定义状态 vs 观察状态
- 幂等操作: 控制器可安全重复执行
- 软删除: 资源标记 `deletion_timestamp` 后再永久删除

### 集群管理模式
1. **Kubernetes 模式**: 轻量路由器部署，Endpoint 作为原生 K8s Deployment
2. **静态节点模式 (SSH)**: 通过 SSH 配置 Ray 集群，Docker 执行

### 数据流
```
Client → neutree-api → PostgREST → PostgreSQL
              ↓
         neutree-core (Controllers)
              ↓
         Orchestrators (K8s/Ray)
              ↓
         Kong Gateway → Model Inference
```

## 核心资源类型

- **Cluster**: 集群配置 (K8s 或静态节点)
- **Endpoint**: 推理服务端点
- **Engine**: 推理引擎 (vLLM, llama.cpp 等)
- **Workspace**: 多租户隔离
- **Role/RoleAssignment**: RBAC 权限
- **APIKey**: API 密钥管理
- **ModelRegistry/ModelCatalog**: 模型注册和目录

## 代码规范

- 使用 golangci-lint，启用 27 个 linter
- 行长度限制: 170 字符
- 函数复杂度限制: 30
- 测试使用 testify (assert, mock)
- Mock 生成使用 mockery v2.53.3

## 数据库

- 使用 PostgreSQL 行级安全 (RLS) 实现细粒度访问控制
- 迁移文件位于 `db/migrations/`，命名格式: `XXX_description.up.sql` / `.down.sql`
- PostgREST 自动从数据库 schema 生成 REST API

## 认证授权

- JWT token 验证
- API key 转换为 JWT
- 基于 Workspace 的资源隔离
- 数据库层 RLS 策略
