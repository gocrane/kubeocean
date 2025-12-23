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

# Kubeocean Worker Cluster Installation Script
# Features:
# 1. Install kubeocean-worker components
# 2. Extract kubeconfig
# 3. Deploy ResourceLeasingPolicy

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

# Script root directory (assuming script is in examples/playbook/installation directory)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

# Configuration variables
LOCALBIN="${PROJECT_ROOT}/bin"
HELM="${LOCALBIN}/helm"
KUBECONFIG_SCRIPT="${PROJECT_ROOT}/hack/kubeconfig.sh"
RLP_SAMPLE="${PROJECT_ROOT}/examples/resourceleasingpolicy_sample.yaml"

# Fixed parameters (use default values)
SERVICEACCOUNT_NAME="kubeocean-syncer"
NAMESPACE="kubeocean-worker"
CLUSTER_NAME=""  # Will be extracted from kubeconfig

# Configurable parameters (can be overridden via environment variables)
OUTPUT_KUBECONFIG="${OUTPUT_KUBECONFIG:-/tmp/kubeconfig-worker}"
SKIP_RLP="${SKIP_RLP:-false}"

# Display help information
show_help() {
    cat << EOF
Kubeocean Worker Cluster Installation Script

Usage: $0 [options]

Options:
  -h, --help                    Display this help message
  -o, --output FILE             Specify kubeconfig output file path (default: /tmp/kubeconfig-worker)
  --skip-rlp                    Skip ResourceLeasingPolicy deployment

Environment Variables:
  OUTPUT_KUBECONFIG             Kubeconfig output file path
  SKIP_RLP                      Skip RLP deployment (true/false)

Prerequisites:
  kubectl                       Kubernetes command-line tool
  helm v3.x                     Helm package manager (version 3.x required)

Examples:
  # Install with default configuration
  $0

  # Specify output path
  $0 --output /tmp/my-kubeconfig

  # Skip ResourceLeasingPolicy deployment
  $0 --skip-rlp

  # Use environment variables
  OUTPUT_KUBECONFIG=/tmp/my-kubeconfig $0

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
            -o|--output)
                OUTPUT_KUBECONFIG="$2"
                shift 2
                ;;
            --skip-rlp)
                SKIP_RLP=true
                shift
                ;;
            *)
                log_error "Unknown argument: $1"
                show_help
                exit 1
                ;;
        esac
    done
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


# Install kubeocean-worker
install_kubeocean_worker() {
    log_info "Installing kubeocean-worker..."
    
    local CHART_PATH="${PROJECT_ROOT}/charts/kubeocean-worker"
    
    if [ ! -d "$CHART_PATH" ]; then
        log_error "Helm chart not found: $CHART_PATH"
        exit 1
    fi
    
    log_info "Using chart: $CHART_PATH"
    
    # Install or upgrade kubeocean-worker
    if ! $HELM upgrade --install kubeocean-worker "$CHART_PATH" \
        --wait \
        --timeout 300s; then
        log_error "Failed to install kubeocean-worker"
        exit 1
    fi
    
    log_success "kubeocean-worker installed successfully!"
    
    # Display deployment status
    log_info "Checking deployment status..."
    kubectl get all -n "$NAMESPACE" 2>/dev/null || log_warning "Namespace $NAMESPACE not created yet or no resources"
}

# Extract kubeconfig
extract_kubeconfig() {
    log_info "Extracting kubeconfig..."
    
    # Check kubeconfig.sh script
    if [ ! -f "$KUBECONFIG_SCRIPT" ]; then
        log_error "Kubeconfig extraction script not found: $KUBECONFIG_SCRIPT"
        exit 1
    fi
    
    # Wait for ServiceAccount creation
    log_info "Waiting for ServiceAccount '$SERVICEACCOUNT_NAME' in namespace '$NAMESPACE'..."
    local max_retries=30
    local retry=0
    
    while [ $retry -lt $max_retries ]; do
        if kubectl get serviceaccount "$SERVICEACCOUNT_NAME" -n "$NAMESPACE" &> /dev/null; then
            log_success "ServiceAccount is ready"
            break
        fi
        retry=$((retry + 1))
        if [ $retry -ge $max_retries ]; then
            log_error "Timeout waiting for ServiceAccount"
            exit 1
        fi
        sleep 2
    done
    
    # Wait for Secret creation
    log_info "Waiting for Secret '$SERVICEACCOUNT_NAME' in namespace '$NAMESPACE'..."
    retry=0
    
    while [ $retry -lt $max_retries ]; do
        if kubectl get secret "$SERVICEACCOUNT_NAME" -n "$NAMESPACE" &> /dev/null; then
            log_success "Secret is ready"
            break
        fi
        retry=$((retry + 1))
        if [ $retry -ge $max_retries ]; then
            log_error "Timeout waiting for Secret"
            exit 1
        fi
        sleep 2
    done
    
    # Extract kubeconfig
    log_info "Extracting kubeconfig to: $OUTPUT_KUBECONFIG"
    
    if ! bash "$KUBECONFIG_SCRIPT" "$SERVICEACCOUNT_NAME" "$NAMESPACE" "$OUTPUT_KUBECONFIG"; then
        log_error "Failed to extract kubeconfig"
        exit 1
    fi
    
    log_success "Kubeconfig saved to: $OUTPUT_KUBECONFIG"
}

# Deploy ResourceLeasingPolicy
deploy_rlp() {
    if [ "$SKIP_RLP" = "true" ]; then
        log_info "Skipping ResourceLeasingPolicy deployment (SKIP_RLP=true)"
        return 0
    fi
    
    log_info "Deploying ResourceLeasingPolicy..."
    
    if [ ! -f "$RLP_SAMPLE" ]; then
        log_error "ResourceLeasingPolicy sample file not found: $RLP_SAMPLE"
        exit 1
    fi
    
    log_info "Applying ResourceLeasingPolicy: $RLP_SAMPLE"
    
    if ! kubectl apply -f "$RLP_SAMPLE"; then
        log_error "Failed to deploy ResourceLeasingPolicy"
        exit 1
    fi
    
    log_success "ResourceLeasingPolicy deployed successfully!"
    
    # Display deployment status
    log_info "ResourceLeasingPolicy status:"
    kubectl get resourceleasingpolicies -o wide || log_warning "Unable to get ResourceLeasingPolicy status"
}

# Display summary information
show_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "Kubeocean Worker Cluster Installation Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Installation Information:"
    echo "   • Cluster Name: $CLUSTER_NAME"
    echo "   • Namespace: $NAMESPACE"
    echo "   • ServiceAccount: $SERVICEACCOUNT_NAME"
    echo "   • Kubeconfig: $OUTPUT_KUBECONFIG"
    echo ""
    echo "🔍 Verification Commands:"
    echo "   • Check worker resources:"
    echo "     kubectl get all -n $NAMESPACE"
    echo ""
    echo "   • Check ResourceLeasingPolicy:"
    echo "     kubectl get resourceleasingpolicies"
    echo ""
    echo "   • Use extracted kubeconfig:"
    echo "     export KUBECONFIG=$OUTPUT_KUBECONFIG"
    echo "     kubectl cluster-info"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🚀 Kubeocean Worker Cluster Installation Script"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    
    # Parse arguments
    parse_args "$@"
    
    # Step 1: Check prerequisites
    echo "📋 Step 1/4: Checking Prerequisites"
    check_prerequisites
    echo ""
    
    # Step 2: Install kubeocean-worker
    echo "📦 Step 2/4: Installing kubeocean-worker"
    install_kubeocean_worker
    echo ""
    
    # Step 3: Extract kubeconfig
    echo "🔐 Step 3/4: Extracting kubeconfig"
    extract_kubeconfig
    echo ""
    
    # Step 4: Deploy ResourceLeasingPolicy
    echo "📋 Step 4/4: Deploying ResourceLeasingPolicy"
    deploy_rlp
    echo ""
    
    # Display summary
    show_summary
}

# Execute main function
main "$@"
