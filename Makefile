GO ?= go
BUF ?= buf
NPM ?= npm
UI_DIR ?= web
BIN_DIR ?= bin
CONTROLLER_GEN ?= controller-gen
COVERAGE_OUT ?= coverage.out
# Runtime files such as bin/clients may live under the repository root. Go's
# ./... wildcard walks those directories too, so local build and test commands
# use explicit source package roots instead.
GO_PACKAGES ?= ./api/... ./cmd/... ./gen/... ./hack/... ./internal/... ./test/...

.PHONY: all binaries build build-go ui clean-assets test test-bdd lint proto generate tools integration e2e fault perf upgrade coverage coverage-check csi-sanity phase0

# Release layout: build the embedded UI and all command binaries in bin/.
all: binaries

binaries: build
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/kouja-agent ./cmd/kouja-agent
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/kouja-csi-controller ./cmd/kouja-csi-controller
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/kouja-csi-node ./cmd/kouja-csi-node
	install -m 0644 deploy/config.example.yaml $(BIN_DIR)/config.example.yaml
	install -m 0755 deploy/kouja-init.sh $(BIN_DIR)/kouja-init.sh

build: ui
	$(GO) build $(GO_PACKAGES)

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
	$(GO) test -tags=unit -coverprofile=$(COVERAGE_OUT) ./internal/...
	@coverage=$$($(GO) tool cover -func=$(COVERAGE_OUT) | awk '/^total:/ {gsub("%", "", $$3); print $$3}'); \
	echo "internal coverage: $$coverage%"; \
	awk -v coverage="$$coverage" 'BEGIN { exit (coverage < 80) }'

csi-sanity:
	$(GO) test -tags=csi_sanity ./test/csi/...

phase0:
	./hack/phase0-probe.sh
