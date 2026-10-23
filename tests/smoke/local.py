"""Check the local OpenAI path with isolated binaries, ports, and database.

Uses only Python's standard library. Does not read config.yaml or call a real
provider. Cross-provider compatibility is outside this smoke check's scope.
Strict execution uses the production main in a Go test binary with a
loopback-only network fixture; the production binary separately checks refusal.
"""

import argparse
import base64
import contextlib
import hashlib
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

from background import background_workflow
from usage_history import usage_history_workflow
from shadow_routing import shadow_routing_workflow


ROOT = Path(__file__).resolve().parents[2]
HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def ports():
    with contextlib.ExitStack() as stack:
        sockets = [stack.enter_context(socket.socket()) for _ in range(2)]
        for sock in sockets:
            sock.bind(("127.0.0.1", 0))
        return [sock.getsockname()[1] for sock in sockets]


def request(url, key=None, payload=None, method=None):
    headers = {}
    if key:
        headers["Authorization"] = "Bearer " + key
    data = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(payload).encode()
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with HTTP.open(req, timeout=10) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        with error:
            return error.code, error.read()


def ready(process, port):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"local service exited with {process.returncode}")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return
        except OSError:
            time.sleep(0.05)
    raise TimeoutError(f"local service did not start on port {port}")


@contextlib.contextmanager
def service(command, log_path, env=None):
    with log_path.open("w+", encoding="utf-8", errors="replace") as log:
        process = subprocess.Popen(command, cwd=ROOT, stdout=log, stderr=log, env=env)
        try:
            yield process
        except BaseException:
            log.flush()
            log.seek(0)
            print(log.read()[-6000:])
            raise
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)


def control_workflow(base, upstream_url, routing_env=None, quotation_enabled=False):
    """Exercise the actual console APIs without using the legacy admin key."""
    assert request(base + "/v1/responses")[0] == 404, "Disabled native WS exposed an upgrade route"
    def api(path, session=None, payload=None, method=None, expected=200):
        status, raw = request(base + path, session, payload, method)
        assert status == expected, (path, status, raw)
        return json.loads(raw) if raw else {}

    with HTTP.open(base + "/", timeout=10) as response:
        assert "frame-ancestors 'none'" in response.headers["Content-Security-Policy"]
        assert "<title>TXAPI · 工作空间</title>".encode() in response.read()
    for asset in ["app.mjs", "api.mjs", "workspace.mjs", "app.css"]:
        status, _ = request(base + "/ui/" + asset)
        assert status == 200
    assert request(base + "/ui/api.test.mjs")[0] == 404
    assert request(base + "/ui/workspace.test.mjs")[0] == 404
    admin = api("/api/auth/login", payload={
        "username": "smoke-admin", "password": "smoke-admin-password-only"
    })["session_token"]
    for path, method in [
        ("/api/admin/managed-channels", "POST"),
        ("/api/admin/managed-credentials/1", "GET"),
        ("/api/admin/managed-credentials/1", "PUT"),
        ("/api/admin/managed-credentials/1/disable", "POST"),
        ("/api/admin/managed-credentials/1/eligibility", "GET"),
        ("/api/admin/managed-credentials/1/eligibility/resume", "POST"),
    ]:
        unavailable = api(path, admin, {} if method != "GET" else None, method, expected=503)
        assert unavailable["error"]["type"] == "managed_inference_unavailable", unavailable
    current_quote = api("/api/admin/quotations/current", admin)["data"]
    assert current_quote["enabled"] == quotation_enabled
    version = current_quote["version"] or "0" * 64
    quote_input = {
        "price_version": version, "model": "mapped-model", "group": "default",
        "usage": {"input_tokens": "10", "output_tokens": "6", "total_tokens": "16",
                  "cached_input_tokens": "3", "cache_write_input_tokens": "2",
                  "reasoning_output_tokens": "4", "completeness": "complete",
                  "source": "provider_reported", "profile": "responses_native_tokens_v2"},
    }
    if quotation_enabled:
        assert current_quote["currency"] == "ACCOUNT_UNIT"
        assert len(version) == 64 and current_quote["unit_scale"] == "1000000000"
        quote = api("/api/admin/quotations/calculate", admin, quote_input)["data"]
        assert quote["known"] and quote["amount_units"] == "28" and quote["price_version"] == version
        unknown_quote = json.loads(json.dumps(quote_input))
        unknown_quote["usage"]["cached_input_tokens"] = None
        unknown = api("/api/admin/quotations/calculate", admin, unknown_quote)["data"]
        assert not unknown["known"] and unknown["amount_units"] == "" and unknown["reason"] == "usage_unknown"
    else:
        assert all(value == "" for name, value in current_quote.items() if name != "enabled")
        api("/api/admin/quotations/calculate", admin, quote_input, expected=503)
    initial = api("/api/admin/config-status", admin)["data"]
    for name in ["console-user", "console-other"]:
        api("/api/admin/users", admin, {
            "username": name, "password": name + "-test-password", "role": "user",
            "model_mode": "allowlist", "models": ["console-model"],
        }, expected=201)
    channel = api("/api/admin/channels", admin, {
        "name": "console-upstream", "type": "openai",
        "base_url": upstream_url, "api_key": "test-upstream-console-only",
        "models": ["console-model"], "model_mapping": {"console-model": "mapped-model"},
    }, expected=201)["data"]
    after = api("/api/admin/config-status", admin)["data"]
    assert after["epoch"] == initial["epoch"]
    assert after["channels"]["desired_revision"] > initial["channels"]["desired_revision"]
    assert after["channels"]["applied_revision"] == after["channels"]["desired_revision"]
    session = api("/api/auth/login", payload={
        "username": "console-user", "password": "console-user-test-password"
    })["session_token"]
    other = api("/api/auth/login", payload={
        "username": "console-other", "password": "console-other-test-password"
    })["session_token"]
    for owner_session in (session, other):
        summary = api("/api/me/billing/summary", owner_session)["data"]
        assert summary["unit_scale"] == "1000000000" and summary["balance_known"] is False
        assert summary["posted_used_units"] is None and summary["held_units"] is None
        assert summary["pending_count"] == "0"
        assert api("/api/me/billing/reservations", owner_session) == {"data": [], "has_more": False}
    api("/api/me/billing/summary", expected=401)
    api("/api/me/billing/reservations?user_id=1", session, expected=400)
    api("/api/me/billing/reservations?limit=01", session, expected=400)
    api("/api/me/billing/reservations/" + "a" * 32, session, expected=404)
    api("/api/admin/config-status", session, expected=403)
    api("/api/admin/quotations/current", session, expected=403)
    # Reject before parsing. urllib closes each connection; an unread POST body
    # can reset that connection on Windows before the early 403 is received.
    api("/api/admin/quotations/calculate", session, method="POST", expected=403)
    assert api("/api/me/models", session)["data"] == ["console-model"]
    key = api("/api/me/keys", session, {
        "name": "console-client", "model_mode": "allowlist", "models": ["console-model"],
    }, expected=201)["data"]
    assert "key" not in api("/api/me/keys", session)["data"][0]
    api(f"/api/me/keys/{key['id']}", other, expected=404)
    api("/v1/chat/completions", key["key"], {
        "model": "console-model", "messages": [{"role": "user", "content": "hello"}],
    })
    deadline = time.monotonic() + 5
    while api("/api/me/usage", session)["data"]["usage"]["total_requests"] != 1:
        assert time.monotonic() < deadline, "owner usage did not settle"
        time.sleep(0.05)
    assert api("/api/me/logs", other)["data"] in [None, []]
    own_logs = api("/api/me/logs", session)["data"]
    assert len(own_logs[0]["price_version"]) == 64, "Request lost its immutable price version"
    if routing_env is None:
        diagnostics = api("/api/me/routing", session)
        assert diagnostics["enabled"] is False and diagnostics["data"] == [], "Default recording was enabled"
    else:
        request_id = own_logs[0]["request_id"]
        deadline = time.monotonic() + 5
        while True:
            diagnostics = api("/api/me/routing?request_id=" + request_id, session)
            if diagnostics["data"]:
                break
            assert time.monotonic() < deadline, "Completed HTTP scope was not observed"
            time.sleep(0.1)
        assert diagnostics["enabled"] and diagnostics["capacity"] == 512 and diagnostics["retention_seconds"] == 900
        assert len(diagnostics["data"]) == 1
        trace = diagnostics["data"][0]
        assert trace["token_id"] == key["id"] and trace["status"] == 200 and len(trace["attempts"]) == 1
        assert "channel_id" not in trace["selections"][0] and "credential_id" not in trace["attempts"][0]
        assert "actual_model" not in trace["attempts"][0]
        api("/api/me/routing/" + trace["id"], other, expected=404)
        detail = api("/api/admin/routing/" + trace["id"], admin)["data"]
        assert detail["attempts"][0]["channel_id"] == channel["id"] and detail["attempts"][0]["actual_model"] == "mapped-model"
        for private in ("test-upstream-console-only", "Authorization", upstream_url):
            assert private not in json.dumps(detail), "Routing observation exposed private source material"
        if routing_env.get("TXAPI_ROUTING_BROWSER"):
            browser_env = dict(routing_env, TXAPI_BASE_URL=base, TXAPI_ROUTING_TRACE_ID=trace["id"], TXAPI_ROUTING_REQUEST_ID=request_id)
            subprocess.run([routing_env.get("TXAPI_ROUTING_BROWSER_NODE", "node"), str(ROOT / "tests" / "browser" / "routing-live.cjs")],
                           env=browser_env, check=True, timeout=90)
    # Credential metadata edits preserve the existing upstream secret.
    credential = channel["credentials"][0]
    api(f"/api/admin/credentials/{credential['id']}", admin,
        {"name": "renamed", "weight": 2}, method="PUT")
    api("/api/admin/config-status/apply", admin, method="POST")
    exposition = request(base + "/metrics")[1].decode()
    assert "gateway_attempts_total" in exposition
    for name in ("gateway_admission_requests_active", "gateway_admission_attempts_active",
                 "gateway_native_ws_enabled", "gateway_native_ws_connections_active",
                 "gateway_native_ws_rounds_active"):
        assert f"{name} 0\n" in exposition, (name, exposition)
    api("/api/auth/logout", session, method="POST", expected=204)
    api("/api/me", session, expected=401)
    return key


def strict_crash_workflow(gateway, config, config_path, work, env, gateway_port, upstream_url, admin):
    """Kill the actual gateway during SSE; recovery must hold uncertain cost."""
    base = f"http://127.0.0.1:{gateway_port}"

    def api(path, payload=None, method=None, expected=200):
        status, body = request(base + path, admin, payload, method)
        assert status == expected, (path, status, body)
        return json.loads(body)

    # A short request budget gives this isolated test a real six-second lease.
    config["server"].update(timeout_seconds=1, total_timeout_seconds=1)
    config_path.write_text(json.dumps(config))
    with service([str(gateway), "-config", str(config_path)], work / "strict-setup.log", env) as process:
        ready(process, gateway_port)
        channel = api("/api/admin/channels", {
            "name": "crash-fixture", "type": "openai", "base_url": "https://api.openai.com",
            "models": ["crash-model"], "model_mapping": {"crash-model": "gpt-4o-mini-2024-07-18"},
            "api_key": "sk-smoke-slow-upstream-only",
        }, "POST", 201)["data"]
        api("/api/admin/prices/crash-model", {"input_price_per_1k": 0.15, "output_price_per_1k": 0.6}, "PUT")
        key = api("/api/me/keys", {"name": "crash-client", "model_mode": "allowlist", "models": ["crash-model"]}, "POST", 201)["data"]
    config["billing"]["provider_contracts"].append(verified_provider_contract(channel, "sk-smoke-slow-upstream-only"))
    config_path.write_text(json.dumps(config))
    with service([str(gateway), "-config", str(config_path)], work / "strict-killed.log", env) as process:
        ready(process, gateway_port)
        payload = {"model": "crash-model", "max_completion_tokens": 32, "service_tier": "default", "stream": True,
                   "messages": [{"role": "user", "content": "hi"}], "stream_options": {"include_usage": True}}
        stream_request = urllib.request.Request(base + "/v1/chat/completions", data=json.dumps(payload).encode(),
                                                headers={"Authorization": "Bearer " + key["key"], "Content-Type": "application/json"})
        with HTTP.open(stream_request, timeout=10) as response:
            billing_id = response.headers["X-TXAPI-Billing-ID"]
            assert response.readline().startswith(b"data:"), "No upstream stream execution"
            process.kill()  # Not SIGTERM: graceful shutdown must not settle this case.
            assert process.wait(timeout=5) != 0
    with contextlib.closing(sqlite3.connect((work / "gateway.db").as_uri() + "?mode=ro", uri=True)) as db:
        state, lease = db.execute("SELECT state,lease_until FROM billing_reservations WHERE id=?", (billing_id,)).fetchone()
        assert state == "active", state
        assert db.execute("SELECT reserved_units FROM billing_balances WHERE owner_type='key' AND owner_id=?", (key["id"],)).fetchone() == (20_000_000_000,)
    time.sleep(max(0, lease / 1000 - time.time()) + 0.05)
    with service([str(gateway), "-config", str(config_path)], work / "strict-recovered.log", env) as process:
        ready(process, gateway_port)
        path = "/api/admin/billing/reservations/" + billing_id
        deadline = time.monotonic() + 5
        while True:
            detail = api(path)["data"]
            if detail["reservation"]["state"] == "pending":
                break
            assert time.monotonic() < deadline, detail
            time.sleep(0.05)
        assert detail["reservation"]["can_resolve"] and detail["reservation"]["reserved_units"] == "20000000000"
        assert not detail["attempts"][0]["cost_known"] and detail["resolution"] is None
        assert detail["reservation"]["provider_held_units"] == "19219200"
        assert len(detail["attempts"][0]["provider_contract_version"]) == 64
        cost_snapshot = json.loads(detail["attempts"][0]["provider_contract_snapshot"])
        assert cost_snapshot["contract"]["input_bound_profile"] == "openai_chat_v1"
        assert cost_snapshot["format"] == 2 and cost_snapshot["selling_bound_units"] == "19219200000"
        assert cost_snapshot["selling_price_version"] == detail["reservation"]["price_version"]
        owner_path = "/api/me/billing/reservations/" + billing_id
        owner_detail = api(owner_path)["data"]
        assert owner_detail["reservation"]["state"] == "pending"
        assert owner_detail["reservation"]["held_units"] == "20000000000"
        assert owner_detail["reservation"]["charge_units"] is None
        for private in ("provider_", "reason", "evidence", "actor_id", "attempts", "credential_id"):
            assert private not in json.dumps(owner_detail), (private, owner_detail)
        owner_before = api("/api/me/billing/summary")["data"]
        assert owner_before["balance_known"] and owner_before["pending_count"] == "1"
        assert int(owner_before["held_units"]) >= 20000000000
        decision = {"action": "waive", "reason": "test operator accepts uncertain upstream cost",
                    "evidence": "isolated forced-kill fixture; no free-execution assumption"}
        for _ in range(2):
            result = api(path + "/resolve", decision, "POST")["data"]
            assert result["charge_units"] == "0" and not result["cost_known"], result
        detail = api(path)["data"]
        assert detail["reservation"]["state"] == "settled" and detail["resolution"]["action"] == "waive"
        assert detail["resolution"]["actor_id"] == 1
        assert detail["reservation"]["provider_held_units"] == "19219200"
        owner_after = api(owner_path)["data"]
        assert owner_after["reservation"]["state"] == "settled"
        assert owner_after["reservation"]["held_units"] == "0"
        assert owner_after["reservation"]["charge_units"] == "0"
        assert owner_after["reservation"]["original_reserved_units"] == "20000000000"
        assert owner_after["resolution_action"] == "waive"
        owner_summary = api("/api/me/billing/summary")["data"]
        assert owner_summary["pending_count"] == "0" and owner_summary["posted_used_units"] == owner_before["posted_used_units"]
        assert int(owner_summary["held_units"]) == int(owner_before["held_units"]) - 20000000000
    with contextlib.closing(sqlite3.connect((work / "gateway.db").as_uri() + "?mode=ro", uri=True)) as db:
        assert db.execute("SELECT COUNT(*) FROM billing_resolutions WHERE reservation_id=?", (billing_id,)).fetchone() == (1,)
        assert db.execute("SELECT used_units,reserved_units FROM billing_balances WHERE owner_type='key' AND owner_id=?", (key["id"],)).fetchone() == (0, 0)
    print("PASS: production main with loopback test transport, SSE forced kill, restart pending hold, audited idempotent waiver", flush=True)


def provider_contract(channel_id, upstream_url, model):
    return {
        "channel_id": channel_id, "base_url": upstream_url, "protocol": "openai", "actual_model": model,
        "operation": "chat", "output_limit_field": "max_tokens", "input_token_ceiling": 512,
        "max_output_tokens": 32, "input_price_per_1k": "0.01", "output_price_per_1k": "0.02",
        "evidence": "isolated smoke fake upstream",
    }


def verified_provider_contract(channel, key):
    return {
        "channel_id": channel["id"], "base_url": "https://api.openai.com", "protocol": "openai",
        "actual_model": "gpt-4o-mini-2024-07-18", "operation": "chat",
        "output_limit_field": "max_completion_tokens", "input_token_ceiling": 128000,
        "input_bound_profile": "openai_chat_v1", "max_output_tokens": 32,
        "input_price_per_1k": "0.00015", "output_price_per_1k": "0.0006",
        "tariff_profile": "openai_standard_usd_2026_10_17", "currency": "USD",
        "credential_bindings": {str(channel["credentials"][0]["id"]): hashlib.sha256(key.encode()).hexdigest()},
        "evidence": "isolated smoke tariff; test-only loopback transport, no real provider request",
    }


def generation_runtime_workflow(gateway, config, config_path, work, env, gateway_port):
    """Boot/restart the actual opt-in runtime and reject missing/wrong keys."""
    credential_keys, content_keys = work / "credential-keys.json", work / "content-keys.json"
    for path in (credential_keys, content_keys):
        path.write_text(json.dumps({
            "active_key": "smoke-1",
            "keys": {"smoke-1": base64.b64encode(os.urandom(32)).decode("ascii")},
            "digest_key": base64.b64encode(os.urandom(32)).decode("ascii"),
        }))
        path.chmod(0o600)
    config["database"]["credential_keys_file"] = str(credential_keys)
    config["generation"] = {"enabled": True, "keys_file": str(content_keys),
                            "workers": 1, "lease_seconds": 1}
    config_path.write_text(json.dumps(config))
    for iteration in range(2):
        with service([str(gateway), "-config", str(config_path)],
                     work / f"generation-runtime-{iteration}.log", env) as process:
            ready(process, gateway_port)
            assert request(f"http://127.0.0.1:{gateway_port}/healthz")[0] == 200
            process.terminate()
            process.wait(timeout=12)
            if os.name != "nt":
                assert process.returncode == 0, "SIGTERM did not join the production runtime"
    with contextlib.closing(sqlite3.connect((work / "gateway.db").as_uri() + "?mode=ro", uri=True)) as db:
        assert db.execute("SELECT COUNT(*) FROM credentials WHERE api_key <> '' OR length(key_ciphertext) = 0 OR key_hash = ''").fetchone() == (0,), "Runtime did not require activated credential encryption"
        assert db.execute("SELECT value FROM settings WHERE key='credentials.encryption_version'").fetchone()[0].startswith("v1:")
        assert db.execute("SELECT COUNT(*) FROM generation_intents").fetchone() == (0,), "HTTP foreground calls became durable tasks"
    original = json.loads(credential_keys.read_text())
    invalid = dict(original, keys={"smoke-1": base64.b64encode(os.urandom(32)).decode("ascii")})
    credential_keys.write_text(json.dumps(invalid))
    for mode in ("wrong-credential-key", "missing-content-file"):
        if mode == "missing-content-file":
            credential_keys.write_text(json.dumps(original))
            config["generation"]["keys_file"] = str(work / "missing-content-keys.json")
            config_path.write_text(json.dumps(config))
        with (work / f"generation-{mode}.log").open("w", encoding="utf-8") as log:
            result = subprocess.run([str(gateway), "-config", str(config_path)], cwd=ROOT,
                                    env=env, stdout=log, stderr=log, timeout=20)
        assert result.returncode != 0, "Invalid secret configuration started the runtime"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable")
    parser.add_argument("--preview", action="store_true", help="Keep isolated services running after checks for manual console QA")
    args = parser.parse_args()
    # Never inherit a real deployment's database path, bind address or secrets.
    clean_env = {key: value for key, value in os.environ.items()
                 if not key.startswith(("GATEWAY_", "TXAPI_ADMIN_", "TXAPI_SMOKE_"))}
    with tempfile.TemporaryDirectory(prefix="gateway-smoke-") as directory:
        work = Path(directory)
        gateway, upstream = work / "gateway", work / "upstream"
        for output, package in [(gateway, "./cmd/gateway"), (upstream, "./tests/upstream/openai")]:
            subprocess.run([args.go, "build", "-o", str(output), package], cwd=ROOT, check=True, timeout=240)
        strict_gateway = work / "gateway-smoke"
        subprocess.run([args.go, "test", "-c", "-o", str(strict_gateway), "./cmd/gateway"],
                       cwd=ROOT, check=True, timeout=240)
        gateway_port, upstream_port = ports()
        key = "sk-local-smoke-only"
        # The management key is mandatory: config.Load refuses to start
        # without it, and this check asserts the binary really starts.
        admin_key = "sk-local-smoke-admin"
        config = {
            "server": {"addr": f"127.0.0.1:{gateway_port}", "retry_times": 1, "timeout_seconds": 10, "route_diagnostics": True},
            "admin_key": admin_key,
            "quotation": {"enabled": True, "currency": "ACCOUNT_UNIT", "prices": [{
                "model": "mapped-model", "uncached_input_units_per_1k": "1000",
                "cached_input_units_per_1k": "100", "cache_write_input_units_per_1k": "2000",
                "output_units_per_1k": "3000"}], "group_rate_units": {"default": "1000000000"}},
            "database": {"driver": "sqlite", "path": str(work / "gateway.db")},
            "channels": [{"name": "local", "type": "openai", "base_url": f"http://127.0.0.1:{upstream_port}",
                          "key": "sk-local-upstream-only", "models": ["smoke-model"],
                          "model_mapping": {"smoke-model": "mapped-model"}, "weight": 1}],
            "tokens": [{"name": "local", "key": key, "models": ["smoke-model"]}],
        }
        # JSON is a YAML subset; this avoids a separate Python YAML dependency.
        config_path = work / "config.json"
        config_path.write_text(json.dumps(config))
        env = dict(clean_env, TXAPI_ADMIN_USERNAME="smoke-admin", TXAPI_ADMIN_PASSWORD="smoke-admin-password-only")
        subprocess.run([str(gateway), "-config", str(config_path), "-init-admin"], env=env, cwd=ROOT, check=True, timeout=30)
        subprocess.run([str(gateway), "-config", str(config_path), "-seed-only"], env=clean_env, cwd=ROOT, check=True, timeout=30)
        subprocess.run([str(gateway), "-config", str(config_path), "-claim-token", "1", "-owner-user", "1"], env=clean_env, cwd=ROOT, check=True, timeout=30)
        with service([str(upstream), f"127.0.0.1:{upstream_port}"], work / "upstream.log", clean_env) as up:
            ready(up, upstream_port)
            base = f"http://127.0.0.1:{gateway_port}"
            mode_payload = {"model": "smoke-model", "messages": [{"role": "user", "content": "hi"}]}
            for mode, expected in [("strict", 503), ("disabled", 200)]:
                mode_env = dict(clean_env, GATEWAY_BILLING_MODE=mode)
                with service([str(gateway), "-config", str(config_path)], work / f"{mode}.log", mode_env) as mode_gw:
                    ready(mode_gw, gateway_port)
                    status, body = request(base + "/v1/chat/completions", key, mode_payload)
                    assert status == expected, (mode, status, body)
                    if mode == "strict":
                        assert json.loads(body)["error"]["type"] == "billing_unavailable", body
            with service([str(gateway), "-config", str(config_path)], work / "gateway.log", clean_env) as gw:
                ready(gw, gateway_port)
                base = f"http://127.0.0.1:{gateway_port}"
                status, body = request(base + "/healthz")
                assert status == 200 and json.loads(body)["status"] == "ok"
                status, _ = request(base + "/v1/models", "invalid-local-key")
                assert status == 401, status
                status, body = request(base + "/v1/models", key)
                assert status == 200
                assert [m["id"] for m in json.loads(body)["data"]] == ["smoke-model"]
                payload = {"model": "smoke-model", "messages": [{"role": "user", "content": "hi"}]}
                status, body = request(base + "/v1/chat/completions", key, payload)
                normal = json.loads(body)
                assert status == 200, body
                assert normal["model"] == "mapped-model", normal
                assert normal["choices"][0]["message"]["content"] == "这是假上游返回的内容，模型名是 mapped-model"
                assert normal["usage"]["total_tokens"] == 18
                payload.update(stream=True, stream_options={"include_usage": True})
                status, body = request(base + "/v1/chat/completions", key, payload)
                assert status == 200, body
                events = []
                for event in body.decode().replace("\r\n", "\n").split("\n\n"):
                    data = "\n".join(line[5:].lstrip() for line in event.splitlines() if line.startswith("data:"))
                    if data:
                        events.append(data)
                assert events and events[-1] == "[DONE]", events
                chunks = [json.loads(data) for data in events[:-1]]
                choices = [choice for chunk in chunks for choice in chunk.get("choices", [])]
                text = "".join(choice.get("delta", {}).get("content", "") for choice in choices)
                assert text == "你好，这是流式响应。", text
                assert any(choice.get("finish_reason") == "stop" for choice in choices)
                assert any(chunk.get("usage", {}).get("total_tokens") == 18 for chunk in chunks)
                control_workflow(base, f"http://127.0.0.1:{upstream_port}", clean_env, quotation_enabled=True)
                if args.preview:
                    print(f"PASS: all smoke checks; isolated console preview at {base}", flush=True)
                    print("Test login: smoke-admin / smoke-admin-password-only. Ctrl+C stops and removes test data.", flush=True)
                    try:
                        input("Press Enter to stop preview: ")
                    except KeyboardInterrupt:
                        pass
                    except EOFError:
                        print("Preview needs interactive stdin; isolated services stopped.", flush=True)
            # Exercise actual strict wiring after all legacy checks have stopped.
            # The permanent accounting-mode marker prevents switching back to legacy.
            with contextlib.closing(sqlite3.connect((work / "gateway.db").as_uri() + "?mode=ro", uri=True)) as db:
                channel_id = db.execute("SELECT id FROM channels WHERE name='local'").fetchone()[0]
            config["billing"] = {
                "mode": "strict", "max_request_cost": "0.10", "max_provider_request_cost": "0.10",
                "provider_contracts": [provider_contract(channel_id, f"http://127.0.0.1:{upstream_port}", "mapped-model")],
            }
            config_path.write_text(json.dumps(config))
            with service([str(gateway), "-config", str(config_path)], work / "strict-ledger.log", clean_env) as strict_gw:
                ready(strict_gw, gateway_port)
                strict_payload = {"model": "smoke-model", "max_tokens": 32,
                                  "messages": [{"role": "user", "content": "hello"}]}
                status, body = request(base + "/v1/chat/completions", key, strict_payload)
                assert status == 503, (status, body)  # Missing prices must fail closed.
                status, body = request(base + "/api/auth/login", payload={
                    "username": "smoke-admin", "password": "smoke-admin-password-only"})
                assert status == 200, body
                admin = json.loads(body)["session_token"]
                status, body = request(base + "/api/admin/prices/smoke-model", admin,
                                       {"input_price_per_1k": 0.15, "output_price_per_1k": 0.6}, "PUT")
                assert status == 200, body
                status, body = request(base + "/v1/chat/completions", key, strict_payload)
                assert status == 503 and json.loads(body)["error"]["type"] == "provider_input_unproven", (status, body)
                # Create the profiled fixture only after the production refusal check.
                status, body = request(base + "/api/admin/channels", admin, {
                    "name": "verified-smoke", "type": "openai", "base_url": "https://api.openai.com",
                    "api_key": "sk-verified-smoke-only", "models": ["smoke-model"],
                    "model_mapping": {"smoke-model": "gpt-4o-mini-2024-07-18"}, "priority": 100,
                }, "POST")
                assert status == 201, body
                verified_channel = json.loads(body)["data"]
            config["billing"]["max_request_cost"] = "20"
            config["billing"]["max_provider_request_cost"] = "1"
            profiled_contract = verified_provider_contract(verified_channel, "sk-verified-smoke-only")
            config["billing"]["provider_contracts"].append({
                field: value for field, value in profiled_contract.items()
                if field not in {"tariff_profile", "currency", "credential_bindings"}
            })
            config_path.write_text(json.dumps(config))
            with service([str(gateway), "-config", str(config_path)], work / "strict-no-tariff.log", clean_env) as strict_gw:
                ready(strict_gw, gateway_port)
                strict_payload = {"model": "smoke-model", "max_completion_tokens": 32, "service_tier": "default",
                                  "messages": [{"role": "user", "content": "hello"}]}
                status, body = request(base + "/v1/chat/completions", key, strict_payload)
                assert status == 503 and json.loads(body)["error"]["type"] == "provider_tariff_unproven", (status, body)
            config["billing"]["provider_contracts"][-1] = profiled_contract
            config_path.write_text(json.dumps(config))
            strict_env = dict(clean_env, TXAPI_SMOKE_MAIN="1", TXAPI_SMOKE_UPSTREAM=f"127.0.0.1:{upstream_port}")
            with service([str(strict_gateway), "-config", str(config_path)], work / "strict-profile.log", strict_env) as strict_gw:
                ready(strict_gw, gateway_port)
                strict_payload = {"model": "smoke-model", "max_completion_tokens": 32, "service_tier": "default",
                                  "messages": [{"role": "user", "content": "hello"}]}
                for stream in [False, True]:
                    strict_payload.update(stream=stream, stream_options={"include_usage": True})
                    status, body = request(base + "/v1/chat/completions", key, strict_payload)
                    assert status == 200, (status, body)
                deadline = time.monotonic() + 5
                while True:
                    status, body = request(base + "/api/me/logs", admin)
                    assert status == 200, body
                    billed = [row for row in json.loads(body)["data"] if row["billing_id"] and row["cost"] > 0]
                    if len(billed) == 2:
                        break
                    assert time.monotonic() < deadline, "Strict request observations did not appear"
                    time.sleep(0.05)
                assert all(row["billing_state"] == "settled" and abs(row["cost"] - 0.00585) < 1e-12 for row in billed), billed
                credential_id = verified_channel["credentials"][0]["id"]
                status, body = request(base + f"/api/admin/credentials/{credential_id}", admin,
                                       {"api_key": "sk-rotated-smoke-only", "weight": 1}, "PUT")
                assert status == 200, body
                status, body = request(base + "/v1/chat/completions", key, strict_payload)
                assert status == 503 and json.loads(body)["error"]["type"] == "provider_credential_unproven", (status, body)
            strict_crash_workflow(strict_gateway, config, config_path, work, strict_env, gateway_port,
                                  f"http://127.0.0.1:{upstream_port}", admin)
            generation_runtime_workflow(gateway, config, config_path, work, clean_env, gateway_port)
            background_workflow(gateway, work, clean_env, service, ready, request)
            usage_history_workflow(gateway, work, clean_env, service, ready, request)
            shadow_routing_workflow(gateway, work, clean_env, service, ready, request)
    print("PASS: production startup/auth/normal/SSE/console, strict missing-price/input/tariff rejection; isolated main settlement, credential rotation refusal/recovery, encrypted runtime restart/key rejection and shutdown")


if __name__ == "__main__":
    main()
