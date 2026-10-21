"""Declared capacity tiers against the real gateway, with complete child reports.

The highest passing configured tier is a bounded observation, not an arbitrary
maximum or a production sizing guarantee. A failed tier stops later load tiers.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import sys
import time

from local import ROOT
from stream_baseline import git


LATENCY_BUDGET_NS = {"first_effective_text_ns": 2_000_000_000,
                     "elapsed_ns": 3_000_000_000,
                     "event_upper_ns": 1_000_000_000}


def run_child(command, clean, timeout):
    child = subprocess.Popen(command, cwd=ROOT, env=clean, start_new_session=os.name != "nt")
    try:
        return child.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        if os.name == "nt":
            subprocess.run(["taskkill", "/PID", str(child.pid), "/T", "/F"], check=False, timeout=20,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        else:
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        try:
            child.wait(timeout=20)
        except subprocess.TimeoutExpired:
            if os.name != "nt":
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            child.kill()
            child.wait(timeout=5)
        raise


def acceptance(report):
    errors = []
    if report.get("fatal_error") or report.get("correctness_failures"):
        errors.append("child correctness or runtime failure")
    if not report.get("planned_measurement_requests") or report.get("started_measurement_requests") != report.get("planned_measurement_requests"):
        errors.append("planned measurement requests were not all started")
    if not report.get("planned_cancellations") or report.get("observed_cancellations") != report.get("planned_cancellations"):
        errors.append("cancellation wave incomplete")
    sustained = report.get("sustained", [])
    budget = sustained[0].get("resource_budget", {}) if len(sustained) == 1 else {}
    if not all(budget.get(key) for key in ("passed", "known", "enforced")):
        errors.append("sustained resource budget missing, unknown or exceeded")
    for value in sustained:
        summary = value.get("summary", {})
        if not value.get("started") or summary.get("completed") != value.get("started") or value.get("active_after_drain") != 0:
            errors.append("sustained completion or drain failed")
        for key, limit in LATENCY_BUDGET_NS.items():
            quantiles = (summary.get("successful_event_latency_ns", {}).get("latency_upper_ns")
                         if key == "event_upper_ns" else summary.get("successful_latency_ns", {}).get(key))
            observed = quantiles.get("p95") if isinstance(quantiles, dict) else None
            if not isinstance(observed, (int, float)) or observed > limit:
                errors.append(key + " p95 missing or above declared budget")
    resources = report.get("resources", [])
    if not resources or any(value.get("fixture_active_after_drain") != 0 for value in resources):
        errors.append("phase drain evidence missing or failed")
    return {"passed": not errors, "errors": errors}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--levels", default="1,4,8,16")
    parser.add_argument("--operations", nargs="+", choices=("chat", "responses"), default=["chat", "responses"])
    parser.add_argument("--sustain-seconds", type=int, default=60)
    args = parser.parse_args()
    try:
        levels = [int(value) for value in args.levels.split(",")]
    except ValueError:
        parser.error("levels must be comma-separated integers")
    if (not levels or len(levels) > 8 or levels != sorted(set(levels)) or
            not all(1 <= value <= 16 for value in levels) or len(set(args.operations)) != len(args.operations)):
        parser.error("levels must be 1..8 increasing unique values in 1..16; operations must be unique")
    if not 60 <= args.sustain_seconds <= 1800 or args.sustain_seconds * len(levels) * len(args.operations) > 7200:
        parser.error("sustain-seconds 60..1800; total declared sustained duration <=7200s")
    args.output = args.output.resolve()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    children = args.output.parent / (args.output.stem + "-phases")
    children.mkdir(exist_ok=False)
    report = {"schema": 1, "source": {"sha": git("rev-parse", "HEAD"),
              "dirty": bool(git("status", "--porcelain")),
              "sha256": {name: hashlib.sha256((ROOT / "tests/smoke" / name).read_bytes()).hexdigest()
                         for name in ("capacity_curve.py", "capacity_baseline.py", "stream_baseline.py", "process_resources.py")}},
              "environment": {"platform": platform.platform(), "python": platform.python_version()},
              "configuration": {"levels": levels, "operations": args.operations,
                  "sustain_seconds_per_tier": args.sustain_seconds, "event_delay_ms": 100,
                  "requests_per_worker": 5, "rounds": 1, "deadline_seconds": 5,
                  "latency_budget_ns": LATENCY_BUDGET_NS,
                  "resource_budget": {"rss_bytes": 256 * 1024 * 1024, "fd_count": 512,
                      "private_commit_bytes": 256 * 1024 * 1024, "handle_count": 512, "average_cpu_cores": .5},
                  "billing": "disabled", "retries": 0, "stop_policy": "first failing tier stops higher tiers for that operation",
                  "scope": "real production main and SQLite; HTTP/1.1 loopback; separate process per tier; highest passing declared tier only"},
              "phases": [], "highest_passing_tier": {}, "failures": []}
    started = time.monotonic()
    clean = {key: value for key, value in os.environ.items() if not key.upper().startswith(("GATEWAY_", "TXAPI_"))}
    clean["GOARCH"] = "amd64"
    try:
        for operation in args.operations:
            report["highest_passing_tier"][operation] = None
            for workers in levels:
                output = children / f"{operation}-{workers}.json"
                phase = {"operation": operation, "workers": workers, "report": str(output.relative_to(args.output.parent)),
                         "exit_code": None, "acceptance": {"passed": False, "errors": ["phase did not finish"]}}
                report["phases"].append(phase)
                command = [sys.executable, str(ROOT / "tests/smoke/capacity_baseline.py"), "--go", args.go,
                           "--operation", operation, "--workers", str(workers), "--requests-per-worker", "5",
                           "--rounds", "1", "--event-delay-ms", "100", "--deadline-seconds", "5",
                           "--sustain-seconds", str(args.sustain_seconds), "--enforce-sustain-budget", "--output", str(output)]
                try:
                    phase["exit_code"] = run_child(command, clean, args.sustain_seconds + 300)
                    if output.exists():
                        child = json.loads(output.read_text(encoding="utf-8"))
                        phase["acceptance"] = acceptance(child)
                        phase["report_sha256"] = hashlib.sha256(output.read_bytes()).hexdigest()
                    if phase["exit_code"] != 0:
                        phase["acceptance"]["passed"] = False
                        phase["acceptance"]["errors"].append("child exited unsuccessfully")
                except Exception as error:
                    phase["acceptance"] = {"passed": False, "errors": [type(error).__name__ + ": " + str(error)[:300]]}
                print(json.dumps({"capacity_tier": phase}), flush=True)
                if not phase["acceptance"]["passed"]:
                    report["failures"].append({"operation": operation, "workers": workers})
                    break
                report["highest_passing_tier"][operation] = workers
    finally:
        report["elapsed_seconds"] = time.monotonic() - started
        report["planned_phases"] = len(levels) * len(args.operations)
        report["started_phases"] = len(report["phases"])
        report["upper_boundary"] = "declared tier ceiling reached; greater capacity was not tested"
        args.output.write_text(json.dumps(report, indent=2), encoding="utf-8")
    print(json.dumps({key: report[key] for key in ("highest_passing_tier", "failures", "elapsed_seconds", "upper_boundary")}), flush=True)
    if report["failures"] or report["started_phases"] != report["planned_phases"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
