# Build related targets and configurations

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager and syncer and proxier  binaries.
	go build -o bin/kubeocean-manager cmd/kubeocean-manager/main.go
	go build -o bin/kubeocean-syncer cmd/kubeocean-syncer/main.go
	go build -o bin/kubeocean-proxier cmd/kubeocean-proxier/main.go

.PHONY: run-manager
run-manager: manifests generate fmt vet ## Run kubeocean-manager from your host.
	go run cmd/kubeocean-manager/main.go

.PHONY: run-syncer
run-syncer: manifests generate fmt vet ## Run kubeocean-syncer from your host.
	go run cmd/kubeocean-syncer/main.go --cluster-binding-name=example-binding

# If you wish built the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64 ). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/dev-best-practices/
.PHONY: docker-build.manager
docker-build.manager: ## Build docker image for manager only.
	$(eval IMG := $(TEST_REGISTRY)/kubeocean-manager:$(IMG_TAG)-$(GOARCH))
	docker build --platform=linux/$(GOARCH) -t ${IMG} -f hack/docker/Dockerfile.manager --build-arg LDFLAGS=${LDFLAGS} --build-arg GOPROXY=${GOPROXY} --build-arg BASEIMAGE=${BASEIMAGE} --build-arg TARGETARCH=${GOARCH} .

.PHONY: docker-build.syncer
docker-build.syncer: ## Build docker image for syncer only.
	$(eval IMG := $(TEST_REGISTRY)/kubeocean-syncer:$(IMG_TAG)-$(GOARCH))
	docker build --platform=linux/$(GOARCH) -t ${IMG} -f hack/docker/Dockerfile.syncer --build-arg LDFLAGS=${LDFLAGS} --build-arg GOPROXY=${GOPROXY} --build-arg BASEIMAGE=${BASEIMAGE} --build-arg TARGETARCH=${GOARCH} .

.PHONY: docker-build.proxier
docker-build.proxier: ## Build docker image for proxier only.
	$(eval IMG := $(TEST_REGISTRY)/kubeocean-proxier:$(IMG_TAG)-$(GOARCH))
	docker build --platform=linux/$(GOARCH) -t ${IMG} -f hack/docker/Dockerfile.proxier --build-arg LDFLAGS=${LDFLAGS} --build-arg GOPROXY=${GOPROXY} --build-arg BASEIMAGE=${BASEIMAGE} --build-arg TARGETARCH=${GOARCH} .

.PHONY: docker-build
docker-build: docker-build.manager docker-build.syncer docker-build.proxier ## Build docker images for manager, syncer and proxier.

.PHONY: docker-push
docker-push: docker-build ## Push docker images for manager, syncer and proxier.
	docker push ${IMG_MANAGER}
	docker push ${IMG_SYNCER}
	docker push ${IMG_PROXIER}

.PHONY: docker-push.manager
docker-push.manager: docker-build.manager ## Push docker image for manager only.
	$(eval IMG := $(TEST_REGISTRY)/kubeocean-manager:$(IMG_TAG)-$(GOARCH))
	docker push ${IMG}

.PHONY: docker-push.syncer
docker-push.syncer: docker-build.syncer ## Push docker image for syncer only.
	$(eval IMG := $(TEST_REGISTRY)/kubeocean-syncer:$(IMG_TAG)-$(GOARCH))
	docker push ${IMG}

.PHONY: docker-push.proxier
docker-push.proxier: docker-build.proxier ## Push docker image for proxier only.
	$(eval IMG := $(TEST_REGISTRY)/kubeocean-proxier:$(IMG_TAG)-$(GOARCH))
	docker push ${IMG}

.PHONY: docker-buildx.manager
docker-buildx.manager: ## Build and push docker images for the manager for cross-platform support.
	@GOARCH=amd64 make docker-push.manager
	@GOARCH=arm64 make docker-push.manager
	$(eval IMGS := $(TEST_REGISTRY)/kubeocean-manager:$(IMG_TAG)-arm64 $(TEST_REGISTRY)/kubeocean-manager:$(IMG_TAG)-amd64)
	@echo "===========> push multi-arch image $(IMG_MANAGER)"
	docker buildx imagetools create -t $(IMG_MANAGER) $(IMGS)

.PHONY: docker-buildx.syncer
docker-buildx.syncer: ## Build and push docker images for the syncer for cross-platform support.
	@GOARCH=amd64 make docker-push.syncer
	@GOARCH=arm64 make docker-push.syncer
	$(eval IMGS := $(TEST_REGISTRY)/kubeocean-syncer:$(IMG_TAG)-arm64 $(TEST_REGISTRY)/kubeocean-syncer:$(IMG_TAG)-amd64)
	@echo "===========> push multi-arch image $(IMG_SYNCER)"
	docker buildx imagetools create -t $(IMG_SYNCER) $(IMGS)

.PHONY: docker-buildx.proxier
docker-buildx.proxier: ## Build and push docker images for the proxier for cross-platform support.
	@GOARCH=amd64 make docker-push.proxier
	@GOARCH=arm64 make docker-push.proxier
	$(eval IMGS := $(TEST_REGISTRY)/kubeocean-proxier:$(IMG_TAG)-arm64 $(TEST_REGISTRY)/kubeocean-proxier:$(IMG_TAG)-amd64)
	@echo "===========> push multi-arch image $(IMG_PROXIER)"
	docker buildx imagetools create -t $(IMG_PROXIER) $(IMGS)

# To use this option you need to:
# - able to use docker buildx . More info: https://docs.docker.com/build/buildx/
# - have a multi-arch builder. More info: https://docs.docker.com/build/building/multi-platform/
# - be able to push the image for your registry (i.e. if you do not inform a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To properly provided solutions that supports more than one platform you should use this option.
.PHONY: docker-buildx
docker-buildx: docker-buildx.manager docker-buildx.syncer docker-buildx.proxier ## Build and push docker images for the manager, syncer and proxier for cross-platform support.

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
GINKGO ?= $(LOCALBIN)/ginkgo
HELM ?= $(LOCALBIN)/helm

## Tool Versions
KUSTOMIZE_VERSION ?= v5.6.0
CONTROLLER_TOOLS_VERSION ?= v0.18.0
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
GOLANGCI_LINT_VERSION ?= v2.3.1
HELM_VERSION ?= v3.18.6

KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || { curl -Ss $(KUSTOMIZE_INSTALL_SCRIPT) | bash -s -- $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN); }

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	test -s $(LOCALBIN)/golangci-lint && $(LOCALBIN)/golangci-lint --version | grep -q $(GOLANGCI_LINT_VERSION) || \
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: helm
helm: $(HELM) ## Download helm locally if necessary. If wrong version is installed, it will be removed before downloading.
$(HELM): $(LOCALBIN)
	@if test -x $(LOCALBIN)/helm && ! $(LOCALBIN)/helm version --short | grep -q $(HELM_VERSION); then \
		echo "$(LOCALBIN)/helm version is not expected $(HELM_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/helm; \
	fi
	@if ! test -s $(LOCALBIN)/helm; then \
		echo "Installing helm $(HELM_VERSION) to $(LOCALBIN)/helm..."; \
		OS=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
		ARCH=$$(uname -m); \
		case $$ARCH in \
			x86_64) ARCH=amd64 ;; \
			aarch64|arm64) ARCH=arm64 ;; \
			*) echo "Unsupported architecture: $$ARCH"; exit 1 ;; \
		esac; \
		HELM_URL="https://get.helm.sh/helm-$(HELM_VERSION)-$$OS-$$ARCH.tar.gz"; \
		curl -fsSL $$HELM_URL | tar -xz -C /tmp --strip-components=1; \
		mv /tmp/helm $(LOCALBIN)/helm; \
		chmod +x $(LOCALBIN)/helm; \
		echo "✅ Helm $(HELM_VERSION) installed successfully to $(LOCALBIN)/helm"; \
	fi

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter.
	$(GOLANGCI_LINT) run

##@ Deployment

.PHONY: install-manager
install-manager: helm ## Install kubeocean manager to the current cluster using helm.
	@echo "🚀 Installing kubeocean manager to current cluster..."
	$(HELM) upgrade --install kubeocean charts/kubeocean \
		--set global.imageRegistry=${TEST_REGISTRY} \
		--set manager.image.tag=${IMG_TAG} \
		--set syncer.image.tag=${IMG_TAG} \
		--set proxier.image.tag=${IMG_TAG} \
		--wait \
		--timeout 300s
	@echo "✅ Kubeocean manager installed successfully!"
	@echo "📋 Check status with: kubectl get pods -n kubeocean-system"

.PHONY: install-worker
install-worker: helm ## Install kubeocean worker resources to the current cluster using helm.
	@echo "🚀 Installing kubeocean worker resources to current cluster..."
	$(HELM) upgrade --install kubeocean-worker charts/kubeocean-worker \
		--wait \
		--timeout 300s
	@echo "✅ Kubeocean worker resources installed successfully!"
	@echo "📋 Check status with: kubectl get all -n kubeocean-worker"

.PHONY: uninstall-manager
uninstall-manager: helm ## Uninstall kubeocean manager from the current cluster.
	@echo "🗑️  Uninstalling kubeocean manager from current cluster..."
	$(HELM) uninstall kubeocean --ignore-not-found
	@echo "✅ Kubeocean manager uninstalled successfully!"

.PHONY: uninstall-worker
uninstall-worker: helm ## Uninstall kubeocean worker resources from the current cluster.
	@echo "🗑️  Uninstalling kubeocean worker resources from current cluster..."
	$(HELM) uninstall kubeocean-worker --ignore-not-found
	@echo "✅ Kubeocean worker resources uninstalled successfully!"

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