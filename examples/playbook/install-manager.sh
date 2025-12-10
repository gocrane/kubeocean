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

# Kubeocean Manager Cluster Installation Script
# Features:
# 1. Install kubeocean-manager components
# 2. Create secrets from worker kubeconfig files
# 3. Create ClusterBinding resources

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
MOUNT_NAMESPACE="kubeocean-worker"

# Configurable parameters (can be overridden via environment variables or command line)
WORKER_KUBECONFIGS=()
CLUSTER_IDS=()
CLUSTER_NAMES=()
SKIP_MANAGER_INSTALL="${SKIP_MANAGER_INSTALL:-false}"

# Default values
DEFAULT_WORKER_KUBECONFIG="/tmp/kubeconfig-worker"
DEFAULT_CLUSTER_ID="cls-example"
DEFAULT_CLUSTER_NAME="example-cluster"

# Display help information
show_help() {
    cat << EOF
Kubeocean Manager Cluster Installation Script

Usage: $0 [options]

Options:
  -h, --help                    Display this help message
  -w, --worker-kubeconfig FILE  Specify worker kubeconfig file (default: /tmp/kubeconfig-worker)
  -i, --cluster-id ID           Specify cluster ID (default: cls-example)
  -n, --cluster-name NAME       Specify cluster name (default: example-cluster)
  --skip-manager                Skip kubeocean-manager installation (only create secrets and bindings)

Environment Variables:
  SKIP_MANAGER_INSTALL          Skip manager installation (true/false)

Prerequisites:
  kubectl                       Kubernetes command-line tool
  helm v3.x                     Helm package manager (version 3.x required)

Examples:
  # Install manager with all default values
  $0

  # Install manager with default kubeconfig but custom ID and name
  $0 --cluster-id cls-prod --cluster-name prod-cluster

  # Install manager with custom kubeconfig
  $0 --worker-kubeconfig /tmp/kubeconfig-worker1

  # Install manager and add a single worker cluster with custom settings
  $0 --worker-kubeconfig /tmp/kubeconfig-worker1 \\
     --cluster-id cls-worker1 \\
     --cluster-name worker1

  # Add multiple worker clusters at once
  $0 --worker-kubeconfig /tmp/kubeconfig-worker1 --cluster-id cls-worker1 --cluster-name worker1 \\
     --worker-kubeconfig /tmp/kubeconfig-worker2 --cluster-id cls-worker2 --cluster-name worker2

  # Only create secrets and bindings (skip manager installation)
  $0 --skip-manager

Notes:
  - Worker kubeconfig defaults to '/tmp/kubeconfig-worker' if not specified
  - Cluster ID defaults to 'cls-example' if not specified
  - Cluster name defaults to 'example-cluster' if not specified
  - Manager will be installed to 'kubeocean-system' namespace
  - Secrets will be created in 'kubeocean-system' namespace
  - ClusterBindings will reference 'kubeocean-worker' as mount namespace

EOF
}

# Parse command line arguments
parse_args() {
    local current_kubeconfig=""
    local current_id=""
    local current_name=""
    local has_explicit_config=false
    
    while [[ $# -gt 0 ]]; do
        case $1 in
            -h|--help)
                show_help
                exit 0
                ;;
            -w|--worker-kubeconfig)
                # Save previous set if exists
                if [ -n "$current_kubeconfig" ]; then
                    # Use default values if not specified
                    [ -z "$current_id" ] && current_id="$DEFAULT_CLUSTER_ID"
                    [ -z "$current_name" ] && current_name="$DEFAULT_CLUSTER_NAME"
                    
                    WORKER_KUBECONFIGS+=("$current_kubeconfig")
                    CLUSTER_IDS+=("$current_id")
                    CLUSTER_NAMES+=("$current_name")
                fi
                current_kubeconfig="$2"
                current_id=""
                current_name=""
                has_explicit_config=true
                shift 2
                ;;
            -i|--cluster-id)
                current_id="$2"
                shift 2
                ;;
            -n|--cluster-name)
                current_name="$2"
                shift 2
                ;;
            --skip-manager)
                SKIP_MANAGER_INSTALL=true
                shift
                ;;
            *)
                log_error "Unknown argument: $1"
                show_help
                exit 1
                ;;
        esac
    done
    
    # Save the last set
    if [ -n "$current_kubeconfig" ]; then
        # Use default values if not specified
        [ -z "$current_id" ] && current_id="$DEFAULT_CLUSTER_ID"
        [ -z "$current_name" ] && current_name="$DEFAULT_CLUSTER_NAME"
        
        WORKER_KUBECONFIGS+=("$current_kubeconfig")
        CLUSTER_IDS+=("$current_id")
        CLUSTER_NAMES+=("$current_name")
    fi
    
    # If no explicit kubeconfig provided, use default
    if [ ${#WORKER_KUBECONFIGS[@]} -eq 0 ]; then
        # Check if cluster-id or cluster-name was provided without kubeconfig
        if [ -n "$current_id" ] || [ -n "$current_name" ]; then
            # Use default kubeconfig with provided or default cluster info
            [ -z "$current_id" ] && current_id="$DEFAULT_CLUSTER_ID"
            [ -z "$current_name" ] && current_name="$DEFAULT_CLUSTER_NAME"
            
            WORKER_KUBECONFIGS+=("$DEFAULT_WORKER_KUBECONFIG")
            CLUSTER_IDS+=("$current_id")
            CLUSTER_NAMES+=("$current_name")
        else
            # Use all default values
            WORKER_KUBECONFIGS+=("$DEFAULT_WORKER_KUBECONFIG")
            CLUSTER_IDS+=("$DEFAULT_CLUSTER_ID")
            CLUSTER_NAMES+=("$DEFAULT_CLUSTER_NAME")
        fi
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
    
    # Check worker kubeconfig files
    log_info "Checking worker kubeconfig files..."
    for i in "${!WORKER_KUBECONFIGS[@]}"; do
        local kubeconfig="${WORKER_KUBECONFIGS[$i]}"
        if [ ! -f "$kubeconfig" ]; then
            log_error "Worker kubeconfig file not found: $kubeconfig"
            exit 1
        fi
        log_info "  ✓ Found: $kubeconfig"
    done
    
    log_success "Prerequisites check passed"
}

# Install kubeocean-manager
install_kubeocean_manager() {
    if [ "$SKIP_MANAGER_INSTALL" = "true" ]; then
        log_info "Skipping kubeocean-manager installation (SKIP_MANAGER_INSTALL=true)"
        return 0
    fi
    
    log_info "Installing kubeocean-manager..."
    
    local CHART_PATH="${PROJECT_ROOT}/charts/kubeocean"
    
    if [ ! -d "$CHART_PATH" ]; then
        log_error "Helm chart not found: $CHART_PATH"
        exit 1
    fi
    
    log_info "Using chart: $CHART_PATH"
    
    # Install or upgrade kubeocean-manager
    if ! $HELM upgrade --install kubeocean "$CHART_PATH" \
        --wait \
        --timeout 300s; then
        log_error "Failed to install kubeocean-manager"
        exit 1
    fi
    
    log_success "kubeocean-manager installed successfully!"
    
    # Display deployment status
    log_info "Checking deployment status..."
    kubectl get pods -n "$MANAGER_NAMESPACE" 2>/dev/null || log_warning "Namespace $MANAGER_NAMESPACE not created yet or no resources"
}

# Create secrets from worker kubeconfigs
create_worker_secrets() {
    log_info "Creating worker kubeconfig secrets..."
    
    for i in "${!WORKER_KUBECONFIGS[@]}"; do
        local kubeconfig="${WORKER_KUBECONFIGS[$i]}"
        local cluster_name="${CLUSTER_NAMES[$i]}"
        local secret_name="${cluster_name}-kubeconfig"
        
        log_info "Creating secret '$secret_name' from $kubeconfig..."
        
        # Create or update secret
        if kubectl create secret generic "$secret_name" \
            --from-file=kubeconfig="$kubeconfig" \
            -n "$MANAGER_NAMESPACE" \
            --dry-run=client -o yaml | kubectl apply -f - &> /dev/null; then
            log_success "  ✓ Secret '$secret_name' created/updated"
        else
            log_error "Failed to create secret '$secret_name'"
            exit 1
        fi
    done
    
    log_success "All worker kubeconfig secrets created successfully!"
}

# Create ClusterBinding resources
create_cluster_bindings() {
    log_info "Creating ClusterBinding resources..."
    
    for i in "${!WORKER_KUBECONFIGS[@]}"; do
        local cluster_id="${CLUSTER_IDS[$i]}"
        local cluster_name="${CLUSTER_NAMES[$i]}"
        local secret_name="${cluster_name}-kubeconfig"
        local binding_name="${cluster_name}"
        
        log_info "Creating ClusterBinding '$binding_name'..."
        
        # Create ClusterBinding from template
        cat <<EOF | kubectl apply -f - &> /dev/null
apiVersion: cloud.tencent.com/v1beta1
kind: ClusterBinding
metadata:
  name: ${binding_name}
  namespace: ${MANAGER_NAMESPACE}
spec:
  clusterID: ${cluster_id}
  mountNamespace: ${MOUNT_NAMESPACE}
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: kubeocean.io/role
        operator: In
        values:
        - worker
  secretRef:
    name: ${secret_name}
    namespace: ${MANAGER_NAMESPACE}
EOF
        
        if [ $? -eq 0 ]; then
            log_success "  ✓ ClusterBinding '$binding_name' created/updated"
        else
            log_error "Failed to create ClusterBinding '$binding_name'"
            exit 1
        fi
    done
    
    log_success "All ClusterBinding resources created successfully!"
}

# Display summary information
show_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "Kubeocean Manager Cluster Installation Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Installation Information:"
    echo "   • Manager Cluster: $MANAGER_CLUSTER_NAME"
    echo "   • Manager Namespace: $MANAGER_NAMESPACE"
    echo "   • Mount Namespace: $MOUNT_NAMESPACE"
    echo "   • Worker Clusters: ${#WORKER_KUBECONFIGS[@]}"
    echo ""
    echo "🔗 Configured Worker Clusters:"
    for i in "${!WORKER_KUBECONFIGS[@]}"; do
        local cluster_id="${CLUSTER_IDS[$i]}"
        local cluster_name="${CLUSTER_NAMES[$i]}"
        local binding_name="${cluster_name}"
        echo "   $((i+1)). Cluster Name: ${cluster_name}"
        echo "      • Cluster ID: ${cluster_id}"
        echo "      • Secret: ${cluster_name}-kubeconfig"
        echo "      • ClusterBinding: ${binding_name}"
    done
    echo ""
    echo "🔍 Verification Commands:"
    echo "   • Check manager pods:"
    echo "     kubectl get pods -n $MANAGER_NAMESPACE"
    echo ""
    echo "   • Check ClusterBindings:"
    echo "     kubectl get clusterbindings"
    echo ""
    echo "   • Check specific ClusterBinding:"
    for i in "${!WORKER_KUBECONFIGS[@]}"; do
        local cluster_name="${CLUSTER_NAMES[$i]}"
        local binding_name="${cluster_name}"
        echo "     kubectl get clusterbinding ${binding_name} -o yaml"
    done
    echo ""
    echo "   • Check secrets:"
    echo "     kubectl get secrets -n $MANAGER_NAMESPACE | grep kubeconfig"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🚀 Kubeocean Manager Cluster Installation Script"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    
    # Parse arguments
    parse_args "$@"
    
    # Step 1: Check prerequisites
    echo "📋 Step 1/4: Checking Prerequisites"
    check_prerequisites
    echo ""
    
    # Step 2: Install kubeocean-manager
    echo "📦 Step 2/4: Installing kubeocean-manager"
    install_kubeocean_manager
    echo ""
    
    # Step 3: Create worker secrets
    echo "🔑 Step 3/4: Creating Worker Kubeconfig Secrets"
    create_worker_secrets
    echo ""
    
    # Step 4: Create ClusterBindings
    echo "🔗 Step 4/4: Creating ClusterBinding Resources"
    create_cluster_bindings
    echo ""
    
    # Display summary
    show_summary
}

# Execute main function
main "$@"
