"""Paired loopback HTTP/1.1 text-SSE baseline against the production gateway.

Only synthetic keys/configuration and disposable SQLite state are used. This is
an observation baseline, not a performance acceptance gate or Provider test.
"""
import argparse
import base64
import contextlib
import hashlib
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import math
import os
from pathlib import Path
import platform
import socket
import ssl
import subprocess
import threading
import time
import uuid

from local import ROOT, ports, ready, service
from process_resources import RESOURCE_SCOPE, Resources

CLIENT_KEY = "sk-baseline-client-only"
UPSTREAM_KEY = "sk-baseline-upstream-only"
TEXT = ["你好", "🌏", " text2", " text3", " text4", " text5", " text6", " text7"]


def clock():
    return time.perf_counter_ns()


def events(operation="chat", tag="0" * 32):
    values = [b": heartbeat\n\n"]
    if operation == "responses":
        response = {"id": "resp_" + tag, "object": "response", "model": "baseline-model",
                    "status": "in_progress", "output": [], "usage": None}
        chunks = [{"type": "response.created", "sequence_number": 0, "response": response}]
        chunks += [{"type": "response.output_text.delta", "sequence_number": index + 1,
                    "item_id": "msg_" + tag, "output_index": 0, "content_index": 0, "delta": text}
                   for index, text in enumerate(TEXT)]
        chunks.append({"type": "response.output_text.done", "sequence_number": 9,
                       "item_id": "msg_" + tag, "output_index": 0, "content_index": 0, "text": "".join(TEXT)})
        completed = dict(response, status="completed", output=[{
            "id": "msg_" + tag, "type": "message", "role": "assistant", "status": "completed",
            "content": [{"type": "output_text", "text": "".join(TEXT), "annotations": []}]}],
            usage={"input_tokens": 11, "output_tokens": 7, "total_tokens": 18})
        chunks.append({"type": "response.completed", "sequence_number": 10, "response": completed})
        return values + [("event: " + c["type"] + "\ndata: " +
                          json.dumps(c, ensure_ascii=False, separators=(",", ":")) + "\n\n").encode() for c in chunks]
    chunks = [{"choices": [{"delta": {"role": "assistant"}}]}]
    chunks += [{"choices": [{"delta": {"content": text}}]} for text in TEXT]
    chunks += [{"choices": [{"delta": {}, "finish_reason": "stop"}]},
               {"choices": [], "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}}]
    values += [("data: " + json.dumps(c, ensure_ascii=False, separators=(",", ":")) + "\n\n").encode() for c in chunks]
    return values + [b"data: [DONE]\n\n"]


class Fixture(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, delay, operation="chat", tcp_nodelay=False):
        super().__init__(("127.0.0.1", 0), FixtureHandler)
        self.delay = delay
        self.operation = operation
        self.records = {}
        self.lock = threading.Lock()
        self.stop_event = threading.Event()
        self.connections = 0
        self.release_held = threading.Event()
        self.tls_context = None
        self.tcp_nodelay = tcp_nodelay

    def get_request(self):
        pair = super().get_request()
        with self.lock:
            self.connections += 1
        if self.tls_context is not None:
            connection, address = pair
            try:
                connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, int(self.tcp_nodelay))
                connection.settimeout(3)
                connection = self.tls_context.wrap_socket(connection, server_side=True)
                connection.settimeout(None)
            except BaseException:
                connection.close()
                raise
            pair = connection, address
        return pair


def fixture_tls(directory):
    server = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    server.minimum_version = ssl.TLSVersion.TLSv1_2
    server.set_alpn_protocols(["http/1.1"])
    server.load_cert_chain(directory / "server.pem", directory / "server-key.pem")
    client = ssl.create_default_context(cafile=str(directory / "ca.pem"))
    client.minimum_version = ssl.TLSVersion.TLSv1_2
    client.set_alpn_protocols(["http/1.1"])
    return server, client


class FixtureHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def handle(self):
        try:
            super().handle()
        except (ConnectionResetError, ConnectionAbortedError):
            pass  # Expected when cancellation closes a persistent test socket.

    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        text = payload["input"] if self.server.operation == "responses" else payload["messages"][0]["content"]
        tag, mode = text.split(":", 1)
        record = {"events": [], "finished": False, "error": None, "mode": mode,
                  "http": self.request_version,
                  "tls": self.connection.version() if isinstance(self.connection, ssl.SSLSocket) else None,
                  "alpn": self.connection.selected_alpn_protocol() if isinstance(self.connection, ssl.SSLSocket) else None,
                  "tcp_nodelay": self.connection.getsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY),
                  "tls_session_reused": self.connection.session_reused if isinstance(self.connection, ssl.SSLSocket) else None,
                  "peer": f"{self.client_address[0]}:{self.client_address[1]}"}
        with self.server.lock:
            self.server.records.setdefault(tag, []).append(record)
        path = "/v1/responses" if self.server.operation == "responses" else "/v1/chat/completions"
        if self.path != path or self.headers.get("Authorization") != "Bearer " + UPSTREAM_KEY:
            self.send_error(401)
            return
        if mode == "hold" and not self.server.release_held.wait(5):
            record["finished"] = True
            self.send_error(504)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        try:
            wire_events = events(self.server.operation, tag)
            for index, event in enumerate(wire_events):
                if mode == "truncated" and index == 5:
                    break
                start = clock()
                if mode == "timeout" and index == 2:
                    # A legal chunk extension trickles bytes frequently enough
                    # to evade per-recv idle timeouts. Only the absolute watcher
                    # can interrupt read1/getresponse's internal receive loop.
                    for byte in ("%x;fixture=" % len(event)).encode() + b"x" * 100 + b"\r\n":
                        self.wfile.write(bytes([byte]))
                        self.wfile.flush()
                        if self.server.stop_event.wait(.01):
                            return
                    self.wfile.write(event + b"\r\n")
                else:
                    self.wfile.write(("%x\r\n" % len(event)).encode() + event + b"\r\n")
                self.wfile.flush()
                record["events"].append({"index": index, "write_start_ns": start, "flush_return_ns": clock()})
                if mode == "cancel" and index == 2:
                    # A real business event precedes the coordinated cancel.
                    # Comments keep the socket observable without a terminal.
                    record["cancel_wait_started"] = True
                    until = time.monotonic() + 5
                    while not self.server.stop_event.wait(.02):
                        if time.monotonic() >= until:
                            record["cancel_wait_expired"] = True
                            raise TimeoutError("cancel was not observed")
                        self.wfile.write(b"10\r\n: cancel-probe\n\n\r\n")
                        self.wfile.flush()
                    return
                if index < len(wire_events) - 1 and self.server.stop_event.wait(self.server.delay):
                    break
            self.wfile.write(b"0\r\n\r\n")
            self.wfile.flush()
        except (OSError, ConnectionError) as error:
            record["error"] = type(error).__name__
        finally:
            record["finished"] = True


class ClientCanceled(Exception):
    """The probe deliberately shut down its exact socket after the barrier."""


def call(connection, key, fixture, deadline_seconds, mode="normal", cancel_barrier=None):
    tag = uuid.uuid4().hex
    native = fixture.operation == "responses"
    payload = json.dumps({"model": "baseline-model", "stream": True,
                          **({"input": tag + ":" + mode, "store": False} if native else
                             {"messages": [{"role": "user", "content": tag + ":" + mode}]})})
    started = clock()
    deadline = started + int(deadline_seconds * 1e9)
    sample = {"fixture_id": tag, "mode": mode, "success": False, "failure": None,
              "headers_ns": None, "first_body_byte_ns": None, "first_effective_text_ns": None,
              "events": [], "http_status": None}
    response = None
    watcher = None
    watcher_stop = threading.Event()
    deadline_expired = threading.Event()
    text, finish, usage, done = [], False, None, 0
    native_created, native_text_done = 0, 0
    native_response_id = None
    buffer = b""
    try:
        connection.timeout = deadline_seconds
        if connection.sock is not None:
            connection.sock.settimeout(deadline_seconds)
        else:
            connection.connect()
        # Capture this exact socket, never the reusable connection object.
        # shutdown interrupts recv even if HTTPResponse retains a socket file.
        request_socket = connection.sock
        if isinstance(request_socket, ssl.SSLSocket):
            sample["client_tls"] = {"version": request_socket.version(), "alpn": request_socket.selected_alpn_protocol()}
        def expire_request():
            if not watcher_stop.wait(max(0, (deadline - clock()) / 1e9)):
                deadline_expired.set()
                with contextlib.suppress(OSError):
                    request_socket.shutdown(socket.SHUT_RDWR)
                request_socket.close()
        watcher = threading.Thread(target=expire_request, daemon=True)
        watcher.start()
        connection.request("POST", "/v1/responses" if native else "/v1/chat/completions", payload,
                           {"Authorization": "Bearer " + key, "Content-Type": "application/json"})
        if connection.sock is not None:
            sample["client_local_port"] = connection.sock.getsockname()[1]
            connection.sock.settimeout(max(.001, (deadline - clock()) / 1e9))
        response = connection.getresponse()
        sample["headers_ns"] = clock() - started
        sample["http_status"] = response.status
        sample["http_version"] = response.version
        if response.status != 200 or response.version != 11:
            # Retain only a bounded error code; arbitrary upstream bodies may
            # contain credentials or private request content.
            try:
                failure = json.loads(response.read(4097)).get("error", {}).get("type", "")
                if (isinstance(failure, str) and len(failure) <= 80 and failure.isascii()
                        and failure.replace("_", "").isalnum()):
                    sample["http_error_type"] = failure
            except (ValueError, AttributeError):
                pass
            raise ValueError("http_status")
        first = True
        while True:
            remaining = (deadline - clock()) / 1e9
            if remaining <= 0:
                raise TimeoutError("request_deadline")
            if connection.sock is not None:
                connection.sock.settimeout(remaining)
            block = response.read1(1 if first else 65536)
            received = clock()
            if not block:
                break
            if first:
                sample["first_body_byte_ns"] = received - started
                first = False
            buffer += block
            if len(buffer) > 1024 * 1024:
                raise ValueError("event_limit")
            while b"\n\n" in buffer:
                event, buffer = buffer.split(b"\n\n", 1)
                index = len(sample["events"])
                sample["events"].append({"index": index, "received_ns": received})
                data = b"\n".join(line[5:].lstrip(b" ") for line in event.splitlines() if line.startswith(b"data:"))
                if not data:
                    continue
                if data == b"[DONE]":
                    done += 1
                    continue
                chunk = json.loads(data)
                if native:
                    typ = chunk.get("type")
                    if typ == "response.created":
                        document = chunk["response"]
                        if document["status"] != "in_progress" or document["model"] != "baseline-model" or not document["id"].startswith("resp_"):
                            raise ValueError("invalid_created_response")
                        native_created += 1
                        native_response_id = document["id"]
                    elif typ == "response.output_text.done":
                        if chunk["text"] != "".join(TEXT):
                            raise ValueError("invalid_done_text")
                        native_text_done += 1
                    elif typ == "response.completed":
                        document = chunk["response"]
                        if document["status"] != "completed" or document["model"] != "baseline-model" or document["id"] != native_response_id or document["output"][0]["content"][0]["text"] != "".join(TEXT):
                            raise ValueError("invalid_terminal_response")
                        finish, usage = True, document["usage"]
                        done += 1
                    elif typ != "response.output_text.delta":
                        raise ValueError("unexpected_responses_event")
                    deltas = [chunk.get("delta", "")] if typ == "response.output_text.delta" else []
                else:
                    deltas = [choice.get("delta", {}).get("content", "") for choice in chunk.get("choices", [])]
                    if any(choice.get("finish_reason") == "stop" for choice in chunk.get("choices", [])):
                        finish = True
                    if "usage" in chunk:
                        usage = chunk["usage"]
                for delta in deltas:
                    if delta:
                        if sample["first_effective_text_ns"] is None:
                            sample["first_effective_text_ns"] = received - started
                        text.append(delta)
                        if cancel_barrier is not None:
                            if text != [TEXT[0]]:
                                raise ValueError("cancel_content_mismatch")
                            cancel_barrier.wait(timeout=min(2, max(.001, (deadline - clock()) / 1e9)))
                            sample["cancel_requested_ns"] = clock()
                            with contextlib.suppress(OSError):
                                request_socket.shutdown(socket.SHUT_RDWR)
                            request_socket.close()
                            connection.close()
                            raise ClientCanceled()
        expected_usage = {"input_tokens": 11, "output_tokens": 7, "total_tokens": 18} if native else {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
        if buffer or text != TEXT or not finish or done != 1 or usage != expected_usage or native and (native_created != 1 or native_text_done != 1):
            raise ValueError("protocol_incomplete_or_content_mismatch")
        if len(sample["events"]) != len(events(fixture.operation)):
            raise ValueError("event_count_mismatch")
        sample["success"] = True
    except ClientCanceled:
        sample["failure"] = "client_cancel"
    except threading.BrokenBarrierError:
        sample["failure"] = "cancel_barrier"
        connection.close()
    except (OSError, ValueError, KeyError, TypeError, IndexError, http.client.HTTPException) as error:
        sample["failure"] = "timeout" if isinstance(error, (TimeoutError, socket.timeout)) else str(error)[:160]
        connection.close()
    finally:
        watcher_stop.set()
        if watcher is not None:
            watcher.join()  # No watcher can outlive this call or close a reused socket.
        if deadline_expired.is_set() or clock() >= deadline:
            sample["success"], sample["failure"] = False, "timeout"
            connection.close()
        if response is not None:
            response.close()
    sample["elapsed_ns"] = clock() - started
    if cancel_barrier is not None:
        sample["cancel_protocol_observation"] = {"text": text, "finish": finish, "done": done, "usage": usage}
    # A timed-out client must not leave the fault-injection handler overlapping
    # the next measured request. This drain is excluded from request latency.
    drain_until = time.monotonic() + 2
    while time.monotonic() < drain_until:
        with fixture.lock:
            attempts = fixture.records.get(tag, [])
            if not attempts or all(item["finished"] for item in attempts):
                break
        time.sleep(.005)
    # The client and fixture use one perf_counter clock, including on Windows.
    with fixture.lock:
        attempts = fixture.records.get(tag, [])
        sample["upstream_attempts"] = len(attempts)
        sample["upstream_handler_finished"] = all(item["finished"] for item in attempts)
        sample["drain_failure"] = not sample["upstream_handler_finished"]
        sample["upstream_write_errors"] = [item["error"] for item in attempts if item["error"]]
        if cancel_barrier is not None:
            sample["upstream_cancellation_observed"] = (
                len(attempts) == 1 and attempts[0]["finished"]
                and attempts[0].get("cancel_wait_started", False)
                and not attempts[0].get("cancel_wait_expired", False)
                and attempts[0]["error"] is not None)
        sent = list(attempts[0]["events"]) if len(attempts) == 1 else []
        sample["upstream_transport"] = [{key: attempt[key] for key in ("http", "tls", "alpn", "tcp_nodelay", "tls_session_reused", "peer")} for attempt in attempts]
    sample["upstream_events"] = sent
    if sample["drain_failure"]:
        sample["success"], sample["failure"] = False, "drain_failure"
    for event, upstream in zip(sample["events"], sent):
        event["latency_lower_ns"] = max(0, event["received_ns"] - upstream["flush_return_ns"])
        event["latency_upper_ns"] = event["received_ns"] - upstream["write_start_ns"]
    if sample["success"] and sample["upstream_attempts"] != 1:
        sample["success"], sample["failure"] = False, "attempt_count_mismatch"
    if sample["success"] and fixture.tls_context is not None and any(
            item["tls"] not in ("TLSv1.2", "TLSv1.3") or item["http"] != "HTTP/1.1" or item["alpn"] != "http/1.1"
            or item["tcp_nodelay"] != int(fixture.tcp_nodelay)
            for item in sample["upstream_transport"]):
        sample["success"], sample["failure"] = False, "upstream_tls_mismatch"
    return sample


def quantiles(values):
    if not values:
        return None
    ordered = sorted(values)
    return {"count": len(values), "p50": ordered[math.ceil(len(values) * .5) - 1],
            "p95": ordered[math.ceil(len(values) * .95) - 1],
            "p99": ordered[math.ceil(len(values) * .99) - 1] if len(values) >= 100 else "insufficient_samples"}


def summarize(samples):
    successful = [s for s in samples if s["success"]]
    failures = {}
    for sample in samples:
        if not sample["success"]:
            name = sample["failure"] or "unknown"
            failures[name] = failures.get(name, 0) + 1
    event_latency = {key: quantiles([event[key] for s in successful for event in s["events"] if key in event])
                     for key in ("latency_lower_ns", "latency_upper_ns")}
    if len(successful) < 100:
        for value in event_latency.values():
            if value is not None:
                value["p99"] = "insufficient_independent_requests"
    return {"started": len(samples), "completed": len(successful),
            "completion_rate": len(successful) / len(samples) if samples else None, "failures": failures,
            "upstream_attempts": sum(s["upstream_attempts"] for s in samples),
            "observed_client_connections": len({s["client_local_port"] for s in samples if "client_local_port" in s}),
            "observed_upstream_peers": len({a["peer"] for s in samples for a in s["upstream_transport"]}),
            "upstream_requests_on_resumed_tls_session": sum(a["tls_session_reused"] is True for s in samples for a in s["upstream_transport"]),
            "successful_event_latency_ns": event_latency,
            "successful_latency_ns": {key: quantiles([s[key] for s in successful]) for key in
                                      ("headers_ns", "first_body_byte_ns", "first_effective_text_ns", "elapsed_ns")}}


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--samples", type=int, default=10)
    parser.add_argument("--warmup", type=int, default=2)
    parser.add_argument("--event-delay-ms", type=int, default=5)
    parser.add_argument("--deadline-seconds", type=float, default=3)
    parser.add_argument("--self-check", action="store_true")
    parser.add_argument("--operation", choices=("chat", "responses"), default="chat")
    parser.add_argument("--tls-upstream", action="store_true", help="Linux: verified ephemeral TLS upstream; gateway edge remains plaintext")
    parser.add_argument("--connection-policy", choices=("persistent", "fresh"), default="persistent")
    parser.add_argument("--fixture-tcp-nodelay", type=int, choices=(0, 1), default=0, help="TLS fixture accepted sockets only; never changes gateway transport")
    args = parser.parse_args()
    if not (1 <= args.samples <= 1000 and 0 <= args.warmup <= 20 and 1 <= args.event_delay_ms <= 100 and .2 <= args.deadline_seconds <= 30):
        parser.error("samples 1..1000, warmup 0..20, delay 1..100ms, deadline .2..30s")
    if args.tls_upstream and platform.system() != "Linux":
        parser.error("TLS production baseline requires Linux process-scoped SSL_CERT_FILE; no host trust changes")
    if args.fixture_tcp_nodelay and not args.tls_upstream:
        parser.error("fixture-tcp-nodelay requires the TLS upstream profile")
    clean = {k: v for k, v in os.environ.items() if not k.upper().startswith(("GATEWAY_", "TXAPI_"))}
    clean["GOARCH"] = "amd64"
    fixture = Fixture(args.event_delay_ms / 1000, args.operation, bool(args.fixture_tcp_nodelay))
    thread = threading.Thread(target=fixture.serve_forever, daemon=True)
    thread.start()
    report = {"schema": 2, "source": {"sha": git("rev-parse", "HEAD"), "dirty": bool(git("status", "--porcelain")),
                                     "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                                     "tls_fixture_harness_sha256": hashlib.sha256((ROOT / "tests/upstream/nativews/main.go").read_bytes()).hexdigest() if args.tls_upstream else None,
                                     "resources_harness_sha256": hashlib.sha256((ROOT / "tests/smoke/process_resources.py").read_bytes()).hexdigest()},
              "environment": {"platform": platform.platform(), "python": platform.python_version(), "logical_cpus": os.cpu_count(),
                              "go": subprocess.check_output([args.go, "version"], env=clean, text=True).strip(), "goarch": "amd64"},
              "configuration": {"samples": args.samples, "warmup": args.warmup, "event_delay_ms": args.event_delay_ms,
                                "deadline_seconds": args.deadline_seconds, "http": "HTTP/1.1 loopback; verified TLS upstream, plaintext gateway edge" if args.tls_upstream else "HTTP/1.1 loopback plaintext", "concurrency": 1,
                                "connection_policy": args.connection_policy,
                                "connection_timing_scope": "connect included; previous close excluded; persistent sockets close on failure",
                                "fixture_tcp_nodelay": args.fixture_tcp_nodelay if args.tls_upstream else "unchanged",
                                "billing": "disabled",
                                "tls_upstream": args.tls_upstream,
                                "certificate_policy": "verified ephemeral CA and loopback IP; process-scoped SSL_CERT_FILE" if args.tls_upstream else None,
                                "auth": "production API Key middleware", "request_logging": "production defaults",
                                "operation": args.operation, "responses_store": False if args.operation == "responses" else None,
                                "fixture_events": len(events(args.operation)), "fixture_payload_bytes": sum(map(len, events(args.operation))),
                                "resource_scope": RESOURCE_SCOPE + "; serial phase combines direct/gateway and retains idle HTTP connections",
                                "event_latency_scope": "complete-event observation latency includes client scheduling and reading waits; lower/upper bounds use upstream write/flush interval"},
              "samples": [], "self_check": [], "drain_failures": []}
    try:
        import tempfile
        with tempfile.TemporaryDirectory(prefix="txapi-stream-baseline-") as directory:
            work = Path(directory)
            tls_client = None
            if args.tls_upstream:
                helper = work / "tls-fixture"
                subprocess.run([args.go, "build", "-o", str(helper), "./tests/upstream/nativews"], cwd=ROOT, env=clean, check=True, timeout=120)
                certificates = work / "certificates"
                subprocess.run([str(helper), "-mode", "prepare", "-directory", str(certificates)], env=clean, check=True, timeout=15)
                fixture.tls_context, tls_client = fixture_tls(certificates)
                clean["SSL_CERT_FILE"] = str(certificates / "ca.pem")
            binary = work / ("gateway.exe" if os.name == "nt" else "gateway")
            subprocess.run([args.go, "build", "-o", str(binary), "./cmd/gateway"], cwd=ROOT, env=clean, check=True, timeout=240)
            port = ports()[0]
            config = {"server": {"addr": f"127.0.0.1:{port}", "retry_times": 0, "timeout_seconds": 5, "total_timeout_seconds": 5},
                      "admin_key": "sk-baseline-admin-only", "billing": {"mode": "disabled"},
                      "database": {"driver": "sqlite", "path": str(work / "gateway.db")},
                      "channels": [{"name": "baseline", "type": "openai", "base_url": f"{'https' if args.tls_upstream else 'http'}://127.0.0.1:{fixture.server_port}",
                                    "key": UPSTREAM_KEY, "models": ["baseline-model"]}],
                      "tokens": [{"name": "baseline", "key": CLIENT_KEY, "models": ["baseline-model"]}]}
            if args.operation == "responses":
                content_keys = work / "content-keys.json"
                content_keys.write_text(json.dumps({"active_key": "fixture", "keys": {
                    "fixture": base64.b64encode(os.urandom(32)).decode()},
                    "digest_key": base64.b64encode(os.urandom(32)).decode()}), encoding="utf-8")
                content_keys.chmod(0o600)
                config["responses"] = {"enabled": True, "keys_file": str(content_keys)}
                credential_keys = work / "credential-keys.json"
                credential_keys.write_text(json.dumps({"active_key": "fixture", "keys": {
                    "fixture": base64.b64encode(os.urandom(32)).decode()},
                    "digest_key": base64.b64encode(os.urandom(32)).decode()}), encoding="utf-8")
                credential_keys.chmod(0o600)
                config["database"]["credential_keys_file"] = str(credential_keys)
            config_path = work / "config.json"
            config_path.write_text(json.dumps(config), encoding="utf-8")
            command = [str(binary), "-config", str(config_path)]
            with (work / "setup.log").open("w") as setup:
                for extra, env in [(["-init-admin"], dict(clean, TXAPI_ADMIN_USERNAME="baseline-admin", TXAPI_ADMIN_PASSWORD="baseline-admin-password-only")),
                                   (["-seed-only"], clean), (["-claim-token", "1", "-owner-user", "1"], clean)]:
                    subprocess.run(command + extra, cwd=ROOT, env=env, stdout=setup, stderr=setup, check=True, timeout=30)
            with contextlib.ExitStack() as services:
                process = services.enter_context(service(command, work / "gateway.log", clean))
                ready(process, port)
                connections = {"direct": http.client.HTTPSConnection("127.0.0.1", fixture.server_port, context=tls_client) if args.tls_upstream else http.client.HTTPConnection("127.0.0.1", fixture.server_port),
                               "gateway": http.client.HTTPConnection("127.0.0.1", port)}
                resources = None
                def run(target, mode="normal", deadline=None, pair=None):
                    if args.connection_policy == "fresh":
                        connections[target].close()
                    result = call(connections[target], UPSTREAM_KEY if target == "direct" else CLIENT_KEY,
                                  fixture, deadline or args.deadline_seconds, mode)
                    result["target"] = target
                    if pair is not None:
                        result["pair"] = pair
                    if result["drain_failure"]:
                        report["drain_failures"].append(result)
                        if pair is not None:
                            report["samples"].append(result)
                        raise RuntimeError("upstream drain_failure; stopped before next request")
                    return result
                try:
                    if args.self_check:
                        denied = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
                        denied.request("GET", "/v1/models", headers={"Authorization": "Bearer sk-baseline-invalid-only"})
                        denied_response = denied.getresponse()
                        assert denied_response.status == 401, "production API Key middleware did not reject invalid key"
                        denied_response.read()
                        denied.close()
                        report["auth_rejection_self_check"] = "passed_401"
                        for target in connections:
                            normal = run(target)
                            timeout = run(target, "timeout", .1)
                            truncated = run(target, "truncated")
                            summary = summarize([normal, truncated, timeout])
                            assert normal["success"] and not truncated["success"] and not timeout["success"], (target, summary)
                            assert timeout["failure"] == "timeout" and summary["started"] == 3 and summary["completed"] == 1
                            assert timeout["elapsed_ns"] < 500_000_000, "absolute deadline failed to interrupt trickle receive"
                            assert normal["first_effective_text_ns"] > normal["first_body_byte_ns"]
                            report["self_check"].append({"target": target, "summary": summary, "samples": [normal, truncated, timeout]})
                        # The truncated fixture legitimately penalizes credential
                        # health. Use a fresh disposable database/runtime for
                        # measurement rather than clearing production health.
                        for connection in connections.values():
                            connection.close()
                        process.terminate()
                        process.wait(timeout=12)
                        config["database"]["path"] = str(work / "measured-gateway.db")
                        config_path.write_text(json.dumps(config), encoding="utf-8")
                        with (work / "measured-setup.log").open("w") as setup:
                            for extra, env in [(["-init-admin"], dict(clean, TXAPI_ADMIN_USERNAME="baseline-admin", TXAPI_ADMIN_PASSWORD="baseline-admin-password-only")),
                                               (["-seed-only"], clean), (["-claim-token", "1", "-owner-user", "1"], clean)]:
                                subprocess.run(command + extra, cwd=ROOT, env=env, stdout=setup, stderr=setup, check=True, timeout=30)
                        process = services.enter_context(service(command, work / "measured-gateway.log", clean))
                        ready(process, port)
                    for _ in range(args.warmup):
                        for target in connections:
                            sample = run(target)
                            assert sample["success"], sample
                    resources = Resources(process.pid)
                    connections_before = fixture.connections
                    for pair in range(args.samples):
                        for target in (["direct", "gateway"] if pair % 2 == 0 else ["gateway", "direct"]):
                            sample = run(target, pair=pair)
                            report["samples"].append(sample)
                    time.sleep(.1)
                    report["resources"] = resources.finish()
                    resources = None
                    report["fixture_connections_total"] = fixture.connections
                    report["fixture_connections_opened_during_measurement"] = fixture.connections - connections_before
                    report["fixture_active_requests_after_measurement"] = sum(
                        not attempt["finished"] for attempts in fixture.records.values() for attempt in attempts)
                finally:
                    if resources is not None:
                        report["resources"] = resources.finish()
                    for connection in connections.values():
                        connection.close()
    except Exception as error:
        report["fatal_error"] = type(error).__name__ + ": " + str(error)[:500]
        report["error_chain"] = []
        while error is not None and len(report["error_chain"]) < 4:
            report["error_chain"].append({"type": type(error).__name__, "message": str(error)[:500]})
            error = error.__cause__ or error.__context__
    finally:
        fixture.stop_event.set()
        fixture.shutdown()
        fixture.server_close()
        thread.join(2)
    report["summary"] = {target: summarize([s for s in report["samples"] if s["target"] == target]) for target in ("direct", "gateway")}
    paired = []
    for pair in range(args.samples):
        values = {s["target"]: s for s in report["samples"] if s["pair"] == pair}
        if len(values) == 2 and all(s["success"] for s in values.values()):
            paired.append({key: values["gateway"][key] - values["direct"][key] for key in
                           ("headers_ns", "first_body_byte_ns", "first_effective_text_ns", "elapsed_ns")})
    report["paired_success_differences_ns"] = {key: quantiles([s[key] for s in paired]) for key in
                                             ("headers_ns", "first_body_byte_ns", "first_effective_text_ns", "elapsed_ns")}
    report["paired_success_count"] = len(paired)
    report["planned_measurement_requests"] = 2 * args.samples
    report["started_measurement_requests"] = len(report["samples"])
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"output": str(args.output.resolve()), "summary": report["summary"], "self_check_targets": len(report["self_check"])}, ensure_ascii=False))
    if (report.get("fatal_error") or report["drain_failures"] or len(report["samples"]) != 2 * args.samples
            or len(paired) != args.samples or any(not sample["success"] for sample in report["samples"])):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
