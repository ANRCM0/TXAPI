"""Finite same-host Chat SSE comparison: direct, TXAPI main and fixed new-api."""

import argparse
import base64
import contextlib
import http.client
import json
import os
from pathlib import Path
import platform
import socket
import sqlite3
import subprocess
import tempfile
import threading
import time
import uuid

from local import ROOT, ports, ready
from process_resources import RESOURCE_SCOPE, Resources
from reference_build import REFERENCE_SHA, digest
from sdk_conformance import clean_environment, gateway_process
from stream_baseline import CLIENT_KEY, UPSTREAM_KEY, TEXT, Fixture, clock, git, quantiles


def api(port, path, token=None, body=None, method=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    with contextlib.closing(http.client.HTTPConnection("127.0.0.1", port, timeout=10)) as connection:
        connection.request(method or ("POST" if body is not None else "GET"), path,
                           json.dumps(body) if body is not None else None, headers)
        response = connection.getresponse()
        raw = response.read(1 << 20)
        value = json.loads(raw)
        if response.status != 200 or not value.get("success"):
            raise RuntimeError(f"reference bootstrap rejected {path}: HTTP {response.status}")
        return value.get("data")


def setup_reference(port, upstream_port):
    password = "Reference-only-2026!Password"
    setup = api(port, "/api/setup")
    if setup["status"] or setup["root_init"]:
        raise RuntimeError("reference database was not empty")
    api(port, "/api/setup", body={"username": "ref-admin", "password": password, "confirmPassword": password,
                                  "SelfUseModeEnabled": False, "DemoSiteEnabled": False})
    session = api(port, "/api/user/login", body={"username": "ref-admin", "password": password})["access_token"]
    options = {"ModelRatio": json.dumps({"baseline-model": 0}), "RetryTimes": "0", "LogConsumeEnabled": "true"}
    for key, value in options.items():
        api(port, "/api/option/", session, {"key": key, "value": value}, "PUT")
    api(port, "/api/channel/", session, {"mode": "single", "channel": {
        "name": "loopback-only", "type": 1, "status": 1, "key": UPSTREAM_KEY,
        "base_url": f"http://127.0.0.1:{upstream_port}", "models": "baseline-model", "group": "default"}})
    api(port, "/api/token/", session, {"name": "reference-only", "remain_quota": 1000000,
                                      "unlimited_quota": False, "expired_time": -1, "group": "default",
                                      "model_limits_enabled": True, "model_limits": "baseline-model"})
    listing = api(port, "/api/token/", session)
    items = listing["items"]
    if len(items) != 1 or items[0]["name"] != "reference-only":
        raise RuntimeError("reference token bootstrap did not create exactly one token")
    token = api(port, f"/api/token/{items[0]['id']}/key", session, {})["key"]
    return "sk-" + token


def call(connection, key, fixture, seconds=5, mode="normal"):
    tag = uuid.uuid4().hex
    sample = {"fixture_id": tag, "success": False, "failure": None, "headers_ns": None,
              "first_body_byte_ns": None, "first_effective_text_ns": None, "events": [], "http_status": None}
    started = clock()
    expired = threading.Event()
    timer = None
    response = None
    received_text = ""
    observed = []
    finish, done, usage, frames = 0, 0, None, 0
    try:
        connection.timeout = seconds
        reused = connection.sock is not None
        if not reused:
            connection.connect()
        exact_socket = connection.sock
        exact_socket.settimeout(seconds)
        sample.update(client_local_port=exact_socket.getsockname()[1], reused_client_socket=reused)

        def interrupt():
            expired.set()
            with contextlib.suppress(OSError):
                exact_socket.shutdown(socket.SHUT_RDWR)
            exact_socket.close()

        timer = threading.Timer(max(.001, seconds - (clock() - started) / 1e9), interrupt)
        timer.daemon = True
        timer.start()
        body = {"model": "baseline-model", "stream": True, "stream_options": {"include_usage": True},
                "messages": [{"role": "user", "content": tag + ":" + mode}]}
        connection.request("POST", "/v1/chat/completions", json.dumps(body),
                           {"Authorization": "Bearer " + key, "Content-Type": "application/json"})
        response = connection.getresponse()
        sample.update(headers_ns=clock() - started, http_status=response.status, http_version=response.version)
        if response.status != 200:
            response.read(65536)
            raise ValueError("http_rejected")
        if response.version != 11 or not response.getheader("Content-Type", "").startswith("text/event-stream"):
            raise ValueError("response_protocol_mismatch")
        buffer, wire_bytes = b"", 0
        while data := response.read1(4096):
            now = clock()
            wire_bytes += len(data)
            if wire_bytes > 1 << 20:
                raise ValueError("response_exceeds_limit")
            if sample["first_body_byte_ns"] is None:
                sample["first_body_byte_ns"] = now - started
            buffer = (buffer + data).replace(b"\r\n", b"\n")
            while b"\n\n" in buffer:
                block, buffer = buffer.split(b"\n\n", 1)
                payload = b"\n".join(line[5:].lstrip(b" ") for line in block.splitlines() if line.startswith(b"data:"))
                if not payload:
                    continue
                frames += 1
                if payload == b"[DONE]":
                    done += 1
                    continue
                if done:
                    raise ValueError("data_after_done")
                value = json.loads(payload)
                if value.get("error"):
                    raise ValueError("provider_error")
                if value.get("usage") is not None:
                    usage = {name: value["usage"].get(name) for name in ("prompt_tokens", "completion_tokens", "total_tokens")}
                for choice in value.get("choices", []):
                    if choice.get("index", 0) != 0:
                        raise ValueError("unexpected_choice")
                    delta = choice.get("delta", {}).get("content") or ""
                    if delta:
                        if finish or not isinstance(delta, str):
                            raise ValueError("invalid_text_delta")
                        if sample["first_effective_text_ns"] is None:
                            sample["first_effective_text_ns"] = now - started
                        previous = len(received_text)
                        received_text += delta
                        if not "".join(TEXT).startswith(received_text):
                            raise ValueError("text_mismatch")
                        offset = 0
                        for index, part in enumerate(TEXT):
                            offset += len(part)
                            if previous < offset <= len(received_text):
                                observed.append({"fragment": index, "received_ns": now})
                    if choice.get("finish_reason") is not None:
                        if choice["finish_reason"] != "stop":
                            raise ValueError("unexpected_finish")
                        finish += 1
        if (buffer.strip() or received_text != "".join(TEXT) or finish != 1 or done != 1
                or usage != {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}):
            raise ValueError("incomplete_semantic_response")
        sample.update(success=True, observed_data_frames=frames, observed_usage=usage)
    except (OSError, ValueError, KeyError, TypeError, http.client.HTTPException) as error:
        sample["failure"] = type(error).__name__ if not isinstance(error, ValueError) else str(error)[:80]
        connection.close()
    finally:
        if timer is not None:
            timer.cancel()
            timer.join()
        if response is not None:
            response.close()
        if expired.is_set() or clock() - started >= int(seconds * 1e9):
            sample.update(success=False, failure="absolute_deadline")
            connection.close()
    sample["elapsed_ns"] = clock() - started
    deadline = time.monotonic() + 2
    while True:
        with fixture.lock:
            attempts = fixture.records.get(tag, [])
            drained = all(item["finished"] for item in attempts)
        if drained or time.monotonic() >= deadline:
            break
        time.sleep(.005)
    with fixture.lock:
        attempts = fixture.records.get(tag, [])
        sample.update(upstream_attempts=len(attempts), drain_failure=not all(a["finished"] for a in attempts),
                      upstream_peers=[a["peer"] for a in attempts])
        sent = list(attempts[0]["events"]) if len(attempts) == 1 else []
    for item in observed:
        index = item["fragment"] + 2  # Heartbeat and assistant role precede text.
        if index < len(sent):
            sample["events"].append({"fragment": item["fragment"],
                                     "latency_lower_ns": max(0, item["received_ns"] - sent[index]["flush_return_ns"]),
                                     "latency_upper_ns": item["received_ns"] - sent[index]["write_start_ns"]})
    if sample["success"] and (len(attempts) != 1 or sample["drain_failure"] or len(sample["events"]) != len(TEXT)):
        sample.update(success=False, failure="attempt_drain_or_fragment_mismatch")
    return sample


def summary(samples):
    successful = [item for item in samples if item["success"]]
    return {"started": len(samples), "completed": len(successful),
            "failures": [item["failure"] for item in samples if not item["success"]],
            "upstream_attempts": sum(item["upstream_attempts"] for item in samples),
            "observed_client_connections": len({item["client_local_port"] for item in samples if "client_local_port" in item}),
            "observed_upstream_peers": len({peer for item in samples for peer in item["upstream_peers"]}),
            "successful_latency_ns": {name: quantiles([item[name] for item in successful]) for name in
                                      ("headers_ns", "first_body_byte_ns", "first_effective_text_ns", "elapsed_ns")},
            "successful_text_fragment_latency_upper_ns": quantiles([event["latency_upper_ns"] for item in successful for event in item["events"]])}


def run(args, report):
    metadata = json.loads((args.reference / "build.json").read_text(encoding="utf-8"))
    reference = Path(metadata["binary"])
    if metadata["sha"] != REFERENCE_SHA or metadata["status"] != "built" or digest(reference) != metadata["binary_sha256"]:
        raise ValueError("reference binary does not match its fixed-source build record")
    report["reference_build"] = metadata
    env = clean_environment()
    env["GOARCH"] = "amd64"
    fixture = Fixture(.005)
    thread = threading.Thread(target=fixture.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="txapi-reference-run-") as temporary, contextlib.ExitStack() as stack:
            work = Path(temporary)
            gateway = work / ("gateway.exe" if os.name == "nt" else "gateway")
            subprocess.run([args.go, "build", "-o", str(gateway), "./cmd/gateway"], cwd=ROOT, env=env, check=True, timeout=240)
            tx_port, ref_port = ports()
            credential_keys = work / "credential-keys.json"
            credential_keys.write_text(json.dumps({"active_key": "fixture", "keys": {
                "fixture": base64.b64encode(os.urandom(32)).decode()},
                "digest_key": base64.b64encode(os.urandom(32)).decode()}))
            credential_keys.chmod(0o600)
            config = {"server": {"addr": f"127.0.0.1:{tx_port}", "retry_times": 0, "timeout_seconds": 5},
                      "admin_key": "reference-comparison-admin-only", "billing": {"mode": "disabled"},
                      "database": {"path": str(work / "gateway.db"), "credential_keys_file": str(credential_keys)},
                      "channels": [{"name": "reference-comparison", "type": "openai", "key": UPSTREAM_KEY,
                                    "base_url": f"http://127.0.0.1:{fixture.server_port}", "models": ["baseline-model"]}],
                      "tokens": [{"name": "client", "key": CLIENT_KEY, "models": ["baseline-model"]}]}
            config_file = work / "config.json"
            config_file.write_text(json.dumps(config))
            for flags, command_env in [(["-init-admin"], dict(env, TXAPI_ADMIN_USERNAME="bench-admin", TXAPI_ADMIN_PASSWORD="bench-password-only")),
                                       (["-seed-only"], env), (["-claim-token", "1", "-owner-user", "1"], env)]:
                subprocess.run([str(gateway), "-config", str(config_file), *flags], cwd=ROOT, env=command_env,
                               check=True, timeout=30, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            tx = stack.enter_context(gateway_process(gateway, config_file, work / "txapi.log", env))
            ready(tx, tx_port)
            # Reference gets an explicit environment, never host SQL/Redis/auth settings.
            reference_env = {name: env[name] for name in ("PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "HOME", "USERPROFILE") if name in env}
            reference_env.update(PORT=str(ref_port), SQLITE_PATH=str(work / "reference.db"),
                                 SESSION_SECRET=uuid.uuid4().hex + uuid.uuid4().hex, CHANNEL_TEST_ENABLED="false",
                                 BATCH_UPDATE_ENABLED="false", RELAY_TIMEOUT="5", GIN_MODE="release")
            with (work / "reference.log").open("w", encoding="utf-8") as log:
                ref = subprocess.Popen([str(reference), "-log-dir", str(work / "reference-logs")],
                                       cwd=work, env=reference_env, stdout=log, stderr=log)
                def stop_reference():
                    if ref.poll() is None:
                        ref.terminate()
                        try:
                            ref.wait(timeout=12)
                        except subprocess.TimeoutExpired:
                            ref.kill()
                            ref.wait(timeout=5)
                stack.callback(stop_reference)
                ready(ref, ref_port)
                reference_key = setup_reference(ref_port, fixture.server_port)
                targets = {"direct": (fixture.server_port, UPSTREAM_KEY), "txapi": (tx_port, CLIENT_KEY),
                           "new_api": (ref_port, reference_key)}
                connections = {name: http.client.HTTPConnection("127.0.0.1", port) for name, (port, _) in targets.items()}
                for connection in connections.values():
                    stack.callback(connection.close)
                for mode, seconds in (("truncated", 5), ("timeout", .1)):
                    negative = call(connections["direct"], UPSTREAM_KEY, fixture, seconds, mode)
                    report["client_self_checks"].append({"mode": mode, "sample": negative})
                    if negative["success"] or negative["upstream_attempts"] != 1 or negative["drain_failure"]:
                        raise RuntimeError("comparison client accepted an incomplete or undrained fault")
                    if mode == "timeout" and (negative["failure"] != "absolute_deadline" or negative["elapsed_ns"] > 500_000_000):
                        raise RuntimeError("comparison client absolute deadline did not interrupt trickle receive")
                for name in ("txapi", "new_api"):
                    rejected = call(connections[name], "sk-invalid-comparison-key", fixture)
                    report["auth_checks"].append({"target": name, "sample": rejected})
                    if rejected["http_status"] != 401 or rejected["upstream_attempts"] != 0:
                        raise RuntimeError("invalid Key was not a zero-send 401")
                for _ in range(2):
                    for name, (_, key) in targets.items():
                        sample = call(connections[name], key, fixture)
                        report["warmup"].append({"target": name, "sample": sample})
                        if not sample["success"]:
                            raise RuntimeError("common-path warmup failed")
                report["resources"] = {}
                for round_number in range(args.rounds):
                    resources = {"txapi": Resources(tx.pid), "new_api": Resources(ref.pid), "harness": Resources(os.getpid())}
                    try:
                        for pair in range(args.samples):
                            order = list(targets)
                            offset = (round_number + pair) % len(order)
                            for name in order[offset:] + order[:offset]:
                                sample = call(connections[name], targets[name][1], fixture)
                                sample.update(target=name, pair=pair, round=round_number)
                                report["samples"].append(sample)
                                if not sample["success"]:
                                    raise RuntimeError("measurement failed; remaining planned requests were not started")
                        time.sleep(.1)
                    finally:
                        report["resources"][str(round_number)] = {name: sampler.finish() for name, sampler in resources.items()}
                with fixture.lock:
                    report["active_upstream_after_measurement"] = sum(not item["finished"] for attempts in fixture.records.values() for item in attempts)
                deadline = time.monotonic() + 5
                expected_logs = 2 + args.samples * args.rounds
                while True:
                    with contextlib.closing(sqlite3.connect((work / "reference.db").as_uri() + "?mode=ro", uri=True, timeout=3)) as db:
                        persisted_logs = db.execute("SELECT COUNT(*) FROM logs WHERE type=2 AND model_name='baseline-model'").fetchone()[0]
                        quota = db.execute("SELECT remain_quota,used_quota FROM tokens WHERE name='reference-only'").fetchone()
                    if persisted_logs == expected_logs:
                        break
                    if time.monotonic() >= deadline:
                        raise RuntimeError("reference consumption log did not preserve every completed request")
                    time.sleep(.05)
                report["reference_persisted_consumption"] = {"rows": persisted_logs, "expected_rows": expected_logs,
                                                             "token_quota_unchanged": quota == (1000000, 0)}
                if quota != (1000000, 0):
                    raise RuntimeError("reference zero-priced fixture changed its finite token quota")
                stop_reference()
                stack.pop_all().close()
    finally:
        fixture.stop_event.set()
        fixture.shutdown()
        fixture.server_close()
        thread.join(timeout=3)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", type=Path, required=True, help="Completed reference_build.py output directory")
    parser.add_argument("--go", default="go")
    parser.add_argument("--samples", type=int, default=20)
    parser.add_argument("--rounds", type=int, default=2)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not (1 <= args.samples <= 1000 and 1 <= args.rounds <= 10):
        parser.error("samples 1..1000, rounds 1..10")
    report = {"schema": 1, "source": {"sha": git("rev-parse", "HEAD"), "dirty": bool(git("status", "--porcelain")),
              "harness_sha256": digest(Path(__file__)), "stream_fixture_sha256": digest(ROOT / "tests/smoke/stream_baseline.py"),
              "resources_sha256": digest(ROOT / "tests/smoke/process_resources.py")},
              "environment": {"platform": platform.platform(), "python": platform.python_version(), "logical_cpus": os.cpu_count()},
              "configuration": {"operation": "Chat text SSE", "http": "HTTP/1.1 loopback plaintext", "concurrency": 1,
              "samples_per_target_per_round": args.samples, "rounds": args.rounds, "warmup_per_target": 2,
              "event_delay_ms": 5, "client_policy": "persistent, close on failure", "request_deadline_seconds": 5,
              "resource_scope": RESOURCE_SCOPE, "auth": "production API Key middleware; public bootstrap only",
              "retry_times": 0, "txapi_billing": "disabled", "reference_billing": "explicit zero model ratio; finite token quota",
              "reference_background_monitoring": "channel testing disabled; batch balance updates disabled",
              "non_equivalence": ["Independent authorization, quota/pricing, caching, admission and logging implementations",
                                  "Reference zero-priced model is not TXAPI disabled billing internals",
                                  "No cross-product ranking or common numeric performance gate"],
              "event_latency_scope": "Same-clock complete text-fragment observation against its own provider write/flush; merged frames cover each completed fragment"},
              "samples": [], "warmup": [], "auth_checks": [], "client_self_checks": []}
    try:
        run(args, report)
    except Exception as error:
        report["fatal_error"] = type(error).__name__ + ": " + str(error)[:180]
    report["summary"] = {name: summary([s for s in report["samples"] if s["target"] == name]) for name in ("direct", "txapi", "new_api")}
    report["planned_measurement_requests"] = args.samples * args.rounds * 3
    report["started_measurement_requests"] = len(report["samples"])
    report["paired_differences_ns"] = {}
    for name in ("txapi", "new_api"):
        pairs = []
        for round_number in range(args.rounds):
            for pair in range(args.samples):
                values = {s["target"]: s for s in report["samples"] if s["round"] == round_number and s["pair"] == pair}
                if "direct" in values and name in values and values["direct"]["success"] and values[name]["success"]:
                    pairs.append({metric: values[name][metric] - values["direct"][metric] for metric in
                                  ("headers_ns", "first_body_byte_ns", "first_effective_text_ns", "elapsed_ns")})
        report["paired_differences_ns"][name] = {metric: quantiles([s[metric] for s in pairs]) for metric in
                                                ("headers_ns", "first_body_byte_ns", "first_effective_text_ns", "elapsed_ns")}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"output": str(args.output.resolve()), "summary": report["summary"], "fatal_error": report.get("fatal_error")}, ensure_ascii=False))
    if (report.get("fatal_error") or report["started_measurement_requests"] != report["planned_measurement_requests"]
            or any(not sample["success"] for sample in report["samples"]) or report.get("active_upstream_after_measurement") != 0):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
