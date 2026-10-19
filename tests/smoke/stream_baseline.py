"""Paired loopback HTTP/1.1 text-SSE baseline against the production gateway.

Only synthetic keys/configuration and disposable SQLite state are used. This is
an observation baseline, not a performance acceptance gate or Provider test.
"""
import argparse
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
import subprocess
import threading
import time
import uuid

from local import ROOT, ports, ready, service

CLIENT_KEY = "sk-baseline-client-only"
UPSTREAM_KEY = "sk-baseline-upstream-only"
TEXT = ["你好", "🌏", " text2", " text3", " text4", " text5", " text6", " text7"]


def clock():
    return time.perf_counter_ns()


def events():
    values = [b": heartbeat\n\n"]
    chunks = [{"choices": [{"delta": {"role": "assistant"}}]}]
    chunks += [{"choices": [{"delta": {"content": text}}]} for text in TEXT]
    chunks += [{"choices": [{"delta": {}, "finish_reason": "stop"}]},
               {"choices": [], "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}}]
    values += [("data: " + json.dumps(c, ensure_ascii=False, separators=(",", ":")) + "\n\n").encode() for c in chunks]
    return values + [b"data: [DONE]\n\n"]


class Fixture(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, delay):
        super().__init__(("127.0.0.1", 0), FixtureHandler)
        self.delay = delay
        self.records = {}
        self.lock = threading.Lock()
        self.stop_event = threading.Event()
        self.connections = 0
        self.release_held = threading.Event()

    def get_request(self):
        pair = super().get_request()
        with self.lock:
            self.connections += 1
        return pair


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
        tag, mode = payload["messages"][0]["content"].split(":", 1)
        record = {"events": [], "finished": False, "error": None, "mode": mode}
        with self.server.lock:
            self.server.records.setdefault(tag, []).append(record)
        if self.path != "/v1/chat/completions" or self.headers.get("Authorization") != "Bearer " + UPSTREAM_KEY:
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
            for index, event in enumerate(events()):
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
                if index < len(events()) - 1 and self.server.stop_event.wait(self.server.delay):
                    break
            self.wfile.write(b"0\r\n\r\n")
            self.wfile.flush()
        except (OSError, ConnectionError) as error:
            record["error"] = type(error).__name__
        finally:
            record["finished"] = True


def resource(pid):
    if platform.system() != "Linux":
        return {"status": "unsupported", "platform": platform.system()}
    try:
        proc = Path("/proc") / str(pid)
        # comm may contain spaces or parentheses; fields start after its last ).
        fields = (proc / "stat").read_text().rsplit(")", 1)[1].split()
        status = dict(line.split(":", 1) for line in (proc / "status").read_text().splitlines())
        return {"status": "available", "cpu_seconds": (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK"),
                "rss_bytes": int(status["VmRSS"].split()[0]) * 1024,
                "process_lifetime_peak_rss_bytes": int(status["VmHWM"].split()[0]) * 1024,
                "threads": int(status["Threads"]), "fd_count": len(list((proc / "fd").iterdir()))}
    except (OSError, KeyError, ValueError):
        return {"status": "unavailable"}


class Resources:
    def __init__(self, gateway_pid):
        self.pids = {"gateway": gateway_pid, "harness_and_fixture": os.getpid()}
        self.before = {k: resource(v) for k, v in self.pids.items()}
        self.peaks = {k: dict(v) for k, v in self.before.items()}
        self.stop = threading.Event()
        self.thread = threading.Thread(target=self.sample, daemon=True)
        self.thread.start()

    def sample(self):
        while not self.stop.wait(.05):
            for name, pid in self.pids.items():
                value = resource(pid)
                if value["status"] == "available":
                    for key in ("rss_bytes", "threads", "fd_count"):
                        self.peaks[name][key] = max(self.peaks[name].get(key, 0), value[key])

    def finish(self):
        self.stop.set()
        self.thread.join(1)
        after = {k: resource(v) for k, v in self.pids.items()}
        result = {"before": self.before, "sampled_peaks": self.peaks, "after_drain": after,
                  "sampling_interval_ms": 50, "cpu_delta_seconds": {}}
        for name in self.pids:
            if self.before[name]["status"] == after[name]["status"] == "available":
                result["cpu_delta_seconds"][name] = after[name]["cpu_seconds"] - self.before[name]["cpu_seconds"]
            else:
                result["cpu_delta_seconds"][name] = "unsupported"
        return result


def call(connection, key, fixture, deadline_seconds, mode="normal"):
    tag = uuid.uuid4().hex
    payload = json.dumps({"model": "baseline-model", "stream": True,
                          "messages": [{"role": "user", "content": tag + ":" + mode}]})
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
        def expire_request():
            if not watcher_stop.wait(max(0, (deadline - clock()) / 1e9)):
                deadline_expired.set()
                with contextlib.suppress(OSError):
                    request_socket.shutdown(socket.SHUT_RDWR)
                request_socket.close()
        watcher = threading.Thread(target=expire_request, daemon=True)
        watcher.start()
        connection.request("POST", "/v1/chat/completions", payload,
                           {"Authorization": "Bearer " + key, "Content-Type": "application/json"})
        if connection.sock is not None:
            sample["client_local_port"] = connection.sock.getsockname()[1]
            connection.sock.settimeout(max(.001, (deadline - clock()) / 1e9))
        response = connection.getresponse()
        sample["headers_ns"] = clock() - started
        sample["http_status"] = response.status
        sample["http_version"] = response.version
        if response.status != 200 or response.version != 11:
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
                for choice in chunk.get("choices", []):
                    delta = choice.get("delta", {}).get("content", "")
                    if delta:
                        if sample["first_effective_text_ns"] is None:
                            sample["first_effective_text_ns"] = received - started
                        text.append(delta)
                    if choice.get("finish_reason") == "stop":
                        finish = True
                if "usage" in chunk:
                    usage = chunk["usage"]
        if buffer or text != TEXT or not finish or done != 1 or usage != {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}:
            raise ValueError("protocol_incomplete_or_content_mismatch")
        if len(sample["events"]) != len(events()):
            raise ValueError("event_count_mismatch")
        sample["success"] = True
    except (OSError, ValueError, http.client.HTTPException) as error:
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
        sent = list(attempts[0]["events"]) if len(attempts) == 1 else []
    sample["upstream_events"] = sent
    if sample["drain_failure"]:
        sample["success"], sample["failure"] = False, "drain_failure"
    for event, upstream in zip(sample["events"], sent):
        event["latency_lower_ns"] = max(0, event["received_ns"] - upstream["flush_return_ns"])
        event["latency_upper_ns"] = event["received_ns"] - upstream["write_start_ns"]
    if sample["success"] and sample["upstream_attempts"] != 1:
        sample["success"], sample["failure"] = False, "attempt_count_mismatch"
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
    args = parser.parse_args()
    if not (1 <= args.samples <= 1000 and 0 <= args.warmup <= 20 and 1 <= args.event_delay_ms <= 100 and .2 <= args.deadline_seconds <= 30):
        parser.error("samples 1..1000, warmup 0..20, delay 1..100ms, deadline .2..30s")
    clean = {k: v for k, v in os.environ.items() if not k.upper().startswith(("GATEWAY_", "TXAPI_"))}
    clean["GOARCH"] = "amd64"
    fixture = Fixture(args.event_delay_ms / 1000)
    thread = threading.Thread(target=fixture.serve_forever, daemon=True)
    thread.start()
    report = {"schema": 1, "source": {"sha": git("rev-parse", "HEAD"), "dirty": bool(git("status", "--porcelain")),
                                     "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest()},
              "environment": {"platform": platform.platform(), "python": platform.python_version(), "logical_cpus": os.cpu_count(),
                              "go": subprocess.check_output([args.go, "version"], env=clean, text=True).strip(), "goarch": "amd64"},
              "configuration": {"samples": args.samples, "warmup": args.warmup, "event_delay_ms": args.event_delay_ms,
                                "deadline_seconds": args.deadline_seconds, "http": "HTTP/1.1 loopback plaintext", "concurrency": 1,
                                "connection_policy": "persistent per target; close on failure", "billing": "disabled",
                                "auth": "production API Key middleware", "request_logging": "production defaults",
                                "fixture_events": len(events()), "fixture_payload_bytes": sum(map(len, events())),
                                "resource_scope": "fixture threads and client share harness PID; sampled peaks are lower bounds; after_drain retains idle HTTP connections",
                                "event_latency_scope": "complete-event observation latency includes client scheduling and reading waits; lower/upper bounds use upstream write/flush interval"},
              "samples": [], "self_check": [], "drain_failures": []}
    try:
        import tempfile
        with tempfile.TemporaryDirectory(prefix="txapi-stream-baseline-") as directory:
            work = Path(directory)
            binary = work / ("gateway.exe" if os.name == "nt" else "gateway")
            subprocess.run([args.go, "build", "-o", str(binary), "./cmd/gateway"], cwd=ROOT, env=clean, check=True, timeout=240)
            port = ports()[0]
            config = {"server": {"addr": f"127.0.0.1:{port}", "retry_times": 0, "timeout_seconds": 5, "total_timeout_seconds": 5},
                      "admin_key": "sk-baseline-admin-only", "billing": {"mode": "disabled"},
                      "database": {"driver": "sqlite", "path": str(work / "gateway.db")},
                      "channels": [{"name": "baseline", "type": "openai", "base_url": f"http://127.0.0.1:{fixture.server_port}",
                                    "key": UPSTREAM_KEY, "models": ["baseline-model"]}],
                      "tokens": [{"name": "baseline", "key": CLIENT_KEY, "models": ["baseline-model"]}]}
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
                connections = {"direct": http.client.HTTPConnection("127.0.0.1", fixture.server_port),
                               "gateway": http.client.HTTPConnection("127.0.0.1", port)}
                resources = None
                def run(target, mode="normal", deadline=None, pair=None):
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
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"output": str(args.output.resolve()), "summary": report["summary"], "self_check_targets": len(report["self_check"])}, ensure_ascii=False))
    if report.get("fatal_error") or report["drain_failures"] or any(not sample["success"] for sample in report["samples"]):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
