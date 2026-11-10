GO ?= go
BUF ?= buf
NPM ?= npm
UI_DIR ?= web
BIN_DIR ?= bin
CONTROLLER_GEN ?= controller-gen
COVERAGE_OUT ?= coverage.out
VERSION ?= $(shell git describe --tags --always --dirty)
PROVIDER_PACKAGES ?= ./cmd/terraform-provider-kouja/... ./internal/terraformprovider/...
CMD_PACKAGES ?= $(shell $(GO) list ./cmd/...)
RELEASE_DIR ?= dist
RELEASE_NAME ?= kouja-$(VERSION)-linux-amd64
RELEASE_STAGE ?= $(RELEASE_DIR)/$(RELEASE_NAME)
RELEASE_ARCHIVE ?= $(RELEASE_DIR)/$(RELEASE_NAME).tar.gz
PROVIDER_VERSION ?= $(patsubst v%,%,$(VERSION))
PROVIDER_RELEASE_NAME ?= terraform-provider-kouja_$(PROVIDER_VERSION)_linux_amd64
# The Terraform provider is a separate deliverable with its own test target.
COVERAGE_PACKAGES ?= $(shell $(GO) list ./internal/... | grep -v '/internal/terraformprovider$$')
COVERAGE_FLOOR ?= 80
# Runtime files such as bin/clients may live under the repository root. Go's
# ./... wildcard walks those directories too, so local build and test commands
# use explicit source package roots instead.
GO_PACKAGES ?= ./api/... ./cmd/... ./gen/... ./hack/... ./internal/... ./test/...

.PHONY: all binaries release build build-go ui clean-assets test test-bdd lint proto generate tools integration e2e fault perf upgrade coverage coverage-check csi-sanity phase0 build-provider test-provider provider-acceptance

# Release layout: build the embedded UI and all command binaries in bin/.
all: binaries

binaries: build
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/kouja-agent ./cmd/kouja-agent
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/kouja-backup ./cmd/kouja-backup
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/kouja-csi-controller ./cmd/kouja-csi-controller
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/kouja-csi-node ./cmd/kouja-csi-node
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/kouja-restore ./cmd/kouja-restore
	install -m 0644 deploy/config.example.yaml $(BIN_DIR)/config.example.yaml
	install -m 0755 deploy/kouja-init.sh $(BIN_DIR)/kouja-init.sh

build: ui
	$(GO) build $(GO_PACKAGES)

# Build the linux/amd64 release archive published by the release workflow.
# Every command under ./cmd is compiled statically; the provider gets its
# version in the release filename and ldflags.
release: ui
	rm -rf "$(RELEASE_STAGE)" "$(RELEASE_ARCHIVE)" "$(RELEASE_ARCHIVE).sha256"
	mkdir -p "$(RELEASE_STAGE)" "$(RELEASE_STAGE)/deploy/install" "$(RELEASE_STAGE)/deploy/systemd" "$(RELEASE_STAGE)/docs"
	@for package in $(CMD_PACKAGES); do \
		name=$$(basename "$$package"); \
		output="$(RELEASE_STAGE)/$$name"; \
		ldflags="-s -w"; \
		if [ "$$name" = "kouja-agent" ]; then \
			ldflags="$$ldflags -X main.agentVersion=$(VERSION)"; \
		elif [ "$$name" = "terraform-provider-kouja" ]; then \
			output="$(RELEASE_STAGE)/$(PROVIDER_RELEASE_NAME)"; \
			ldflags="$$ldflags -X main.version=$(PROVIDER_VERSION)"; \
		fi; \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$$ldflags" -o "$$output" "$$package"; \
	done
	install -m 0644 deploy/config.example.yaml "$(RELEASE_STAGE)/config.example.yaml"
	install -m 0755 deploy/kouja-init.sh "$(RELEASE_STAGE)/kouja-init.sh"
	install -m 0755 deploy/install/install.sh "$(RELEASE_STAGE)/deploy/install/install.sh"
	install -m 0644 deploy/systemd/* "$(RELEASE_STAGE)/deploy/systemd/"
	install -m 0644 README.md LICENSE "$(RELEASE_STAGE)/"
	install -m 0644 docs/*.md "$(RELEASE_STAGE)/docs/"
	cd "$(RELEASE_STAGE)" && sha256sum "$(PROVIDER_RELEASE_NAME)" > "$(PROVIDER_RELEASE_NAME).sha256"
	cd "$(RELEASE_STAGE)" && sha256sum kouja-agent kouja-backup kouja-csi-controller kouja-csi-node kouja-restore "$(PROVIDER_RELEASE_NAME)" "$(PROVIDER_RELEASE_NAME).sha256" config.example.yaml kouja-init.sh README.md LICENSE > SHA256SUMS
	tar -C "$(RELEASE_DIR)" -czf "$(RELEASE_ARCHIVE)" "$(RELEASE_NAME)"
	cd "$(RELEASE_DIR)" && sha256sum "$(RELEASE_NAME).tar.gz" > "$(RELEASE_NAME).tar.gz.sha256"
	@printf 'created %s\n' "$(RELEASE_ARCHIVE)"

# Node-free build. Clean generated assets first so a previous full build does
# not accidentally embed the browser bundle.
build-go: clean-assets
	$(GO) build $(GO_PACKAGES)

ui: clean-assets
	cd $(UI_DIR) && $(NPM) ci
	cd $(UI_DIR) && $(NPM) run build
	test -f internal/adminui/assets/index.html

clean-assets:
	find internal/adminui/assets -mindepth 1 -maxdepth 1 \
		! -name .gitkeep ! -name .gitignore -exec rm -rf {} +

test:
	$(GO) test -tags=unit $(GO_PACKAGES)

test-bdd:
	$(GO) test -v ./test/bdd

lint:
	bash -n deploy/kouja-init.sh deploy/install/install.sh
	$(GO) vet $(GO_PACKAGES)
	golangci-lint run $(GO_PACKAGES)

proto:
	$(BUF) lint
	$(BUF) generate

generate:
	$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths=./api/...
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:dir=deploy/kubernetes
	mv deploy/kubernetes/kouja.io_koujavolumeplacements.yaml deploy/kubernetes/crd-koujavolumeplacement.yaml

tools:
	$(GO) install github.com/bufbuild/buf/cmd/buf@v1.47.0
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.0
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
	$(GO) install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.24.0
	$(GO) install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@v2.24.0
	$(GO) install ./hack/protoc-gen-kouja-client
	$(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

integration:
	$(GO) test -tags=integration ./test/integration/...

e2e:
	$(GO) test -tags=e2e ./test/e2e/...

fault:
	$(GO) test -tags=fault ./test/fault/...

perf:
	$(GO) test -tags=perf ./test/perf/...

upgrade:
	$(GO) test -tags=upgrade ./test/upgrade/...

coverage:
	$(GO) test -tags=unit -coverprofile=$(COVERAGE_OUT) $(GO_PACKAGES)
	$(GO) tool cover -func=$(COVERAGE_OUT)

coverage-check:
	$(GO) test -tags=unit -coverprofile=$(COVERAGE_OUT) $(COVERAGE_PACKAGES)
	@coverage=$$($(GO) tool cover -func=$(COVERAGE_OUT) | awk '/^total:/ {gsub("%", "", $$3); print $$3}'); \
	echo "internal coverage: $$coverage% (floor $(COVERAGE_FLOOR)%, provider excluded)"; \
	awk -v coverage="$$coverage" 'BEGIN { exit (coverage < $(COVERAGE_FLOOR)) }'

csi-sanity:
	$(GO) test -tags=csi_sanity ./test/csi/...

phase0:
	./hack/phase0-probe.sh

build-provider:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BIN_DIR)/terraform-provider-kouja ./cmd/terraform-provider-kouja

test-provider:
	$(GO) test -tags=unit $(PROVIDER_PACKAGES)

provider-acceptance:
	TF_ACC=1 $(GO) test -tags=acceptance ./internal/terraformprovider/...
