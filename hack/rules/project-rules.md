# Tapestry 项目规则

## 1. 代码风格规范

### Go 代码规范

- **版本要求**: Go 1.24+ 
- **格式化**: 使用 `go fmt` 统一代码格式
- **Linting**: 使用 `golangci-lint` 进行代码检查，配置见 `.golangci.yml`
- **导入顺序**: 标准库 → 第三方库 → 项目内部包
- **命名规范**:
  - 包名：小写，简洁，避免下划线
  - 函数/方法：驼峰命名，公开函数首字母大写
  - 变量：驼峰命名，缩写词保持大写（如 `HTTPClient`）
  - 常量：大写加下划线或驼峰命名

## 2. 架构设计原则

### 分层架构

- **Tapestry Manager**: 管理层，负责 ClusterBinding 和 ResourceLeasingPolicy 生命周期
- **Tapestry Syncer**: 同步层，每个物理集群对应一个独立实例
- **Bottom-up Syncer**: 物理集群 → 虚拟集群状态同步
- **Top-down Syncer**: 虚拟集群 → 物理集群资源同步

### 高可用设计

- **Leader Election**: 所有控制器组件必须支持 leader election
- **故障隔离**: 单个 Syncer 故障不影响其他集群
- **幂等性**: 所有同步操作必须确保幂等性
- **状态一致性**: 使用 ResourceVersion 和状态哈希避免重复操作

### 模板化部署

- **共享 RBAC**: 多个 Syncer 共享 ServiceAccount、Role、RoleBinding
- **ConfigMap 模板**: 使用模板化配置支持动态 Deployment 创建
- **参数化配置**: 支持通过模板变量自定义配置

## 3. 测试规范

### 测试分层

- **单元测试**: 使用 `fakeclient` 测试控制器逻辑，覆盖率要求 80%+
- **集成测试**: 使用 `envtest` 测试组件交互
- **E2E 测试**: 完整工作流端到端测试

### 测试命令

```bash
# 单元测试（pkg目录），若需详细输出加 -v
go test ./pkg/...

# 单元+集成测试（简洁输出）
make test

# 单元+集成测试（详细输出）
make test-verbose

# 完整集成测试（基于envtest）
make test-int

# 集成测试单独某些用例（基于envtest），如测试"Virtual Node Resource Tests"用例集
make test-int-focus FOCUS="Virtual Node Resource Tests"

```

### 测试规则

- 测试文件以 `_test.go` 结尾
- 集成测试以 `_integration_test.go` 结尾
- 基于 envtest 的集成测试放在 `test/integration` 目录
- E2E 测试放在 `test/e2e/` 目录
- 使用 Ginkgo + Gomega 测试框架
- 测试中禁用 `gocyclo`、`errcheck`、`dupl`、`gosec` linter

## 4. 资源管理规范

### CRD 设计

- **API 版本**: 使用 `v1beta1`
- **Group**: `cloud.tencent.com`
- **命名**: 使用复数形式（如 `clusterbindings`）
- **字段验证**: 必须包含 OpenAPI v3 schema 验证

### 标签和注解规范

- **标签前缀**: `tapestry.io/`
- **关键标签**:
  - `tapestry.io/managed-by`: 标识 Tapestry 管理的资源
  - `tapestry.io/cluster-id`: 物理集群标识
  - `tapestry.io/physical-node-name`: 物理节点名称映射

### 常量管理

- 所有共用常量定义在 `api/v1beta1/constants.go`
- 使用明确的常量名，避免硬编码字符串
- 常量命名采用驼峰式，以类型或用途为前缀

## 5. 错误处理规范

### 错误分类

- **连接错误**: 使用指数退避重试，最大间隔 5 分钟
- **权限错误**: 更新资源状态，记录详细错误信息
- **资源冲突**: 使用哈希后缀解决命名冲突
- **验证错误**: 设置资源状态为 `Failed`，更新 conditions

### 状态管理

- **Phase**: `Pending`、`Ready`、`Failed`、`Active`、`Inactive`
- **Conditions**: 使用标准 Kubernetes condition 格式
- **错误记录**: 记录到 status.conditions 和 Kubernetes events

## 6. 日志规范

### 日志级别

- **Error**: 系统错误，需要立即关注
- **Info**: 重要操作和状态变化
- **Debug**: 详细调试信息，开发环境使用

### 日志格式

- 使用结构化日志（JSON 格式）
- 包含关键上下文字段：`component`、`operation`、资源标识
- 错误日志必须包含错误详情和堆栈信息

### 日志示例

```go
log.Info("Creating virtual node", 
    "virtualNode", virtualNodeName,
    "physicalNode", physicalNodeName,
    "resources", availableResources,
    "policiesCount", len(policies))
```

## 7. 构建和部署规范

### Makefile 目标

- `make build`: 构建二进制文件
- `make docker-build`: 构建 Docker 镜像
- `make test`: 运行测试
- `make deploy`: 部署到 Kubernetes
- `make manifests generate`: 生成 CRD 和代码

### 镜像规范

- **Manager 镜像**: `ccr.ccs.tencentyun.com/tke-eni-test/tapestry-manager`
- **Syncer 镜像**: `ccr.ccs.tencentyun.com/tke-eni-test/tapestry-syncer`
- 使用多阶段构建减少镜像大小
- 非 root 用户运行，安全约束

### 资源配置

按集群规模配置资源：

- **小规模** (< 100 节点): CPU 100m-500m, Memory 128Mi-512Mi
- **中等规模** (100-1000 节点): CPU 200m-1000m, Memory 256Mi-1Gi  
- **大规模** (> 1000 节点): CPU 500m-2000m, Memory 512Mi-2Gi

## 8. 安全规范

### RBAC 原则

- **最小权限**: 每个组件仅获得必需权限
- **专用账户**: 使用专门的 ServiceAccount
- **权限分离**: Manager 和 Syncer 使用不同的 RBAC

### 敏感信息管理

- **Kubeconfig**: 存储在 Kubernetes Secret 中
- **TLS 证书**: 使用 Kubernetes TLS 机制
- **网络安全**: 支持集群间 TLS 加密通信

## 9. 监控规范

### 指标暴露

- **端口**: Manager `:8080/metrics`, Syncer `:8080/metrics`
- **格式**: Prometheus 格式
- **关键指标**: 同步延迟、错误计数、资源利用率、Leader 状态

### 健康检查

- **Liveness**: `:8081/healthz`
- **Readiness**: `:8081/readyz`
- **超时设置**: 5 秒响应超时

## 10. 文档规范

### 文档结构

- **README.md**: 项目概述和快速开始
- **docs/**: 详细设计文档和架构说明
- **examples/**: 示例配置文件
- **API 文档**: 通过代码注释自动生成

### 注释规范

- 公开函数/方法必须有注释
- 复杂逻辑必须有行内注释
- CRD 字段必须有 `description` 标签
- 使用中英文混合，关键术语保持英文

## 11. 版本管理规范

### Git 规范

- **分支策略**: 主分支 `main`，功能分支 `feature/*`
- **提交信息**: 使用描述性提交信息，包含变更类型
- **PR 要求**: 代码审查通过，所有测试通过

### 发版规范

- **语义化版本**: 遵循 SemVer 规范
- **变更日志**: 维护 CHANGELOG.md
- **兼容性**: API 变更必须向后兼容或提供迁移指南

## 12. 开发环境规范

### 环境要求

- **Go**: 1.24.3+
- **Kubernetes**: 1.28+
- **Docker**: 用于镜像构建
- **kubectl**: 正确配置集群访问

### IDE 配置

- 启用 `gofmt` 和 `goimports` 自动格式化
- 配置 `golangci-lint` 集成
- 使用项目根目录的 `.golangci.yml` 配置
