GO ?= go
BUF ?= buf
CONTROLLER_GEN ?= controller-gen
COVERAGE_OUT ?= coverage.out

.PHONY: all build test test-bdd lint proto generate tools integration e2e fault perf upgrade coverage coverage-check csi-sanity phase0

all: build

build:
	$(GO) build ./...

test:
	$(GO) test -tags=unit ./...

test-bdd:
	$(GO) test -v ./test/bdd

lint:
	$(GO) vet ./...
	golangci-lint run

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
	$(GO) test -tags=unit -coverprofile=$(COVERAGE_OUT) ./...
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
