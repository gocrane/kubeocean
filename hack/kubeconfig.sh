#!/bin/bash

# 开启错误捕获和显示
set -e
trap 'echo "Error occurred at line $LINENO while executing command: $BASH_COMMAND"' ERR

# 参数校验
if [ "$#" -ne 3 ]; then
    echo "Usage: $0 <serviceaccount-name> <namespace> <output-file>"
    exit 1
fi

SERVICE_ACCOUNT_NAME=$1
NAMESPACE=$2
OUTPUT_FILE=$3

# 获取当前上下文和集群名称
CURRENT_CONTEXT=$(kubectl config current-context)
CLUSTER_NAME=$(kubectl config get-contexts "$CURRENT_CONTEXT" | awk '{print $3}' | tail -n 1)

# 获取集群 API server 地址
SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')

# 获取 ServiceAccount 的 Secret 名称，直接使用 SERVICE_ACCOUNT_NAME
SECRET_NAME=$SERVICE_ACCOUNT_NAME

# 获取 Token 和 CA 证书数据
TOKEN=$(kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" -o jsonpath='{.data.token}' | base64 --decode)
CA_DATA=$(kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" -o jsonpath='{.data.ca\.crt}')

# 创建 kubeconfig 文件
cat <<EOF > "$OUTPUT_FILE"
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: "$CA_DATA"
    server: "$SERVER"
  name: "$CLUSTER_NAME"
contexts:
- context:
    cluster: "$CLUSTER_NAME"
    user: "$SERVICE_ACCOUNT_NAME"
    namespace: "$NAMESPACE"
  name: "$SERVICE_ACCOUNT_NAME@$CLUSTER_NAME"
current-context: "$SERVICE_ACCOUNT_NAME@$CLUSTER_NAME"
users:
- name: "$SERVICE_ACCOUNT_NAME"
  user:
    token: "$TOKEN"
EOF
echo "Kubeconfig file has been written to $OUTPUT_FILE"
