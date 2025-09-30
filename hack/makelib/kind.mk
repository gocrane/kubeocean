# KIND (Kubernetes in Docker) related targets and configurations

##@ KIND Cluster

# KIND cluster configuration
# Cluster name can be configured via CLUSTER_NAME or KIND_CLUSTER_NAME environment variables
CLUSTER_NAME ?= kubeocean-test
KIND_CLUSTER_NAME ?= $(CLUSTER_NAME)
KIND_K8S_VERSION ?= v1.28.0
KIND_CONFIG_FILE ?= hack/makelib/kind/kind-config-kubeocean-test.yaml

# Multi-cluster configuration for kubeocean
KIND_MANAGER_CLUSTER ?= kubeocean-manager
KIND_WORKER1_CLUSTER ?= kubeocean-worker1
KIND_WORKER2_CLUSTER ?= kubeocean-worker2

## Tool Binaries
KIND ?= $(LOCALBIN)/kind

## Tool Versions
KIND_VERSION ?= v0.30.0

.PHONY: kind
kind: $(KIND) ## Download kind locally if necessary.
$(KIND): $(LOCALBIN)
	test -s $(LOCALBIN)/kind || \
	curl -Lo $(LOCALBIN)/kind https://kind.sigs.k8s.io/dl/$(KIND_VERSION)/kind-linux-amd64 && \
	chmod +x $(LOCALBIN)/kind

.PHONY: kind-create
kind-create: kind ## Create KIND cluster for kubeocean development.
	@if $(KIND) get clusters 2>/dev/null | grep -q "^$(KIND_CLUSTER_NAME)$$"; then \
		echo "✅ KIND cluster '$(KIND_CLUSTER_NAME)' already exists, skipping creation..."; \
		echo "🔗 Context: kind-$(KIND_CLUSTER_NAME)"; \
		CONTAINER_IP=$$(docker inspect $(KIND_CLUSTER_NAME)-control-plane --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "unknown"); \
		echo "🌐 API Server: https://$$CONTAINER_IP:6443"; \
		echo "📊 Metrics: http://$$CONTAINER_IP:8080/metrics"; \
		echo "🏥 Health: http://$$CONTAINER_IP:8081/healthz"; \
	else \
		echo "🚀 Creating KIND cluster '$(KIND_CLUSTER_NAME)' with Kubernetes $(KIND_K8S_VERSION)..."; \
		$(KIND) create cluster --name $(KIND_CLUSTER_NAME) --config $(KIND_CONFIG_FILE) --wait 300s; \
		echo "⚙️  Setting up kubeconfig context..."; \
		$(KIND) export kubeconfig --name $(KIND_CLUSTER_NAME); \
		echo "🔍 Verifying cluster status..."; \
		kubectl cluster-info --context kind-$(KIND_CLUSTER_NAME); \
		kubectl get nodes -o wide; \
		echo ""; \
		echo "✅ KIND cluster '$(KIND_CLUSTER_NAME)' is ready!"; \
		echo "📋 Cluster details:"; \
		echo "   • Name: $(KIND_CLUSTER_NAME)"; \
		echo "   • Kubernetes version: $(KIND_K8S_VERSION)"; \
		echo "   • Nodes: 3 (1 control-plane + 2 workers)"; \
		echo "   • Context: kind-$(KIND_CLUSTER_NAME)"; \
	fi

.PHONY: kind-delete
kind-delete: kind ## Delete KIND cluster.
	@echo "🗑️  Deleting KIND cluster '$(KIND_CLUSTER_NAME)'..."
	$(KIND) delete cluster --name $(KIND_CLUSTER_NAME)
	@echo "✅ KIND cluster '$(KIND_CLUSTER_NAME)' deleted successfully."

##@ Multi-Cluster KIND Operations

.PHONY: kind-create-manager
kind-create-manager: ## Create KIND manager cluster (virtual cluster).
	@echo "🚀 Creating KIND manager cluster '$(KIND_MANAGER_CLUSTER)'..."
	KIND_CLUSTER_NAME=$(KIND_MANAGER_CLUSTER) make kind-create
	@echo "✅ Manager cluster '$(KIND_MANAGER_CLUSTER)' is ready!"

.PHONY: kind-create-worker1
kind-create-worker1: ## Create KIND worker cluster 1 (physical cluster).
	@echo "🚀 Creating KIND worker cluster '$(KIND_WORKER1_CLUSTER)'..."
	KIND_CLUSTER_NAME=$(KIND_WORKER1_CLUSTER) make kind-create
	@echo "✅ Worker cluster 1 '$(KIND_WORKER1_CLUSTER)' is ready!"

.PHONY: kind-create-worker2
kind-create-worker2: ## Create KIND worker cluster 2 (physical cluster).
	@echo "🚀 Creating KIND worker cluster '$(KIND_WORKER2_CLUSTER)'..."
	KIND_CLUSTER_NAME=$(KIND_WORKER2_CLUSTER) make kind-create
	@echo "✅ Worker cluster 2 '$(KIND_WORKER2_CLUSTER)' is ready!"

.PHONY: kind-create-all
kind-create-all: kind-create-manager kind-create-worker1 kind-create-worker2 ## Create all kubeocean KIND clusters.
	@echo ""
	@echo "🎉 All kubeocean KIND clusters created successfully!"
	@echo ""
	@echo "🔍 Check status with: make kind-status"

.PHONY: kind-delete-manager
kind-delete-manager: kind ## Delete KIND manager cluster.
	@echo "🗑️  Deleting KIND manager cluster '$(KIND_MANAGER_CLUSTER)'..."
	$(KIND) delete cluster --name $(KIND_MANAGER_CLUSTER)
	@echo "✅ Manager cluster deleted."

.PHONY: kind-delete-worker1
kind-delete-worker1: kind ## Delete KIND worker cluster 1.
	@echo "🗑️  Deleting KIND worker cluster '$(KIND_WORKER1_CLUSTER)'..."
	$(KIND) delete cluster --name $(KIND_WORKER1_CLUSTER)
	@echo "✅ Worker cluster 1 deleted."

.PHONY: kind-delete-worker2
kind-delete-worker2: kind ## Delete KIND worker cluster 2.
	@echo "🗑️  Deleting KIND worker cluster '$(KIND_WORKER2_CLUSTER)'..."
	$(KIND) delete cluster --name $(KIND_WORKER2_CLUSTER)
	@echo "✅ Worker cluster 2 deleted."

.PHONY: kind-delete-all
kind-delete-all: kind-delete-manager kind-delete-worker1 kind-delete-worker2 ## Delete all kubeocean KIND clusters.
	@echo "✅ All kubeocean KIND clusters deleted."

.PHONY: kind-load-images
kind-load-images: kind docker-build ## Load locally built images into KIND cluster.
	@echo "📦 Loading kubeocean images into KIND cluster..."
	$(KIND) load docker-image $(IMG_MANAGER) --name $(KIND_CLUSTER_NAME)
	$(KIND) load docker-image $(IMG_SYNCER) --name $(KIND_CLUSTER_NAME)
	$(KIND) load docker-image $(IMG_PROXIER) --name $(KIND_CLUSTER_NAME)
	@echo "✅ Images loaded successfully."

.PHONY: kind-status
kind-status: kind ## Show KIND cluster status for kubeocean.
	@echo "📊 Kubeocean KIND Status:"
	@echo ""
	@echo "🔍 Available clusters:"
	@$(KIND) get clusters 2>/dev/null || echo "❌ No KIND clusters found"
	@echo ""
	@echo "📋 Kubeocean cluster details:"
	@echo ""
	@# Check Cluster
	@echo "🎛️  Cluster:"
	@if $(KIND) get clusters 2>/dev/null | grep -q "$(KIND_CLUSTER_NAME)"; then \
		echo "   ✅ $(KIND_CLUSTER_NAME) - Running"; \
		echo "   🔗 Context: kind-$(KIND_CLUSTER_NAME)"; \
		CONTAINER_IP=$$(docker inspect $(KIND_CLUSTER_NAME)-control-plane --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "unknown"); \
		echo "   🌐 API Server: https://$$CONTAINER_IP:6443"; \
		echo "   📊 Metrics: http://$$CONTAINER_IP:8080/metrics"; \
		echo "   🏥 Health: http://$$CONTAINER_IP:8081/healthz"; \
		echo "   📋 Nodes:"; \
		kubectl get nodes -o wide --context kind-$(KIND_CLUSTER_NAME) 2>/dev/null | sed 's/^/      /' || echo "      ❌ Cannot get nodes"; \
	else \
		echo "   ❌ $(KIND_CLUSTER_NAME) - Not found"; \
		echo "   🚀 Create with: make kind-create"; \
	fi

.PHONY: kind-status-all
kind-status-all: kind ## Show KIND multi-cluster status for kubeocean.
	@echo "📊 Kubeocean KIND Multi-Cluster Status:"
	@echo ""
	@echo "🔍 Available clusters:"
	@$(KIND) get clusters 2>/dev/null || echo "❌ No KIND clusters found"
	@echo ""
	@echo "📋 Kubeocean cluster details:"
	@echo ""
	@# Check Manager Cluster
	@echo "🎛️  Manager Cluster (Virtual Cluster):"
	@if $(KIND) get clusters 2>/dev/null | grep -q "$(KIND_MANAGER_CLUSTER)"; then \
		echo "   ✅ $(KIND_MANAGER_CLUSTER) - Running"; \
		echo "   🔗 Context: kind-$(KIND_MANAGER_CLUSTER)"; \
		CONTAINER_IP=$$(docker inspect $(KIND_MANAGER_CLUSTER)-control-plane --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "unknown"); \
		echo "   🌐 API Server: https://$$CONTAINER_IP:6443"; \
		echo "   📊 Metrics: http://$$CONTAINER_IP:8080/metrics"; \
		echo "   🏥 Health: http://$$CONTAINER_IP:8081/healthz"; \
		echo "   📋 Nodes:"; \
		kubectl get nodes -o wide --context kind-$(KIND_MANAGER_CLUSTER) 2>/dev/null | sed 's/^/      /' || echo "      ❌ Cannot get nodes"; \
	else \
		echo "   ❌ $(KIND_MANAGER_CLUSTER) - Not found"; \
		echo "   🚀 Create with: make kind-create-manager"; \
	fi
	@echo ""
	@# Check Worker Cluster 1
	@echo "🏭 Worker Cluster 1 (Physical Cluster):"
	@if $(KIND) get clusters 2>/dev/null | grep -q "$(KIND_WORKER1_CLUSTER)"; then \
		echo "   ✅ $(KIND_WORKER1_CLUSTER) - Running"; \
		echo "   🔗 Context: kind-$(KIND_WORKER1_CLUSTER)"; \
		CONTAINER_IP=$$(docker inspect $(KIND_WORKER1_CLUSTER)-control-plane --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "unknown"); \
		echo "   🌐 API Server: https://$$CONTAINER_IP:6443"; \
		echo "   📊 Metrics: http://$$CONTAINER_IP:8080/metrics"; \
		echo "   🏥 Health: http://$$CONTAINER_IP:8081/healthz"; \
		echo "   📋 Nodes:"; \
		kubectl get nodes -o wide --context kind-$(KIND_WORKER1_CLUSTER) 2>/dev/null | sed 's/^/      /' || echo "      ❌ Cannot get nodes"; \
	else \
		echo "   ❌ $(KIND_WORKER1_CLUSTER) - Not found"; \
		echo "   🚀 Create with: make kind-create-worker1"; \
	fi
	@echo ""
	@# Check Worker Cluster 2
	@echo "🏭 Worker Cluster 2 (Physical Cluster):"
	@if $(KIND) get clusters 2>/dev/null | grep -q "$(KIND_WORKER2_CLUSTER)"; then \
		echo "   ✅ $(KIND_WORKER2_CLUSTER) - Running"; \
		echo "   🔗 Context: kind-$(KIND_WORKER2_CLUSTER)"; \
		CONTAINER_IP=$$(docker inspect $(KIND_WORKER2_CLUSTER)-control-plane --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "unknown"); \
		echo "   🌐 API Server: https://$$CONTAINER_IP:6443"; \
		echo "   📊 Metrics: http://$$CONTAINER_IP:8080/metrics"; \
		echo "   🏥 Health: http://$$CONTAINER_IP:8081/healthz"; \
		echo "   📋 Nodes:"; \
		kubectl get nodes -o wide --context kind-$(KIND_WORKER2_CLUSTER) 2>/dev/null | sed 's/^/      /' || echo "      ❌ Cannot get nodes"; \
	else \
		echo "   ❌ $(KIND_WORKER2_CLUSTER) - Not found"; \
		echo "   🚀 Create with: make kind-create-worker2"; \
	fi
	@echo ""
	@echo "📚 Quick Commands:"
	@echo "   • Create all clusters: make kind-create-all"
	@echo "   • Delete all clusters: make kind-clean"
	@echo "   • Load images: make kind-load-images"

.PHONY: kind-logs
kind-logs: kind ## Show logs from KIND cluster nodes.
	@echo "📜 KIND cluster logs:"
	@echo ""
	@echo "🔍 Control-plane logs:"
	$(KIND) export logs --name $(KIND_CLUSTER_NAME) /tmp/kind-logs-$(KIND_CLUSTER_NAME) 2>/dev/null || echo "❌ Cannot export logs"
	@if [ -d "/tmp/kind-logs-$(KIND_CLUSTER_NAME)" ]; then \
		echo "📁 Logs exported to: /tmp/kind-logs-$(KIND_CLUSTER_NAME)"; \
		echo ""; \
		echo "🔧 Key log files:"; \
		find /tmp/kind-logs-$(KIND_CLUSTER_NAME) -name "*.log" | head -5 | while read log; do echo "   $$log"; done; \
	fi

.PHONY: kind-clean
kind-clean: ## Clean up KIND related files and clusters.
	@echo "🧹 Cleaning up all KIND resources..."
	@# Delete all kubeocean-related KIND clusters
	@for cluster in $$($(KIND) get clusters 2>/dev/null | grep kubeocean || true); do \
		echo "🗑️  Deleting cluster: $$cluster"; \
		$(KIND) delete cluster --name $$cluster; \
	done
	@# Clean up log files
	@rm -rf /tmp/kind-logs-*
	@echo "✅ KIND cleanup completed."
