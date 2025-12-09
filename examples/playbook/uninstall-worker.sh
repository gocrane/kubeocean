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

# Kubeocean Worker Cluster Uninstallation Script
# Features:
# 1. Delete ResourceLeasingPolicy resources
# 2. Uninstall kubeocean-worker components

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
WORKER_NAMESPACE="kubeocean-worker"

# Default values
DEFAULT_RLP_NAME="example-policy"

# Configurable parameters
RLP_NAMES=()

# Display help information
show_help() {
    cat << EOF
Kubeocean Worker Cluster Uninstallation Script

Usage: $0 [options]

Options:
  -h, --help                    Display this help message
  -r, --rlp-name NAME           Specify ResourceLeasingPolicy name to delete (default: example-policy, can be used multiple times)

Prerequisites:
  kubectl                       Kubernetes command-line tool
  helm v3.x                     Helm package manager (version 3.x required)

Examples:
  # Uninstall with default RLP name
  $0

  # Uninstall specific RLP
  $0 --rlp-name my-policy

  # Uninstall multiple RLPs
  $0 --rlp-name policy1 --rlp-name policy2

Notes:
  - RLP name defaults to 'example-policy' if not specified
  - ResourceLeasingPolicy will be deleted with --wait=false
  - Finalizers will be removed to ensure deletion
  - Worker components will be uninstalled last

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
            -r|--rlp-name)
                RLP_NAMES+=("$2")
                shift 2
                ;;
            *)
                log_error "Unknown argument: $1"
                show_help
                exit 1
                ;;
        esac
    done
    
    # If no RLP names specified, use default
    if [ ${#RLP_NAMES[@]} -eq 0 ]; then
        RLP_NAMES+=("$DEFAULT_RLP_NAME")
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
    CLUSTER_NAME=$(kubectl config get-contexts "$CURRENT_CONTEXT" | awk '{print $3}' | tail -n 1)
    
    if [ -z "$CLUSTER_NAME" ]; then
        log_error "Failed to extract cluster name from kubeconfig"
        exit 1
    fi
    
    log_info "Detected cluster name: $CLUSTER_NAME"
    
    # Check Helm installation
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
    
    log_success "Prerequisites check passed"
}

# Delete ResourceLeasingPolicy resources
delete_resource_leasing_policies() {
    log_info "Deleting ResourceLeasingPolicy resources..."
    
    local deleted_count=0
    local failed_count=0
    
    for rlp_name in "${RLP_NAMES[@]}"; do
        # Check if RLP exists
        if ! kubectl get resourceleasingpolicy "$rlp_name" &> /dev/null; then
            log_warning "ResourceLeasingPolicy '$rlp_name' not found, skipping"
            continue
        fi
        
        log_info "Deleting ResourceLeasingPolicy '$rlp_name'..."
        
        # Delete RLP with --wait=false
        if kubectl delete resourceleasingpolicy "$rlp_name" --wait=false 2>&1; then
            log_info "  ResourceLeasingPolicy deletion initiated"
            
            # Remove finalizers to ensure deletion
            log_info "  Removing finalizers..."
            if kubectl patch resourceleasingpolicy "$rlp_name" \
                --type json \
                -p='[{"op": "remove", "path": "/metadata/finalizers"}]' &> /dev/null; then
                log_success "  ✓ Finalizers removed"
            else
                # Finalizers might already be gone or resource might be deleted
                log_info "  Finalizers already removed or resource deleted"
            fi
            
            # Verify deletion
            if ! kubectl get resourceleasingpolicy "$rlp_name" &> /dev/null; then
                log_success "  ✓ ResourceLeasingPolicy '$rlp_name' deleted successfully"
                deleted_count=$((deleted_count + 1))
            else
                log_warning "  ResourceLeasingPolicy '$rlp_name' still exists, but deletion is in progress"
                deleted_count=$((deleted_count + 1))
            fi
        else
            log_error "Failed to delete ResourceLeasingPolicy '$rlp_name'"
            failed_count=$((failed_count + 1))
        fi
    done
    
    if [ $failed_count -gt 0 ]; then
        log_error "Failed to delete $failed_count ResourceLeasingPolicy(ies)"
        exit 1
    fi
    
    if [ $deleted_count -gt 0 ]; then
        log_success "Successfully deleted $deleted_count ResourceLeasingPolicy(ies)"
    else
        log_warning "No ResourceLeasingPolicies were deleted"
    fi
}

# Uninstall kubeocean-worker
uninstall_kubeocean_worker() {
    log_info "Uninstalling kubeocean-worker..."
    
    # Check if any ResourceLeasingPolicies exist
    log_info "Checking for existing ResourceLeasingPolicies..."
    local existing_rlps=$(kubectl get resourceleasingpolicies --no-headers 2>/dev/null | wc -l)
    
    if [ "$existing_rlps" -gt 0 ]; then
        log_error "Cannot uninstall kubeocean-worker: $existing_rlps ResourceLeasingPolicy(ies) still exist"
        log_error "Please delete all ResourceLeasingPolicies before uninstalling worker"
        log_error ""
        log_error "Existing ResourceLeasingPolicies:"
        kubectl get resourceleasingpolicies --no-headers 2>/dev/null | awk '{print "  • " $1}' || true
        log_error ""
        log_error "To delete ResourceLeasingPolicies, run:"
        log_error "  $0 --rlp-name <rlp-name>"
        log_error "Or manually delete them:"
        log_error "  kubectl delete resourceleasingpolicy <rlp-name>"
        exit 1
    fi
    
    log_success "No ResourceLeasingPolicies found, safe to proceed"
    
    # Check if release exists
    if ! $HELM list -a 2>/dev/null | grep -q "^kubeocean-worker"; then
        log_warning "Helm release 'kubeocean-worker' not found"
        return 0
    fi
    
    # Uninstall kubeocean-worker with retry logic
    log_info "Uninstalling Helm release 'kubeocean-worker'..."
    local max_retry_time=60  # Maximum retry time in seconds
    local retry_interval=5   # Retry interval in seconds
    local elapsed=0
    local uninstall_success=false

    while [ $elapsed -lt $max_retry_time ]; do
        # Attempt to uninstall
        local error_output
        local exit_code=0
        error_output=$($HELM uninstall kubeocean-worker --timeout 300s 2>&1 || exit_code=$?)

        if [ $exit_code -ne 0 ]; then
            # Display error output
            log_warning "Helm uninstall failed (attempt $((elapsed / retry_interval + 1))):"
            echo "$error_output" | sed 's/^/  /' | head -10
        fi

        # Check if release still exists
        if ! $HELM list -a 2>/dev/null | grep -q "^kubeocean-worker"; then
            log_success "Helm release 'kubeocean-worker' no longer exists (uninstall succeeded)"
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
        log_error "Failed to uninstall kubeocean-worker after ${max_retry_time} seconds"
        log_error "Please check the release status with: helm list -a"
        log_error "Last error output:"
        echo "$error_output" | sed 's/^/  /'
        exit 1
    fi
    
    # Display remaining resources
    log_info "Checking remaining resources..."
    local remaining_pods=$(kubectl get pods -n "$WORKER_NAMESPACE" --no-headers 2>/dev/null | wc -l)
    if [ "$remaining_pods" -gt 0 ]; then
        log_warning "Some pods still exist in namespace $WORKER_NAMESPACE:"
        kubectl get pods -n "$WORKER_NAMESPACE" 2>/dev/null | sed 's/^/  /'
    else
        log_success "All pods removed from namespace $WORKER_NAMESPACE"
    fi
}

# Display summary information
show_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "Kubeocean Worker Cluster Uninstallation Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Uninstallation Information:"
    echo "   • Cluster Name: $CLUSTER_NAME"
    echo "   • Worker Namespace: $WORKER_NAMESPACE"
    echo "   • RLPs Removed: ${#RLP_NAMES[@]}"
    echo ""
    echo "🗑️  Removed Resources:"
    for rlp_name in "${RLP_NAMES[@]}"; do
        echo "   • ResourceLeasingPolicy: ${rlp_name}"
    done
    echo "   • Helm Release: kubeocean-worker"
    echo ""
    echo "🔍 Verification Commands:"
    echo "   • Check remaining ResourceLeasingPolicies:"
    echo "     kubectl get resourceleasingpolicies"
    echo ""
    echo "   • Check Helm releases:"
    echo "     helm list -a"
    echo ""
    echo "   • Check remaining pods:"
    echo "     kubectl get pods -n $WORKER_NAMESPACE"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🗑️  Kubeocean Worker Cluster Uninstallation Script"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    
    # Parse arguments
    parse_args "$@"
    
    # Step 1: Check prerequisites
    echo "📋 Step 1/3: Checking Prerequisites"
    check_prerequisites
    echo ""
    
    # Step 2: Delete ResourceLeasingPolicies
    echo "📋 Step 2/3: Deleting ResourceLeasingPolicy Resources"
    delete_resource_leasing_policies
    echo ""
    
    # Step 3: Uninstall kubeocean-worker
    echo "📦 Step 3/3: Uninstalling kubeocean-worker"
    uninstall_kubeocean_worker
    echo ""
    
    # Display summary
    show_summary
}

# Execute main function
main "$@"
