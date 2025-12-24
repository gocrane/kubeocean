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

# Kubeocean LLM Training and Inference Demo - Cleanup Script
# Features:
# 1. Pre-check required tools and contexts
# 2. Delete VERL training resources from manager cluster
# 3. Optionally uninstall KubeRay operator
# 4. Delete vLLM inference service from worker cluster
# 5. Optionally uninstall HPC controller
# 6. Call uninstall-tke.sh to cleanup Kubeocean

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

# Uninstallation script path
UNINSTALL_TKE_SCRIPT="${SCRIPT_DIR}/../installation-tke/uninstall-tke.sh"

# Resource YAML files
RAYCLUSTER_YAML="${SCRIPT_DIR}/verl-raycluster.yaml"
VLLM_YAML="${SCRIPT_DIR}/is-qwen2-5-05b-vllm.yaml"

# Configuration variables (will be loaded from config.env)
MANAGER_REGION=""
MANAGER_CLUSTER_ID=""
WORKER_REGION=""
WORKER_CLUSTER_ID=""
WORKER_CLUSTER_NAME="example-cluster"
MANAGER_NAMESPACE="default"
WORKER_NAMESPACE="default"

# Cleanup control flags (default: true = skip uninstall)
SKIP_UNINSTALL_HPC="true"
SKIP_UNINSTALL_KUBERAY="true"

# Display help information
show_help() {
    cat << EOF
Kubeocean LLM Training and Inference Demo - Cleanup Script

This script automates the complete cleanup process by sequentially executing:
1. Pre-check required tools and kubectl contexts
2. Delete VERL training resources (RayCluster) from manager cluster
3. Optionally uninstall KubeRay operator (if SKIP_UNINSTALL_KUBERAY=false)
4. Delete vLLM inference service from worker cluster
5. Optionally uninstall HPC controller addon (if SKIP_UNINSTALL_HPC=false)
6. Call uninstall-tke.sh to cleanup Kubeocean components

Usage: $0 [options]

Options:
  -h, --help                   Display this help message
  -c, --config FILE            Configuration file path (default: ./config.env)

Required Configuration Variables (in config file):
  MANAGER_REGION               Manager cluster region
  MANAGER_CLUSTER_ID           Manager cluster ID
  WORKER_REGION                Worker cluster region
  WORKER_CLUSTER_ID            Worker cluster ID

Optional Configuration Variables:
  WORKER_CLUSTER_NAME          Worker cluster name (default: example-cluster)
  MANAGER_NAMESPACE            Manager namespace (default: default)
  WORKER_NAMESPACE             Worker namespace (default: default)
  SKIP_UNINSTALL_HPC           Skip HPC controller uninstall (default: true)
  SKIP_UNINSTALL_KUBERAY       Skip KubeRay operator uninstall (default: true)

Prerequisites:
  tccli                        Tencent Cloud CLI tool (must be installed and configured)
  jq                           Command-line JSON processor
  kubectl                      Kubernetes command-line tool
  helm                         Helm package manager (version 3.x required)

Examples:
  # Use default configuration file (./config.env)
  $0

  # Use custom configuration file
  $0 -c /path/to/my-config.env

  # Force uninstall HPC and KubeRay
  SKIP_UNINSTALL_HPC=false SKIP_UNINSTALL_KUBERAY=false $0

Note:
  - By default, HPC controller and KubeRay operator will NOT be uninstalled
  - Set SKIP_UNINSTALL_HPC=false to also uninstall HPC controller
  - Set SKIP_UNINSTALL_KUBERAY=false to also uninstall KubeRay operator

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
    log_info "Skip Uninstall HPC: ${SKIP_UNINSTALL_HPC}"
    log_info "Skip Uninstall KubeRay: ${SKIP_UNINSTALL_KUBERAY}"

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

    # Check helm (only if KubeRay uninstall is not skipped)
    if [ "$SKIP_UNINSTALL_KUBERAY" = "false" ]; then
        if ! command -v helm &> /dev/null; then
            missing_tools+=("helm")
        else
            log_success "helm is installed"
        fi
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

    if [ -z "$WORKER_REGION" ]; then
        log_error "WORKER_REGION is required. Please set it in config file"
        exit 1
    fi

    if [ -z "$WORKER_CLUSTER_ID" ]; then
        log_error "WORKER_CLUSTER_ID is required. Please set it in config file"
        exit 1
    fi

    log_success "All prerequisites check passed"
}

# Check kubectl contexts
check_kubectl_contexts() {
    log_info "Checking kubectl contexts..."

    local manager_context="manager-admin-$MANAGER_CLUSTER_ID"
    local worker_context="worker-admin-$WORKER_CLUSTER_ID"

    # Check manager context
    if ! kubectl config get-contexts "$manager_context" &> /dev/null; then
        log_error "Manager cluster context not found: $manager_context"
        log_error "Please ensure manager cluster is configured in kubectl"
        log_error "You can run install-tke.sh first or manually configure kubectl"
        exit 1
    fi
    log_success "Manager context found: $manager_context"

    # Check worker context
    if ! kubectl config get-contexts "$worker_context" &> /dev/null; then
        log_error "Worker cluster context not found: $worker_context"
        log_error "Please ensure worker cluster is configured in kubectl"
        log_error "You can run install-tke.sh first or manually configure kubectl"
        exit 1
    fi
    log_success "Worker context found: $worker_context"

    log_success "All required kubectl contexts are available"
}

# Delete VERL training resources from manager cluster
delete_verl_training() {
    log_info "Deleting VERL training resources from manager cluster..."

    local manager_context="manager-admin-$MANAGER_CLUSTER_ID"

    # Switch to manager cluster context
    if ! kubectl config use-context "$manager_context" &> /dev/null; then
        log_error "Failed to switch to manager cluster context: $manager_context"
        exit 1
    fi
    log_success "Switched to manager cluster context: $manager_context"

    # Check if RayCluster YAML exists
    if [ ! -f "$RAYCLUSTER_YAML" ]; then
        log_warning "RayCluster YAML not found: $RAYCLUSTER_YAML"
        log_warning "Skipping RayCluster deletion"
    else
        # Delete RayCluster
        log_info "Deleting RayCluster resources..."
        if kubectl delete -f "$RAYCLUSTER_YAML" -n "$MANAGER_NAMESPACE" --ignore-not-found=true 2>&1; then
            log_success "RayCluster delete command executed"
        else
            log_warning "Failed to delete RayCluster, but continuing..."
        fi
    fi

    # Wait for pods to be deleted (40 seconds polling)
    log_info "Waiting for Ray pods to be deleted (up to 40 seconds)..."
    local max_wait=40
    local wait_time=0
    local interval=2

    while [ $wait_time -lt $max_wait ]; do
        local pod_count
        pod_count=$(kubectl get pods -n "$MANAGER_NAMESPACE" -l ray.io/cluster=verl-cluster --no-headers 2>/dev/null | wc -l || echo "0")

        if [ "$pod_count" -eq 0 ]; then
            log_success "All Ray pods have been deleted"
            return 0
        fi

        if [ $((wait_time % 10)) -eq 0 ]; then
            log_info "Still waiting... ($pod_count pods remaining, ${wait_time}s elapsed)"
        fi

        sleep $interval
        wait_time=$((wait_time + interval))
    done

    # Check final status
    local remaining_pods
    remaining_pods=$(kubectl get pods -n "$MANAGER_NAMESPACE" -l ray.io/cluster=verl-cluster --no-headers 2>/dev/null | wc -l || echo "0")
    
    if [ "$remaining_pods" -eq 0 ]; then
        log_success "All Ray pods have been deleted"
    else
        log_warning "Timeout: $remaining_pods Ray pods still exist after ${max_wait}s"
        log_warning "You may need to manually check and clean up remaining pods"
    fi
}

# Uninstall KubeRay operator
uninstall_kuberay() {
    if [ "$SKIP_UNINSTALL_KUBERAY" = "true" ]; then
        log_info "Skipping KubeRay operator uninstall (SKIP_UNINSTALL_KUBERAY=true)"
        return 0
    fi

    log_info "Uninstalling KubeRay operator..."

    local manager_context="manager-admin-$MANAGER_CLUSTER_ID"

    # Switch to manager cluster context
    kubectl config use-context "$manager_context" &> /dev/null || true

    # Check if KubeRay operator is installed
    if ! helm list -n "$MANAGER_NAMESPACE" 2>/dev/null | grep -q kuberay-operator; then
        log_warning "KubeRay operator not found, skipping uninstall"
        return 0
    fi

    # Uninstall KubeRay operator
    log_info "Uninstalling kuberay-operator helm release..."
    if helm uninstall kuberay-operator -n "$MANAGER_NAMESPACE" 2>&1; then
        log_success "KubeRay operator uninstalled successfully"
    else
        log_warning "Failed to uninstall KubeRay operator, but continuing..."
    fi

    # Wait a moment for cleanup
    sleep 3
}

# Delete vLLM inference service from worker cluster
delete_vllm_service() {
    log_info "Deleting vLLM inference service from worker cluster..."

    local worker_context="worker-admin-$WORKER_CLUSTER_ID"

    # Switch to worker cluster context
    if ! kubectl config use-context "$worker_context" &> /dev/null; then
        log_error "Failed to switch to worker cluster context: $worker_context"
        exit 1
    fi
    log_success "Switched to worker cluster context: $worker_context"

    # Check if vLLM YAML exists
    if [ ! -f "$VLLM_YAML" ]; then
        log_warning "vLLM YAML not found: $VLLM_YAML"
        log_warning "Skipping vLLM service deletion"
    else
        # Delete vLLM resources
        log_info "Deleting vLLM inference service resources..."
        if kubectl delete -f "$VLLM_YAML" -n "$WORKER_NAMESPACE" --ignore-not-found=true 2>&1; then
            log_success "vLLM service delete command executed"
        else
            log_warning "Failed to delete vLLM service, but continuing..."
        fi
    fi

    # Wait for pods to be deleted (40 seconds polling)
    log_info "Waiting for vLLM pods to be deleted (up to 40 seconds)..."
    local max_wait=40
    local wait_time=0
    local interval=2

    while [ $wait_time -lt $max_wait ]; do
        local pod_count
        pod_count=$(kubectl get pods -n "$WORKER_NAMESPACE" -l app.kubernetes.io/instance=is-qwen2-5-05b-vllm --no-headers 2>/dev/null | wc -l || echo "0")

        if [ "$pod_count" -eq 0 ]; then
            log_success "All vLLM pods have been deleted"
            return 0
        fi

        if [ $((wait_time % 10)) -eq 0 ]; then
            log_info "Still waiting... ($pod_count pods remaining, ${wait_time}s elapsed)"
        fi

        sleep $interval
        wait_time=$((wait_time + interval))
    done

    # Check final status
    local remaining_pods
    remaining_pods=$(kubectl get pods -n "$WORKER_NAMESPACE" -l app.kubernetes.io/instance=is-qwen2-5-05b-vllm --no-headers 2>/dev/null | wc -l || echo "0")
    
    if [ "$remaining_pods" -eq 0 ]; then
        log_success "All vLLM pods have been deleted"
    else
        log_warning "Timeout: $remaining_pods vLLM pods still exist after ${max_wait}s"
        log_warning "You may need to manually check and clean up remaining pods"
    fi
}

# Uninstall HPC controller addon
uninstall_hpc_controller() {
    if [ "$SKIP_UNINSTALL_HPC" = "true" ]; then
        log_info "Skipping HPC controller uninstall (SKIP_UNINSTALL_HPC=true)"
        return 0
    fi

    log_info "Uninstalling HPC controller addon..."

    local addon_name="tke-hpc-controller"
    
    # Check if addon exists
    local addon_info
    ret=0
    addon_info=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DescribeAddon \
        --ClusterId "$WORKER_CLUSTER_ID" \
        --AddonName "$addon_name" 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_warning "Failed to query HPC controller addon status, it may not exist"
        log_info "Skipping HPC controller uninstall"
        return 0
    fi

    # Check addon phase
    local phase
    phase=$(echo "$addon_info" | jq -r '.Addons[0].Phase // empty')
    
    if [ -z "$phase" ] || [ "$phase" = "null" ]; then
        log_warning "HPC controller addon not found, skipping uninstall"
        return 0
    fi

    log_info "Current HPC controller addon phase: $phase"

    # Delete addon
    log_info "Deleting HPC controller addon..."
    ret=0
    local delete_result
    delete_result=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DeleteAddon \
        --ClusterId "$WORKER_CLUSTER_ID" \
        --AddonName "$addon_name" 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_warning "Failed to delete HPC controller addon"
        log_warning "$delete_result"
        log_info "Continuing with cleanup..."
        return 0
    fi

    log_success "HPC controller addon deletion initiated"

    # Wait for addon to be deleted (up to 60 seconds)
    log_info "Waiting for HPC controller addon to be deleted..."
    local max_retries=30
    local retry=0

    while [ $retry -lt $max_retries ]; do
        sleep 2

        ret=0
        addon_info=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DescribeAddon \
            --ClusterId "$WORKER_CLUSTER_ID" \
            --AddonName "$addon_name" 2>&1) || ret=$?

        if [ $ret -ne 0 ]; then
            log_success "HPC controller addon deleted successfully"
            return 0
        fi

        phase=$(echo "$addon_info" | jq -r '.Addons[0].Phase // empty')
        
        if [ -z "$phase" ] || [ "$phase" = "null" ]; then
            log_success "HPC controller addon deleted successfully"
            return 0
        fi

        if [ $((retry % 10)) -eq 0 ]; then
            log_info "Still deleting... (phase: $phase, attempt $((retry + 1))/$max_retries)"
        fi

        retry=$((retry + 1))
    done

    log_warning "Timeout waiting for HPC controller addon deletion"
    log_warning "Addon may still be in deletion process, but continuing..."
}

# Uninstall Kubeocean components
uninstall_kubeocean() {
    log_info "Uninstalling Kubeocean components..."

    # Check if uninstall script exists
    if [ ! -f "$UNINSTALL_TKE_SCRIPT" ]; then
        log_error "Uninstall script not found: $UNINSTALL_TKE_SCRIPT"
        exit 1
    fi

    # Call uninstall-tke.sh with current config file
    log_info "Calling uninstall-tke.sh with config: $CONFIG_FILE"
    
    if bash "$UNINSTALL_TKE_SCRIPT" -c "$CONFIG_FILE"; then
        log_success "Kubeocean uninstallation completed"
    else
        log_error "Kubeocean uninstallation failed"
        exit 1
    fi
}

# Display final summary
show_final_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "LLM Training and Inference Demo Cleanup Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Cleanup Summary:"
    echo "   • Configuration: $CONFIG_FILE"
    echo "   • VERL training resources deleted ✓"
    if [ "$SKIP_UNINSTALL_KUBERAY" = "false" ]; then
        echo "   • KubeRay operator uninstalled ✓"
    else
        echo "   • KubeRay operator retained (SKIP_UNINSTALL_KUBERAY=true)"
    fi
    echo "   • vLLM inference service deleted ✓"
    if [ "$SKIP_UNINSTALL_HPC" = "false" ]; then
        echo "   • HPC controller uninstalled ✓"
    else
        echo "   • HPC controller retained (SKIP_UNINSTALL_HPC=true)"
    fi
    echo "   • Kubeocean components uninstalled ✓"
    echo ""
    echo "🔍 Verification Commands:"
    echo ""
    echo "   # Check if contexts are cleaned up"
    echo "   kubectl config get-contexts"
    echo ""
    echo "   # Verify manager cluster (if context still exists)"
    echo "   kubectl config use-context manager-admin-$MANAGER_CLUSTER_ID"
    echo "   kubectl get all -n kubeocean-system"
    echo "   kubectl get raycluster -n $MANAGER_NAMESPACE"
    echo ""
    echo "   # Verify worker cluster (if context still exists)"
    echo "   kubectl config use-context worker-admin-$WORKER_CLUSTER_ID"
    echo "   kubectl get all -n kubeocean-worker"
    echo "   kubectl get deployment is-qwen2-5-05b-vllm -n $WORKER_NAMESPACE"
    echo ""
    echo "🎉 Cleanup completed successfully!"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🧹 Kubeocean LLM Training and Inference Demo"
    echo "   🧹 Cleanup Script"
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
    echo "📋 Step 1/7: Checking Prerequisites"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    check_prerequisites
    echo ""
    
    # Step 2: Check kubectl contexts
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🔍 Step 2/7: Checking Kubectl Contexts"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    check_kubectl_contexts
    echo ""
    
    # Step 3: Delete VERL training resources
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🎓 Step 3/7: Deleting VERL Training Resources"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    delete_verl_training
    echo ""
    
    # Step 4: Uninstall KubeRay operator
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🔌 Step 4/7: KubeRay Operator Cleanup"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    uninstall_kuberay
    echo ""
    
    # Step 5: Delete vLLM inference service
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🤖 Step 5/7: Deleting vLLM Inference Service"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    delete_vllm_service
    echo ""
    
    # Step 6: Uninstall HPC controller
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🔧 Step 6/7: HPC Controller Cleanup"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    uninstall_hpc_controller
    echo ""
    
    # Step 7: Uninstall Kubeocean
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "📦 Step 7/7: Uninstalling Kubeocean"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    uninstall_kubeocean
    echo ""
    
    # Display final summary
    show_final_summary
}

# Execute main function
main "$@"

