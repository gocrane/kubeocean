# Tapestry 开发指南

## 快速开始

### 环境准备
```bash
# Go 1.24.3+, Kubernetes 1.28+
go version
kubectl version --client
```

### 开发流程
```bash
# 1. 安装依赖和 CRD
make install

# 2. 生成代码
make manifests generate

# 3. 代码检查
make fmt vet lint

# 4. 运行测试
make test

# 集成测试
make test-int

# 指定集成测试用例
make test-int-focus FOCUS="Virtual Node Resource Tests"

# 5. 构建
make build
```

## 编码规范速查

### 函数命名模式
```go
// ✅ 正确
func (r *VirtualPodReconciler) shouldManageVirtualPod(ctx context.Context, pod *corev1.Pod) (bool, string, error)
func validateTimeWindows(policy *ResourceLeasingPolicy) error
func isValidTimeFormat(timeStr string) bool

// ❌ 错误  
func (r *VirtualPodReconciler) ShouldManage_VirtualPod() // 下划线
func validate_time_windows() // 下划线
```

### 日志记录模式
```go
// ✅ 正确
log.Info("Creating virtual node", 
    "virtualNode", virtualNodeName,
    "physicalNode", physicalNodeName,
    "resources", availableResources)

log.Error(err, "Failed to create pod",
    "pod", pod.Name,
    "namespace", pod.Namespace)

// ❌ 错误
log.Info("Creating virtual node: " + virtualNodeName) // 字符串拼接
log.Error(err, "Error occurred") // 缺少上下文
```

### 错误处理模式
```go
// ✅ 正确
if err := r.validateTimeWindows(policy); err != nil {
    policy.Status.Phase = Failed
    r.updatePolicyConditionsWithValidationError(policy, err.Error())
    return ctrl.Result{}, r.Client.Status().Update(ctx, policy)
}

// ❌ 错误
if err != nil {
    return err // 没有状态更新
}
```

## 测试编写模式

### 单元测试结构
```go
func TestControllerFunction(t *testing.T) {
    tests := []struct {
        name        string
        input       *Type
        expected    ExpectedType
        expectError bool
    }{
        {
            name: "valid case description",
            // ...
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // 使用 fakeclient
            fakeClient := fakeclient.NewClientBuilder().
                WithScheme(scheme).
                WithObjects(tt.input).
                Build()
            
            // 测试逻辑
            result, err := function(tt.input)
            
            // 断言
            if tt.expectError {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
                assert.Equal(t, tt.expected, result)
            }
        })
    }
}
```

### 集成测试模式
```go
func TestIntegration(t *testing.T) {
    // 使用 envtest
    testEnv := &envtest.Environment{
        CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
    }
    
    cfg, err := testEnv.Start()
    require.NoError(t, err)
    defer testEnv.Stop()
    
    // 测试逻辑
}
```

## 常用代码片段

### Controller 基础结构
```go
type MyReconciler struct {
    client.Client
    Log    logr.Logger
    Scheme *runtime.Scheme
}

func (r *MyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := r.Log.WithValues("resource", req.NamespacedName)
    
    // 1. 获取资源
    resource := &MyType{}
    if err := r.Get(ctx, req.NamespacedName, resource); err != nil {
        if apierrors.IsNotFound(err) {
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, err
    }
    
    // 2. 业务逻辑
    
    // 3. 更新状态
    return ctrl.Result{}, nil
}
```

### 状态更新模式
```go
func (r *MyReconciler) updateStatus(ctx context.Context, resource *MyType, phase MyPhase, condition metav1.Condition) error {
    if resource.Status.Phase != phase {
        resource.Status.Phase = phase
        resource.Status.LastUpdateTime = &metav1.Time{Time: time.Now()}
        
        // 更新或添加 condition
        meta.SetStatusCondition(&resource.Status.Conditions, condition)
        
        return r.Client.Status().Update(ctx, resource)
    }
    return nil
}
```

### 验证函数模式
```go
func (r *MyReconciler) validateResource(resource *MyType) error {
    if resource.Spec.Field == "" {
        return fmt.Errorf("field cannot be empty")
    }
    
    // 更多验证逻辑
    
    return nil
}
```

## 调试技巧

### 本地运行
```bash
# 运行 Manager (需要先安装 CRD)
make install
make run-manager

# 运行 Syncer (需要指定 ClusterBinding)
export CLUSTER_BINDING_NAME=my-cluster
make run-syncer
```

### 日志调试
```bash
# 开启调试日志
./tapestry-manager --zap-devel --zap-log-level=debug

# 查看特定资源事件
kubectl get events --field-selector involvedObject.kind=ClusterBinding
```

### 常见问题排查

1. **CRD 未安装**: `make install`
2. **权限不足**: 检查 RBAC 配置
3. **Leader Election 失败**: 检查 lease 权限
4. **测试失败**: 运行 `make test-verbose` 查看详细输出

## 提交规范

### 提交信息格式
```
type(scope): description

[optional body]

[optional footer]
```

### 提交类型
- `feat`: 新功能
- `fix`: 修复 bug  
- `docs`: 文档变更
- `style`: 格式化代码
- `refactor`: 重构代码
- `test`: 添加测试
- `chore`: 构建/工具变更

### 示例
```
feat(controller): add time window validation for ResourceLeasingPolicy

- Add validateTimeWindows function with format checking
- Update status to Failed when validation fails  
- Add comprehensive unit tests

Closes #123
```
