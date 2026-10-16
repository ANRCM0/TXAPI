GO ?= go
PYTHON ?= python3
NODE ?= node
CONFIG ?= config.yaml

.PHONY: init prepare build run test vet check smoke delivery contracts web-check clean

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
	$(MAKE) web-check

web-check:
	$(NODE) --check web/assets/app.mjs
	$(NODE) --check web/assets/api.mjs
	$(NODE) --check tests/browser/navigation.cjs
	$(NODE) --test web/assets/api.test.mjs

contracts:
	$(GO) test -count=1 ./internal/architecture
	$(GO) test -count=1 ./internal/server -run TestOpenAPI

smoke:
	$(PYTHON) tests/smoke/local.py --go "$(GO)"

delivery:
	$(PYTHON) tests/smoke/delivery.py --go "$(GO)"

# Local config and databases are deliberately preserved.
clean:
	rm -rf bin
