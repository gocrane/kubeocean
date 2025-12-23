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

# Kubeocean Manager Cluster Installation Script for TKE
# Features:
# 1. Pre-check TKE cluster and tccli tool
# 2. Enable cluster internal network access
# 3. Extract cluster kubeconfig
# 4. Enable kube-dns internal network access
# 5. Install kubeocean-manager components

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
REGION=""
CLUSTER_ID=""
SUBNET_ID=""
WORKER_CLUSTER_ID=""
WORKER_KUBECONFIG="/tmp/kubeconfig-worker"

# VPC information (will be extracted from cluster info)
VPC_ID=""

# Display help information
show_help() {
    cat << EOF
Kubeocean Manager Cluster Installation Script for TKE

Usage: $0 [options]

Options:
  -h, --help                   Display this help message
  -r, --region REGION          TKE cluster region (required)
  -c, --cluster-id ID          TKE cluster ID (required)
  -s, --subnet-id ID           Subnet ID for internal network access (required for enabling internal access)
  -w, --worker-kubeconfig FILE Worker cluster kubeconfig path (default: /tmp/kubeconfig-worker)
  -i, --worker-cluster-id ID   Worker cluster ID (required)

Prerequisites:
  tccli                        Tencent Cloud CLI tool (must be installed and configured)
  jq                           Command-line JSON processor
  kubectl                      Kubernetes command-line tool
  helm v3.x                    Helm package manager (version 3.x required)

Examples:
  # Install TKE manager cluster with default worker kubeconfig path
  $0 --region ap-guangzhou --cluster-id cls-xxxxxxxx --subnet-id subnet-xxxxxxxx \\
     --worker-cluster-id cls-worker-xxx

  # Install with custom worker kubeconfig path
  $0 --region ap-guangzhou --cluster-id cls-xxxxxxxx --subnet-id subnet-xxxxxxxx \\
     --worker-kubeconfig /tmp/my-kubeconfig --worker-cluster-id cls-worker-xxx

  # Short form
  $0 -r ap-guangzhou -c cls-xxxxxxxx -s subnet-xxxxxxxx -i cls-worker-xxx

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
            -r|--region)
                REGION="$2"
                shift 2
                ;;
            -c|--cluster-id)
                CLUSTER_ID="$2"
                shift 2
                ;;
            -s|--subnet-id)
                SUBNET_ID="$2"
                shift 2
                ;;
            -w|--worker-kubeconfig)
                WORKER_KUBECONFIG="$2"
                shift 2
                ;;
            -i|--worker-cluster-id)
                WORKER_CLUSTER_ID="$2"
                shift 2
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

    # Check required parameters
    if [ -z "$REGION" ]; then
        log_error "Region is required. Please specify with --region"
        exit 1
    fi

    if [ -z "$CLUSTER_ID" ]; then
        log_error "Cluster ID is required. Please specify with --cluster-id"
        exit 1
    fi

    if [ -z "$WORKER_CLUSTER_ID" ]; then
        log_error "Worker cluster ID is required. Please specify with --worker-cluster-id"
        exit 1
    fi

    # Check if worker kubeconfig file exists
    if [ ! -f "$WORKER_KUBECONFIG" ]; then
        log_error "Worker kubeconfig file not found: $WORKER_KUBECONFIG"
        exit 1
    fi

    log_info "Region: $REGION"
    log_info "Cluster ID: $CLUSTER_ID"
    log_info "Worker Cluster ID: $WORKER_CLUSTER_ID"
    log_info "Worker Kubeconfig: $WORKER_KUBECONFIG"

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
    cluster_info=$(TENCENTCLOUD_REGION="$REGION" tccli tke DescribeClusters --ClusterIds "[\"$CLUSTER_ID\"]" 2>&1) || ret=$?

    if [ $ret -ne 0 ]; then
        log_error "Failed to query cluster information"
        log_error "$cluster_info"
        exit 1
    fi

    # Check if cluster exists
    local total_count
    total_count=$(echo "$cluster_info" | jq -r '.TotalCount // 0')

    if [ "$total_count" -eq 0 ]; then
        log_error "Cluster $CLUSTER_ID not found in region $REGION"
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
    endpoint_status=$(TENCENTCLOUD_REGION="$REGION" tccli tke DescribeClusterEndpointStatus --ClusterId "$CLUSTER_ID" 2>&1) || ret=$?

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
    if [ -z "$SUBNET_ID" ]; then
        log_error "Subnet ID is required for enabling internal access. Please specify with --subnet-id"
        exit 1
    fi

    # Enable internal access
    local enable_result
    ret=0
    enable_result=$(TENCENTCLOUD_REGION="$REGION" tccli tke CreateClusterEndpoint \
        --ClusterId "$CLUSTER_ID" \
        --SubnetId "$SUBNET_ID" \
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
        endpoint_status=$(TENCENTCLOUD_REGION="$REGION" tccli tke DescribeClusterEndpointStatus --ClusterId "$CLUSTER_ID" 2>&1) || ret=$?

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

# Enable kube-dns internal network access
enable_kube_dns_internal_access() {
    log_info "Checking kube-dns internal network access..."

    # Check if kube-dns-intranet service already exists
    if kubectl get svc kube-dns-intranet -n kube-system &> /dev/null; then
        log_success "kube-dns-intranet service already exists"
        return 0
    fi

    log_info "Creating kube-dns-intranet service..."

    # Check subnet ID parameter
    if [ -z "$SUBNET_ID" ]; then
        log_error "Subnet ID is required for creating kube-dns-intranet service. Please specify with --subnet-id"
        exit 1
    fi

    # Create kube-dns-intranet service
    local kube_dns_yaml=$(cat <<EOF
apiVersion: v1
kind: Service
metadata:
  annotations:
    service.kubernetes.io/qcloud-loadbalancer-internal-subnetid: $SUBNET_ID
  name: kube-dns-intranet
  namespace: kube-system
spec:
  allocateLoadBalancerNodePorts: true
  externalTrafficPolicy: Cluster
  internalTrafficPolicy: Cluster
  ipFamilies:
  - IPv4
  ipFamilyPolicy: SingleStack
  ports:
  - name: dns
    port: 53
    protocol: UDP
    targetPort: 53
  - name: dns-tcp
    port: 53
    protocol: TCP
    targetPort: 53
  selector:
    k8s-app: kube-dns
  sessionAffinity: None
  type: LoadBalancer
EOF
)

    if echo "$kube_dns_yaml" | kubectl apply -f - ; then
        log_success "kube-dns-intranet service created successfully"
    else
        log_error "Failed to create kube-dns-intranet service"
        exit 1
    fi
}

# Get cluster kubeconfig
get_cluster_kubeconfig() {
    log_info "Getting cluster kubeconfig..."

    # Get cluster security info (including kubeconfig)
    local security_info
    ret=0
    security_info=$(TENCENTCLOUD_REGION="$REGION" tccli tke DescribeClusterSecurity --ClusterId "$CLUSTER_ID" 2>&1) || ret=$?

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
    local temp_kubeconfig="/tmp/tke-kubeconfig-$CLUSTER_ID"
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

    # Set kubectl context name
    local context_name="manager-admin-$CLUSTER_ID"

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

    # Verify connection
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Failed to connect to cluster using the kubeconfig"
        exit 1
    fi

    log_success "Successfully connected to cluster"
}

# Install kubeocean-manager
install_kubeocean_manager() {
    log_info "Installing kubeocean-manager..."

    # Call install-manager.sh script
    local install_manager_script="${SCRIPT_DIR}/install-manager.sh"

    if [ ! -f "$install_manager_script" ]; then
        log_error "install-manager.sh script not found: $install_manager_script"
        exit 1
    fi

    log_info "Calling install-manager.sh with worker cluster: $WORKER_CLUSTER_ID"

    if bash "$install_manager_script" \
        --worker-kubeconfig "$WORKER_KUBECONFIG" \
        --cluster-id "$WORKER_CLUSTER_ID"; then
        log_success "kubeocean-manager installed successfully"
    else
        log_error "Failed to install kubeocean-manager"
        exit 1
    fi
}

# Display summary information
show_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "TKE Manager Cluster Installation Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Installation Information:"
    echo "   • Region: $REGION"
    echo "   • Cluster ID: $CLUSTER_ID"
    echo "   • VPC ID: $VPC_ID"
    echo "   • Subnet ID: $SUBNET_ID"
    echo "   • Context: manager-admin-$CLUSTER_ID (merged to ~/.kube/config)"
    echo "   • Worker Cluster ID: $WORKER_CLUSTER_ID"
    echo "   • Worker Kubeconfig: $WORKER_KUBECONFIG"
    echo ""
    echo "🔍 Next Steps:"
    echo "   1. Verify manager installation:"
    echo "      kubectl get all -n kubeocean-system"
    echo ""
    echo "   2. Check ClusterBinding status:"
    echo "      kubectl get clusterbindings"
    echo ""
    echo "   3. Switch to this context anytime:"
    echo "      kubectl config use-context manager-admin-$CLUSTER_ID"
    echo ""
    echo "   4. Check synced resources in worker cluster:"
    echo "      # Switch to worker cluster context and check"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🚀 TKE Manager Cluster Installation Script"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""

    # Parse arguments
    parse_args "$@"

    # Step 1: Check prerequisites
    echo "📋 Step 1/5: Checking Prerequisites"
    check_prerequisites
    echo ""

    # Step 2: Enable cluster internal network access
    echo "🌐 Step 2/5: Enabling Cluster Internal Network Access"
    enable_cluster_internal_access
    echo ""

    # Step 3: Get cluster kubeconfig
    echo "🔐 Step 3/5: Getting Cluster Kubeconfig"
    get_cluster_kubeconfig
    echo ""

    # Step 4: Enable kube-dns internal network access
    echo "🔌 Step 4/5: Enabling Kube-DNS Internal Network Access"
    enable_kube_dns_internal_access
    echo ""

    # Step 5: Install kubeocean-manager
    echo "📦 Step 5/5: Installing Kubeocean-Manager"
    install_kubeocean_manager
    echo ""

    # Display summary
    show_summary
}

# Execute main function
main "$@"
