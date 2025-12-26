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

# Kubeocean LLM Training and Inference Demo - One-Click Deployment Script
# Features:
# 1. Pre-check required tools
# 2. Load configuration from config.env
# 3. Install Kubeocean using install-tke.sh
# 4. Deploy vLLM inference service
# 5. Patch ResourceLeasingPolicy timeWindows (if SKIP_CURRENT_TIME_CHECKING=true)
# 6. Deploy VERL training job on manager cluster

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

# Installation script paths
INSTALL_TKE_SCRIPT="${SCRIPT_DIR}/../installation-tke/install-tke.sh"
VLLM_INFER_SCRIPT="${SCRIPT_DIR}/vllm-infer-demo.sh"
VERL_TRAINING_SCRIPT="${SCRIPT_DIR}/llm-kuberay-verl-demo.sh"

# Configuration variables (will be loaded from config.env)
MANAGER_REGION=""
MANAGER_CLUSTER_ID=""
MANAGER_SUBNET_ID=""
WORKER_REGION=""
WORKER_CLUSTER_ID=""
WORKER_SUBNET_ID=""
WORKER_KUBECONFIG="/tmp/kubeconfig-worker"
WORKER_CLUSTER_NAME="example-cluster"
SKIP_MANAGER_UNINSTALL="false"
SKIP_INSTALL_HPC="false"
SKIP_CURRENT_TIME_CHECKING="false"

# Ray job ID (will be extracted after training deployment)
RAY_JOB_ID=""
RAYCLUSTER_NAME="verl-cluster"
MANAGER_NAMESPACE="default"

# Display help information
show_help() {
    cat << EOF
Kubeocean LLM Training and Inference Demo - One-Click Deployment Script

This script automates the complete deployment process by sequentially executing:
1. Pre-check required tools (tccli, jq, kubectl, helm)
2. Load configuration from config.env
3. Install Kubeocean using install-tke.sh
4. Deploy vLLM inference service using vllm-infer-demo.sh
5. Deploy VERL training job using llm-kuberay-verl-demo.sh

Usage: $0 [options]

Options:
  -h, --help                   Display this help message
  -c, --config FILE            Configuration file path (default: ./config.env)

Required Configuration Variables (in config file):
  MANAGER_REGION               Manager cluster region
  MANAGER_CLUSTER_ID           Manager cluster ID
  MANAGER_SUBNET_ID            Manager cluster subnet ID
  WORKER_REGION                Worker cluster region
  WORKER_CLUSTER_ID            Worker cluster ID
  WORKER_SUBNET_ID             Worker cluster subnet ID

Optional Configuration Variables:
  WORKER_KUBECONFIG            Worker kubeconfig path (default: /tmp/kubeconfig-worker)
  WORKER_CLUSTER_NAME          Worker cluster name for binding (default: example-cluster)
  SKIP_MANAGER_UNINSTALL       Skip manager uninstall (default: false)
  SKIP_INSTALL_HPC             Skip tke-hpc-controller installation (default: false)
  SKIP_CURRENT_TIME_CHECKING   Skip time window checking (default: false)
                               When false, training can only be submitted between 18:00-08:00
                               When true, skips time check and clears RLP timeWindows

Prerequisites:
  tccli                        Tencent Cloud CLI tool (must be installed and configured)
  jq                           Command-line JSON processor
  kubectl                      Kubernetes command-line tool
  helm v3.x                    Helm package manager (version 3.x required)

Examples:
  # Use default configuration file (./config.env)
  $0

  # Use custom configuration file
  $0 -c /path/to/my-config.env

  # Quick setup
  cp config.env.example config.env
  vim config.env
  $0

EOF
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
                show_help
                exit 1
                ;;
        esac
    done
}

# Check if config file exists
check_config_file() {
    if [ ! -f "$CONFIG_FILE" ]; then
        log_error "Configuration file not found: $CONFIG_FILE"
        log_info "Please create config file first:"
        log_info "  cp config.env.example config.env"
        log_info "  vim config.env"
        exit 1
    fi
    
    log_success "Configuration file found: $CONFIG_FILE"
}

# Load configuration from config file
load_config() {
    log_info "Loading configuration from: $CONFIG_FILE"

    # Source the config file
    set -a
    # shellcheck disable=SC1090
    source "$CONFIG_FILE"
    set +a

    # Log loaded configuration
    log_info "Manager Region: ${MANAGER_REGION}"
    log_info "Manager Cluster ID: ${MANAGER_CLUSTER_ID}"
    log_info "Worker Region: ${WORKER_REGION}"
    log_info "Worker Cluster ID: ${WORKER_CLUSTER_ID}"
    log_info "Worker Cluster Name: ${WORKER_CLUSTER_NAME}"

    log_success "Configuration loaded"
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking required tools..."

    local missing_tools=()

    # Check tccli
    if ! command -v tccli &> /dev/null; then
        missing_tools+=("tccli")
    else
        log_success "tccli is installed"
    fi

    # Check jq
    if ! command -v jq &> /dev/null; then
        missing_tools+=("jq")
    else
        log_success "jq is installed"
    fi

    # Check kubectl
    if ! command -v kubectl &> /dev/null; then
        missing_tools+=("kubectl")
    else
        log_success "kubectl is installed"
    fi

    # Check helm
    if ! command -v helm &> /dev/null; then
        missing_tools+=("helm")
    else
        log_success "helm is installed"
    fi

    # If any tools are missing, report and exit
    if [ ${#missing_tools[@]} -gt 0 ]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        log_error "Please install missing tools before running this script"
        echo ""
        echo "Installation guides:"
        echo "  tccli:  https://cloud.tencent.com/document/product/440/6176"
        echo "  jq:     https://jqlang.org/download/"
        echo "  kubectl: https://kubernetes.io/docs/tasks/tools/"
        echo "  helm:   https://helm.sh/docs/intro/install/"
        exit 1
    fi

    # Check tccli configuration
    if ! tccli --version &> /dev/null; then
        log_error "tccli is not configured properly. Please run 'tccli configure' first"
        exit 1
    fi

    log_success "tccli configuration check passed"

    # Check required configuration variables
    if [ -z "$MANAGER_REGION" ]; then
        log_error "MANAGER_REGION is required. Please set it in config file"
        exit 1
    fi

    if [ -z "$MANAGER_CLUSTER_ID" ]; then
        log_error "MANAGER_CLUSTER_ID is required. Please set it in config file"
        exit 1
    fi

    if [ -z "$MANAGER_SUBNET_ID" ]; then
        log_error "MANAGER_SUBNET_ID is required. Please set it in config file"
        exit 1
    fi

    if [ -z "$WORKER_REGION" ]; then
        log_error "WORKER_REGION is required. Please set it in config file"
        exit 1
    fi

    if [ -z "$WORKER_CLUSTER_ID" ]; then
        log_error "WORKER_CLUSTER_ID is required. Please set it in config file"
        exit 1
    fi

    if [ -z "$WORKER_SUBNET_ID" ]; then
        log_error "WORKER_SUBNET_ID is required. Please set it in config file"
        exit 1
    fi

    log_success "All prerequisites check passed"
}

# Check required scripts exist
check_scripts() {
    log_info "Checking required scripts..."

    if [ ! -f "$INSTALL_TKE_SCRIPT" ]; then
        log_error "install-tke.sh not found: $INSTALL_TKE_SCRIPT"
        exit 1
    fi
    log_success "install-tke.sh found"

    if [ ! -f "$VLLM_INFER_SCRIPT" ]; then
        log_error "vllm-infer-demo.sh not found: $VLLM_INFER_SCRIPT"
        exit 1
    fi
    log_success "vllm-infer-demo.sh found"

    if [ ! -f "$VERL_TRAINING_SCRIPT" ]; then
        log_error "llm-kuberay-verl-demo.sh not found: $VERL_TRAINING_SCRIPT"
        exit 1
    fi
    log_success "llm-kuberay-verl-demo.sh found"

    log_success "All required scripts exist"
}

# Install Kubeocean using install-tke.sh
install_kubeocean() {
    log_info "Installing Kubeocean using install-tke.sh..."
    log_info "This will install both worker and manager clusters and create cluster binding"

    # Create a temporary config file for install-tke.sh in the installation-tke directory
    local tke_config_file="${SCRIPT_DIR}/../installation-tke/config.env"
    
    # Copy our config to the installation-tke directory if it doesn't exist
    if [ ! -f "$tke_config_file" ]; then
        log_info "Creating config file for install-tke.sh at: $tke_config_file"
        cp "$CONFIG_FILE" "$tke_config_file"
    else
        log_warning "Config file already exists at $tke_config_file, using existing one"
    fi

    if bash "$INSTALL_TKE_SCRIPT" -c "$tke_config_file"; then
        log_success "Kubeocean installation completed"
    else
        log_error "Kubeocean installation failed"
        exit 1
    fi
}

# Deploy vLLM inference service
deploy_vllm_inference() {
    log_info "Deploying vLLM inference service..."

    if bash "$VLLM_INFER_SCRIPT"; then
        log_success "vLLM inference service deployment completed"
    else
        log_error "vLLM inference service deployment failed"
        exit 1
    fi
}

# Patch ResourceLeasingPolicy timeWindows if SKIP_CURRENT_TIME_CHECKING is true
patch_rlp_time_windows() {
    if [ "$SKIP_CURRENT_TIME_CHECKING" != "true" ]; then
        log_info "Time checking is enabled, skipping RLP timeWindows patch"
        return 0
    fi
    
    log_info "Patching ResourceLeasingPolicy timeWindows (SKIP_CURRENT_TIME_CHECKING=true)..."
    
    # Switch to worker cluster context
    local worker_context="worker-admin-$WORKER_CLUSTER_ID"
    
    if ! kubectl config use-context "$worker_context" &> /dev/null; then
        log_error "Failed to switch to worker cluster context: $worker_context"
        log_error "Please ensure the worker cluster is properly configured"
        exit 1
    fi
    
    log_success "Switched to worker cluster context: $worker_context"
    
    # Check if the RLP exists
    if ! kubectl get resourceleasingpolicy example-policy &> /dev/null; then
        log_warning "ResourceLeasingPolicy 'example-policy' not found, skipping patch"
        return 0
    fi
    
    # Patch the RLP to set timeWindows to empty
    log_info "Removing timeWindows from example-policy..."
    
    if kubectl patch resourceleasingpolicy example-policy --type='json' \
        -p='[{"op": "replace", "path": "/spec/timeWindows", "value": []}]'; then
        log_success "ResourceLeasingPolicy 'example-policy' timeWindows cleared"
    else
        log_warning "Failed to patch ResourceLeasingPolicy, but continuing..."
    fi
}

# Deploy VERL training job
deploy_verl_training() {
    log_info "Deploying VERL training job on manager cluster..."

    if bash "$VERL_TRAINING_SCRIPT"; then
        log_success "VERL training job deployment completed"
        
        # Try to extract Ray job ID
        extract_ray_job_id
    else
        log_error "VERL training job deployment failed"
        exit 1
    fi
}

# Extract Ray job ID from manager cluster
extract_ray_job_id() {
    log_info "Attempting to extract Ray job ID..."
    
    # Switch to manager cluster context
    local manager_context="manager-admin-$MANAGER_CLUSTER_ID"
    
    if ! kubectl config use-context "$manager_context" &> /dev/null; then
        log_warning "Failed to switch to manager cluster context, skipping job ID extraction"
        return 0
    fi
    
    # Find the Ray head pod
    local head_pod
    head_pod=$(kubectl get pods -n "$MANAGER_NAMESPACE" \
        -l ray.io/cluster="$RAYCLUSTER_NAME",ray.io/node-type=head \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    
    if [ -z "$head_pod" ]; then
        log_warning "Ray head pod not found, skipping job ID extraction"
        return 0
    fi
    
    # Get the latest job ID using ray job list
    local job_list_output
    job_list_output=$(kubectl exec -n "$MANAGER_NAMESPACE" "$head_pod" -- bash -c "ray job list 2>&1" 2>&1) || true
    
    if [ -z "$job_list_output" ]; then
        log_warning "Failed to get Ray job list"
        return 0
    fi
    
    # Extract the most recent job ID (first non-header line with raysubmit_)
    local job_id
    job_id=$(echo "$job_list_output" | grep -oP "raysubmit_[a-zA-Z0-9]+" | head -1)
    
    if [ -n "$job_id" ]; then
        RAY_JOB_ID="$job_id"
        log_success "Extracted Ray Job ID: $RAY_JOB_ID"
    else
        log_warning "Could not extract Ray job ID from job list output"
    fi
}

# Display final summary
show_final_summary() {
    local manager_context="manager-admin-$MANAGER_CLUSTER_ID"
    local worker_context="worker-admin-$WORKER_CLUSTER_ID"

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "LLM Training and Inference Demo Deployment Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Deployment Summary:"
    echo "   • Configuration: $CONFIG_FILE"
    echo "   • Kubeocean installed ✓"
    echo "   • vLLM inference service deployed ✓"
    echo "   • VERL training job submitted ✓"
    if [ -n "$RAY_JOB_ID" ]; then
        echo "   • Ray Job ID: $RAY_JOB_ID"
    fi
    echo ""
    echo "📊 Cluster Information:"
    echo "   Manager Cluster:"
    echo "     • Region: $MANAGER_REGION"
    echo "     • Cluster ID: $MANAGER_CLUSTER_ID"
    echo "     • Context: $manager_context"
    echo ""
    echo "   Worker Cluster:"
    echo "     • Region: $WORKER_REGION"
    echo "     • Cluster ID: $WORKER_CLUSTER_ID"
    echo "     • Context: $worker_context"
    echo ""
    echo "🔍 Quick Verification Commands:"
    echo ""
    echo "   # Check Kubeocean components"
    echo "   kubectl config use-context $manager_context"
    echo "   kubectl get all -n kubeocean-system"
    echo "   kubectl get clusterbindings"
    echo ""
    echo "   # Check vLLM inference service on worker cluster"
    echo "   kubectl config use-context $worker_context"
    echo "   kubectl get deployment is-qwen2-5-05b-vllm -n default"
    echo "   kubectl get pods -l app.kubernetes.io/instance=is-qwen2-5-05b-vllm -n default"
    echo "   kubectl get service is-qwen2-5-05b-vllm -n default"
    echo ""
    echo "   # Check VERL training on manager cluster"
    echo "   kubectl config use-context $manager_context"
    echo "   kubectl get raycluster verl-cluster -n default"
    echo "   kubectl get pods -l ray.io/cluster=verl-cluster -n default"
    echo ""
    echo "   # Monitor training job"
    echo "   HEAD_POD=\$(kubectl get pods -n default -l ray.io/cluster=verl-cluster,ray.io/node-type=head -o jsonpath='{.items[0].metadata.name}')"
    
    if [ -n "$RAY_JOB_ID" ]; then
        echo "   kubectl exec -n default \$HEAD_POD -- ray job status $RAY_JOB_ID"
        echo "   kubectl exec -n default \$HEAD_POD -- ray job logs $RAY_JOB_ID --follow"
    else
        echo "   kubectl exec -n default \$HEAD_POD -- ray job list"
    fi
    
    echo "   kubectl logs -n default \$HEAD_POD -f"
    echo ""
    echo "🎉 Demo environment is ready to use!"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🚀 Kubeocean LLM Training and Inference Demo"
    echo "   🚀 One-Click Deployment Script"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    
    # Parse arguments
    parse_args "$@"
    
    # Check config file
    check_config_file
    echo ""
    
    # Load configuration
    load_config
    echo ""
    
    # Step 1: Check prerequisites
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "📋 Step 1/5: Checking Prerequisites"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    check_prerequisites
    check_scripts
    echo ""
    
    # Step 2: Install Kubeocean
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "📦 Step 2/5: Installing Kubeocean"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    install_kubeocean
    echo ""
    
    # Step 3: Deploy vLLM inference service
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🤖 Step 3/5: Deploying vLLM Inference Service"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    deploy_vllm_inference
    echo ""
    
    # Step 4: Deploy VERL training job
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🎓 Step 4/5: Deploying VERL Training Job"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    patch_rlp_time_windows
    deploy_verl_training
    echo ""
    
    # Step 5: Display final summary
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "📊 Step 5/5: Summary"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    show_final_summary
}

# Execute main function
main "$@"

