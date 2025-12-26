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

# Kubeocean One-Click Uninstallation Script for TKE
# Features:
# 1. Uninstall manager cluster and remove cluster binding
# 2. Uninstall worker cluster components

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
Kubeocean One-Click Uninstallation Script for TKE

This script automates the complete uninstallation process by sequentially executing:
1. uninstall-manager-tke.sh - Uninstall manager cluster components and remove binding
2. uninstall-worker-tke.sh  - Uninstall worker cluster components

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
  SKIP_MANAGER_UNINSTALL       Skip manager uninstallation (default: false)

Prerequisites:
  tccli                        Tencent Cloud CLI tool (must be installed and configured)
  jq                           Command-line JSON processor
  kubectl                      Kubernetes command-line tool

Examples:
  # Use default configuration file (./config.env)
  $0

  # Use custom configuration file
  $0 -c /path/to/my-config.env

  # Quick uninstallation
  $0

Note:
  - Manager cluster will be uninstalled first, then worker cluster
  - Kubectl config contexts will be automatically cleaned up
  - Set SKIP_MANAGER_UNINSTALL=true to only remove cluster binding without uninstalling manager components

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
        log_info "Please provide a valid configuration file"
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
    export WORKER_REGION
    export WORKER_CLUSTER_ID
    export WORKER_CLUSTER_NAME
    export SKIP_MANAGER_UNINSTALL

    log_success "Configuration loaded and exported as environment variables"
}

# Uninstall manager cluster
uninstall_manager() {
    local uninstall_manager_script="${SCRIPT_DIR}/uninstall-manager-tke.sh"

    if [ ! -f "$uninstall_manager_script" ]; then
        log_error "uninstall-manager-tke.sh not found: $uninstall_manager_script"
        exit 1
    fi

    log_info "Executing uninstall-manager-tke.sh..."
    log_info "Configuration passed via environment variables"

    if bash "$uninstall_manager_script"; then
        log_success "Manager cluster uninstallation completed"
    else
        log_error "Manager cluster uninstallation failed"
        exit 1
    fi
}

# Uninstall worker cluster
uninstall_worker() {
    local uninstall_worker_script="${SCRIPT_DIR}/uninstall-worker-tke.sh"
    
    if [ ! -f "$uninstall_worker_script" ]; then
        log_error "uninstall-worker-tke.sh not found: $uninstall_worker_script"
        exit 1
    fi
    
    log_info "Executing uninstall-worker-tke.sh..."
    log_info "Configuration passed via environment variables"
    
    if bash "$uninstall_worker_script"; then
        log_success "Worker cluster uninstallation completed"
    else
        log_error "Worker cluster uninstallation failed"
        exit 1
    fi
}

# Display final summary
show_final_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_success "Kubeocean One-Click Uninstallation Complete!"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "📋 Uninstallation Summary:"
    echo "   • Configuration: $CONFIG_FILE"
    echo "   • Manager cluster components uninstalled ✓"
    echo "   • Worker cluster components uninstalled ✓"
    echo "   • Kubectl config contexts cleaned up ✓"
    echo ""
    echo "🔍 Verification:"
    echo "   # Verify manager namespace removed (switch context if needed)"
    echo "   kubectl get ns kubeocean-system"
    echo ""
    echo "   # Verify worker namespace removed (switch context if needed)"
    echo "   kubectl get ns kubeocean-worker"
    echo ""
    echo "   # Check kubectl contexts"
    echo "   kubectl config get-contexts"
    echo ""
    echo "🎉 Kubeocean has been completely uninstalled!"
    echo ""
}

# Main function
main() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "   🗑️  Kubeocean One-Click Uninstallation for TKE"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    
    # Parse arguments
    parse_args "$@"
    
    # Check config file
    check_config_file
    
    # Load configuration and export as environment variables
    load_config
    echo ""
    
    # Step 1: Uninstall manager cluster
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🗑️  Phase 1/2: Uninstalling Manager Cluster"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    uninstall_manager
    echo ""
    
    # Step 2: Uninstall worker cluster
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🗑️  Phase 2/2: Uninstalling Worker Cluster"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    uninstall_worker
    echo ""
    
    # Display final summary
    show_final_summary
}

# Execute main function
main "$@"


