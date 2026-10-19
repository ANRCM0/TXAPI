"""Bounded concurrent HTTP/1.1 SSE baseline and admission recovery, loopback only.

Runs the production main with synthetic API Keys and a disposable SQLite DB.
Performance is reported, not gated. Overload rejection and recovery are checked.
"""
import argparse
import concurrent.futures
import contextlib
import hashlib
import http.client
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import threading
import time

from stream_baseline import (CLIENT_KEY, UPSTREAM_KEY, Fixture, call,
                             clock, events, git, summarize)
from local import ROOT, ports, ready, service
from process_resources import RESOURCE_SCOPE, Resources


class CapacityFixture(Fixture):
    # Keep the test server's listen backlog from being the 1..16 client limit.
    request_queue_size = 64


def active(fixture, mode=None):
    with fixture.lock:
        return sum(not item["finished"] and (mode is None or item["mode"] == mode)
                   for attempts in fixture.records.values() for item in attempts)


def wait_idle(fixture):
    deadline = time.monotonic() + 2
    while active(fixture) and time.monotonic() < deadline:
        time.sleep(.005)
    if active(fixture):
        raise RuntimeError("upstream did not drain before next phase")


def wave(target, port, fixture, workers, requests, deadline):
    start = threading.Barrier(workers + 1, timeout=5)
    samples = []
    lock = threading.Lock()

    def worker(index):
        connection = http.client.HTTPConnection("127.0.0.1", port)
        try:
            start.wait()
            for sequence in range(requests):
                value = call(connection, UPSTREAM_KEY if target == "direct" else CLIENT_KEY,
                             fixture, deadline)
                value.update(worker=index, sequence=sequence, target=target)
                with lock:
                    samples.append(value)
                if not value["success"]:
                    break
        finally:
            connection.close()

    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        jobs = [pool.submit(worker, i) for i in range(workers)]
        started = clock()
        start.wait()
        errors = []
        for job in jobs:
            try:
                job.result()
            except Exception as error:
                errors.append(type(error).__name__ + ": " + str(error)[:160])
        elapsed = clock() - started
    wait_idle(fixture)
    return {"target": target, "planned": workers * requests, "elapsed_ns": elapsed,
            "completed_per_second": sum(s["success"] for s in samples) * 1e9 / elapsed,
            "summary": summarize(samples), "worker_errors": errors, "samples": samples}


def cancel_wave(fixture, port, workers, deadline, results):
    """Cancel only after every worker has observed a valid business event."""
    barrier = threading.Barrier(workers)
    result = {"planned": workers, "samples": [], "worker_errors": []}
    results.append(result)

    def worker():
        connection = http.client.HTTPConnection("127.0.0.1", port)
        try:
            return call(connection, CLIENT_KEY, fixture, deadline, "cancel", barrier)
        finally:
            connection.close()

    started = clock()
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        jobs = [pool.submit(worker) for _ in range(workers)]
        for job in jobs:
            try:
                result["samples"].append(job.result())
            except Exception as error:
                result["worker_errors"].append(type(error).__name__ + ": " + str(error)[:160])
    result["elapsed_including_drain_ns"] = clock() - started
    result["observed_cancellations"] = sum(
        not s["success"] and s["failure"] == "client_cancel" and s["http_status"] == 200
        and s["first_effective_text_ns"] is not None and s.get("cancel_requested_ns") is not None
        and s["upstream_attempts"] == 1 and s["upstream_cancellation_observed"] and not s["drain_failure"]
        and not s["cancel_protocol_observation"]["finish"] and s["cancel_protocol_observation"]["done"] == 0
        and s["cancel_protocol_observation"]["usage"] is None for s in result["samples"])
    times = [s["cancel_requested_ns"] for s in result["samples"] if "cancel_requested_ns" in s]
    result["cancel_request_spread_ns"] = max(times) - min(times) if times else None
    result["active_after_cancel"] = active(fixture)
    if (result["worker_errors"] or len(result["samples"]) != workers
            or result["observed_cancellations"] != workers or result["active_after_cancel"] != 0):
        raise RuntimeError("not every worker canceled one accepted stream and drained its upstream")
    return result


def overload(fixture, port, limit, deadline, results):
    """Fill the Key's slots, prove no-send 429, release and probe recovery."""
    fixture.release_held.clear()
    held = []

    def hold():
        connection = http.client.HTTPConnection("127.0.0.1", port)
        try:
            return call(connection, CLIENT_KEY, fixture, deadline, "hold")
        finally:
            connection.close()

    result = {"configured_key_limit": limit, "holders": held, "recovery_probes": []}
    results.append(result)
    with concurrent.futures.ThreadPoolExecutor(max_workers=limit) as pool:
        jobs = [pool.submit(hold) for _ in range(limit)]
        try:
            until = time.monotonic() + min(2, deadline / 2)
            while active(fixture, "hold") != limit and time.monotonic() < until:
                time.sleep(.005)
            result["observed_held_requests"] = active(fixture, "hold")
            if result["observed_held_requests"] != limit:
                raise RuntimeError("could not fill configured Key capacity")
            connection = http.client.HTTPConnection("127.0.0.1", port)
            try:
                rejection = call(connection, CLIENT_KEY, fixture, deadline)
            finally:
                connection.close()
            result["rejected"] = rejection
            if rejection["success"] or rejection["http_status"] != 429 or rejection["upstream_attempts"] != 0:
                raise RuntimeError("overload was not a no-send 429")
        finally:
            released = clock()
            fixture.release_held.set()
            for job in jobs:
                held.append(job.result())
    if not all(s["success"] and s["upstream_attempts"] == 1 for s in held):
        raise RuntimeError("accepted holders did not complete exactly once")
    wait_idle(fixture)
    # A response EOF can race the request owner's final lease cleanup. Only
    # no-send 429 probes may repeat, and every probe stays in the report.
    until = time.monotonic() + 2
    started = clock()
    while True:
        connection = http.client.HTTPConnection("127.0.0.1", port)
        try:
            probe = call(connection, CLIENT_KEY, fixture, deadline)
        finally:
            connection.close()
        result["recovery_probes"].append(probe)
        if probe["success"]:
            break
        if (probe["http_status"] != 429 or probe["upstream_attempts"] != 0
                or time.monotonic() >= until):
            raise RuntimeError("capacity did not recover after releasing holders")
        time.sleep(.01)
    result["post_drain_probe_latency_ns"] = clock() - started
    # A single successful request proves at least one free slot. Fill every
    # slot again to detect a partial lease leak as well.
    fixture.release_held.clear()
    result["recovery_holders"] = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=limit) as pool:
        jobs = [pool.submit(hold) for _ in range(limit)]
        try:
            until = time.monotonic() + min(2, deadline / 2)
            while active(fixture, "hold") != limit and time.monotonic() < until:
                time.sleep(.005)
            result["observed_recovered_capacity"] = active(fixture, "hold")
            if result["observed_recovered_capacity"] != limit:
                raise RuntimeError("full Key capacity did not recover")
        finally:
            fixture.release_held.set()
            for job in jobs:
                result["recovery_holders"].append(job.result())
    if not all(s["success"] and s["upstream_attempts"] == 1 for s in result["recovery_holders"]):
        raise RuntimeError("recovered holders did not complete exactly once")
    wait_idle(fixture)
    result["recovery_observed_ns"] = clock() - released
    result["active_after_recovery"] = active(fixture)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--requests-per-worker", type=int, default=25)
    parser.add_argument("--rounds", type=int, default=2)
    parser.add_argument("--event-delay-ms", type=int, default=5)
    parser.add_argument("--deadline-seconds", type=float, default=5)
    args = parser.parse_args()
    if not (1 <= args.workers <= 16 and 1 <= args.requests_per_worker <= 200
            and 1 <= args.rounds <= 4 and 1 <= args.event_delay_ms <= 100
            and 3 <= args.deadline_seconds <= 15
            and args.workers * args.requests_per_worker * args.rounds <= 3200):
        parser.error("workers 1..16, requests 1..200, rounds 1..4, total/target <=3200, delay 1..100ms, deadline 3..15s")
    clean = {k: v for k, v in os.environ.items() if not k.upper().startswith(("GATEWAY_", "TXAPI_"))}
    clean["GOARCH"] = "amd64"
    report = {"schema": 2, "source": {"sha": git("rev-parse", "HEAD"), "dirty": bool(git("status", "--porcelain")),
              "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
              "resources_harness_sha256": hashlib.sha256((ROOT / "tests/smoke/process_resources.py").read_bytes()).hexdigest(),
              "stream_harness_sha256": hashlib.sha256((ROOT / "tests/smoke/stream_baseline.py").read_bytes()).hexdigest()},
              "environment": {"platform": platform.platform(), "python": platform.python_version(),
                              "logical_cpus": os.cpu_count(), "goarch": "amd64",
                              "go": subprocess.check_output([args.go, "version"], env=clean, text=True).strip()},
              "configuration": vars(args) | {"output": str(args.output), "http": "HTTP/1.1 loopback plaintext",
                  "auth": "production API Key", "billing": "disabled", "retries": 0,
                  "connection_policy": "persistent per worker/phase; close on failure and at phase end",
                  "fixture_backlog": 64, "fixture_events": len(events()),
                  "resource_scope": RESOURCE_SCOPE + "; cancel/overload/direct/gateway phase intervals are separate",
                  "cancellation_policy": "one wave per round; all workers observe first text before barrier/socket shutdown; intentional cancellations reported separately",
                  "limits": "bounded closed-loop workload, not maximum capacity, long-duration soak, TLS/H2 or Provider acceptance"},
              "waves": [], "cancellations": [], "overload": [], "resources": [], "correctness_failures": []}
    fixture = CapacityFixture(args.event_delay_ms / 1000)
    thread = threading.Thread(target=fixture.serve_forever, daemon=True)
    thread.start()
    resources = None
    try:
        with tempfile.TemporaryDirectory(prefix="txapi-capacity-baseline-") as directory:
            work = Path(directory)
            binary = work / ("gateway.exe" if os.name == "nt" else "gateway")
            subprocess.run([args.go, "build", "-o", str(binary), "./cmd/gateway"], cwd=ROOT, env=clean, check=True, timeout=240)
            port = ports()[0]
            config = {"server": {"addr": f"127.0.0.1:{port}", "retry_times": 0,
                       "timeout_seconds": 20, "total_timeout_seconds": 20,
                       "concurrency": {"instance": args.workers + 2, "user": args.workers + 1,
                                       "key": args.workers, "channel": args.workers}},
                      "admin_key": "sk-capacity-admin-only", "billing": {"mode": "disabled"},
                      "database": {"driver": "sqlite", "path": str(work / "gateway.db")},
                      "channels": [{"name": "baseline", "type": "openai", "base_url": f"http://127.0.0.1:{fixture.server_port}",
                                    "key": UPSTREAM_KEY, "models": ["baseline-model"]}],
                      "tokens": [{"name": "baseline", "key": CLIENT_KEY, "models": ["baseline-model"]}]}
            report["configuration"]["capacity"] = config["server"]["concurrency"]
            path = work / "config.json"
            path.write_text(json.dumps(config), encoding="utf-8")
            command = [str(binary), "-config", str(path)]
            with (work / "setup.log").open("w") as log:
                for extra, env in [(["-init-admin"], dict(clean, TXAPI_ADMIN_USERNAME="baseline-admin", TXAPI_ADMIN_PASSWORD="baseline-admin-password-only")),
                                   (["-seed-only"], clean), (["-claim-token", "1", "-owner-user", "1"], clean)]:
                    subprocess.run(command + extra, cwd=ROOT, env=env, stdout=log, stderr=log, check=True, timeout=30)
            with service(command, work / "gateway.log", clean) as process:
                ready(process, port)
                for round_index in range(args.rounds):
                    for target in ("cancel", "overload") + (("direct", "gateway") if round_index % 2 == 0 else ("gateway", "direct")):
                        resources = Resources(process.pid)
                        before_connections = fixture.connections
                        if target == "cancel":
                            value = cancel_wave(fixture, port, args.workers, args.deadline_seconds, report["cancellations"])
                            value["round"] = round_index
                        elif target == "overload":
                            value = overload(fixture, port, args.workers, args.deadline_seconds, report["overload"])
                            value["round"] = round_index
                        else:
                            value = wave(target, fixture.server_port if target == "direct" else port, fixture,
                                         args.workers, args.requests_per_worker, args.deadline_seconds)
                            value["round"] = round_index
                            report["waves"].append(value)
                            if (value["worker_errors"] or len(value["samples"]) != value["planned"]
                                    or value["summary"]["completed"] != value["planned"]):
                                report["correctness_failures"].append({"round": round_index, "target": target,
                                                                       "summary": value["summary"]})
                        time.sleep(.1)
                        value = resources.finish()
                        resources = None
                        value.update(round=round_index, phase=target,
                                     fixture_connections_opened=fixture.connections - before_connections,
                                     fixture_active_after_drain=active(fixture))
                        report["resources"].append(value)
    except Exception as error:
        report["fatal_error"] = type(error).__name__ + ": " + str(error)[:500]
    finally:
        if resources is not None:
            value = resources.finish()
            value.update(round=round_index, phase=target,
                         fixture_connections_opened=fixture.connections - before_connections,
                         fixture_active_after_drain=active(fixture))
            report["resources"].append(value)
        fixture.release_held.set()
        fixture.stop_event.set()
        fixture.shutdown()
        fixture.server_close()
        thread.join(2)
    report["summary"] = {target: summarize([s for w in report["waves"] if w["target"] == target for s in w["samples"]])
                         for target in ("direct", "gateway")}
    report["started_measurement_requests"] = sum(s["started"] for s in report["summary"].values())
    report["planned_measurement_requests"] = 2 * args.workers * args.requests_per_worker * args.rounds
    report["planned_cancellations"] = args.workers * args.rounds
    report["observed_cancellations"] = sum(x.get("observed_cancellations", 0) for x in report["cancellations"])
    report["unpaired_distribution_differences_ns"] = {
        key: {q: report["summary"]["gateway"]["successful_latency_ns"][key][q] -
              report["summary"]["direct"]["successful_latency_ns"][key][q] for q in ("p50", "p95")}
        for key in ("first_body_byte_ns", "first_effective_text_ns")
        if all(report["summary"][t]["successful_latency_ns"][key] for t in ("direct", "gateway"))}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"output": str(args.output.resolve()), "summary": report["summary"],
                      "overload_rounds": len(report["overload"]), "canceled": report["observed_cancellations"],
                      "fatal_error": report.get("fatal_error")}, ensure_ascii=False))
    if (report.get("fatal_error") or report["correctness_failures"]
            or report["started_measurement_requests"] != report["planned_measurement_requests"]
            or report["observed_cancellations"] != report["planned_cancellations"]):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
