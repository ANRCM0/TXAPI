"""Actual-main opt-in matrix, aggregate consumer and bounded debug consumer."""

import contextlib
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socket
import shlex
import sqlite3
import subprocess
import threading
import time


def shadow_routing_workflow(gateway, work, env, service, ready, request):
    work = Path(work) / "shadow-routing"
    work.mkdir()
    env = {key: value for key, value in env.items()
           if not key.startswith(("GATEWAY_", "TXAPI_ADMIN_", "TXAPI_SMOKE_"))}
    lock = threading.Lock()
    state = {"posts": 0, "errors": []}
    upstream_key, client_key = "sk-shadow-upstream-only", "sk-shadow-client-only"
    document = {"id": "chatcmpl-shadow", "object": "chat.completion", "created": 1,
                "model": "shadow-actual", "choices": [{"index": 0, "message": {
                    "role": "assistant", "content": "shadow-output"}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3}}
    success = json.dumps(document, separators=(",", ":")).encode()

    class Upstream(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_POST(self):
            try:
                self.connection.settimeout(5)
                assert self.path == "/v1/chat/completions", self.path
                assert self.headers.get("Authorization") == "Bearer " + upstream_key
                size = int(self.headers.get("Content-Length", "0"))
                assert 0 < size < 16384
                body = json.loads(self.rfile.read(size))
                assert body["model"] == "shadow-actual"
                with lock:
                    state["posts"] += 1
                rejected = body["messages"][0]["content"] == "reject-this-input"
                data = b'{"error":{"message":"fixture input rejection","type":"fixture_rejection"}}' if rejected else success
                self.send_response(400 if rejected else 200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
            except Exception as error:
                with lock:
                    state["errors"].append(repr(error))

    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    upstream.daemon_threads = False
    thread = threading.Thread(target=upstream.serve_forever, kwargs={"poll_interval": 0.05},
                              name="shadow-smoke-upstream")
    thread.start()

    def stop_upstream():
        upstream.shutdown()
        upstream.server_close()
        thread.join(timeout=5)
        assert not thread.is_alive(), "shadow upstream did not join"

    with contextlib.ExitStack() as cleanup:
        cleanup.callback(stop_upstream)
        for diagnostics, scoring in [(False, False), (False, True), (True, False), (True, True)]:
            enabled = diagnostics and scoring
            case = work / f"diagnostics-{int(diagnostics)}-scoring-{int(scoring)}"
            case.mkdir()
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                port = sock.getsockname()[1]
            base = f"http://127.0.0.1:{port}"
            database, config_file, log_file = case / "gateway.db", case / "config.json", case / "gateway.log"
            config = {"server": {"addr": f"127.0.0.1:{port}", "retry_times": 0,
                                 "timeout_seconds": 5, "route_diagnostics": diagnostics,
                                 "shadow_route_scoring": scoring}, "log_level": "debug",
                      "admin_key": "shadow-management-only", "billing": {"mode": "disabled"},
                      "database": {"driver": "sqlite", "path": str(database)},
                      "channels": [{"name": f"shadow-{number}", "type": "openai",
                                    "base_url": f"http://127.0.0.1:{upstream.server_port}",
                                    "key": upstream_key, "models": ["shadow-alias"],
                                    "model_mapping": {"shadow-alias": "shadow-actual"}, "priority": 3}
                                   for number in (1, 2)],
                      "tokens": [{"name": "shadow-client", "key": client_key, "models": ["shadow-alias"]}]}
            config_file.write_text(json.dumps(config), encoding="utf-8")
            admin_env = dict(env, TXAPI_ADMIN_USERNAME="shadow-admin", TXAPI_ADMIN_PASSWORD="shadow-admin-password-only")
            with (case / "setup.log").open("wb") as setup:
                for flags, command_env in [(["-init-admin"], admin_env), (["-seed-only"], env),
                                           (["-claim-token", "1", "-owner-user", "1"], env)]:
                    subprocess.run([str(gateway), "-config", str(config_file), *flags], cwd=case,
                                   env=command_env, check=True, timeout=30, stdout=setup, stderr=setup)

            def finances():
                with contextlib.closing(sqlite3.connect(database)) as db:
                    return [db.execute(sql).fetchall() for sql in (
                        "SELECT * FROM billing_balances ORDER BY owner_type,owner_id",
                        "SELECT * FROM billing_reservations ORDER BY id",
                        "SELECT * FROM billing_attempts ORDER BY reservation_id,number",
                        "SELECT * FROM billing_resolutions ORDER BY reservation_id",
                        "SELECT id,used_quota FROM users ORDER BY id",
                        "SELECT id,used_quota FROM tokens ORDER BY id")]

            with service([str(gateway), "-config", str(config_file)], log_file, env) as process:
                ready(process, port)
                before_finances = finances()
                with lock:
                    before_posts = state["posts"]
                started = time.monotonic()

                def generation(reject=False):
                    status, raw = request(base + "/v1/chat/completions", client_key, {
                        "model": "shadow-alias", "messages": [{"role": "user", "content":
                            "reject-this-input" if reject else "hello"}]})
                    assert status == (400 if reject else 200), (status, raw)
                    if not reject:
                        assert json.loads(raw) == document, raw

                clean = 11 if enabled else 1
                for _ in range(clean - 1):
                    generation()
                generation(reject=True)
                if enabled:
                    time.sleep(max(0, 1.1 - (time.monotonic() - started)))
                generation()
                status, raw = request(base + "/metrics")
                assert status == 200, raw
                lines = [line for line in raw.decode().splitlines() if line.startswith("gateway_shadow_")]
                if enabled:
                    values = dict(line.rsplit(" ", 1) for line in lines)
                    assert int(values["gateway_shadow_eligible_total"]) == clean + 1, values
                    assert int(values["gateway_shadow_compared_total"]) == clean + 1, values
                    assert int(values['gateway_shadow_samples_total{outcome="clean_http_200"}']) == clean, values
                    assert int(values['gateway_shadow_samples_total{outcome="http_error"}']) == 1, values
                    forbidden = ("channel", "credential", "user", "model", "session", "request", upstream_key, client_key)
                    assert not any(word in line for line in lines for word in forbidden), lines
                else:
                    assert not lines, lines
                assert finances() == before_finances, "shadow changed financial state"
                with lock:
                    assert state["posts"] - before_posts == clean + 1, state
                    assert not state["errors"], state

            records = []
            for line in log_file.read_text(encoding="utf-8").splitlines():
                try:
                    record = dict(part.split("=", 1) for part in shlex.split(line) if "=" in part)
                except ValueError:
                    continue
                if record.get("msg") == "shadow route comparison":
                    for key in ("actual_channel_id", "recommended_channel_id", "pool_size", "epoch"):
                        record[key] = int(record[key])
                    for key in ("compared", "matched"):
                        record[key] = record[key] == "true"
                    record["candidates"] = json.loads(record["candidates"])
                    records.append(record)
            if enabled:
                assert len(records) >= 2, records
                allowed = {"time", "level", "source", "msg", "actual_channel_id", "recommended_channel_id",
                           "pool_size", "epoch", "reason", "compared", "matched", "candidates"}
                for record in records:
                    assert set(record) <= allowed, record
                    assert record["pool_size"] == 2 and record["compared"], record
                    assert record["actual_channel_id"] in (1, 2), record
                    assert record["recommended_channel_id"] in (1, 2), record
                    assert len(record["candidates"]) == 2, record
                    for candidate in record["candidates"]:
                        assert set(candidate) == {"channel_id", "active", "live", "errors", "neutral",
                                                   "numerator", "denominator"}, candidate
                times = [datetime.fromisoformat(row["time"].replace("Z", "+00:00")) for row in records]
                assert all((right - left).total_seconds() >= 0.999 for left, right in zip(times, times[1:])), times
                assert any(candidate["live"] >= 4 and not candidate["neutral"]
                           for row in records for candidate in row["candidates"]), records
            else:
                assert not records, records
    print("PASS: actual main shadow flag matrix, aggregate/debug consumers, exact sends/output and unchanged disabled financial state")
