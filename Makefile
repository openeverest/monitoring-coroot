## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

# CONTAINER_TOOL defines the container tool to be used for building images.
CONTAINER_TOOL ?= docker

# Image URL to use for all building/pushing image targets.
IMG ?= ghcr.io/openeverest/monitoring-coroot-dev:latest
_IMG_REPO = $(firstword $(subst :, ,$(IMG)))
_IMG_TAG  = $(lastword $(subst :, ,$(IMG)))

CHART_DIR ?= charts/monitoring-coroot
RELEASE_NAMESPACE ?= openeverest-system

GOLANGCI_LINT_VERSION ?= v2.14.0

# CRDs of the openeverest core version pinned in go.mod.
OPENEVEREST_MODULE = github.com/openeverest/openeverest/v2
OPENEVEREST_CRDS = $$(go list -m -f '{{.Dir}}' $(OPENEVEREST_MODULE))/config/crd/bases
.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: lint
lint: ## Run golangci-lint.
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

.PHONY: tidy-check
tidy-check: ## Verify go.mod and go.sum are tidy.
	go mod tidy
	git diff --exit-code -- go.mod go.sum

.PHONY: run
run: ## Run the controller against the current kubeconfig.
	go run ./cmd/controller

##@ Testing

.PHONY: test-unit
test-unit: ## Run unit tests.
	go test -race -coverprofile=coverage.out ./...

##@ Build

.PHONY: build
build: $(LOCALBIN) ## Build the controller binary.
	CGO_ENABLED=0 go build -o $(LOCALBIN)/controller ./cmd/controller

.PHONY: docker-build
docker-build: ## Build the controller image.
	$(CONTAINER_TOOL) build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push the controller image.
	$(CONTAINER_TOOL) push $(IMG)

##@ Helm

.PHONY: helm-lint
helm-lint: ## Lint the chart.
	helm lint $(CHART_DIR)

.PHONY: helm-template
helm-template: ## Render the chart.
	helm template monitoring-coroot $(CHART_DIR) -n $(RELEASE_NAMESPACE)

##@ Cluster

.PHONY: install-crds
install-crds: ## Install the OpenEverest CRDs the controller needs.
	go mod download $(OPENEVEREST_MODULE)
	kubectl apply --server-side -f $(OPENEVEREST_CRDS)
	kubectl wait --for=condition=Established --timeout=60s -f $(OPENEVEREST_CRDS)

.PHONY: deploy-ci
deploy-ci: ## Install the chart with a locally available image (IMG).
	kubectl create namespace $(RELEASE_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	kubectl label --overwrite namespace $(RELEASE_NAMESPACE) pod-security.kubernetes.io/enforce=restricted
	helm upgrade --install monitoring-coroot $(CHART_DIR) \
		--namespace $(RELEASE_NAMESPACE) \
		--set image.repository=$(_IMG_REPO) \
		--set image.tag=$(_IMG_TAG) \
		--set image.pullPolicy=Never \
		--wait --timeout 2m
