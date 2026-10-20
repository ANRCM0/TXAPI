GO ?= go
PYTHON ?= python3
NODE ?= node
CONFIG ?= config.yaml

.PHONY: init prepare build run test vet check smoke delivery compatibility sdk-conformance ws-sdk-conformance codex-conformance contracts web-check clean

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
	$(NODE) --check tests/browser/billing.cjs
	$(NODE) --check tests/browser/live.cjs
	$(NODE) --test web/assets/api.test.mjs

contracts:
	$(GO) test -count=1 ./internal/architecture
	$(GO) test -count=1 ./internal/server -run TestOpenAPI

smoke:
	$(PYTHON) tests/smoke/local.py --go "$(GO)"

delivery:
	$(PYTHON) tests/smoke/delivery.py --go "$(GO)"

# Install tests/smoke/requirements-sdk.txt in an isolated environment first.
sdk-conformance:
	$(PYTHON) tests/smoke/sdk_conformance.py --go "$(GO)"

# Install requirements-ws-sdk.txt in a separate environment from the HTTP SDK.
ws-sdk-conformance:
	$(PYTHON) tests/smoke/ws_sdk_conformance.py --go "$(GO)"

# Supply an official Codex 0.163.0 binary; CI checks the release archive digest.
codex-conformance:
	$(PYTHON) tests/smoke/codex_conformance.py --go "$(GO)" --codex "$(CODEX)"
	$(PYTHON) tests/smoke/codex_ws_conformance.py --go "$(GO)" --codex "$(CODEX)"

# Requires Playwright and a local Chromium executable, like the browser checks.
compatibility:
	$(PYTHON) tests/smoke/compatibility.py --go "$(GO)" --node "$(NODE)"

# Local config and databases are deliberately preserved.
clean:
	rm -rf bin
