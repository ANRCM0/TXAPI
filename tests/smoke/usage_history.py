"""Actual-main usage metadata and one precisely controlled process-death point."""

import base64
import contextlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import socket
import sqlite3
import subprocess
import threading
import time
import urllib.request


def usage_history_workflow(gateway, work, env, service, ready, request):
    work = Path(work) / "usage-history"
    work.mkdir()
    env = {key: value for key, value in env.items()
           if not key.startswith(("GATEWAY_", "TXAPI_ADMIN_", "TXAPI_SMOKE_"))}
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    release, blocked = threading.Event(), threading.Event()
    lock = threading.Lock()
    state = {"posts": 0, "errors": []}
    upstream_key, client_key = "sk-isolated-usage-upstream-only", "sk-isolated-usage-client-only"

    class Upstream(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_POST(self):
            try:
                self.connection.settimeout(5)
                assert self.path == "/v1/chat/completions", self.path
                assert self.headers.get("Authorization") == "Bearer " + upstream_key
                length = int(self.headers.get("Content-Length", "0"))
                assert 0 < length < 16384
                body = json.loads(self.rfile.read(length))
                assert body["model"] == "actual-history-model"
                with lock:
                    state["posts"] += 1
                    assert state["posts"] <= 4, "unexpected upstream replay"
                slow = body["messages"][0]["content"] == "block-after-first-chunk"
                usage = {"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5,
                         "prompt_tokens_details": {"cached_tokens": 2},
                         "completion_tokens_details": {"reasoning_tokens": 0}}
                document = {"id": "chatcmpl-usage-smoke", "object": "chat.completion", "created": 1,
                            "model": "actual-history-model", "choices": [{"index": 0,
                            "message": {"role": "assistant", "content": "smoke-output"},
                            "finish_reason": "stop"}], "usage": usage}
                if not body.get("stream"):
                    raw = json.dumps(document, separators=(",", ":")).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(raw)))
                    self.end_headers()
                    self.wfile.write(raw)
                    return
                chunk = {"id": document["id"], "object": "chat.completion.chunk", "created": 1,
                         "model": document["model"], "choices": [{"index": 0,
                         "delta": {"content": "smoke-output"}, "finish_reason": None}]}
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(("data: " + json.dumps(chunk, separators=(",", ":")) + "\n\n").encode())
                self.wfile.flush()
                if slow:
                    blocked.set()
                    assert release.wait(15), "controlled upstream was never released"
                chunk["choices"] = [{"index": 0, "delta": {}, "finish_reason": "stop"}]
                chunk["usage"] = usage
                self.wfile.write(("data: " + json.dumps(chunk, separators=(",", ":")) + "\n\ndata: [DONE]\n\n").encode())
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                pass
            except Exception as error:
                with lock:
                    state["errors"].append(repr(error))

    class BoundedServer(ThreadingHTTPServer):
        daemon_threads = False
        block_on_close = True
        request_queue_size = 4

        def __init__(self):
            self.slots = threading.BoundedSemaphore(2)
            super().__init__(("127.0.0.1", 0), Upstream)

        def process_request(self, connection, address):
            if not self.slots.acquire(blocking=False):
                connection.close()
                return
            try:
                super().process_request(connection, address)
            except BaseException:
                self.slots.release()
                raise

        def process_request_thread(self, connection, address):
            try:
                super().process_request_thread(connection, address)
            finally:
                self.slots.release()

    upstream = BoundedServer()
    thread = threading.Thread(target=upstream.serve_forever, kwargs={"poll_interval": 0.05},
                              name="usage-history-upstream")
    thread.start()

    def stop_upstream():
        release.set()
        upstream.shutdown()
        upstream.server_close()
        thread.join(timeout=5)
        assert not thread.is_alive(), "usage upstream server did not join"

    with contextlib.ExitStack() as stack:
        stack.callback(stop_upstream)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        database, config_file = work / "usage.db", work / "config.json"
        key_file = work / "credential.json"
        key_file.write_text(json.dumps({"active_key": "fixture", "keys": {
            "fixture": base64.b64encode(os.urandom(32)).decode()},
            "digest_key": base64.b64encode(os.urandom(32)).decode()}), encoding="utf-8")
        key_file.chmod(0o600)
        config = {"server": {"addr": f"127.0.0.1:{port}", "timeout_seconds": 10, "retry_times": 0},
                  "admin_key": "isolated-usage-management-key", "billing": {"mode": "disabled"},
                  "database": {"driver": "sqlite", "path": str(database), "credential_keys_file": str(key_file)},
                  "usage_history": {"enabled": True, "instance_records": 32, "owner_records": 16,
                                    "retention_seconds": 3600},
                  "channels": [{"name": "usage-loopback", "type": "openai",
                                "base_url": f"http://127.0.0.1:{upstream.server_port}", "key": upstream_key,
                                "models": ["history-alias"], "model_mapping": {"history-alias": "actual-history-model"}}],
                  "tokens": [{"name": "usage-client", "key": client_key, "models": ["history-alias"]}]}

        def write_config():
            config_file.write_text(json.dumps(config), encoding="utf-8")

        def api(path, session=None, payload=None, expected=200):
            status, raw = request(base + path, session, payload)
            assert status == expected, (path, status, raw)
            return json.loads(raw)

        def start(log):
            return service([str(gateway), "-config", str(config_file)], work / log, env)

        def financial():
            with contextlib.closing(sqlite3.connect(database)) as db:
                return [db.execute(sql).fetchall() for sql in (
                    "SELECT * FROM billing_balances ORDER BY owner_type,owner_id",
                    "SELECT * FROM billing_reservations ORDER BY id",
                    "SELECT * FROM billing_attempts ORDER BY reservation_id,number",
                    "SELECT * FROM billing_resolutions ORDER BY reservation_id",
                    "SELECT id,used_quota FROM users ORDER BY id",
                    "SELECT id,used_quota FROM tokens ORDER BY id")]

        def posts():
            with lock:
                return state["posts"]

        def generation(stream=False, slow=False):
            payload = {"model": "history-alias", "stream": stream,
                       "messages": [{"role": "user", "content": "block-after-first-chunk" if slow else "hello"}]}
            return urllib.request.Request(base + "/v1/chat/completions", data=json.dumps(payload).encode(),
                headers={"Authorization": "Bearer " + client_key, "Content-Type": "application/json",
                         "X-Request-ID": "same-usage-smoke-request"})

        def history_count():
            with contextlib.closing(sqlite3.connect(database)) as db:
                return db.execute("SELECT COUNT(*) FROM usage_invocations").fetchone()[0]

        write_config()
        admin_env = dict(env, TXAPI_ADMIN_USERNAME="usage-admin", TXAPI_ADMIN_PASSWORD="usage-admin-password-only")
        for flags, command_env in [(["-init-admin"], admin_env), (["-seed-only"], env)]:
            subprocess.run([str(gateway), "-config", str(config_file), *flags], env=command_env,
                           check=True, timeout=30, cwd=work)
        with start("setup.log") as process:
            ready(process, port)
            admin = api("/api/auth/login", payload={"username": "usage-admin", "password": "usage-admin-password-only"})["session_token"]
            owner = api("/api/admin/users", admin, {"username": "usage-owner", "password": "usage-owner-password-only",
                "role": "user", "model_mode": "allowlist", "models": ["history-alias"]}, expected=201)["data"]
            api("/api/admin/users", admin, {"username": "usage-other", "password": "usage-other-password-only",
                "role": "user", "model_mode": "allowlist", "models": ["history-alias"]}, expected=201)
            owner_session = api("/api/auth/login", payload={"username": "usage-owner", "password": "usage-owner-password-only"})["session_token"]
            other_session = api("/api/auth/login", payload={"username": "usage-other", "password": "usage-other-password-only"})["session_token"]
        subprocess.run([str(gateway), "-config", str(config_file), "-claim-token", "1", "-owner-user", str(owner["id"])],
                       env=env, check=True, timeout=30, cwd=work)
        with contextlib.closing(sqlite3.connect(database)) as db:
            db.executemany("INSERT INTO billing_balances(owner_type,owner_id,used_units,reserved_units) VALUES(?,?,37,11)",
                           [("user", owner["id"]), ("key", 1)])
            db.execute("INSERT INTO billing_reservations(id,user_id,token_id,price_version,origin_model,reserved_units,state,lease_until) VALUES(?,?,?,?,?,?,?,?)",
                       ("1" * 32, owner["id"], 1, "unrelated-history-price", "history-alias", 11, "reserved", int(time.time() * 1000) + 3600000))
            db.execute("UPDATE users SET used_quota=7.25 WHERE id=?", (owner["id"],))
            db.execute("UPDATE tokens SET used_quota=8.5 WHERE id=1")
            db.commit()
        before = financial()
        sealed = {}
        with start("killed.log") as process:
            ready(process, port)
            for stream in (False, True):
                with opener.open(generation(stream), timeout=12) as response:
                    identifier = response.headers.get("X-TXAPI-Usage-ID", "")
                    assert re.fullmatch(r"usage_[a-f0-9]{32}", identifier), identifier
                    assert identifier not in sealed, "client Request ID deduplicated generation"
                    raw = response.read()
                    assert raw.count(b"smoke-output") == 1, raw
                    if stream:
                        assert raw.count(b"[DONE]") == 1, raw
                deadline = time.monotonic() + 5
                while True:
                    detail = api("/api/admin/usage-records/" + identifier, admin)["data"]
                    if detail["record"]["state"] == "sealed":
                        break
                    assert time.monotonic() < deadline, detail
                    time.sleep(0.02)
                assert detail["record"]["request_id"] == "same-usage-smoke-request"
                assert detail["record"]["transport"] == "http"
                assert "protocol_status" not in detail["record"]["host"]
                assert detail["record"]["stream"] is stream and len(detail["attempts"]) == 1
                target = detail["attempts"][0]
                assert target["actual_model"] == "actual-history-model" and target["channel_id"] == 1
                assert target["group"] == "default" and target["credential_id"] > 0
                assert target["state"] == "observed" and target["may_dispatch"] is True
                quantities = target["usage"]
                names = ("input_tokens", "output_tokens", "total_tokens", "cached_input_tokens",
                         "cache_write_input_tokens", "reasoning_output_tokens")
                assert all(name in quantities and (quantities[name] is None or
                           isinstance(quantities[name], str) and quantities[name].isdigit()) for name in names), quantities
                assert quantities["completeness"] == "complete" and quantities["source"] == "provider_reported", quantities
                assert quantities["profile"] == "chat_native_tokens_v1", quantities
                assert [quantities[name] for name in names] == ["4", "1", "5", "2", None, "0"], quantities
                own = api("/api/me/usage-records/" + identifier, owner_session)["data"]
                assert "quotation_version" not in own["record"]
                assert not any(name in own["attempts"][0] for name in
                               ("actual_model", "group", "protocol", "channel_id", "credential_id")), own
                api("/api/me/usage-records/" + identifier, other_session, expected=404)
                api("/api/me/usage-records/" + identifier, client_key, expected=401)
                sealed[identifier] = detail
            assert posts() == 2 and history_count() == 2
            response = opener.open(generation(True, True), timeout=12)
            try:
                crashed_id = response.headers.get("X-TXAPI-Usage-ID", "")
                assert re.fullmatch(r"usage_[a-f0-9]{32}", crashed_id)
                first = bytearray()
                while not first.endswith(b"\n\n"):
                    line = response.readline()
                    assert line, "stream ended before its first chunk"
                    first.extend(line)
                assert b"smoke-output" in first and b"[DONE]" not in first
                assert blocked.wait(2), "upstream did not remain blocked"
                with contextlib.closing(sqlite3.connect(database)) as db:
                    assert db.execute("SELECT state FROM usage_invocations WHERE id=?", (crashed_id,)).fetchone() == ("open",)
                    assert db.execute("SELECT state,may_dispatch,input_tokens,output_tokens,total_tokens,cached_input_tokens,cache_write_input_tokens,reasoning_output_tokens FROM usage_attempt_receipts WHERE invocation_id=?", (crashed_id,)).fetchone() == ("may_dispatch", 1, None, None, None, None, None, None)
                process.kill()
                process.wait(timeout=5)
            finally:
                response.close()
                release.set()
        assert posts() == 3 and financial() == before
        with start("recovered.log") as process:
            ready(process, port)
            recovered = api("/api/admin/usage-records/" + crashed_id, admin)["data"]
            assert recovered["record"]["state"] == "interrupted" and len(recovered["attempts"]) == 1
            assert recovered["attempts"][0]["state"] == "interrupted" and recovered["attempts"][0]["may_dispatch"]
            assert recovered["attempts"][0]["usage"]["input_tokens"] is None
            for identifier, detail in sealed.items():
                assert api("/api/admin/usage-records/" + identifier, admin)["data"] == detail
            assert posts() == 3 and history_count() == 3 and financial() == before
        config["usage_history"]["enabled"] = False
        write_config()
        with start("disabled.log") as process:
            ready(process, port)
            listing = api("/api/me/usage-records", owner_session)
            assert listing["enabled"] is False and len(listing["data"]) == 3
            for identifier, detail in sealed.items():
                assert api("/api/admin/usage-records/" + identifier, admin)["data"] == detail
            with opener.open(generation(False), timeout=12) as response:
                assert response.headers.get("X-TXAPI-Usage-ID") is None
                assert response.read().count(b"smoke-output") == 1
            assert posts() == 4 and history_count() == 3 and financial() == before
        with lock:
            assert state["errors"] == [], state["errors"]
        assert financial() == before
    result = {"kill_point": "after_first_sse_chunk_before_observe",
              "not_covered": ["accepted_before_dispatch", "returned_usage_before_terminal_write"]}
    print("PASS: actual main Chat native JSON/SSE quantities/cache/zero reasoning, session isolation, sealed restart, first-chunk kill/interrupted recovery without replay, disabled capture and unchanged financial state")
    return result
