# Kubeocean 文档编写规则

## 文档格式规范

### 1. 代码块格式
- **强制要求**：所有代码块必须明确指定语言标记
  - Shell 命令使用 `sh` 标记
  - YAML 配置使用 `yaml` 标记
  - 其他语言使用对应标记
- **空行要求**：代码块前后必须有空行，提高可读性
- **示例**：
  ```markdown
  执行以下命令：
  
  ```sh
  kubectl get pods
  ```
  
  查看结果...
  ```

### 2. YAML Front Matter
- **执行类文档**：需要添加 front matter 指定工作目录
  ```yaml
  ---
  cwd: ../
  ---
  ```
- **适用文档**：快速开始、安装指南等需要执行命令的教程文档

### 3. 段落结构
- 标题后添加空行
- 步骤间添加空行分隔
- 说明性文字与代码块之间添加空行
- 代码块与代码块之间添加空行

## 命令编写规范

### 1. 优先使用环境变量
- **原则**：将重复使用的值提取为环境变量
- **位置**：在开始步骤或需要自定义的地方设置环境变量
- **好的示例**：
  ```sh
  export CLUSTER_NAME=kubeocean-worker1
  export CLUSTERID=cls-worker1
  kubectl config use-context kind-$CLUSTER_NAME
  ```
- **避免**：硬编码重复值

### 2. 使用 Heredoc 创建配置文件
- **原则**：优先使用 heredoc 直接创建 YAML/配置文件，而非引用外部文件或使用占位符
- **优势**：
  - 用户可以直接复制粘贴执行
  - 环境变量自动展开，无需手动编辑
  - 避免 `sed -i` 原地修改文件
- **标准格式**：
  ```sh
  # 设置必要的环境变量
  export SUBNET_ID=subnet-abcdefgh
  
  # 使用 heredoc 创建配置文件
  cat > config.yaml <<EOF
  apiVersion: v1
  kind: Service
  metadata:
    name: my-service
    annotations:
      subnet-id: $SUBNET_ID
  EOF
  ```
- **避免**：
  ```sh
  # 不推荐：需要手动编辑文件
  sed -i "s|<subnetId>|subnet-xxx|" config.yaml
  
  # 不推荐：管道方式虽可行但不如 heredoc 直观
  cat examples/config.yaml | sed "s/<subnetId>/$SUBNET_ID/g" | kubectl create -f -
  ```

### 3. 简化多集群操作
- **模式**：使用环境变量 + 通用步骤
- **说明**：在步骤开始提供环境变量设置，告知用户如何复用步骤
- **示例**：
  ```markdown
  0. 设置环境变量
  
  ```sh
  export CLUSTER_NAME=kubeocean-worker1
  export CLUSTERID=cls-worker1
  # 将 CLUSTER_NAME 设为 kubeocean-worker2，CLUSTERID 设为 cls-worker2，
  # 再重新执行即可完成第二个业务集群注册
  ```
  ```

### 4. 命令与结果分离
- **原则**：执行命令和查看结果分开展示
- **格式**：
  ```markdown
  部署完成后，可使用 `kubectl` 查看结果
  
  ```sh
  kubectl get pods -owide -w
  ```
  
  可以观察到以下输出：
  
  ```sh
  NAME        READY   STATUS    RESTARTS   AGE
  test-pod    1/1     Running   0          10s
  ```
  ```
- **避免**：将命令、说明和输出混在一起

## 示例和注释规范

### 1. 占位符格式
- **环境变量替换**：使用 `$VARIABLE_NAME` 或 `${VARIABLE_NAME}`
- **示例值**：提供具体示例，如 `subnet-abcdefgh`、`cls-worker1`
- **注释说明**：必要时添加注释说明如何修改

## 文档类型特定规则

### 快速开始文档（Quick Start）
1. 必须包含 YAML front matter
2. 步骤编号从 0 开始（当需要预先设置环境变量时）
3. 提供完整的端到端流程
4. 每个步骤都要验证结果
5. 使用环境变量支持多集群场景
6. 命令可直接复制粘贴执行

### 安装文档（Installation）
1. 必须包含 YAML front matter
2. 按照依赖关系组织步骤
3. 提供多种安装方式（图形界面 + CLI）
4. 使用 heredoc 创建所有配置文件
5. 命令可直接复制粘贴执行

## 多语言文档同步规则

### 1. 内容一致性
- 中英文档内容必须保持同步
- 修改一个语言版本后立即同步到另一个版本
- 仅翻译文字说明，命令和配置保持一致

### 2. 命名约定
- 中文文档：`*_zh.md`
- 英文文档：`*.md`
- 同一主题的文档放在相同目录

### 3. 引用路径
- 图片、文件引用使用相对路径
- 跨语言文档互相引用时使用正确的文件名后缀

## 检查清单

在提交文档前，确保：

- [ ] 所有代码块都有语言标记
- [ ] 代码块前后有空行
- [ ] 可执行教程包含 front matter
- [ ] 重复值已提取为环境变量
- [ ] YAML 配置使用 heredoc 创建
- [ ] 命令可以直接复制粘贴执行
- [ ] 提供了预期输出示例
- [ ] 中英文文档已同步
- [ ] 环境变量命名清晰（大写+下划线）
- [ ] 包含必要的验证步骤

## 反例总结

### 不推荐的做法

1. **代码块无语言标记**
   ```markdown
   # 错误
   ```
   kubectl get pods
   ```
   
   # 正确
   ```sh
   kubectl get pods
   ```
   ```

2. **硬编码重复值**
   ```sh
   # 错误
   kubectl config use-context kind-kubeocean-worker1
   kubectl get pods -n kubeocean-worker1
   
   # 正确
   export CLUSTER_NAME=kubeocean-worker1
   kubectl config use-context kind-$CLUSTER_NAME
   kubectl get pods -n $CLUSTER_NAME
   ```

3. **需要手动编辑文件**
   ```sh
   # 错误：用户需要手动编辑 config.yaml
   kubectl apply -f config.yaml  # 文件中有 <placeholder>
   
   # 正确：直接生成可用配置
   export VALUE=real-value
   cat > config.yaml <<EOF
   key: $VALUE
   EOF
   kubectl apply -f config.yaml
   ```

4. **缺少空行导致渲染问题**
   ```markdown
   # 错误
   执行以下命令：
   ```sh
   command
   ```
   然后查看结果
   
   # 正确
   执行以下命令：
   
   ```sh
   command
   ```
   
   然后查看结果
   ```

## 维护原则

1. **用户体验优先**：文档应该让用户可以无障碍地复制粘贴命令执行
2. **自动化优先**：使用环境变量和脚本，减少手动操作
3. **清晰性优先**：结构清晰，步骤明确，每步都有验证
4. **一致性优先**：同类文档遵循相同的格式和风格
5. **可维护性优先**：使用变量和模板，便于后续更新
