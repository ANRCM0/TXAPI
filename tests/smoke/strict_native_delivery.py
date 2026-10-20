"""Bounded Linux Docker consumer of static-key strict Responses/WS/background.

Only the final shipped main owns billing. Fixture transcripts and read-only,
stopped-container SQLite copies are verification evidence, never authority.
No external provider, host DNS/trust edits, database writes or forced expiry.
"""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import threading
import time
import uuid

from native_ws_delivery import ADMIN, CLIENT_KEY, ENV, PROVIDER_KEY, Installation, run, until
from local import ROOT


class StrictInstallation(Installation):
    def __init__(self, work, image, helper):
        super().__init__(work, image, helper)
        self.provider_mode = "strict-serve"
        self.settings["usage_history"]["enabled"] = False
        self.settings["responses"]["background"] = {"enabled": True, "channel_ids": [1]}
        self.settings["responses_ws"] = {"enabled": True, "models": ["m"]}
        self.settings["channels"] = [self.settings["channels"][0]]
        self.settings["channels"][0].update(models=["alias"], model_mapping={"alias": "m"})
        self.settings["tokens"][0]["models"] = ["alias"]
        self.settings["billing"] = {
            # The whole-input selling bound is 0.6336; provider tariff is separate.
            "mode": "strict", "max_request_cost": "1.00", "max_provider_request_cost": "0.10",
            "provider_contracts": [{"channel_id": 1, "base_url": "https://api.openai.com", "protocol": "openai",
                "actual_model": "m", "operation": "responses", "output_limit_field": "max_output_tokens",
                "input_token_ceiling": 4096, "input_bound_profile": "operator_responses_v1", "max_output_tokens": 32,
                "input_price_per_1k": "0.001", "output_price_per_1k": "0.002", "currency": "USD",
                "tariff_profile": "operator_declared_tokens_v1", "policy_version": "isolated-static-fixture-v1",
                "policy_expires_at": int(time.time()) + 3600, "max_request_bytes": 4096,
                "additional_cost_per_attempt": "0",
                "credential_bindings": {"1": hashlib.sha256(PROVIDER_KEY.encode()).hexdigest()},
                "evidence": "Synthetic isolated provider only: whole input <=4096 tokens, output <=32, all classes included; <=32 control RPCs have zero extra fee. Not a real-provider invoice attestation."}]}
        self.resource_limits = ["--cpus", "2", "--memory", "512m", "--pids-limit", "128"]
        self.save()

    def call(self, body, *, idempotency="", path="/v1/responses", method="POST", status=200):
        fields = {"key": CLIENT_KEY, "path": path, "method": method,
                  "payload": "" if body is None else json.dumps(body)}
        if idempotency:
            fields["idempotency"] = idempotency
        result = json.loads(self.helper("http", **fields).stdout)
        assert result["status"] == status, result
        return result

    def restart_strict(self):
        run(["docker", "start", self.gateway])
        self.ready()

    def snapshot(self, name, *, killed=False):
        if not killed:
            run(["docker", "stop", "--time", "15", self.gateway])
            assert self.inspect()["State"]["ExitCode"] == 0
        else:
            assert not self.inspect()["State"]["Running"]
        copy = self.work / ("ledger-" + name)
        copy.mkdir()
        run(["docker", "cp", self.gateway + ":/app/data/.", str(copy)])
        with contextlib.closing(sqlite3.connect((copy / "gateway.db").as_uri() + "?mode=ro", uri=True)) as db:
            db.row_factory = sqlite3.Row
            assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
            value = {table: [dict(row) for row in db.execute("SELECT * FROM " + table)] for table in
                     ("billing_reservations", "billing_attempts", "billing_balances", "billing_resolutions")}
            value["background_jobs"] = [dict(row) for row in db.execute(
                "SELECT public_id,billing_id,billing_mode,control_calls,stage,native_state FROM background_jobs")]
        return value

    def diagnostics(self):
        value = {}
        for name, container in (("gateway", self.gateway), ("provider", self.provider)):
            if container in self.containers:
                try:
                    value[name + "_logs"] = run(["docker", "logs", "--tail", "200", container], capture=True,
                                                  check=False, timeout=15).stdout[-65536:]
                except Exception as error:
                    value[name + "_log_error"] = type(error).__name__
        if self.provider in self.containers:
            try:
                value["provider_transcript"] = self.stats()
            except Exception as error:
                value["provider_transcript_error"] = type(error).__name__
        return value


def redact_report(value, installation=None):
    secrets = [CLIENT_KEY, PROVIDER_KEY, ADMIN["password"]]
    if installation:
        secrets += [installation.settings["admin_key"], *installation.key_material]
    encoded = json.dumps(value)
    for secret in secrets:
        encoded = encoded.replace(secret, "[REDACTED]")
    return json.loads(encoded)


def payload(value, *, background=False):
    body = {"model": "alias", "input": value, "max_output_tokens": 32, "store": False}
    if background:
        body["background"] = True
    return body


def validate_ledger(value, expected, *, recovering=False):
    """Check every reservation and both owner balances; no derived zero usage."""
    rows = value["billing_reservations"]
    attempts = value["billing_attempts"]
    assert len(rows) == len(expected) == len(attempts), (len(rows), len(expected), len(attempts))
    assert not value["billing_resolutions"], "the consumer must never resolve unknowns"
    summaries = []
    for row in rows:
        rule = expected[row["id"]]
        receipts = [a for a in attempts if a["reservation_id"] == row["id"]]
        assert len(receipts) == 1 and receipts[0]["number"] == 1
        receipt = receipts[0]
        known = rule["usage"] in ("known", "zero")
        assert row["reserved_units"] == 1_000_000_000 and row["provider_budget_units"] == 100_000_000
        assert row["provider_held_units"] == receipt["provider_bound_units"] == 4_160_000
        assert row["provider_policy_version"], row
        if known:
            charge, cost, tokens = (900_000, 900_000, (2, 1)) if rule["usage"] == "known" else (0, 0, (0, 0))
            assert row["state"] == "settled" and row["charge_units"] == charge, row
            assert receipt["cost_known"] == 1 and receipt["cost_units"] == cost, receipt
            assert (receipt["prompt_tokens"], receipt["output_tokens"]) == tokens
        else:
            allowed = ("active", "reserved", "pending") if recovering else ("pending",)
            assert row["state"] in allowed and row["charge_units"] == 0, row
            assert receipt["cost_known"] == 0, receipt
        summaries.append({"id": row["id"], "scenario": rule["scenario"], "usage": rule["usage"],
                          "state": row["state"], "charge_units": row["charge_units"],
                          "selling_hold_units": row["reserved_units"], "provider_hold_units": row["provider_held_units"],
                          "attempts": 1, "cost_known": bool(receipt["cost_known"]),
                          "attempt_selling_cost_units": receipt["cost_units"],
                          "lease_until": row["lease_until"]})
    held = sum(r["reserved_units"] for r in rows if r["state"] != "settled")
    used = sum(r["charge_units"] for r in rows if r["state"] == "settled")
    balances = value["billing_balances"]
    assert {(b["owner_type"], b["owner_id"]) for b in balances} == {("user", 1), ("key", 1)}
    assert all((b["used_units"], b["reserved_units"]) == (used, held) for b in balances), balances
    return {"reservations": summaries, "balances": balances, "background_jobs": value["background_jobs"]}


def exercise(i, report):
    session = i.api("/api/auth/login", body=ADMIN, method="POST")["session_token"]
    i.api("/api/admin/prices/alias", session, {"input_price_per_1k": .15, "output_price_per_1k": .6}, "PUT")
    expected = {}

    def add_snapshot(name, usage, scenario, *, recovering=False, killed=False):
        value = i.snapshot(name, killed=killed)
        new = [r for r in value["billing_reservations"] if r["id"] not in expected]
        assert len(new) == len(usage), (name, new)
        # Multiple additions are WS rounds; classify by receipt facts rather than
        # timestamp/ID ordering, which cannot establish round order.
        for row in new:
            receipt = next(a for a in value["billing_attempts"] if a["reservation_id"] == row["id"])
            kind = usage[0] if len(usage) == 1 else ("unknown" if not receipt["cost_known"] else
                                                   "zero" if receipt["prompt_tokens"] == 0 else "known")
            expected[row["id"]] = {"usage": kind, "scenario": scenario}
        result = validate_ledger(value, expected, recovering=recovering)
        report["checkpoints"].append({"name": name, **result, "raw_ledger": value})
        return value

    for kind in ("known", "zero", "missing"):
        body = payload("http-" + kind)
        first = i.call(body, idempotency="strict-http-" + kind)
        replay = i.call(body, idempotency="strict-http-" + kind)
        assert first["body"]["id"] == replay["body"]["id"] and replay["idempotency"] == "replayed"
        rows = i.stats()["requests"]
        assert len([r for r in rows if r["input"] == "http-" + kind and r["transport"] == "http"]) == 1
        add_snapshot("http-" + kind, ["unknown" if kind == "missing" else kind], "HTTP JSON + idempotent replay")
        i.restart_strict()

    rounds = [dict(payload("ws-" + kind), type="response.create") for kind in ("known", "zero", "missing")]
    events = [json.loads(line) for line in i.helper("strict-ws", key=CLIENT_KEY, payload=json.dumps(rounds)).stdout.splitlines()]
    assert [e["round"] for e in events if e["event"]["type"] == "response.completed"] == [1, 2, 3]
    report["ws_events"] = events
    ws_wire = [r for r in i.stats()["requests"] if r["transport"] == "ws"]
    assert len(ws_wire) == 3 and len({r["connection"] for r in ws_wire}) == 1
    add_snapshot("ws-rounds", ["known", "zero", "unknown"], "one WS connection, three separately financed rounds")
    i.restart_strict()

    for number, kind in enumerate(("known", "zero", "missing", "cancel"), 1):
        body = payload("background-" + kind, background=True)
        first = i.call(body, idempotency="strict-background-" + kind)["body"]
        replay = i.call(body, idempotency="strict-background-" + kind)["body"]
        assert first["id"] == replay["id"]
        until(lambda: any(r["input"] == "background-" + kind and r["transport"] == "http" for r in i.stats()["requests"]),
              "background submit did not reach the provider")
        path = "/v1/responses/" + first["id"]
        if kind == "cancel":
            i.call(None, path=path + "/cancel")
        terminal = until(lambda: (d if (d := i.call(None, path=path, method="GET")["body"])["status"] in
                                  ("completed", "cancelled", "failed", "incomplete") else None), "background terminal", seconds=45)
        assert terminal["status"] == ("cancelled" if kind == "cancel" else "completed"), terminal
        before = len(i.stats()["requests"])
        assert i.call(body, idempotency="strict-background-" + kind)["body"]["id"] == first["id"]
        assert len(i.stats()["requests"]) == before, "terminal replay made another provider RPC"
        value = add_snapshot("background-" + kind, [kind if kind in ("known", "zero") else "unknown"], "native background submit/poll/cancel")
        jobs = value["background_jobs"]
        assert len(jobs) == number
        for job in jobs:
            assert job["billing_mode"] == "strict" and job["billing_id"] in expected and 1 <= job["control_calls"] <= 32
        wire = [r for r in i.stats()["requests"] if r["input"] == "background-" + kind]
        assert len([r for r in wire if r["transport"] == "http"]) == 1 and len(wire) <= 32
        report["background_wire"].append({"scenario": kind, "calls": wire})
        i.restart_strict()

    # Kill the shipped PID1 only after the independent provider observed dispatch.
    errors = []
    def blocked_request():
        try:
            i.call(payload("http-block"), idempotency="strict-kill")
        except Exception as error:
            errors.append(type(error).__name__)
    worker = threading.Thread(target=blocked_request)
    worker.start()
    try:
        until(lambda: any(r["input"] == "http-block" for r in i.stats()["requests"]), "kill dispatch never observed")
        run(["docker", "kill", "--signal", "KILL", i.gateway])
    finally:
        worker.join(45)
        assert not worker.is_alive(), "blocked caller did not join"
    assert i.inspect()["State"]["ExitCode"] == 137 and errors
    value = add_snapshot("killed", ["unknown"], "KILL after dispatch, receipt absent", killed=True, recovering=True)
    lease = max(r["lease_until"] for r in value["billing_reservations"] if expected[r["id"]]["scenario"].startswith("KILL"))
    i.restart_strict()
    wait = max(0, lease / 1000 - time.time()) + 35
    assert wait <= 350, "unexpected unbounded real recovery lease"
    deadline = time.monotonic() + wait
    while time.monotonic() < deadline:
        time.sleep(min(2, deadline - time.monotonic()))
    recovered = add_snapshot("recovered", [], "")
    i.restart_strict()
    before = len(i.stats()["requests"])
    refused = i.call(payload("http-block"), idempotency="strict-kill", status=409)
    assert refused["body"]["error"]["type"] in ("idempotency_unknown", "idempotency_in_progress"), refused
    assert len(i.stats()["requests"]) == before
    report["kill_replay"] = refused
    # A second real restart must neither duplicate settlement nor release unknowns.
    time.sleep(2)
    again = add_snapshot("recovered-again", [], "")
    assert recovered["billing_balances"] == again["billing_balances"]
    transcript = i.stats()["requests"]
    assert len([r for r in transcript if r["input"] == "http-block"]) == 1
    assert len(transcript) <= 136  # Four <=32-RPC jobs + six foreground creates + one killed create.
    report["wire"] = transcript
    report["passed"] = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    report = {"schema": 1, "source_sha": run(["git", "rev-parse", "HEAD"], capture=True).stdout.strip(),
              "source_dirty": bool(run(["git", "status", "--porcelain"], capture=True).stdout.strip()),
              "source_sha256": {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in
                  ("tests/upstream/nativews/main.go", "tests/upstream/nativews/strict.go",
                   "tests/smoke/strict_native_delivery.py", "tests/smoke/native_ws_delivery.py")},
              "scope": "final shipped static API strict main; synthetic operator policy; isolated Linux Docker; not real-provider tariff certification",
              "declared": {"selling_hold_units": 1_000_000_000, "provider_budget_units": 100_000_000,
                  "provider_attempt_bound_units": 4_160_000, "known_charge_units": 900_000,
                  "known_attempt_selling_cost_units": 900_000,
                  "declared_token_cost_units": 4_000,
                  "declared_token_cost_scope": "arithmetic from synthetic operator token tariff and known 2/1 tokens; not a provider invoice or BillingAttempt.CostUnits",
                  "known_tokens": [2, 1], "zero_tokens": [0, 0],
                  "missing_usage": "pending, cost_known false, both user/key holds retained; never settled as zero",
                  "attempts_per_generation": 1, "background_control_rpc_limit": 32,
                  "intended_ci_timeout_seconds": 900, "real_lease_wait_limit_seconds": 350},
              "passed": False, "checkpoints": [], "background_wire": []}
    args.output = args.output.resolve()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    image = "txapi-strict-smoke:" + uuid.uuid4().hex[:12]
    i = None
    docker_ready = False
    try:
        assert os.name != "nt", "Linux Docker required; Windows source/unit checks are separate evidence"
        assert run(["docker", "info", "--format", "{{.OSType}}"], capture=True).stdout.strip() == "linux"
        docker_ready = True
        with tempfile.TemporaryDirectory(prefix="txapi-strict-") as temporary:
            work = Path(temporary)
            helper = work / "nativews"
            run([args.go, "build", "-o", str(helper), "./tests/upstream/nativews"], timeout=180,
                env=dict(ENV, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0"))
            helper.chmod(0o755)
            fixtures = work / "fixtures"
            run([str(helper), "-mode", "prepare", "-directory", str(fixtures)])
            i = StrictInstallation(fixtures, image, helper)
            try:
                run(["docker", "build", "--tag", image, str(ROOT)], timeout=600)
                i.create()
                report["image_id"] = i.inspect()["Image"]
                exercise(i, report)
            except Exception as error:
                report["primary_error"] = type(error).__name__ + ": " + str(error)[:1000]
                raise
            finally:
                if i:
                    try:
                        report["diagnostics"] = i.diagnostics()
                    except Exception as error:
                        report["diagnostics_error"] = type(error).__name__
                    i.cleanup()
    except Exception as error:
        report["passed"] = False
        report["error"] = type(error).__name__ + ": " + str(error)[:1000]
        raise
    finally:
        args.output.write_text(json.dumps(redact_report(report, i), indent=2), encoding="utf-8")
        if docker_ready:
            run(["docker", "image", "rm", image], check=False)


if __name__ == "__main__":
    main()
