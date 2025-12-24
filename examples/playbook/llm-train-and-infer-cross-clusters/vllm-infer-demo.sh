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

# vLLM Inference Service Deployment Script for TKE
# Features:
# 1. Pre-check TKE cluster and tccli tool
# 2. Get cluster kubeconfig and set context
# 3. Check and install tke-hpc-controller addon if needed
# 4. Deploy vLLM inference service with HorizontalPodCronscaler

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

# Worker Cluster Configuration variables
WORKER_REGION=""
WORKER_CLUSTER_ID=""
WORKER_SUBNET_ID=""
WORKER_NAMESPACE="default"
SKIP_INSTALL_HPC="false"

# VPC information (will be extracted from cluster info)
VPC_ID=""

# Display help information
show_help() {
    cat << EOF
vLLM Inference Service Deployment Script for TKE

This script automates the deployment of vLLM inference service with the following steps:
1. Pre-check tccli and cluster connectivity
2. Get cluster admin kubeconfig and set kubectl context
3. Check and install tke-hpc-controller addon if needed
4. Deploy vLLM inference service with HorizontalPodCronscaler

Usage: $0 [options]

Options:
  -h, --help                   Display this help message
  -c, --config FILE            Configuration file path (default: ./config.env)

Required Configuration Variables (in config file):
  WORKER_REGION                Worker cluster region
  WORKER_CLUSTER_ID            Worker cluster ID
  WORKER_SUBNET_ID             Worker cluster subnet ID for internal network access

Optional Configuration Variables:
  WORKER_NAMESPACE             Namespace for deployment (default: default)
  SKIP_INSTALL_HPC             Skip tke-hpc-controller installation (default: false)

Prerequisites:
  tccli                        Tencent Cloud CLI tool (must be installed and configured)
  jq                           Command-line JSON processor
  kubectl                      Kubernetes command-line tool

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
    if [ -z "$WORKER_REGION" ]; then
        log_error "WORKER_REGION is required. Please set it in config file"
        exit 1
    fi

    if [ -z "$WORKER_CLUSTER_ID" ]; then
        log_error "WORKER_CLUSTER_ID is required. Please set it in config file"
        exit 1
    fi

    log_info "Worker Region: $WORKER_REGION"
    log_info "Worker Cluster ID: $WORKER_CLUSTER_ID"
    log_info "Worker Namespace: $WORKER_NAMESPACE"

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

    # Check if cluster exists and get VPC info
    log_info "Checking cluster existence and getting VPC info..."
    local cluster_info
    ret=0
    cluster_info=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DescribeClusters --ClusterIds "[\"$WORKER_CLUSTER_ID\"]" 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to query cluster information"
        log_error "$cluster_info"
        exit 1
    fi

    # Check if cluster exists
    local total_count
    total_count=$(echo "$cluster_info" | jq -r '.TotalCount // 0')

    if [ "$total_count" -eq 0 ]; then
        log_error "Cluster $WORKER_CLUSTER_ID not found in region $WORKER_REGION"
        exit 1
    fi

    # Extract VPC ID
    VPC_ID=$(echo "$cluster_info" | jq -r '.Clusters[0].ClusterNetworkSettings.VpcId // empty')

    if [ -z "$VPC_ID" ]; then
        log_error "Failed to get VPC ID from cluster information"
        exit 1
    fi

    log_success "Cluster found in VPC: $VPC_ID"

    # Check kubectl
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first"
        exit 1
    fi

    log_success "Prerequisites check passed"
}

# Enable cluster internal network access
enable_cluster_internal_access() {
    log_info "Checking cluster internal network access status..."

    # Check if internal access is already enabled
    local endpoint_status
    ret=0
    endpoint_status=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DescribeClusterEndpointStatus --ClusterId "$WORKER_CLUSTER_ID" 2>&1) || ret=$?

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
    if [ -z "$WORKER_SUBNET_ID" ]; then
        log_error "WORKER_SUBNET_ID is required for enabling internal access. Please set it in config file"
        exit 1
    fi

    # Enable internal access
    local enable_result
    ret=0
    enable_result=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke CreateClusterEndpoint \
        --ClusterId "$WORKER_CLUSTER_ID" \
        --SubnetId "$WORKER_SUBNET_ID" \
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
        endpoint_status=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DescribeClusterEndpointStatus --ClusterId "$WORKER_CLUSTER_ID" 2>&1) || ret=$?

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
    local context_name="worker-admin-$WORKER_CLUSTER_ID"
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
    security_info=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DescribeClusterSecurity --ClusterId "$WORKER_CLUSTER_ID" 2>&1) || ret=$?

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
    local temp_kubeconfig="/tmp/tke-kubeconfig-$WORKER_CLUSTER_ID"
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
    local context_name="worker-admin-$WORKER_CLUSTER_ID"

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

# Check and install tke-hpc-controller addon
check_and_install_hpc_controller() {
    # Check if we should skip HPC installation
    if [ "$SKIP_INSTALL_HPC" = "true" ]; then
        log_warning "SKIP_INSTALL_HPC is set to true, skipping tke-hpc-controller installation"
        return 0
    fi

    log_info "Checking tke-hpc-controller addon status..."

    local addon_name="tke-hpc-controller"
    
    # Check if addon is already installed
    local addon_info
    ret=0
    addon_info=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DescribeAddon \
        --ClusterId "$WORKER_CLUSTER_ID" \
        --AddonName "$addon_name" 2>&1) || ret=$?

    if [ $ret -eq 0 ]; then
        # Addon exists, check its phase
        local phase
        phase=$(echo "$addon_info" | jq -r '.Addons[0].Phase // empty')
        
        if [ "$phase" = "Succeeded" ]; then
            log_success "tke-hpc-controller addon is already installed and running"
            return 0
        elif [ "$phase" = "Failed" ]; then
            log_warning "tke-hpc-controller addon exists but in Failed state"
            log_info "Will attempt to reinstall..."
        else
            log_info "tke-hpc-controller addon is in phase: $phase"
            log_info "Waiting for it to become Succeeded..."
            # Continue to polling below
        fi
    else
        # Addon doesn't exist, need to install
        log_info "tke-hpc-controller addon not found, installing..."
        
        local install_result
        ret=0
        install_result=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke InstallAddon \
            --ClusterId "$WORKER_CLUSTER_ID" \
            --AddonName "$addon_name"  2>&1) || ret=$?

        if [ $ret -ne 0 ]; then
            log_error "Failed to install tke-hpc-controller addon"
            log_error "$install_result"
            exit 1
        fi

        log_success "tke-hpc-controller addon installation initiated"
    fi

    # Poll for addon status (timeout: 300 seconds / 5 minutes)
    log_info "Waiting for tke-hpc-controller addon to be ready..."
    local max_retries=150
    local retry=0

    while [ $retry -lt $max_retries ]; do
        sleep 2

        ret=0
        addon_info=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DescribeAddon \
            --ClusterId "$WORKER_CLUSTER_ID" \
            --AddonName "$addon_name" 2>&1) || ret=$?

        if [ $ret -eq 0 ]; then
            local phase
            phase=$(echo "$addon_info" | jq -r '.Addons[0].Phase // empty')

            if [ "$phase" = "Succeeded" ]; then
                log_success "tke-hpc-controller addon is ready"
                return 0
            elif [ "$phase" = "Failed" ]; then
                log_error "tke-hpc-controller addon installation failed"
                local reason
                reason=$(echo "$addon_info" | jq -r '.Addons[0].Reason // "Unknown reason"')
                log_error "Reason: $reason"
                exit 1
            elif [ "$phase" = "Installing" ] || [ "$phase" = "Upgrading" ]; then
                if [ $((retry % 10)) -eq 0 ]; then
                    log_info "Still installing... (attempt $((retry + 1))/$max_retries)"
                fi
            else
                log_info "Current phase: $phase (attempt $((retry + 1))/$max_retries)"
            fi
        else
            log_warning "Failed to query addon status, retrying..."
        fi

        retry=$((retry + 1))
    done

    log_error "Timeout waiting for tke-hpc-controller addon to be ready"
    exit 1
}

# Deploy vLLM inference service
deploy_vllm_service() {
    log_info "Deploying vLLM inference service..."

    local yaml_file="${SCRIPT_DIR}/is-qwen2-5-05b-vllm.yaml"

    if [ ! -f "$yaml_file" ]; then
        log_error "YAML file not found: $yaml_file"
        exit 1
    fi

    log_info "Using YAML file: $yaml_file"

    # Apply the YAML file
    ret=0
    if kubectl apply -f "$yaml_file" -n "$WORKER_NAMESPACE" 2>&1; then
        log_success "vLLM inference service deployed successfully"
    else
        log_error "Failed to deploy vLLM inference service"
        exit 1
    fi

    log_info "Checking deployment status..."
    
    # Wait a moment for resources to be created
    sleep 2

    # Display deployment status
    log_info "Deployment resources:"
    kubectl get deployment is-qwen2-5-05b-vllm -n "$WORKER_NAMESPACE" || log_warning "Deployment not found yet"
    
    log_info "Service resources:"
    kubectl get service is-qwen2-5-05b-vllm -n "$WORKER_NAMESPACE" || log_warning "Service not found yet"
    
    log_info "HorizontalPodCronscaler resources:"
    kubectl get horizontalpodcronscaler is-qwen -n "$WORKER_NAMESPACE" || log_warning "HorizontalPodCronscaler not found yet"

    log_success "Deployment initiated. Use 'kubectl get pods -n $WORKER_NAMESPACE' to check pod status"
}

# Display summary information
show_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "vLLM Inference Service Deployment Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Deployment Information:"
    echo "   • Region: $WORKER_REGION"
    echo "   • Cluster ID: $WORKER_CLUSTER_ID"
    echo "   • Namespace: $WORKER_NAMESPACE"
    echo "   • Context: worker-admin-$WORKER_CLUSTER_ID"
    echo ""
    echo "🔍 Next Steps:"
    echo "   1. Check deployment status:"
    echo "      kubectl get deployment is-qwen2-5-05b-vllm -n $WORKER_NAMESPACE"
    echo ""
    echo "   2. Check pod status:"
    echo "      kubectl get pods -l app.kubernetes.io/instance=is-qwen2-5-05b-vllm -n $WORKER_NAMESPACE"
    echo ""
    echo "   3. Check service:"
    echo "      kubectl get service is-qwen2-5-05b-vllm -n $WORKER_NAMESPACE"
    echo ""
    echo "   4. Check HorizontalPodCronscaler:"
    echo "      kubectl get horizontalpodcronscaler is-qwen -n $WORKER_NAMESPACE"
    echo "      kubectl describe horizontalpodcronscaler is-qwen -n $WORKER_NAMESPACE"
    echo ""
    echo "   5. View pod logs:"
    echo "      kubectl logs -l app.kubernetes.io/instance=is-qwen2-5-05b-vllm -n $WORKER_NAMESPACE"
    echo ""
    echo "   6. Test the inference service (after pods are ready):"
    echo "      kubectl port-forward svc/is-qwen2-5-05b-vllm 8000:8000 -n $WORKER_NAMESPACE"
    echo "      curl http://localhost:8000/health"
    echo ""
    echo "📝 Scheduled Scaling:"
    echo "   • Scale up to 4 replicas: Every day at 08:00"
    echo "   • Scale down to 2 replicas: Every day at 18:00"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🚀 vLLM Inference Service Deployment Script"
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
    echo "📋 Step 1/5: Checking Prerequisites"
    check_prerequisites
    echo ""

    # Step 2: Enable cluster internal network access and get kubeconfig
    echo "🔐 Step 2/5: Setting up Cluster Access"
    enable_cluster_internal_access
    get_cluster_kubeconfig
    echo ""

    # Step 3: Check and install tke-hpc-controller addon
    if [ "$SKIP_INSTALL_HPC" != "true" ]; then
        echo "🔌 Step 3/5: Checking tke-hpc-controller Addon"
        check_and_install_hpc_controller
        echo ""
    else
        echo "🔌 Step 3/5: Checking tke-hpc-controller Addon"
        log_warning "Skipping tke-hpc-controller installation (SKIP_INSTALL_HPC=true)"
        echo ""
    fi

    # Step 4: Deploy vLLM inference service
    echo "📦 Step 4/5: Deploying vLLM Inference Service"
    deploy_vllm_service
    echo ""

    # Step 5: Display summary
    echo "📊 Step 5/5: Summary"
    show_summary
}

# Execute main function
main "$@"

