"""Actual production-binary background/SSE recovery, using loopback only."""

import base64
import contextlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import threading
import time
import urllib.request


def background_workflow(gateway, work, env, service, ready, request):
    work = Path(work) / "native-background"
    work.mkdir()
    state = {"posts": 0, "cancels": 0, "resumes": 0, "finished": False, "cancelled": set()}
    lock = threading.Lock()
    credential = "sk-isolated-background-upstream"

    def document(remote_id, status):
        return {"id": remote_id, "object": "response", "status": status, "output": [],
                "usage": {"input_tokens": 2, "output_tokens": 1, "total_tokens": 3,
                          "input_tokens_details": {"cached_tokens": 1},
                          "output_tokens_details": {"reasoning_tokens": 0}}}

    def event(seq, remote_id, status):
        kind = "response.completed" if status == "completed" else "response.created"
        return (f"event: {kind}\ndata: " + json.dumps({"type": kind, "sequence_number": seq,
                "response": document(remote_id, status)}, separators=(",", ":")) + "\n\n").encode()

    class Upstream(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def send(self, data, stream=False):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream" if stream else "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            try:
                self.wfile.write(data)
            except (BrokenPipeError, ConnectionResetError):
                pass

        def do_POST(self):
            assert self.headers.get("Authorization") == "Bearer " + credential
            if self.path.endswith("/cancel"):
                remote_id = self.path.split("/")[-2]
                with lock:
                    state["cancels"] += 1
                    state["cancelled"].add(remote_id)
                self.send(json.dumps(document(remote_id, "cancelled")).encode())
                return
            assert self.path == "/v1/responses"
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            assert body["background"] is True and body["store"] is False
            with lock:
                state["posts"] += 1
                remote_id = f"resp_native_{state['posts']}"
            self.send(event(0, remote_id, "queued") if body.get("stream") else
                      json.dumps(document(remote_id, "in_progress")).encode(), bool(body.get("stream")))

        def do_GET(self):
            assert self.headers.get("Authorization") == "Bearer " + credential
            from urllib.parse import urlsplit, parse_qs
            url = urlsplit(self.path)
            remote_id = url.path.split("/")[-1]
            query = parse_qs(url.query)
            with lock:
                finished = state["finished"]
                cancelled = remote_id in state["cancelled"]
                if query:
                    state["resumes"] += 1
            if query:
                assert query == {"stream": ["true"], "starting_after": ["0"]}, query
                self.send(event(1, remote_id, "completed") if finished else b"", True)
            else:
                self.send(json.dumps(document(remote_id, "cancelled" if cancelled else "in_progress")).encode())

    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    with contextlib.ExitStack() as stack:
        stack.callback(upstream.server_close)
        stack.callback(upstream.shutdown)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        base = f"http://127.0.0.1:{port}"

        def occupancy(expected):
            deadline = time.monotonic() + 10
            while True:
                status, raw = request(base + "/metrics")
                assert status == 200, (status, raw)
                samples = {parts[0]: int(parts[1]) for line in raw.decode().splitlines()
                           if line.startswith("gateway_background_") and len(parts := line.split()) == 2}
                if (samples.get("gateway_background_snapshot_success") == 1
                        and samples.get("gateway_background_snapshot_timestamp_seconds", 0) > 0
                        and all(samples.get(name) == value for name, value in expected.items())):
                    assert samples["gateway_background_snapshot_timestamp_seconds"] <= time.time() + 1
                    return samples
                assert time.monotonic() < deadline, (expected, samples)
                time.sleep(0.05)

        database, config_file = work / "background.db", work / "config.json"
        credential_keys, content_keys = work / "credential.json", work / "content.json"
        for path in (credential_keys, content_keys):
            path.write_text(json.dumps({"active_key": "test", "keys": {"test": base64.b64encode(os.urandom(32)).decode()},
                                        "digest_key": base64.b64encode(os.urandom(32)).decode()}))
            path.chmod(0o600)
        token = "sk-isolated-background-client"
        config = {"server": {"addr": f"127.0.0.1:{port}", "timeout_seconds": 10}, "admin_key": "isolated-background-admin-key",
                  "database": {"path": str(database), "credential_keys_file": str(credential_keys)}, "billing": {"mode": "disabled"},
                  "channels": [{"name": "native", "type": "openai", "base_url": f"http://127.0.0.1:{upstream.server_port}",
                                "key": credential, "models": ["m"]}],
                  "tokens": [{"name": "background-client", "key": token, "models": ["m"]}],
                  "responses": {"enabled": True, "keys_file": str(content_keys), "background": {"enabled": False}}}
        config_file.write_text(json.dumps(config))
        admin_env = dict(env, TXAPI_ADMIN_USERNAME="bg-smoke-admin", TXAPI_ADMIN_PASSWORD="bg-smoke-password-only")
        for flags, command_env in [(["-init-admin"], admin_env), (["-seed-only"], env),
                                   (["-claim-token", "1", "-owner-user", "1"], env)]:
            subprocess.run([str(gateway), "-config", str(config_file), *flags], env=command_env, check=True, timeout=30)
        payload = {"model": "m", "input": "private-background-smoke-input", "background": True, "store": False, "stream": True}
        with service([str(gateway), "-config", str(config_file)], work / "disabled.log", env) as process:
            ready(process, port)
            occupancy({"gateway_background_enabled": 0, 'gateway_background_jobs{stage="terminal"}': 0,
                       "gateway_background_events_retained": 0, "gateway_background_reserved_bytes": 0})
            assert request(base + "/v1/responses", token, payload)[0] == 400
            assert state["posts"] == 0
        config["responses"]["background"] = {"enabled": True, "channel_ids": [1]}
        config_file.write_text(json.dumps(config))
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with service([str(gateway), "-config", str(config_file)], work / "first.log", env) as process:
            ready(process, port)
            req = urllib.request.Request(base + "/v1/responses", data=json.dumps(payload).encode(), headers={
                "Authorization": "Bearer " + token, "Content-Type": "application/json", "Idempotency-Key": "main-background-observer"})
            with opener.open(req, timeout=12) as response:
                assert response.headers["Content-Type"] == "text/event-stream"
                assert response.headers["Cache-Control"] == "no-store"
                assert response.headers["X-TXAPI-Background-Retention"] == "temporary"
                public_id = response.headers["X-TXAPI-Response-ID"]
                first = b""
                while not first.endswith(b"\n\n"):
                    line = response.readline()
                    assert line, "SSE ended before first persisted event"
                    first += line
                assert b'"sequence_number":0' in first and b"resp_native_" not in first and b"[DONE]" not in first
            # This is an ungraceful production-binary death, not a mocked Close.
            process.kill()
            process.wait(timeout=5)
        with service([str(gateway), "-config", str(config_file)], work / "restart.log", env) as process:
            ready(process, port)
            assert request(base + "/v1/responses/" + public_id, token)[0] == 200
            with lock:
                state["finished"] = True
            deadline = time.monotonic() + 45  # A killed real owner may retain its fixed 30s lease.
            while True:
                status, body = request(base + "/v1/responses/" + public_id, token)
                assert status == 200, (status, body)
                if json.loads(body)["status"] == "completed":
                    break
                assert time.monotonic() < deadline, "Native event tail was not resumed after crash"
                time.sleep(1)
            status, body = request(base + "/v1/responses/" + public_id + "?stream=true&starting_after=0", token)
            assert status == 200 and b'"sequence_number":1' in body and b'"sequence_number":0' not in body
            assert state["posts"] == 1 and state["resumes"] >= 1
            status, body = request(base + "/v1/responses", token, dict(payload, stream=False))
            assert status == 200, (status, body)
            cancel_id = json.loads(body)["id"]
            deadline = time.monotonic() + 10
            while True:
                with contextlib.closing(sqlite3.connect(database)) as db:
                    stage = db.execute("SELECT stage FROM background_jobs WHERE public_id=?", (cancel_id,)).fetchone()[0]
                if stage == "remote_active":
                    break
                assert time.monotonic() < deadline
                time.sleep(0.05)
            assert request(base + "/v1/responses/" + cancel_id + "/cancel", token, method="POST")[0] == 200
            while True:
                status, body = request(base + "/v1/responses/" + cancel_id, token)
                if status == 200 and json.loads(body)["status"] == "cancelled":
                    break
                assert time.monotonic() < deadline, (status, body)
                time.sleep(0.1)
            assert state["posts"] == 2 and state["cancels"] == 1
            assert request(base + "/v1/responses/" + cancel_id + "?stream=true", token)[0] == 409
            assert request(base + "/v1/responses/" + public_id + "?stream=true&starting_after=-1", token)[0] == 400
            assert request(base + "/v1/responses/" + cancel_id, token, method="DELETE")[0] == 200
            status, body = request(base + "/api/auth/login", payload={"username": "bg-smoke-admin", "password": "bg-smoke-password-only"})
            assert status == 200
            session = json.loads(body)["session_token"]
            assert request(base + "/api/me/background", token)[0] == 401
            status, body = request(base + "/api/admin/background", session)
            assert status == 200 and len(json.loads(body)["data"]) == 2
            tasks = {item["id"]: item for item in json.loads(body)["data"]}
            assert tasks[public_id]["protocol_status"] == "completed"
            assert tasks[cancel_id]["protocol_status"] == "cancelled" and tasks[cancel_id]["deleted"]
            for item in tasks.values():
                usage = item["usage"]
                assert usage == {"input_tokens": "2", "output_tokens": "1", "total_tokens": "3",
                                 "cached_input_tokens": "1", "cache_write_input_tokens": None,
                                 "reasoning_output_tokens": "0", "completeness": "complete",
                                 "source": "provider_reported", "profile": "responses_native_tokens_v2"}, usage
            status, detail_body = request(base + "/api/me/background/" + public_id, session)
            assert status == 200 and json.loads(detail_body)["data"]["usage"] == tasks[public_id]["usage"]
            for secret in (credential, "private-background-smoke-input", "resp_native_"):
                assert secret.encode() not in body, "Task metadata exposed private content"
            status, body = request(base + "/v1/responses", token, dict(payload, stream=False))
            assert status == 200
            console_id = json.loads(body)["id"]
            # Receipt delivery can precede dispatch. Exercise remote cancellation
            # in both headless CI and the slower optional browser workflow.
            deadline = time.monotonic() + 10
            while True:
                status, body = request(base + "/api/me/background/" + console_id, session)
                assert status == 200, (status, body)
                if json.loads(body)["data"]["stage"] == "remote_active":
                    break
                assert time.monotonic() < deadline, "Console task never became remote active"
                time.sleep(0.05)
            with lock:
                assert state["posts"] == 3, "Console fixture did not dispatch exactly once"
            occupancy({"gateway_background_enabled": 1, 'gateway_background_jobs{stage="terminal"}': 2,
                       'gateway_background_jobs{stage="remote_active"}': 1, "gateway_background_replay_pending": 0,
                       "gateway_background_events_retained": 2})
            if env.get("TXAPI_TASK_BROWSER"):
                browser_env = dict(env, TXAPI_BASE_URL=base, TXAPI_TEST_ADMIN_USERNAME="bg-smoke-admin",
                                   TXAPI_TEST_ADMIN_PASSWORD="bg-smoke-password-only", TXAPI_TEST_TASK_ID=console_id)
                subprocess.run([env.get("TXAPI_TASK_BROWSER_NODE", "node"), str(Path(__file__).resolve().parents[1] / "browser" / "tasks-live.cjs")], env=browser_env, check=True, timeout=90)
            status, body = request(base + "/api/me/background/" + console_id, session)
            assert status == 200
            revision = json.loads(body)["data"]["revision"]
            status, body = request(base + "/api/me/background/" + console_id + "/cancel", session, {"revision": revision})
            assert status == 200 and json.loads(body)["data"]["cancel_requested"]
            deadline = time.monotonic() + 10
            while True:
                status, body = request(base + "/v1/responses/" + console_id, token)
                if status == 200 and json.loads(body)["status"] == "cancelled":
                    break
                assert time.monotonic() < deadline, (status, body)
                time.sleep(0.1)
            with lock:
                assert state["posts"] == 3 and state["cancels"] == 2, "Console cancellation replayed or skipped remote cleanup"
            with contextlib.closing(sqlite3.connect(database)) as db:
                events, event_bytes, reserved = db.execute(
                    "SELECT SUM(event_count),SUM(stored_bytes),SUM(reserved_bytes) FROM background_jobs").fetchone()
            occupancy({"gateway_background_enabled": 1, 'gateway_background_jobs{stage="terminal"}': 3,
                       "gateway_background_replay_pending": 0, "gateway_background_events_retained": events,
                       "gateway_background_event_bytes": event_bytes, "gateway_background_reserved_bytes": reserved})

        for mode in ("legacy", "strict"):
            config["billing"]["mode"] = mode
            config_file.write_text(json.dumps(config))
            with (work / (mode + ".log")).open("w") as log:
                result = subprocess.run([str(gateway), "-config", str(config_file)], env=env, stdout=log, stderr=log, timeout=20)
            assert result.returncode != 0, "Charged background mode was silently accepted"
        config["billing"]["mode"] = "disabled"
        config["responses"] = {"enabled": False, "keys_file": str(work / "missing-content.json")}
        with contextlib.closing(sqlite3.connect(database)) as db:
            db.execute("UPDATE background_jobs SET content_expires_at=1")
            db.commit()
        config_file.write_text(json.dumps(config))
        with service([str(gateway), "-config", str(config_file)], work / "privacy-disabled.log", env) as process:
            ready(process, port)
            occupancy({"gateway_background_enabled": 0, "gateway_background_controls_active": 0,
                       'gateway_background_jobs{stage="terminal"}': 3, "gateway_background_replay_pending": 0,
                       "gateway_background_events_retained": 0, "gateway_background_event_bytes": 0,
                       "gateway_background_reserved_bytes": 0})
            with contextlib.closing(sqlite3.connect(database)) as db:
                assert db.execute("SELECT COUNT(*) FROM background_events").fetchone() == (0,)
                assert db.execute("SELECT SUM(length(ciphertext)+length(snapshot)+reserved_bytes+replay_pending) FROM background_jobs").fetchone() == (0,)
                assert db.execute("SELECT COUNT(*) FROM credentials WHERE api_key<>'' OR length(key_ciphertext)=0").fetchone() == (0,)
                assert db.execute("SELECT COUNT(*) FROM background_jobs WHERE usage_total_tokens=3 AND usage_cached_input_tokens=1 AND usage_reasoning_output_tokens=0").fetchone() == (3,), "Content cleanup erased usage metadata"
        assert state["posts"] == 3, "Feature-off cleanup resubmitted a remote task"
    print("PASS: actual main native background/SSE disconnect, SIGKILL restart/cursor, cancel/delete, exact task usage queries, charged refusal and feature-off TTL", flush=True)
