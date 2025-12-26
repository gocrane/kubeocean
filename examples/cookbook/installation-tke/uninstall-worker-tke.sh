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

# Kubeocean Worker Cluster Uninstallation Script for TKE
# Features:
# 1. Get cluster kubeconfig
# 2. Uninstall kubeocean-worker components
# 3. Delete ResourceLeasingPolicy resources

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

# Script root directory (assuming script is in examples/playbook/installation-tke directory)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

# Configuration file path
CONFIG_FILE="${SCRIPT_DIR}/config.env"

# Worker Cluster Configuration variables
WORKER_REGION=""
WORKER_CLUSTER_ID=""

# Display help information
show_help() {
    cat << EOF
Kubeocean Worker Cluster Uninstallation Script for TKE

Usage: $0 [options]

Options:
  -h, --help                   Display this help message
  -c, --config FILE            Configuration file path (default: ./config.env)

Required Configuration Variables (in config file):
  WORKER_REGION                Worker cluster region
  WORKER_CLUSTER_ID            Worker cluster ID

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

    # Check tccli installation
    if ! command -v tccli &> /dev/null; then
        log_error "tccli is not installed. Please install Tencent Cloud CLI first"
        log_error "Visit: https://cloud.tencent.com/document/product/440/6176"
        exit 1
    fi
    
    log_success "Found tccli: $(which tccli)"

    # Check jq installation
    if ! command -v jq &> /dev/null; then
        log_error "jq is not installed. Please install jq first"
        log_error "Visit: https://jqlang.org/download/"
        exit 1
    fi
    
    log_success "Found jq: $(which jq)"

    # Check kubectl
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first"
        exit 1
    fi
    
    log_success "Found kubectl: $(which kubectl)"

    # Check Helm installation
    if ! command -v helm &> /dev/null; then
        log_error "Helm is not installed. Please install Helm first"
        log_error "Visit: https://helm.sh/docs/intro/install/"
        exit 1
    fi
    
    # Check Helm version (must be v3)
    HELM_VERSION_OUTPUT=$(helm version --short 2>/dev/null || echo "")
    if [ -z "$HELM_VERSION_OUTPUT" ]; then
        log_error "Failed to get Helm version"
        exit 1
    fi
    
    if echo "$HELM_VERSION_OUTPUT" | grep -q "^v3\."; then
        log_success "Helm version check passed: $HELM_VERSION_OUTPUT"
    else
        log_error "Helm version must be v3.x, but found: $HELM_VERSION_OUTPUT"
        exit 1
    fi

    log_success "Prerequisites check passed"
}

# Get cluster kubeconfig
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

    # Check if cluster exists and get cluster info
    local ret=0
    cluster_info=$(TENCENTCLOUD_REGION="$WORKER_REGION" tccli tke DescribeClusters --ClusterIds "[\"$WORKER_CLUSTER_ID\"]" 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to get cluster info: $cluster_info"
        exit 1
    fi

    # Check if cluster exists
    local cluster_count=$(echo "$cluster_info" | jq -r '.Clusters | length')
    if [ "$cluster_count" -eq 0 ]; then
        log_error "Cluster $WORKER_CLUSTER_ID not found in region $WORKER_REGION"
        exit 1
    fi

    log_success "Cluster found: $WORKER_CLUSTER_ID"

    # Get cluster security info (kubeconfig)
    log_info "Getting cluster security credentials..."
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

# Uninstall kubeocean-worker
uninstall_kubeocean_worker() {
    log_info "Uninstalling kubeocean-worker..."

    # Call uninstall-worker.sh script (in parent installation directory)
    local uninstall_worker_script="${SCRIPT_DIR}/../installation/uninstall-worker.sh"

    if [ ! -f "$uninstall_worker_script" ]; then
        log_error "uninstall-worker.sh script not found: $uninstall_worker_script"
        exit 1
    fi

    log_info "Calling uninstall-worker.sh"

    if bash "$uninstall_worker_script"; then
        log_success "kubeocean-worker uninstalled successfully"
    else
        log_error "Failed to uninstall kubeocean-worker"
        exit 1
    fi
}

# Cleanup kubectl config
cleanup_kubectl_config() {
    log_info "Cleaning up kubectl config..."
    
    local context_name="worker-admin-$WORKER_CLUSTER_ID"
    
    # Check if context exists
    if ! kubectl config get-contexts "$context_name" &> /dev/null; then
        log_info "Context '$context_name' not found, skipping cleanup"
        return 0
    fi
    
    # Get cluster and user names associated with this context
    local cluster_name
    local user_name
    
    cluster_name=$(kubectl config view -o jsonpath="{.contexts[?(@.name=='$context_name')].context.cluster}" 2>/dev/null)
    user_name=$(kubectl config view -o jsonpath="{.contexts[?(@.name=='$context_name')].context.user}" 2>/dev/null)
    
    log_info "Found context: $context_name"
    if [ -n "$cluster_name" ]; then
        log_info "  Cluster: $cluster_name"
    fi
    if [ -n "$user_name" ]; then
        log_info "  User: $user_name"
    fi
    
    # Delete context
    if kubectl config delete-context "$context_name" &> /dev/null; then
        log_success "Deleted context: $context_name"
    else
        log_warning "Failed to delete context: $context_name"
    fi
    
    # Delete cluster
    if [ -n "$cluster_name" ]; then
        if kubectl config delete-cluster "$cluster_name" &> /dev/null; then
            log_success "Deleted cluster: $cluster_name"
        else
            log_warning "Failed to delete cluster: $cluster_name"
        fi
    fi
    
    # Delete user
    if [ -n "$user_name" ]; then
        if kubectl config unset "users.$user_name" &> /dev/null; then
            log_success "Deleted user: $user_name"
        else
            log_warning "Failed to delete user: $user_name"
        fi
    fi
    
    log_success "Kubectl config cleanup completed"
}

# Display summary information
show_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "Kubeocean Worker Cluster Uninstallation Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Uninstallation Information:"
    echo "   • Worker Region: $WORKER_REGION"
    echo "   • Worker Cluster ID: $WORKER_CLUSTER_ID"
    echo "   • Context: worker-admin-$WORKER_CLUSTER_ID (cleaned up)"
    echo ""
    echo "🔍 Verification Commands:"
    echo "   • Check if namespace deleted:"
    echo "     kubectl get ns kubeocean-worker"
    echo ""
    echo "   • Check if ResourceLeasingPolicy deleted:"
    echo "     kubectl get resourceleasingpolicies"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🗑️  TKE Worker Cluster Uninstallation Script"
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
    echo "📋 Step 1/4: Checking Prerequisites"
    check_prerequisites
    echo ""

    # Step 2: Get cluster kubeconfig
    echo "🔐 Step 2/4: Getting Cluster Kubeconfig"
    get_cluster_kubeconfig
    echo ""

    # Step 3: Uninstall kubeocean-worker
    echo "🗑️  Step 3/4: Uninstalling Kubeocean-Worker"
    uninstall_kubeocean_worker
    echo ""

    # Step 4: Cleanup kubectl config
    echo "🧹 Step 4/4: Cleaning Up Kubectl Config"
    cleanup_kubectl_config
    echo ""

    # Display summary
    show_summary
}

# Execute main function
main "$@"

