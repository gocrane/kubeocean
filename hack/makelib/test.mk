# Test related targets and configurations

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: ginkgo
ginkgo: $(GINKGO) ## Download ginkgo locally if necessary.
$(GINKGO): $(LOCALBIN)
	test -s $(LOCALBIN)/ginkgo || GOBIN=$(LOCALBIN) go install github.com/onsi/ginkgo/v2/ginkgo@2.23.4

.PHONY: test
test: manifests generate fmt vet envtest ginkgo ## Run tests with ginkgo in parallel (minimal output).
	@echo "Running tests..."
	@KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" $(GINKGO) -p --succinct --no-color --cover --coverprofile=cover.out ./... >/dev/null 2>&1 && echo "✅ All tests passed! Coverage report: cover.out" || (echo "❌ Tests failed. Run 'make test-verbose' for detailed output." && exit 1)

.PHONY: test-verbose
test-verbose: manifests generate fmt vet envtest ginkgo ## Run tests with ginkgo in parallel with verbose output.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" $(GINKGO) -p -v --cover --coverprofile=cover.out ./...

##@ Integration Test

.PHONY: test-int-build
test-int-build: manifests generate envtest ## Build integration test binary without running.
	mkdir -p .testcache
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -c ./test/integration -o ./.testcache/integration.test

.PHONY: test-int
test-int: manifests generate envtest ginkgo ## Run integration tests in parallel.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" $(GINKGO) -p -v ./test/integration

.PHONY: test-int-focus
test-int-focus: manifests generate envtest ginkgo ## Run integration tests with focus filter. Usage: make test-int-focus FOCUS="test pattern"
	@if [ -z "$(FOCUS)" ]; then \
		echo "Error: FOCUS parameter is required. Usage: make test-int-focus FOCUS=\"test pattern\""; \
		echo "Example: make test-int-focus FOCUS=\"Virtual Node Resource Tests\""; \
		exit 1; \
	fi
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" $(GINKGO) -v --focus="$(FOCUS)" ./test/integration

##@ End-to-End Test

# K8s E2E test configuration
K8S_VERSION ?= v1.28.0
K8S_E2E_BINARY ?= $(LOCALBIN)/e2e.test
K8S_SOURCE_DIR ?= /tmp/kubernetes-$(subst v,,$(K8S_VERSION))
E2E_TEST_TIMEOUT ?= 30s
E2E_FOCUS ?= "Pods should run through the lifecycle of Pods and PodStatus"

.PHONY: k8s-e2e
k8s-e2e: $(K8S_E2E_BINARY) ## Download Kubernetes source and build E2E test binary.
	@echo "✅ Kubernetes E2E test binary ready at: $(K8S_E2E_BINARY)"

.PHONY: test-k8s-e2e
test-k8s-e2e: k8s-e2e ## Run Kubernetes E2E tests with minimal test suite (Pod creation/deletion).
	@echo "🚀 Running Kubernetes E2E tests..."
	@echo "📋 Test Configuration:"
	@echo "   • Kubernetes Version: $(K8S_VERSION)"
	@echo "   • Timeout: $(E2E_TEST_TIMEOUT)"
	@echo "   • Focus: $(E2E_FOCUS)"
	@echo "   • Binary: $(K8S_E2E_BINARY)"
	@echo ""
	@echo "🧪 Running E2E tests..."
	@KUBECONFIG_PATH=$${KUBECONFIG:-$$HOME/.kube/config}; \
	echo "   • Using kubeconfig: $$KUBECONFIG_PATH"; \
	echo "   • Current context: $$(kubectl config current-context)"; \
	echo "   • Cluster info: $$(kubectl cluster-info | head -1)"; \
	echo ""; \
	$(K8S_E2E_BINARY) \
		--ginkgo.focus=$(E2E_FOCUS) \
		--ginkgo.v \
		--ginkgo.timeout=$(E2E_TEST_TIMEOUT) \
		-provider=local \
		-kubeconfig=$$KUBECONFIG_PATH \
		-disable-log-dump \
		-repo-root=$(K8S_SOURCE_DIR) \
		-allowed-not-ready-nodes=4 \
		-non-blocking-taints="kubeocean.io/vnode"
	@echo "✅ E2E tests completed!"

.PHONY: test-k8s-e2e-focus
test-k8s-e2e-focus: k8s-e2e ## Run specific E2E tests with focus filter. Usage: make test-k8s-e2e-focus FOCUS="test pattern"
	@if [ -z "$(FOCUS)" ]; then \
		echo "Error: FOCUS parameter is required. Usage: make test-k8s-e2e-focus FOCUS=\"test pattern\""; \
		echo "Example: make test-k8s-e2e-focus FOCUS=\"Pod Creation\""; \
		exit 1; \
	fi
	@echo "🚀 Running focused E2E tests: $(FOCUS)"
	@KUBECONFIG_PATH=$${KUBECONFIG:-$$HOME/.kube/config}; \
	$(K8S_E2E_BINARY) \
		--ginkgo.focus=$(FOCUS) \
		--ginkgo.v \
		--ginkgo.timeout=$(E2E_TEST_TIMEOUT) \
		-provider=local \
		-kubeconfig=$$KUBECONFIG_PATH \
		-disable-log-dump \
		-repo-root=$(K8S_SOURCE_DIR) \
		-non-blocking-taints="kubeocean.io/vnode"

$(K8S_E2E_BINARY): $(LOCALBIN)
	@echo "📦 Downloading Kubernetes $(K8S_VERSION) source code..."
	@if [ ! -d "$(K8S_SOURCE_DIR)" ]; then \
		echo "   Downloading https://github.com/kubernetes/kubernetes/archive/refs/tags/$(K8S_VERSION).tar.gz"; \
		wget -q -O /tmp/kubernetes-$(K8S_VERSION).tar.gz \
			https://github.com/kubernetes/kubernetes/archive/refs/tags/$(K8S_VERSION).tar.gz; \
		echo "   Extracting to $(K8S_SOURCE_DIR)..."; \
		tar -xzf /tmp/kubernetes-$(K8S_VERSION).tar.gz -C /tmp/; \
		echo "   Cleaning up archive..."; \
		rm -f /tmp/kubernetes-$(K8S_VERSION).tar.gz; \
		echo "✅ Kubernetes source downloaded successfully"; \
	else \
		echo "✅ Kubernetes source already exists at $(K8S_SOURCE_DIR)"; \
	fi
	@echo "🔨 Building Kubernetes E2E test binary..."
	@cd $(K8S_SOURCE_DIR) && \
		echo "   Setting up build environment..." && \
		export KUBE_BUILD_PLATFORMS=linux/amd64 && \
		echo "   Building e2e.test binary..." && \
		make WHAT=test/e2e/e2e.test && \
		echo "   Copying binary to $(K8S_E2E_BINARY)..." && \
		cp _output/bin/e2e.test $(K8S_E2E_BINARY) && \
		chmod +x $(K8S_E2E_BINARY)
	@echo "✅ E2E test binary built successfully at $(K8S_E2E_BINARY)"

.PHONY: clean-k8s-e2e
clean-k8s-e2e: ## Clean up downloaded Kubernetes source and E2E binary.
	@echo "🧹 Cleaning up Kubernetes E2E resources..."
	@rm -rf $(K8S_SOURCE_DIR)
	@rm -f $(K8S_E2E_BINARY)
	@echo "✅ Cleanup completed!"

.PHONY: test-kind-k8s-e2e
test-kind-k8s-e2e: kind-deploy ## Run Kubernetes E2E tests in KIND cluster with full setup.
	@echo ""
	@echo "🔧 Step 1: Configuring ClusterBindings in manager cluster..."
	@kubectl config use-context kind-$(KIND_MANAGER_CLUSTER)
	@echo "   • Patching cb-worker1 to disable node default taint..."
	@kubectl patch clusterbinding cb-worker1 -n kubeocean-system --type=merge -p '{"spec":{"disableNodeDefaultTaint":true}}'
	@echo "   • Patching cb-worker2 to disable node default taint..."
	@kubectl patch clusterbinding cb-worker2 -n kubeocean-system --type=merge -p '{"spec":{"disableNodeDefaultTaint":true}}'
	@echo "   ✅ ClusterBindings configured"
	@echo ""
	@echo "🧹 Step 2: Cleaning up kindnet DaemonSet in manager cluster..."
	@kubectl delete daemonset kindnet -n kube-system --ignore-not-found=true
	@echo "   ✅ Kindnet DaemonSet removed"
	@echo ""
	@echo "⚙️  Step 3: Configuring kube-proxy DaemonSet nodeAffinity..."
	@kubectl config use-context kind-$(KIND_MANAGER_CLUSTER)
	@kubectl patch daemonset kube-proxy -n kube-system --type=json -p='[{"op":"add","path":"/spec/template/spec/affinity","value":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"node.kubernetes.io/instance-type","operator":"NotIn","values":["vnode"]}]}]}}}}]' 2>/dev/null || \
	kubectl patch daemonset kube-proxy -n kube-system --type=merge -p='{"spec":{"template":{"spec":{"affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"node.kubernetes.io/instance-type","operator":"NotIn","values":["vnode"]}]}]}}}}}}}}'
	@echo "   ✅ kube-proxy nodeAffinity configured to exclude vnode"
	@echo ""
	@echo "🚫 Step 4: Cordoning manager cluster worker nodes..."
	@kubectl config use-context kind-$(KIND_MANAGER_CLUSTER)
	@kubectl cordon $(KIND_MANAGER_CLUSTER)-worker 2>/dev/null && echo "   • Cordoned $(KIND_MANAGER_CLUSTER)-worker" || echo "   ⚠️  Node $(KIND_MANAGER_CLUSTER)-worker not found or already cordoned"
	@kubectl cordon $(KIND_MANAGER_CLUSTER)-worker2 2>/dev/null && echo "   • Cordoned $(KIND_MANAGER_CLUSTER)-worker2" || echo "   ⚠️  Node $(KIND_MANAGER_CLUSTER)-worker2 not found or already cordoned"
	@echo "   ✅ Manager worker nodes cordoned"
	@echo ""
	@echo "🧪 Step 5: Running Kubernetes E2E tests..."
	@$(MAKE) test-k8s-e2e
	@echo ""
	@echo "🎉 KIND E2E test suite completed successfully!"

