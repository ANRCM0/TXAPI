GO ?= go
PYTHON ?= python3
CONFIG ?= config.yaml

.PHONY: init prepare build run test vet check smoke contracts clean

prepare:
	mkdir -p bin .local/data

init: prepare
	@test -f config.yaml || cp configs/gateway.example.yaml config.yaml

build: prepare
	$(GO) build -o bin/gateway ./cmd/gateway

run: prepare
	$(GO) run ./cmd/gateway -config "$(CONFIG)"

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

check:
	$(GO) build ./...
	$(GO) vet ./...
	$(GO) test ./...

contracts:
	$(GO) test -count=1 ./internal/architecture
	$(GO) test -count=1 ./internal/server -run TestOpenAPI

smoke:
	$(PYTHON) tests/smoke/local.py --go "$(GO)"

# Local config and databases are deliberately preserved.
clean:
	rm -rf bin
