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


def request(url, key=None, payload=None):
    headers = {}
    if key:
        headers["Authorization"] = "Bearer " + key
    data = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(payload).encode()
    req = urllib.request.Request(url, data=data, headers=headers)
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
def service(command, log_path):
    with log_path.open("w+") as log:
        process = subprocess.Popen(command, cwd=ROOT, stdout=log, stderr=log)
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable")
    args = parser.parse_args()
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
        env = dict(os.environ, TXAPI_ADMIN_USERNAME="smoke-admin", TXAPI_ADMIN_PASSWORD="smoke-admin-password-only")
        subprocess.run([str(gateway), "-config", str(config_path), "-init-admin"], env=env, cwd=ROOT, check=True, timeout=30)
        subprocess.run([str(gateway), "-config", str(config_path), "-seed-only"], cwd=ROOT, check=True, timeout=30)
        subprocess.run([str(gateway), "-config", str(config_path), "-claim-token", "1", "-owner-user", "1"], cwd=ROOT, check=True, timeout=30)
        with service([str(upstream), f"127.0.0.1:{upstream_port}"], work / "upstream.log") as up:
            ready(up, upstream_port)
            with service([str(gateway), "-config", str(config_path)], work / "gateway.log") as gw:
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
    print("PASS: startup, authentication, model list/mapping, normal response, complete SSE text/finish/usage")


if __name__ == "__main__":
    main()
