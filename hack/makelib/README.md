# Makefile 模块化结构

本目录包含了模块化的 Makefile 文件，将原本的大型 Makefile 分解为三个功能模块：

## 文件结构

```
hack/makelib/
├── README.md          # 本说明文件
├── build.mk           # 构建相关的目标和配置
└── test.mk            # 测试相关的目标和配置
```

## 模块说明

### build.mk - 构建模块
包含以下功能：
- **构建目标**: `build`, `run-manager`, `run-syncer`
- **Docker 构建**: `docker-build`, `docker-build.manager`, `docker-build.syncer`
- **Docker 推送**: `docker-push`, `docker-push.manager`, `docker-push.syncer`
- **跨平台构建**: `docker-buildx`
- **构建依赖**: `kustomize`, `controller-gen`, `envtest`, `golangci-lint`, `ginkgo`

### test.mk - 测试模块
包含以下功能：
- **开发工具**: `manifests`, `generate`, `fmt`, `vet`, `lint`
- **单元测试**: `test`, `test-verbose`
- **集成测试**: `test-int`, `test-int-build`, `test-int-focus`

## 使用方法

主 Makefile 会自动包含所有模块文件，使用方法与之前完全相同：

```bash
# 查看所有可用命令
make help

# 构建相关
make build
make docker-build

# 测试相关
make test
make test-int
```

## 添加新模块

要添加新的模块文件：

1. 在 `hack/makelib/` 目录下创建新的 `.mk` 文件
2. 在主 Makefile 中添加 `include hack/makelib/your-module.mk`
3. 按照现有模式组织目标和配置
