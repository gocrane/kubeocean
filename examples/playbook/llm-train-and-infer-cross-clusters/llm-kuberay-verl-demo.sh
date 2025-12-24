#!/bin/bash

# Copyright 2025 The Kubeocean Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# LLM KubeRay VERL Training Deployment Script for TKE
# Features:
# 1. Pre-check TKE cluster and required tools
# 2. Get manager cluster kubeconfig and set context
# 3. Deploy KubeRay Operator using Helm
# 4. Create RayCluster for VERL training
# 5. Wait for RayCluster pods to be ready
# 6. Submit VERL training job to Ray cluster
# 7. Verify job submission status

set -e
set -o pipefail

# Color definitions
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

log_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

log_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

log_error() {
    echo -e "${RED}❌ $1${NC}"
}

# Error handling
trap 'log_error "Script execution failed with exit code: $?, at line: $LINENO"' ERR

# Script root directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Configuration file path
CONFIG_FILE="${SCRIPT_DIR}/config.env"

# Manager Cluster Configuration variables
MANAGER_REGION=""
MANAGER_CLUSTER_ID=""
MANAGER_SUBNET_ID=""
MANAGER_NAMESPACE="default"

# RayCluster configuration
RAYCLUSTER_NAME="verl-cluster"
RAYCLUSTER_YAML="${SCRIPT_DIR}/verl-raycluster.yaml"

# Time window checking (default: false = check time window)
SKIP_CURRENT_TIME_CHECKING="false"

# VPC information (will be extracted from cluster info)
VPC_ID=""

# Ray job ID (will be extracted after job submission)
RAY_JOB_ID=""

# Display help information
show_help() {
    cat << EOF
LLM KubeRay VERL Training Deployment Script for TKE

This script automates the deployment of VERL training on RayCluster with the following steps:
1. Pre-check tccli, kubectl, helm and cluster connectivity
2. Get manager cluster admin kubeconfig and set kubectl context
3. Deploy KubeRay Operator using Helm
4. Create RayCluster for VERL training
5. Wait for RayCluster pods to be ready
6. Submit VERL training job to Ray cluster
7. Verify job submission status

Usage: $0 [options]

Options:
  -h, --help                   Display this help message
  -c, --config FILE            Configuration file path (default: ./config.env)

Required Configuration Variables (in config file):
  MANAGER_REGION               Manager cluster region
  MANAGER_CLUSTER_ID           Manager cluster ID
  MANAGER_SUBNET_ID            Manager cluster subnet ID for internal network access

Optional Configuration Variables:
  MANAGER_NAMESPACE            Namespace for deployment (default: default)
  SKIP_CURRENT_TIME_CHECKING   Skip time window checking (default: false)
                               When false, script checks if current time is between 18:00-08:00
                               When true, skips the time check

Prerequisites:
  tccli                        Tencent Cloud CLI tool (must be installed and configured)
  jq                           Command-line JSON processor
  kubectl                      Kubernetes command-line tool
  helm                         Helm package manager (version 3.x required)

Examples:
  # Use default configuration file (./config.env)
  $0

  # Use custom configuration file
  $0 --config /path/to/my-config.env

Configuration File Example:
  cp config.env.example config.env
  # Edit config.env with your values
  vim config.env

EOF
}

# Load configuration from file
load_config() {
    if [[ -f "$CONFIG_FILE" ]]; then
        log_info "Loading configuration from: $CONFIG_FILE"
        set -a
        # shellcheck disable=SC1090
        source "$CONFIG_FILE"
        set +a
        log_success "Configuration loaded"
    fi
}

# Parse command line arguments
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -h|--help)
                show_help
                exit 0
                ;;
            -c|--config)
                CONFIG_FILE="$2"
                shift 2
                ;;
            *)
                log_error "Unknown argument: $1"
                log_info "Only -h/--help and -c/--config options are supported"
                log_info "All other configurations should be provided in config file"
                show_help
                exit 1
                ;;
        esac
    done
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    # Check required parameters
    if [ -z "$MANAGER_REGION" ]; then
        log_error "MANAGER_REGION is required. Please set it in config file"
        exit 1
    fi

    if [ -z "$MANAGER_CLUSTER_ID" ]; then
        log_error "MANAGER_CLUSTER_ID is required. Please set it in config file"
        exit 1
    fi

    log_info "Manager Region: $MANAGER_REGION"
    log_info "Manager Cluster ID: $MANAGER_CLUSTER_ID"
    log_info "Manager Namespace: $MANAGER_NAMESPACE"

    # Check tccli installation
    if ! command -v tccli &> /dev/null; then
        log_error "tccli is not installed. Please install Tencent Cloud CLI first"
        log_error "Installation guide: https://cloud.tencent.com/document/product/440/6176"
        exit 1
    fi

    log_success "tccli is installed"

    # Check tccli configuration
    if ! tccli --version &> /dev/null; then
        log_error "tccli is not configured properly. Please run 'tccli configure' first"
        exit 1
    fi

    log_success "tccli configuration check passed"

    # Check jq installation
    if ! command -v jq &> /dev/null; then
        log_error "jq is not installed. Please install jq first"
        log_error "Installation guide: https://jqlang.org/download/"
        exit 1
    fi

    log_success "jq is installed"

    # Check kubectl
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first"
        exit 1
    fi

    log_success "kubectl is installed"

    # Check helm
    if ! command -v helm &> /dev/null; then
        log_error "helm is not installed. Please install helm first"
        log_error "Installation guide: https://helm.sh/docs/intro/install/"
        exit 1
    fi

    log_success "helm is installed"

    # Check if cluster exists and get VPC info
    log_info "Checking cluster existence and getting VPC info..."
    local cluster_info
    ret=0
    cluster_info=$(TENCENTCLOUD_REGION="$MANAGER_REGION" tccli tke DescribeClusters --ClusterIds "[\"$MANAGER_CLUSTER_ID\"]" 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to query cluster information"
        log_error "$cluster_info"
        exit 1
    fi

    # Check if cluster exists
    local total_count
    total_count=$(echo "$cluster_info" | jq -r '.TotalCount // 0')

    if [ "$total_count" -eq 0 ]; then
        log_error "Cluster $MANAGER_CLUSTER_ID not found in region $MANAGER_REGION"
        exit 1
    fi

    # Extract VPC ID
    VPC_ID=$(echo "$cluster_info" | jq -r '.Clusters[0].ClusterNetworkSettings.VpcId // empty')

    if [ -z "$VPC_ID" ]; then
        log_error "Failed to get VPC ID from cluster information"
        exit 1
    fi

    log_success "Cluster found in VPC: $VPC_ID"

    # Check RayCluster YAML file
    if [ ! -f "$RAYCLUSTER_YAML" ]; then
        log_error "RayCluster YAML file not found: $RAYCLUSTER_YAML"
        exit 1
    fi

    log_success "RayCluster YAML file found: $RAYCLUSTER_YAML"
    
    # Check time window
    check_time_window
    
    log_success "Prerequisites check passed"
}

# Check if current time is within allowed time window (18:00-08:00)
check_time_window() {
    if [ "$SKIP_CURRENT_TIME_CHECKING" = "true" ]; then
        log_warning "Time window checking is skipped (SKIP_CURRENT_TIME_CHECKING=true)"
        return 0
    fi
    
    log_info "Checking current time window..."
    
    # Get current hour (0-23)
    local current_hour
    current_hour=$(date +%H)
    
    # Remove leading zero if present
    current_hour=$((10#$current_hour))
    
    # Check if time is between 18:00-23:59 or 00:00-08:00
    if [ $current_hour -ge 18 ] || [ $current_hour -lt 8 ]; then
        log_success "Current time is within allowed window (18:00-08:00)"
        log_info "Current hour: ${current_hour}:00"
    else
        log_error "Current time is NOT within allowed window!"
        log_error "Training jobs can only be submitted between 18:00-08:00"
        log_error "Current hour: ${current_hour}:00"
        log_error ""
        log_error "Options:"
        log_error "  1. Wait until 18:00 to run this script"
        log_error "  2. Set SKIP_CURRENT_TIME_CHECKING=true in config.env to skip this check"
        exit 1
    fi
}

# Enable cluster internal network access
enable_cluster_internal_access() {
    log_info "Checking cluster internal network access status..."

    # Check if internal access is already enabled
    local endpoint_status
    ret=0
    endpoint_status=$(TENCENTCLOUD_REGION="$MANAGER_REGION" tccli tke DescribeClusterEndpointStatus --ClusterId "$MANAGER_CLUSTER_ID" 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to query cluster endpoint status"
        log_error "$endpoint_status"
        exit 1
    fi

    local status
    status=$(echo "$endpoint_status" | jq -r '.Status // empty')

    if [ "$status" = "Created" ]; then
        log_success "Cluster internal network access is already enabled"
        return 0
    fi

    # Need to enable internal access
    log_info "Enabling cluster internal network access..."

    # Check subnet ID parameter
    if [ -z "$MANAGER_SUBNET_ID" ]; then
        log_error "MANAGER_SUBNET_ID is required for enabling internal access. Please set it in config file"
        exit 1
    fi

    # Enable internal access
    local enable_result
    ret=0
    enable_result=$(TENCENTCLOUD_REGION="$MANAGER_REGION" tccli tke CreateClusterEndpoint \
        --ClusterId "$MANAGER_CLUSTER_ID" \
        --SubnetId "$MANAGER_SUBNET_ID" \
        --IsExtranet false 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to enable cluster internal network access"
        log_error "$enable_result"
        exit 1
    fi

    log_info "Waiting for internal network access to be ready..."

    # Poll for status (timeout: 120 seconds)
    local max_retries=60
    local retry=0

    while [ $retry -lt $max_retries ]; do
        sleep 2

        ret=0
        endpoint_status=$(TENCENTCLOUD_REGION="$MANAGER_REGION" tccli tke DescribeClusterEndpointStatus --ClusterId "$MANAGER_CLUSTER_ID" 2>&1) || ret=$?

        if [ $ret -eq 0 ]; then
            status=$(echo "$endpoint_status" | jq -r '.Status // empty')

            if [ "$status" = "Created" ]; then
                log_success "Cluster internal network access enabled successfully"
                return 0
            elif [ "$status" = "Creating" ]; then
                log_info "Still creating... (attempt $((retry + 1))/$max_retries)"
            else
                log_warning "Unexpected status: $status"
            fi
        fi

        retry=$((retry + 1))
    done

    log_error "Timeout waiting for cluster internal network access to be ready"
    exit 1
}

# Get cluster kubeconfig and set context
get_cluster_kubeconfig() {
    log_info "Getting cluster kubeconfig..."

    # Check if context already exists
    local context_name="manager-admin-$MANAGER_CLUSTER_ID"
    if kubectl config get-contexts "$context_name" &> /dev/null; then
        log_info "Context '$context_name' already exists, reusing it"
        kubectl config use-context "$context_name"
        log_success "Switched to existing context: $context_name"
        return 0
    fi

    log_info "Context not found, fetching kubeconfig from TKE..."

    # Wait for internal load balancer information to update
    log_info "Waiting 5 seconds for internal load balancer information to update..."
    sleep 5

    # Get cluster security info (including kubeconfig)
    local security_info
    ret=0
    security_info=$(TENCENTCLOUD_REGION="$MANAGER_REGION" tccli tke DescribeClusterSecurity --ClusterId "$MANAGER_CLUSTER_ID" 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to get cluster security information"
        log_error "$security_info"
        exit 1
    fi

    # Extract internal kubeconfig
    local kubeconfig_content
    kubeconfig_content=$(echo "$security_info" | jq -r '.Kubeconfig // empty')

    if [ -z "$kubeconfig_content" ]; then
        log_error "Failed to extract kubeconfig from cluster security information"
        exit 1
    fi

    # Extract PgwEndpoint (internal network endpoint)
    local pgw_endpoint
    pgw_endpoint=$(echo "$security_info" | jq -r '.PgwEndpoint // empty')

    if [ -z "$pgw_endpoint" ]; then
        log_error "Failed to extract PgwEndpoint from cluster security information"
        exit 1
    fi

    log_info "PgwEndpoint: $pgw_endpoint"

    # Save kubeconfig to temporary file
    local temp_kubeconfig="/tmp/tke-kubeconfig-$MANAGER_CLUSTER_ID"
    echo "$kubeconfig_content" > "$temp_kubeconfig"

    # Replace server address with PgwEndpoint
    log_info "Replacing server address with PgwEndpoint..."
    local cluster_name
    cluster_name=$(kubectl --kubeconfig="$temp_kubeconfig" config view -o jsonpath='{.clusters[0].name}' 2>/dev/null)

    if [ -n "$cluster_name" ]; then
        kubectl --kubeconfig="$temp_kubeconfig" config set-cluster "$cluster_name" --server="https://$pgw_endpoint" &> /dev/null
        log_success "Server address updated to: $pgw_endpoint"
    else
        log_error "Failed to get cluster name from kubeconfig"
        exit 1
    fi

    # Verify connection before merging
    log_info "Verifying cluster connection..."
    if ! kubectl --kubeconfig="$temp_kubeconfig" cluster-info &> /dev/null; then
        log_error "Failed to connect to cluster using the kubeconfig"
        log_error "Please check if the cluster internal network access is properly configured"
        rm -f "$temp_kubeconfig"
        exit 1
    fi
    log_success "Successfully verified cluster connection"

    # Set kubectl context name
    local context_name="manager-admin-$MANAGER_CLUSTER_ID"

    log_info "Merging kubeconfig to default kubectl config"
    log_info "Context name: $context_name"

    # Get default KUBECONFIG path
    local default_kubeconfig="${HOME}/.kube/config"

    # Create .kube directory if it doesn't exist
    mkdir -p "${HOME}/.kube"

    # If default kubeconfig doesn't exist, create an empty one
    if [ ! -f "$default_kubeconfig" ]; then
        touch "$default_kubeconfig"
    fi

    # Merge the TKE kubeconfig into default kubeconfig
    KUBECONFIG="$default_kubeconfig:$temp_kubeconfig" kubectl config view --flatten > "${temp_kubeconfig}.merged"
    mv "${temp_kubeconfig}.merged" "$default_kubeconfig"

    # Get the original context name from temp kubeconfig
    local original_context
    original_context=$(kubectl --kubeconfig="$temp_kubeconfig" config current-context 2>/dev/null)

    # Rename context to our desired name
    if [ -n "$original_context" ]; then
        kubectl config rename-context "$original_context" "$context_name" &> /dev/null || true
    fi

    # Set as current context
    kubectl config use-context "$context_name" &> /dev/null

    # Clean up temporary file
    rm -f "$temp_kubeconfig"

    log_success "Kubeconfig merged to: $default_kubeconfig"
    log_success "Context name: $context_name"
    log_success "Cluster is ready for use"
}

# Deploy KubeRay Operator using Helm
deploy_kuberay_operator() {
    log_info "Checking KubeRay Operator status..."

    # Check if KubeRay Operator is already installed in the namespace
    if helm list -n "$MANAGER_NAMESPACE" | grep -q kuberay-operator; then
        log_success "KubeRay Operator is already installed in namespace: $MANAGER_NAMESPACE"
        return 0
    fi

    log_info "Installing KubeRay Operator using Helm in namespace: $MANAGER_NAMESPACE"

    # Add KubeRay Helm repository
    log_info "Adding KubeRay Helm repository..."
    helm repo add kuberay https://ray-project.github.io/kuberay-helm/ || log_warning "Helm repo may already exist"
    helm repo update

    # Install KubeRay Operator
    ret=0
    helm install kuberay-operator kuberay/kuberay-operator \
        --namespace "$MANAGER_NAMESPACE" \
        --version 1.5.0 2>&1 || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to install KubeRay Operator"
        exit 1
    fi

    log_success "KubeRay Operator installed successfully"

    # Wait for operator to be ready
    log_info "Waiting for KubeRay Operator to be ready..."
    kubectl wait --for=condition=available --timeout=180s \
        deployment/kuberay-operator -n "$MANAGER_NAMESPACE" || log_warning "Operator may still be starting"

    log_success "KubeRay Operator is ready"
}

# Create RayCluster
create_raycluster() {
    log_info "Checking if RayCluster '$RAYCLUSTER_NAME' already exists..."

    # Check if RayCluster already exists
    if kubectl get raycluster "$RAYCLUSTER_NAME" -n "$MANAGER_NAMESPACE" &> /dev/null; then
        log_success "RayCluster '$RAYCLUSTER_NAME' already exists, skipping creation"
        return 0
    fi

    log_info "Creating RayCluster '$RAYCLUSTER_NAME'..."

    # Apply RayCluster YAML
    ret=0
    kubectl apply -f "$RAYCLUSTER_YAML" -n "$MANAGER_NAMESPACE" 2>&1 || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to create RayCluster"
        exit 1
    fi

    log_success "RayCluster '$RAYCLUSTER_NAME' created successfully"
}

# Wait for RayCluster pods to be ready
wait_for_raycluster_ready() {
    log_info "Waiting for RayCluster pods to be ready..."

    local max_retries=180  # 6 minutes (180 * 2 seconds)
    local retry=0

    while [ $retry -lt $max_retries ]; do
        sleep 2

        # Get all pods for this RayCluster
        local pods_status
        pods_status=$(kubectl get pods -n "$MANAGER_NAMESPACE" \
            -l ray.io/cluster="$RAYCLUSTER_NAME" \
            -o jsonpath='{range .items[*]}{.metadata.name}:{.status.phase}{"\n"}{end}' 2>&1) || true

        if [ -z "$pods_status" ]; then
            if [ $((retry % 10)) -eq 0 ]; then
                log_info "Waiting for pods to be created... (attempt $((retry + 1))/$max_retries)"
            fi
            retry=$((retry + 1))
            continue
        fi

        # Check if all pods are Running
        local all_running=true
        local pod_count=0
        local running_count=0

        while IFS=: read -r pod_name pod_phase; do
            if [ -n "$pod_name" ]; then
                pod_count=$((pod_count + 1))
                if [ "$pod_phase" = "Running" ]; then
                    running_count=$((running_count + 1))
                else
                    all_running=false
                fi
            fi
        done <<< "$pods_status"

        if [ "$pod_count" -eq 0 ]; then
            if [ $((retry % 10)) -eq 0 ]; then
                log_info "Waiting for pods to be created... (attempt $((retry + 1))/$max_retries)"
            fi
        elif $all_running && [ "$running_count" -gt 0 ]; then
            log_success "All RayCluster pods are running ($running_count/$pod_count)"
            
            # Display pod status
            log_info "RayCluster pods status:"
            kubectl get pods -n "$MANAGER_NAMESPACE" -l ray.io/cluster="$RAYCLUSTER_NAME"
            
            return 0
        else
            if [ $((retry % 10)) -eq 0 ]; then
                log_info "Pods status: $running_count/$pod_count running (attempt $((retry + 1))/$max_retries)"
            fi
        fi

        retry=$((retry + 1))
    done

    log_error "Timeout waiting for RayCluster pods to be ready"
    log_info "Current pod status:"
    kubectl get pods -n "$MANAGER_NAMESPACE" -l ray.io/cluster="$RAYCLUSTER_NAME"
    exit 1
}

# Submit VERL training job
submit_verl_training_job() {
    log_info "Submitting VERL training job to RayCluster..."

    # Find the Ray head pod
    local head_pod
    head_pod=$(kubectl get pods -n "$MANAGER_NAMESPACE" \
        -l ray.io/cluster="$RAYCLUSTER_NAME",ray.io/node-type=head \
        -o jsonpath='{.items[0].metadata.name}' 2>&1)

    if [ -z "$head_pod" ]; then
        log_error "Failed to find Ray head pod"
        exit 1
    fi

    log_info "Ray head pod: $head_pod"

    # Prepare working directory
    log_info "Preparing working directory in Ray head pod..."
    kubectl exec -n "$MANAGER_NAMESPACE" "$head_pod" -- bash -c "mkdir -p /workspace/work" || true

    # Submit Ray job
    log_info "Submitting VERL training job..."
    
    local job_submit_output
    job_submit_output=$(kubectl exec -n "$MANAGER_NAMESPACE" "$head_pod" -- bash -c "
cd /workspace/work && \
ray job submit --no-wait --working-dir /workspace/work -- \
PYTHONUNBUFFERED=1 python3 -m verl.trainer.main_ppo \
data.train_files=/root/data/gsm8k/train.parquet \
data.val_files=/root/data/gsm8k/test.parquet \
data.train_batch_size=256 \
data.max_prompt_length=512 \
data.max_response_length=256 \
actor_rollout_ref.model.path=/root/models/Qwen2.5-0.5B-Instruct \
actor_rollout_ref.actor.optim.lr=1e-6 \
actor_rollout_ref.actor.ppo_mini_batch_size=64 \
actor_rollout_ref.actor.ppo_micro_batch_size_per_gpu=4 \
actor_rollout_ref.rollout.log_prob_micro_batch_size_per_gpu=8 \
actor_rollout_ref.rollout.tensor_model_parallel_size=1 \
actor_rollout_ref.rollout.gpu_memory_utilization=0.4 \
actor_rollout_ref.ref.log_prob_micro_batch_size_per_gpu=4 \
critic.optim.lr=1e-5 \
critic.model.path=/root/models/Qwen2.5-0.5B-Instruct \
critic.ppo_micro_batch_size_per_gpu=4 \
algorithm.kl_ctrl.kl_coef=0.001 \
trainer.logger=['console'] \
trainer.val_before_train=False \
trainer.default_hdfs_dir=null \
trainer.n_gpus_per_node=2 \
trainer.nnodes=2 \
trainer.save_freq=150 \
trainer.test_freq=10 \
trainer.default_local_dir=/data/checkpoints/verl_Qwen2.5_gsm8k_2 \
trainer.total_epochs=10 2>&1
" 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to submit VERL training job"
        log_error "$job_submit_output"
        exit 1
    fi

    log_success "VERL training job submitted"
    echo "$job_submit_output"

    # Extract job ID from output
    local job_id
    job_id=$(echo "$job_submit_output" | grep -oP "Job '.*?' submitted" | grep -oP "'.*?'" | tr -d "'")
    
    if [ -z "$job_id" ]; then
        # Try alternative format
        job_id=$(echo "$job_submit_output" | grep -oP "raysubmit_[a-zA-Z0-9]+" | head -1)
    fi

    if [ -n "$job_id" ]; then
        # Save job ID to global variable
        RAY_JOB_ID="$job_id"
        log_info "Ray Job ID: $RAY_JOB_ID"
        
        # Wait a moment for job to initialize
        log_info "Waiting 5 seconds for job to initialize..."
        sleep 5
        
        # Check job status
        log_info "Checking job status..."
        local job_status
        job_status=$(kubectl exec -n "$MANAGER_NAMESPACE" "$head_pod" -- bash -c "ray job status $RAY_JOB_ID" 2>&1) || true
        
        log_info "Job status output:"
        echo "$job_status"
        
        if echo "$job_status" | grep -qE "(RUNNING|SUCCEEDED)"; then
            log_success "VERL training job is running successfully"
        elif echo "$job_status" | grep -q "PENDING"; then
            log_info "VERL training job is pending, it will start soon"
        else
            log_warning "Job status check returned unexpected result, but job was submitted"
        fi
    else
        log_warning "Could not extract job ID, but job submission command completed"
    fi
}

# Display summary information
show_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "VERL Training Deployment Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Deployment Information:"
    echo "   • Region: $MANAGER_REGION"
    echo "   • Cluster ID: $MANAGER_CLUSTER_ID"
    echo "   • Namespace: $MANAGER_NAMESPACE"
    echo "   • RayCluster Name: $RAYCLUSTER_NAME"
    echo "   • Context: manager-admin-$MANAGER_CLUSTER_ID"
    if [ -n "$RAY_JOB_ID" ]; then
        echo "   • Ray Job ID: $RAY_JOB_ID"
    fi
    echo ""
    echo "🔍 Next Steps:"
    echo "   1. Check RayCluster status:"
    echo "      kubectl get raycluster $RAYCLUSTER_NAME -n $MANAGER_NAMESPACE"
    echo "      kubectl describe raycluster $RAYCLUSTER_NAME -n $MANAGER_NAMESPACE"
    echo ""
    echo "   2. Check Ray pods:"
    echo "      kubectl get pods -n $MANAGER_NAMESPACE -l ray.io/cluster=$RAYCLUSTER_NAME"
    echo ""
    echo "   3. View Ray head pod logs:"
    echo "      HEAD_POD=\$(kubectl get pods -n $MANAGER_NAMESPACE -l ray.io/cluster=$RAYCLUSTER_NAME,ray.io/node-type=head -o jsonpath='{.items[0].metadata.name}')"
    echo "      kubectl logs -n $MANAGER_NAMESPACE \$HEAD_POD -f"
    echo ""
    
    if [ -n "$RAY_JOB_ID" ]; then
        echo "   4. Check Ray job status:"
        echo "      kubectl exec -n $MANAGER_NAMESPACE \$HEAD_POD -- ray job status $RAY_JOB_ID"
        echo ""
        echo "   5. Monitor training progress:"
        echo "      kubectl exec -n $MANAGER_NAMESPACE \$HEAD_POD -- ray job logs $RAY_JOB_ID --follow"
        echo ""
        echo "   6. List all Ray jobs:"
        echo "      kubectl exec -n $MANAGER_NAMESPACE \$HEAD_POD -- ray job list"
        echo ""
    else
        echo "   4. Check Ray job status (replace JOB_ID with actual job ID):"
        echo "      kubectl exec -n $MANAGER_NAMESPACE \$HEAD_POD -- ray job status JOB_ID"
        echo ""
        echo "   5. List all Ray jobs:"
        echo "      kubectl exec -n $MANAGER_NAMESPACE \$HEAD_POD -- ray job list"
        echo ""
    fi
    
    echo "   7. Access Ray Dashboard (port-forward):"
    echo "      kubectl port-forward -n $MANAGER_NAMESPACE \$HEAD_POD 8265:8265"
    echo "      # Then open http://localhost:8265 in your browser"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🚀 LLM KubeRay VERL Training Deployment Script"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""

    # Step 0: Parse config file path first
    for arg in "$@"; do
        if [[ "$arg" == "-c" || "$arg" == "--config" ]]; then
            shift
            CONFIG_FILE="$1"
            break
        fi
        shift
    done

    # Load configuration file
    load_config

    # Parse arguments (command line overrides config file)
    parse_args "$@"

    # Step 1: Check prerequisites
    echo "📋 Step 1/7: Checking Prerequisites"
    check_prerequisites
    echo ""

    # Step 2: Enable cluster internal network access and get kubeconfig
    echo "🔐 Step 2/7: Setting up Manager Cluster Access"
    enable_cluster_internal_access
    get_cluster_kubeconfig
    echo ""

    # Step 3: Deploy KubeRay Operator
    echo "🔌 Step 3/7: Deploying KubeRay Operator"
    deploy_kuberay_operator
    echo ""

    # Step 4: Create RayCluster
    echo "📦 Step 4/7: Creating RayCluster"
    create_raycluster
    echo ""

    # Step 5: Wait for RayCluster pods to be ready
    echo "⏳ Step 5/7: Waiting for RayCluster Pods"
    wait_for_raycluster_ready
    echo ""

    # Step 6: Submit VERL training job
    echo "🚀 Step 6/7: Submitting VERL Training Job"
    submit_verl_training_job
    echo ""

    # Step 7: Display summary
    echo "📊 Step 7/7: Summary"
    show_summary
}

# Execute main function
main "$@"

