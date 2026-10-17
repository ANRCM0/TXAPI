"""Build a disposable production gateway and run real console acceptance.

Only temporary SQLite/configuration and loopback upstreams are used. Playwright
must already be available through NODE_PATH; no dependencies are installed.
"""

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import threading

from local import ROOT, ports, ready, request

STAGE = "arguments"


def main():
    global STAGE
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--node", default="node")
    args = parser.parse_args()
    clean = {key: value for key, value in os.environ.items()
             if not key.upper().startswith(("GATEWAY_", "TXAPI_"))
             and key.upper() not in {"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"}}
    clean["NO_PROXY"] = "127.0.0.1,localhost,::1"
    admin_name, user_name = "compat-admin", "compat-user"
    admin_password, user_password = secrets.token_urlsafe(24), secrets.token_urlsafe(24)
    upstream_key = secrets.token_urlsafe(24)
    models = ["s6-live-1440", "s6-live-390"]

    class Upstream(BaseHTTPRequestHandler):
        def log_message(self, *unused):
            pass

        def do_POST(self):
            if self.headers.get("Authorization") not in {"Bearer " + upstream_key, "Bearer " + upstream_key + "-first"}:
                self.send_error(401)
                return
            try:
                value = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            except (ValueError, TypeError):
                self.send_error(400)
                return
            if self.path != "/v1/chat/completions" or value.get("model") not in models:
                self.send_error(400)
                return
            body = json.dumps({"model": value["model"], "choices": [{"index": 0,
                "message": {"role": "assistant", "content": "s6-live-browser-ok"}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3}}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    with tempfile.TemporaryDirectory(prefix="txapi-compatibility-") as directory:
        work = Path(directory)
        binary = work / ("gateway.exe" if os.name == "nt" else "gateway")
        STAGE = "build"
        with (work / "build.log").open("w", encoding="utf-8") as build_log:
            subprocess.run([args.go, "build", "-o", str(binary), "./cmd/gateway"], cwd=ROOT,
                           env=clean, stdout=build_log, stderr=build_log, check=True, timeout=240)
        upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        upstream.daemon_threads = True
        thread = threading.Thread(target=upstream.serve_forever, daemon=True)
        thread.start()
        process = None
        try:
            gateway_port = ports()[0]
            base = f"http://127.0.0.1:{gateway_port}"
            upstream_url = f"http://127.0.0.1:{upstream.server_port}"
            config = {"server": {"addr": f"127.0.0.1:{gateway_port}", "retry_times": 0,
                                  "timeout_seconds": 5, "total_timeout_seconds": 10},
                      "admin_key": secrets.token_urlsafe(24),
                      "database": {"driver": "sqlite", "path": str(work / "gateway.db")},
                      "billing": {"mode": "disabled"}}
            config_path = work / "config.json"
            config_path.write_text(json.dumps(config), encoding="utf-8")
            env = dict(clean, TXAPI_ADMIN_USERNAME=admin_name, TXAPI_ADMIN_PASSWORD=admin_password)
            with (work / "gateway.log").open("w", encoding="utf-8") as log:
                STAGE = "init"
                subprocess.run([str(binary), "-config", str(config_path), "-init-admin"], cwd=ROOT,
                               env=env, stdout=log, stderr=log, check=True, timeout=30)
                process = subprocess.Popen([str(binary), "-config", str(config_path)], cwd=ROOT,
                                           env=clean, stdout=log, stderr=log)
                ready(process, gateway_port)
                STAGE = "setup"

                def api(path, payload, token=None, expected=200):
                    status, raw = request(base + path, token, payload)
                    if status != expected:
                        raise RuntimeError(f"Disposable setup API {path} returned HTTP {status}")
                    return json.loads(raw)

                admin = api("/api/auth/login", {"username": admin_name, "password": admin_password})["session_token"]
                api("/api/admin/channels", {"name": "compatibility-bootstrap", "type": "openai",
                    "base_url": upstream_url, "api_key": upstream_key, "models": ["s6-bootstrap"]}, admin, 201)
                api("/api/admin/users", {"username": user_name, "password": user_password, "role": "user",
                    "model_mode": "allowlist", "models": models}, admin, 201)
                request(base + "/api/auth/logout", admin, method="POST")
                browser_env = dict(clean, TXAPI_BASE_URL=base,
                    TXAPI_TEST_ADMIN_USERNAME=admin_name, TXAPI_TEST_ADMIN_PASSWORD=admin_password,
                    TXAPI_TEST_USER_USERNAME=user_name, TXAPI_TEST_USER_PASSWORD=user_password,
                    TXAPI_TEST_MODEL="s6-live", TXAPI_TEST_UPSTREAM_URL=upstream_url,
                    TXAPI_TEST_UPSTREAM_KEY=upstream_key)
                STAGE = "browser"
                subprocess.run([args.node, str(ROOT / "tests/browser/live.cjs")], cwd=ROOT,
                               env=browser_env, check=True, timeout=180)
        finally:
            if process is not None and process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
            upstream.shutdown()
            upstream.server_close()
            thread.join(timeout=5)
    print("PASS: production binary, disposable SQLite, real API/UI compatibility workflow and cleanup")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Never dump gateway logs, response bodies or fixture credentials.
        raise SystemExit(f"FAIL: disposable compatibility acceptance [{STAGE}] ({type(error).__name__})") from None
