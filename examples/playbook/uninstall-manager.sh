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

# Kubeocean Manager Cluster Uninstallation Script
# Features:
# 1. Delete ClusterBinding resources
# 2. Delete worker kubeconfig secrets
# 3. Uninstall kubeocean-manager components

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

# Script root directory (assuming script is in examples/playbook directory)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Configuration variables
LOCALBIN="${PROJECT_ROOT}/bin"
HELM="${LOCALBIN}/helm"
MANAGER_NAMESPACE="kubeocean-system"

# Default values
DEFAULT_CLUSTER_NAME="example-cluster"

# Configurable parameters
CLUSTER_NAMES=()
SKIP_MANAGER_UNINSTALL="${SKIP_MANAGER_UNINSTALL:-false}"
DELETE_TIMEOUT=60  # 1 minute timeout for deletion

# Display help information
show_help() {
    cat << EOF
Kubeocean Manager Cluster Uninstallation Script

Usage: $0 [options]

Options:
  -h, --help                    Display this help message
  -n, --cluster-name NAME       Specify cluster name to delete (default: example-cluster, can be used multiple times)
  --skip-manager                Skip kubeocean-manager uninstallation (only delete bindings and secrets)

Environment Variables:
  SKIP_MANAGER_UNINSTALL        Skip manager uninstallation (true/false)

Prerequisites:
  kubectl                       Kubernetes command-line tool
  helm v3.x                     Helm package manager (version 3.x required)

Examples:
  # Uninstall with default cluster name
  $0

  # Uninstall specific cluster
  $0 --cluster-name worker1

  # Uninstall multiple clusters
  $0 --cluster-name worker1 --cluster-name worker2

  # Only delete bindings and secrets (skip manager uninstallation)
  $0 --skip-manager

  # Delete specific cluster without uninstalling manager
  $0 --skip-manager --cluster-name worker1

Notes:
  - Cluster name defaults to 'example-cluster' if not specified
  - ClusterBinding deletion timeout is 60 seconds
  - Script will verify deletion success before proceeding
  - Manager uninstallation is performed last

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
            -n|--cluster-name)
                CLUSTER_NAMES+=("$2")
                shift 2
                ;;
            --skip-manager)
                SKIP_MANAGER_UNINSTALL=true
                shift
                ;;
            *)
                log_error "Unknown argument: $1"
                show_help
                exit 1
                ;;
        esac
    done
    
    # If no cluster names specified, use default
    if [ ${#CLUSTER_NAMES[@]} -eq 0 ]; then
        CLUSTER_NAMES+=("$DEFAULT_CLUSTER_NAME")
    fi
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    # Check kubectl
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first"
        exit 1
    fi
    
    # Check kubectl connection
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Cannot connect to Kubernetes cluster. Please check kubeconfig configuration"
        exit 1
    fi
    
    # Extract cluster name from kubeconfig
    CURRENT_CONTEXT=$(kubectl config current-context)
    MANAGER_CLUSTER_NAME=$(kubectl config get-contexts "$CURRENT_CONTEXT" | awk '{print $3}' | tail -n 1)
    
    if [ -z "$MANAGER_CLUSTER_NAME" ]; then
        log_error "Failed to extract cluster name from kubeconfig"
        exit 1
    fi
    
    log_info "Detected manager cluster name: $MANAGER_CLUSTER_NAME"
    
    # Check Helm installation (only if not skipping manager uninstall)
    if [ "$SKIP_MANAGER_UNINSTALL" != "true" ]; then
        log_info "Checking Helm installation..."
        HELM_CMD=""
        
        # First check system-wide Helm
        if command -v helm &> /dev/null; then
            HELM_CMD="helm"
            log_info "Found system Helm: $(which helm)"
        # Then check local bin
        elif [ -x "$HELM" ]; then
            HELM_CMD="$HELM"
            log_info "Found local Helm: $HELM"
        else
            log_error "Helm is not installed. Please install Helm first"
            log_error "You can install Helm by running: make helm"
            log_error "Or visit: https://helm.sh/docs/intro/install/"
            exit 1
        fi
        
        # Check Helm version (must be v3)
        HELM_VERSION_OUTPUT=$($HELM_CMD version --short 2>/dev/null || echo "")
        if [ -z "$HELM_VERSION_OUTPUT" ]; then
            log_error "Failed to get Helm version"
            exit 1
        fi
        
        # Extract major version number
        if echo "$HELM_VERSION_OUTPUT" | grep -q "^v3\."; then
            log_success "Helm version check passed: $HELM_VERSION_OUTPUT"
        else
            log_error "Helm version must be v3.x, but found: $HELM_VERSION_OUTPUT"
            log_error "Please upgrade Helm to version 3.x"
            log_error "Visit: https://helm.sh/docs/intro/install/"
            exit 1
        fi
        
        # Update HELM variable to use the found Helm command
        HELM="$HELM_CMD"
    fi
    
    log_success "Prerequisites check passed"
}

# Delete ClusterBinding resources
delete_cluster_bindings() {
    log_info "Deleting ClusterBinding resources..."
    
    local deleted_count=0
    local failed_count=0
    
    for cluster_name in "${CLUSTER_NAMES[@]}"; do
        local binding_name="${cluster_name}"
        
        # Check if ClusterBinding exists
        if ! kubectl get clusterbinding "$binding_name" &> /dev/null; then
            log_warning "ClusterBinding '$binding_name' not found, skipping"
            continue
        fi
        
        log_info "Deleting ClusterBinding '$binding_name' (timeout: ${DELETE_TIMEOUT}s)..."
        
        # Delete ClusterBinding with timeout
        if kubectl delete clusterbinding "$binding_name" --timeout="${DELETE_TIMEOUT}s" 2>&1; then
            # Verify deletion success
            if ! kubectl get clusterbinding "$binding_name" &> /dev/null; then
                log_success "  ✓ ClusterBinding '$binding_name' deleted successfully"
                deleted_count=$((deleted_count + 1))
            else
                log_error "ClusterBinding '$binding_name' still exists after deletion"
                log_error "ClusterBinding may have finalizers or other blocking conditions"
                exit 1
            fi
        else
            log_error "Failed to delete ClusterBinding '$binding_name'"
            log_error "Deletion may have timed out or encountered an error"
            exit 1
        fi
    done
    
    if [ $deleted_count -gt 0 ]; then
        log_success "Successfully deleted $deleted_count ClusterBinding(s)"
    else
        log_warning "No ClusterBindings were deleted"
    fi
}

# Delete worker secrets
delete_worker_secrets() {
    log_info "Deleting worker kubeconfig secrets..."
    
    local deleted_count=0
    local failed_count=0
    
    for cluster_name in "${CLUSTER_NAMES[@]}"; do
        local secret_name="${cluster_name}-kubeconfig"
        
        # Check if secret exists
        if ! kubectl get secret "$secret_name" -n "$MANAGER_NAMESPACE" &> /dev/null; then
            log_warning "Secret '$secret_name' not found, skipping"
            continue
        fi
        
        log_info "Deleting secret '$secret_name'..."
        
        # Delete secret
        if kubectl delete secret "$secret_name" -n "$MANAGER_NAMESPACE" &> /dev/null; then
            log_success "  ✓ Secret '$secret_name' deleted successfully"
            deleted_count=$((deleted_count + 1))
        else
            log_error "Failed to delete secret '$secret_name'"
            failed_count=$((failed_count + 1))
        fi
    done
    
    if [ $failed_count -gt 0 ]; then
        log_error "Failed to delete $failed_count secret(s)"
        exit 1
    fi
    
    if [ $deleted_count -gt 0 ]; then
        log_success "Successfully deleted $deleted_count secret(s)"
    else
        log_warning "No secrets were deleted"
    fi
}

# Uninstall kubeocean-manager
uninstall_kubeocean_manager() {
    if [ "$SKIP_MANAGER_UNINSTALL" = "true" ]; then
        log_info "Skipping kubeocean-manager uninstallation (SKIP_MANAGER_UNINSTALL=true)"
        return 0
    fi
    
    log_info "Uninstalling kubeocean-manager..."
    
    # Check if any ClusterBindings exist
    log_info "Checking for existing ClusterBindings..."
    local existing_bindings=$(kubectl get clusterbindings --no-headers 2>/dev/null | wc -l)
    
    if [ "$existing_bindings" -gt 0 ]; then
        log_error "Cannot uninstall kubeocean-manager: $existing_bindings ClusterBinding(s) still exist"
        log_error "Please delete all ClusterBindings before uninstalling manager"
        log_error ""
        log_error "Existing ClusterBindings:"
        kubectl get clusterbindings --no-headers 2>/dev/null | awk '{print "  • " $1}' || true
        log_error ""
        log_error "To delete ClusterBindings, run:"
        log_error "  $0 --skip-manager --cluster-name <cluster-name>"
        log_error "Or manually delete them:"
        log_error "  kubectl delete clusterbinding <binding-name>"
        exit 1
    fi
    
    log_success "No ClusterBindings found, safe to proceed"
    
    # Check if release exists
    if ! $HELM list -a 2>/dev/null | grep -q "^kubeocean"; then
        log_warning "Helm release 'kubeocean' not found"
        return 0
    fi
    
    # Uninstall kubeocean with retry logic
    log_info "Uninstalling Helm release 'kubeocean'..."
    local max_retry_time=60  # Maximum retry time in seconds
    local retry_interval=5   # Retry interval in seconds
    local elapsed=0
    local uninstall_success=false

    while [ $elapsed -lt $max_retry_time ]; do
        # Attempt to uninstall
        local error_output
        local exit_code=0
        error_output=$($HELM uninstall kubeocean --timeout 300s 2>&1 || exit_code=$?)

        if [ $exit_code -ne 0 ]; then
            # Display error output
            log_warning "Helm uninstall failed (attempt $((elapsed / retry_interval + 1))):"
            echo "$error_output" | sed 's/^/  /' | head -10
        fi

        # Check if release still exists
        if ! $HELM list -a 2>/dev/null | grep -q "^kubeocean"; then
            log_success "Helm release 'kubeocean' no longer exists (uninstall succeeded)"
            uninstall_success=true
            break
        fi

        # Wait before retry
        log_info "Retrying in ${retry_interval} seconds..."
        sleep $retry_interval
        elapsed=$((elapsed + retry_interval))
    done

    # Check final status
    if [ "$uninstall_success" = false ]; then
        log_error "Failed to uninstall kubeocean-manager after ${max_retry_time} seconds"
        log_error "Please check the release status with: helm list -a"
        log_error "Last error output:"
        echo "$error_output" | sed 's/^/  /'
        exit 1
    fi
    
    # Display remaining resources
    log_info "Checking remaining resources..."
    local remaining_pods=$(kubectl get pods -n "$MANAGER_NAMESPACE" --no-headers 2>/dev/null | wc -l)
    if [ "$remaining_pods" -gt 0 ]; then
        log_warning "Some pods still exist in namespace $MANAGER_NAMESPACE:"
        kubectl get pods -n "$MANAGER_NAMESPACE" 2>/dev/null | sed 's/^/  /'
    else
        log_success "All pods removed from namespace $MANAGER_NAMESPACE"
    fi
}

# Display summary information
show_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "Kubeocean Manager Cluster Uninstallation Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Uninstallation Information:"
    echo "   • Manager Cluster: $MANAGER_CLUSTER_NAME"
    echo "   • Manager Namespace: $MANAGER_NAMESPACE"
    echo "   • Clusters Removed: ${#CLUSTER_NAMES[@]}"
    echo ""
    echo "🗑️  Removed Resources:"
    for cluster_name in "${CLUSTER_NAMES[@]}"; do
        echo "   • ClusterBinding: ${cluster_name}"
        echo "   • Secret: ${cluster_name}-kubeconfig"
    done
    if [ "$SKIP_MANAGER_UNINSTALL" != "true" ]; then
        echo "   • Helm Release: kubeocean"
    fi
    echo ""
    echo "🔍 Verification Commands:"
    echo "   • Check remaining ClusterBindings:"
    echo "     kubectl get clusterbindings"
    echo ""
    echo "   • Check remaining secrets:"
    echo "     kubectl get secrets -n $MANAGER_NAMESPACE | grep kubeconfig"
    echo ""
    if [ "$SKIP_MANAGER_UNINSTALL" != "true" ]; then
        echo "   • Check Helm releases:"
        echo "     helm list -a"
        echo ""
        echo "   • Check remaining pods:"
        echo "     kubectl get pods -n $MANAGER_NAMESPACE"
        echo ""
    fi
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🗑️  Kubeocean Manager Cluster Uninstallation Script"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    
    # Parse arguments
    parse_args "$@"
    
    # Determine number of steps
    local total_steps=4
    if [ "$SKIP_MANAGER_UNINSTALL" = "true" ]; then
        total_steps=3
    fi
    
    # Step 1: Check prerequisites
    echo "📋 Step 1/$total_steps: Checking Prerequisites"
    check_prerequisites
    echo ""
    
    # Step 2: Delete ClusterBindings
    echo "🔗 Step 2/$total_steps: Deleting ClusterBinding Resources"
    delete_cluster_bindings
    echo ""
    
    # Step 3: Delete worker secrets
    echo "🔑 Step 3/$total_steps: Deleting Worker Kubeconfig Secrets"
    delete_worker_secrets
    echo ""
    
    # Step 4: Uninstall kubeocean-manager
    if [ "$SKIP_MANAGER_UNINSTALL" != "true" ]; then
        echo "📦 Step 4/$total_steps: Uninstalling kubeocean-manager"
        uninstall_kubeocean_manager
        echo ""
    fi
    
    # Display summary
    show_summary
}

# Execute main function
main "$@"
