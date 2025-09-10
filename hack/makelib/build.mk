# Build related targets and configurations
# 构建相关的目标和配置

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager and syncer binaries.
	go build -o bin/kubeocean-manager cmd/kubeocean-manager/main.go
	go build -o bin/kubeocean-syncer cmd/kubeocean-syncer/main.go

.PHONY: run-manager
run-manager: manifests generate fmt vet ## Run kubeocean-manager from your host.
	go run cmd/kubeocean-manager/main.go

.PHONY: run-syncer
run-syncer: manifests generate fmt vet ## Run kubeocean-syncer from your host.
	go run cmd/kubeocean-syncer/main.go --cluster-binding-name=example-binding

# If you wish built the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64 ). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/dev-best-practices/
.PHONY: docker-build
docker-build: ## Build docker images for manager and syncer.
	docker build -t ${IMG_MANAGER} -f hack/docker/Dockerfile.manager .
	docker build -t ${IMG_SYNCER} -f hack/docker/Dockerfile.syncer .

.PHONY: docker-build.manager
docker-build.manager: ## Build docker image for manager only.
	docker build -t ${IMG_MANAGER} -f hack/docker/Dockerfile.manager .

.PHONY: docker-build.syncer
docker-build.syncer: ## Build docker image for syncer only.
	docker build -t ${IMG_SYNCER} -f hack/docker/Dockerfile.syncer .

.PHONY: docker-push
docker-push: docker-build ## Push docker images for manager and syncer.
	docker push ${IMG_MANAGER}
	docker push ${IMG_SYNCER}

.PHONY: docker-push.manager
docker-push.manager: docker-build.manager ## Push docker image for manager only.
	docker push ${IMG_MANAGER}

.PHONY: docker-push.syncer
docker-push.syncer: docker-build.syncer ## Push docker image for syncer only.
	docker push ${IMG_SYNCER}

# PLATFORMS defines the target platforms for the manager image be build to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - able to use docker buildx . More info: https://docs.docker.com/build/buildx/
# - have a multi-arch builder. More info: https://docs.docker.com/build/building/multi-platform/
# - be able to push the image for your registry (i.e. if you do not inform a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To properly provided solutions that supports more than one platform you should use this option.
PLATFORMS ?= linux/arm64,linux/amd64
.PHONY: docker-buildx
docker-buildx: ## Build and push docker images for the manager and syncer for cross-platform support.
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' hack/docker/Dockerfile.manager > Dockerfile.cross.manager
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' hack/docker/Dockerfile.syncer > Dockerfile.cross.syncer
	- docker buildx create --name project-v3-builder
	docker buildx use project-v3-builder
	- docker buildx build --push --platform=$(PLATFORMS) --tag ${IMG_MANAGER} -f Dockerfile.cross.manager .
	- docker buildx build --push --platform=$(PLATFORMS) --tag ${IMG_SYNCER} -f Dockerfile.cross.syncer .
	- docker buildx rm project-v3-builder
	rm Dockerfile.cross.manager Dockerfile.cross.syncer

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

## Tool Versions
KUSTOMIZE_VERSION ?= v5.6.0
CONTROLLER_TOOLS_VERSION ?= v0.18.0
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
GOLANGCI_LINT_VERSION ?= v2.3.1

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

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	test -s $(LOCALBIN)/golangci-lint && $(LOCALBIN)/golangci-lint --version | grep -q $(GOLANGCI_LINT_VERSION) || \
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: ginkgo
ginkgo: $(GINKGO) ## Download ginkgo locally if necessary.
$(GINKGO): $(LOCALBIN)
	test -s $(LOCALBIN)/ginkgo || GOBIN=$(LOCALBIN) go install github.com/onsi/ginkgo/v2/ginkgo@2.23.4
