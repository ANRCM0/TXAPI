"""Check the local OpenAI path with isolated binaries, ports, and database.

Uses only Python's standard library. Does not read config.yaml or call a real
provider. Cross-provider compatibility is outside this smoke check's scope.
"""

import argparse
import contextlib
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


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


def control_workflow(base, upstream_port):
    """Exercise the actual console APIs without using the legacy admin key."""
    def api(path, session=None, payload=None, method=None, expected=200):
        status, raw = request(base + path, session, payload, method)
        assert status == expected, (path, status, raw)
        return json.loads(raw) if raw else {}

    with HTTP.open(base + "/", timeout=10) as response:
        assert "frame-ancestors 'none'" in response.headers["Content-Security-Policy"]
        assert b"TXAPI / Control Room" in response.read()
    for asset in ["app.mjs", "api.mjs", "app.css"]:
        status, _ = request(base + "/ui/" + asset)
        assert status == 200
    assert request(base + "/ui/api.test.mjs")[0] == 404
    admin = api("/api/auth/login", payload={
        "username": "smoke-admin", "password": "smoke-admin-password-only"
    })["session_token"]
    initial = api("/api/admin/config-status", admin)["data"]
    for name in ["console-user", "console-other"]:
        api("/api/admin/users", admin, {
            "username": name, "password": name + "-test-password", "role": "user",
            "model_mode": "allowlist", "models": ["console-model"],
        }, expected=201)
    channel = api("/api/admin/channels", admin, {
        "name": "console-upstream", "type": "openai",
        "base_url": f"http://127.0.0.1:{upstream_port}", "api_key": "test-upstream-console-only",
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
    api("/api/admin/config-status", session, expected=403)
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
    # Credential metadata edits preserve the existing upstream secret.
    credential = channel["credentials"][0]
    api(f"/api/admin/credentials/{credential['id']}", admin,
        {"name": "renamed", "weight": 2}, method="PUT")
    api("/api/admin/config-status/apply", admin, method="POST")
    assert "gateway_attempts_total" in request(base + "/metrics")[1].decode()
    api("/api/auth/logout", session, method="POST", expected=204)
    api("/api/me", session, expected=401)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable")
    parser.add_argument("--preview", action="store_true", help="Keep isolated services running after checks for manual console QA")
    args = parser.parse_args()
    # Never inherit a real deployment's database path, bind address or secrets.
    clean_env = {key: value for key, value in os.environ.items()
                 if not key.startswith(("GATEWAY_", "TXAPI_ADMIN_"))}
    with tempfile.TemporaryDirectory(prefix="gateway-smoke-") as directory:
        work = Path(directory)
        gateway, upstream = work / "gateway", work / "upstream"
        for output, package in [(gateway, "./cmd/gateway"), (upstream, "./tests/upstream/openai")]:
            subprocess.run([args.go, "build", "-o", str(output), package], cwd=ROOT, check=True, timeout=240)
        gateway_port, upstream_port = ports()
        key = "sk-local-smoke-only"
        # The management key is mandatory: config.Load refuses to start
        # without it, and this check asserts the binary really starts.
        admin_key = "sk-local-smoke-admin"
        config = {
            "server": {"addr": f"127.0.0.1:{gateway_port}", "retry_times": 1, "timeout_seconds": 10},
            "admin_key": admin_key,
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
                control_workflow(base, upstream_port)
                if args.preview:
                    print(f"PASS: all smoke checks; isolated console preview at {base}", flush=True)
                    print("Test login: smoke-admin / smoke-admin-password-only. Ctrl+C stops and removes test data.", flush=True)
                    try:
                        input("Press Enter to stop preview: ")
                    except KeyboardInterrupt:
                        pass
                    except EOFError:
                        print("Preview needs interactive stdin; isolated services stopped.", flush=True)
    print("PASS: startup, auth, model mapping, normal/SSE response, embedded console, S4 admin-user-publication-call-usage workflow")


if __name__ == "__main__":
    main()
