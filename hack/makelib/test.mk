# Test related targets and configurations
# 测试相关的目标和配置

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter.
	$(GOLANGCI_LINT) run

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
