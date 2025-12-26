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

# Kubeocean One-Click Installation Script for TKE
# Features:
# 1. Install and configure worker cluster
# 2. Install and configure manager cluster
# 3. Create cluster binding

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

# Display help information
show_help() {
    cat << EOF
Kubeocean One-Click Installation Script for TKE

This script automates the complete installation process by sequentially executing:
1. install-worker-tke.sh  - Install worker cluster components
2. install-manager-tke.sh - Install manager cluster components and create binding

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
  WORKER_CLUSTER_NAME          Worker cluster name for RLP (default: example-cluster)

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
  cp config.env.template config.env
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
        log_info "  cp config.env.template config.env"
        log_info "  vim config.env"
        exit 1
    fi
    
    log_info "Using configuration file: $CONFIG_FILE"
}

# Load configuration from config file
load_config() {
    log_info "Loading configuration from: $CONFIG_FILE"

    # Source the config file
    # shellcheck disable=SC1090
    source "$CONFIG_FILE"

    # Export all configuration variables so they are available to child scripts
    export MANAGER_REGION
    export MANAGER_CLUSTER_ID
    export MANAGER_SUBNET_ID
    export WORKER_REGION
    export WORKER_CLUSTER_ID
    export WORKER_SUBNET_ID
    export WORKER_KUBECONFIG
    export WORKER_CLUSTER_NAME

    log_success "Configuration loaded and exported as environment variables"
}

# Install worker cluster
install_worker() {
    local install_worker_script="${SCRIPT_DIR}/install-worker-tke.sh"
    
    if [ ! -f "$install_worker_script" ]; then
        log_error "install-worker-tke.sh not found: $install_worker_script"
        exit 1
    fi
    
    log_info "Executing install-worker-tke.sh..."
    log_info "Configuration passed via environment variables"
    
    if bash "$install_worker_script"; then
        log_success "Worker cluster installation completed"
    else
        log_error "Worker cluster installation failed"
        exit 1
    fi
}

# Install manager cluster
install_manager() {
    local install_manager_script="${SCRIPT_DIR}/install-manager-tke.sh"

    if [ ! -f "$install_manager_script" ]; then
        log_error "install-manager-tke.sh not found: $install_manager_script"
        exit 1
    fi

    log_info "Executing install-manager-tke.sh..."
    log_info "Configuration passed via environment variables"

    if bash "$install_manager_script"; then
        log_success "Manager cluster installation completed"
    else
        log_error "Manager cluster installation failed"
        exit 1
    fi
}

# Display final summary
show_final_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "Kubeocean One-Click Installation Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Installation Summary:"
    echo "   • Configuration: $CONFIG_FILE"
    echo "   • Worker cluster components installed ✓"
    echo "   • Manager cluster components installed ✓"
    echo "   • Cluster binding created ✓"
    echo ""
    echo "🔍 Quick Verification:"
    echo "   # Check worker cluster (switch context if needed)"
    echo "   kubectl get nodes -l kubeocean.io/role=worker"
    echo "   kubectl get all -n kubeocean-worker"
    echo "   kubectl get resourceleasingpolicies"
    echo ""
    echo "   # Check manager cluster (switch context if needed)"
    echo "   kubectl get all -n kubeocean-system"
    echo "   kubectl get clusterbindings"
    echo ""
    echo "🎉 Kubeocean is ready to use!"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🚀 Kubeocean One-Click Installation for TKE"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    
    # Parse arguments
    parse_args "$@"
    
    # Check config file
    check_config_file
    
    # Load configuration and export as environment variables
    load_config
    echo ""
    
    # Step 1: Install worker cluster
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "📦 Phase 1/2: Installing Worker Cluster"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    install_worker
    echo ""
    
    # Step 2: Install manager cluster
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "📦 Phase 2/2: Installing Manager Cluster"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    install_manager
    echo ""
    
    # Display final summary
    show_final_summary
}

# Execute main function
main "$@"

