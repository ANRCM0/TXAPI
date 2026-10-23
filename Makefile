GO ?= go
PYTHON ?= python3
NODE ?= node
CONFIG ?= config.yaml

.PHONY: init prepare build run test vet check smoke delivery p0-linux strict-native compatibility sdk-conformance ws-sdk-conformance codex-conformance contracts web-check browser-check clean

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
	$(NODE) --check web/assets/workspace.mjs
	$(NODE) --test web/assets/api.test.mjs web/assets/workspace.test.mjs

# Playwright 1.62.1 and Chromium must be installed outside the production tree.
browser-check: web-check
	$(NODE) tests/browser/navigation.cjs
	$(NODE) tests/browser/billing.cjs
	$(NODE) tests/browser/tasks.cjs
	$(NODE) tests/browser/routing.cjs
	$(NODE) tests/browser/workspace.cjs
	$(MAKE) compatibility

contracts:
	$(GO) test -count=1 ./internal/architecture
	$(GO) test -count=1 ./internal/server -run TestOpenAPI

smoke:
	$(PYTHON) tests/smoke/local.py --go "$(GO)"

delivery:
	$(PYTHON) tests/smoke/delivery.py --go "$(GO)"

# Linux Docker only. P0_OUTPUT must name a new report directory.
P0_OUTPUT ?= .local/p0-linux
p0-linux:
	$(PYTHON) tests/smoke/capacity_baseline_test.py
	$(PYTHON) tests/smoke/capacity_curve_test.py
	$(PYTHON) tests/smoke/docker_resources_test.py
	$(PYTHON) tests/smoke/capacity_curve.py --go "$(GO)" --levels 1,4,8,16 --sustain-seconds 60 --output "$(P0_OUTPUT)/capacity-curve.json"
	$(PYTHON) tests/smoke/native_ws_delivery.py --go "$(GO)" --soak-seconds 900 --soak-workers 2 --output "$(P0_OUTPUT)/docker-protocol-soak.json"

# Linux Docker only; includes a real lease wait after KILL.
STRICT_NATIVE_OUTPUT ?= .local/strict-native/report.json
strict-native:
	$(PYTHON) tests/smoke/strict_native_delivery_test.py
	$(PYTHON) tests/smoke/strict_native_delivery.py --go "$(GO)" --output "$(STRICT_NATIVE_OUTPUT)"

# Install tests/smoke/requirements-sdk.txt in an isolated environment first.
sdk-conformance:
	$(PYTHON) tests/smoke/sdk_conformance.py --go "$(GO)"
	$(PYTHON) tests/smoke/background_sdk_conformance.py --go "$(GO)"
	$(PYTHON) tests/smoke/managed_function_sdk_conformance.py --go "$(GO)"

# Install requirements-ws-sdk.txt in a separate environment from the HTTP SDK.
ws-sdk-conformance:
	$(PYTHON) tests/smoke/ws_sdk_conformance.py --go "$(GO)"

# Supply an official Codex 0.163.0 binary; CI checks the release archive digest.
codex-conformance:
	$(PYTHON) tests/smoke/codex_conformance.py --go "$(GO)" --codex "$(CODEX)"
	$(PYTHON) tests/smoke/codex_ws_conformance.py --go "$(GO)" --codex "$(CODEX)"
	$(PYTHON) tests/smoke/codex_standard_conformance.py --go "$(GO)" --codex "$(CODEX)"
	$(PYTHON) tests/smoke/codex_compaction_conformance.py --go "$(GO)" --codex "$(CODEX)"

# Requires Playwright and a local Chromium executable, like the browser checks.
compatibility:
	$(PYTHON) tests/smoke/compatibility.py --go "$(GO)" --node "$(NODE)"

# Local config and databases are deliberately preserved.
clean:
	rm -rf bin
