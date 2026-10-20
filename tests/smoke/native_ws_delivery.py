"""Actual shipped gateway + reverse proxy + TLS WS fixture, isolated Docker only.

No host DNS/trust changes, production dial overrides, personal databases or real
provider traffic. All runtime containers share one labelled internal network.
"""

import argparse
import base64
import contextlib
import hashlib
import json
import math
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import threading
import time
import uuid

from local import ROOT


PROVIDER_KEY = "nativews-deployment-provider-fixture-only"
CLIENT_KEY = "nativews-deployment-client-fixture-only"
ADMIN = {"username": "nativews-admin", "password": "nativews-admin-fixture-only"}
ENV = {key: value for key, value in os.environ.items()
       if not key.upper().startswith(("GATEWAY_", "TXAPI_", "OPENAI_"))}


def run(command, *, capture=False, check=True, timeout=60, env=ENV):
    return subprocess.run(command, cwd=ROOT, env=env, check=check, timeout=timeout,
                          stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                          stderr=subprocess.STDOUT, text=True)


def until(predicate, message, seconds=30):
    deadline = time.monotonic() + seconds
    while True:
        result = predicate()
        if result:
            return result
        assert time.monotonic() < deadline, message
        time.sleep(0.1)


def percentile(values):
    ordered = sorted(values)
    return ordered[math.ceil(len(ordered) * .95) - 1] if ordered else None


class ContainerResources:
    """Read PID 1 inside the labelled test container, without host privileges."""
    def __init__(self, installation):
        self.installation = installation
        self.values, self.errors = [], []
        self.identity = None
        self.started = time.monotonic()
        self.stop = threading.Event()
        self.sample()
        self.thread = threading.Thread(target=self.loop)
        self.thread.start()

    def sample(self):
        try:
            response = run(["docker", "exec", self.installation.gateway, "/smoke-nativews", "-mode", "resources"],
                           capture=True, timeout=15)
            value = json.loads(response.stdout)
            if any(key not in value for key in ("process_identity", "rss_bytes", "cpu_ticks", "fd_count")):
                raise RuntimeError("incomplete process resource sample")
            if self.identity is not None and value["process_identity"] != self.identity:
                raise RuntimeError("gateway process changed during phase")
            self.identity = value["process_identity"]
            value["elapsed_seconds"] = time.monotonic() - self.started
            self.values.append(value)
        except Exception as error:
            self.errors.append(type(error).__name__ + ": " + str(error)[:160])

    def loop(self):
        while not self.stop.wait(2):
            self.sample()

    def finish(self):
        self.stop.set()
        self.thread.join(20)
        assert not self.thread.is_alive(), "container sampler did not join"
        self.sample()
        elapsed = time.monotonic() - self.started
        values = self.values
        delta = values[-1]["cpu_ticks"] - values[0]["cpu_ticks"] if len(values) >= 2 else None
        return {"scope": "same non-root shipped gateway PID1 only; read-only /proc; 2s samples; peaks are lower bounds; test helper exec is separate PID",
                "samples": values, "errors": self.errors, "elapsed_seconds": elapsed,
                "known": not self.errors and len(values) >= 3 and delta is not None and delta >= 0,
                "rss_bytes": max((value["rss_bytes"] for value in values), default=None),
                "fd_count": max((value["fd_count"] for value in values), default=None),
                "average_cpu_cores": delta / os.sysconf("SC_CLK_TCK") / elapsed if delta is not None and delta >= 0 else None}


class Installation:
    def __init__(self, work, image, helper):
        self.work, self.image = work, image
        self.name = "txapi-nativews-" + uuid.uuid4().hex[:12]
        self.network, self.volume = self.name + "-network", self.name + "-data"
        self.gateway, self.provider = self.name + "-gateway", self.name + "-provider"
        self.label = "txapi.nativews.fixture=" + self.name
        self.containers, self.created_volume, self.created_network = [], False, False
        self.resource_limits = []
        self.config = work / "config.json"
        self.settings = {
            "server": {"addr": ":8080", "timeout_seconds": 30, "retry_times": 0,
                       "concurrency": {"instance": 8, "user": 4, "key": 2, "channel": 4}},
            "admin_key": "nativews-deployment-admin-key-fixture-only",
            "database": {"path": "/app/data/gateway.db", "credential_keys_file": "/fixtures/credentials.json"},
            "billing": {"mode": "disabled"},
            "responses": {"enabled": True, "keys_file": "/fixtures/content.json"},
            "responses_ws": {"enabled": True, "persistent_history": True, "models": ["m"]},
            "usage_history": {"enabled": True, "instance_records": 64, "owner_records": 64, "retention_seconds": 3600},
            "channels": [{"name": "official-fixture", "type": "openai", "base_url": "https://api.openai.com",
                          "key": PROVIDER_KEY, "models": ["alias"], "model_mapping": {"alias": "m"}}],
            "tokens": [{"name": "nativews-client", "key": CLIENT_KEY, "models": ["alias"]}],
        }
        self.key_material = []
        for model in ("cli-text", "cli-function-result", "cli-custom", "gpt-5.5"):
            native = "native-" + ("cli-tool-search" if model == "gpt-5.5" else model)
            self.settings["channels"][0]["models"].append(model)
            self.settings["channels"][0]["model_mapping"][model] = native
            self.settings["tokens"][0]["models"].append(model)
            self.settings["responses_ws"]["models"].append(native)
        work.chmod(0o755)
        for name in ("credentials.json", "content.json"):
            active, digest = (base64.b64encode(os.urandom(32)).decode() for _ in range(2))
            self.key_material += [active, digest]
            path = work / name
            path.write_text(json.dumps({"active_key": "fixture", "keys": {"fixture": active}, "digest_key": digest}))
            # Synthetic disposable material mounted read-only for image uid 10001.
            path.chmod(0o644)
        self.save()
        self.mounts = ["--mount", f"type=bind,source={work},target=/fixtures,readonly",
                       "--mount", f"type=volume,source={self.volume},target=/app/data"]
        self.helper_mount = ["--mount", f"type=bind,source={helper},target=/smoke-nativews,readonly"]

    def save(self):
        self.config.write_text(json.dumps(self.settings))
        self.config.chmod(0o644)

    def create(self):
        run(["docker", "network", "create", "--internal", "--label", self.label, self.network])
        self.created_network = True
        state = json.loads(run(["docker", "network", "inspect", self.network], capture=True).stdout)[0]
        assert state["Internal"] and state["Labels"]["txapi.nativews.fixture"] == self.name
        run(["docker", "volume", "create", "--label", self.label, self.volume])
        self.created_volume = True
        run(["docker", "create", "--name", self.provider, "--label", self.label,
             "--network", self.network, "--network-alias", "api.openai.com", "--network-alias", "nativews-fixture", "--user", "0",
             *self.helper_mount, "--mount", f"type=bind,source={self.work},target=/fixtures,readonly",
             "--entrypoint", "/smoke-nativews", self.image])
        self.containers.append(self.provider)
        run(["docker", "start", self.provider])
        until(lambda: self.helper("http", base="http://127.0.0.1:9911", path="/stats", check=False).returncode == 0,
              "TLS fixture did not start")
        for flags, extra in [(["-init-admin"], ["-e", "TXAPI_ADMIN_USERNAME=" + ADMIN["username"],
                                               "-e", "TXAPI_ADMIN_PASSWORD=" + ADMIN["password"]]),
                             (["-seed-only"], []), (["-claim-token", "1", "-owner-user", "1"], [])]:
            run(["docker", "run", "--rm", "--network", "none", *self.mounts, *extra, self.image,
                 "-config", "/fixtures/config.json", *flags])
        run(["docker", "create", "--name", self.gateway, "--label", self.label,
             "--network", self.network, "--network-alias", "gateway", *self.resource_limits, *self.mounts, *self.helper_mount,
             "-e", "SSL_CERT_FILE=/fixtures/ca.pem", self.image, "-config", "/fixtures/config.json"])
        self.containers.append(self.gateway)
        run(["docker", "start", self.gateway])
        self.ready()
        for name in self.containers:
            state = json.loads(run(["docker", "inspect", name], capture=True).stdout)[0]
            assert set(state["NetworkSettings"]["Networks"]) == {self.network}, "runtime escaped the internal network"
        assert self.inspect()["Config"]["User"] == "app", "must use shipped non-root entrypoint"

    def helper(self, mode, *, base="http://127.0.0.1:8081", check=True, helper_timeout=40, **fields):
        command = ["docker", "exec", self.provider, "/smoke-nativews", "-mode", mode, "-base", base]
        for key, value in fields.items():
            command += ["-" + key, value]
        return run(command, capture=True, check=check, timeout=helper_timeout)

    def sustained_protocols(self, seconds, workers, reports):
        for protocol in (1, 2):
            phase = {"protocol": protocol, "requested_seconds": seconds, "workers": workers,
                     "limits": {"rss_bytes": 256 * 1024 * 1024, "fd_count": 256, "average_cpu_cores": 1,
                                "first_text_p95_ns": 2_000_000_000, "complete_p95_ns": 3_000_000_000,
                                "event_upper_p95_ns": 1_000_000_000}}
            reports.append(phase)
            resources = ContainerResources(self)
            try:
                payload = json.dumps({"seconds": seconds, "workers": workers, "protocol": protocol})
                result = self.helper("soak", base="http://127.0.0.1:9911", payload=payload,
                                     helper_timeout=seconds + 30, **{"timeout-seconds": str(seconds + 20)})
                phase["report"] = json.loads(result.stdout)
            finally:
                phase["resources"] = resources.finish()
            value, observed = phase["report"], phase["resources"]
            rows = value["samples"]
            phase["observed_p95_ns"] = {
                key: percentile([sample[field] for sample in rows]) for key, field in
                (("first_text_p95_ns", "first_text_ns"), ("complete_p95_ns", "complete_ns"))}
            phase["observed_p95_ns"]["event_upper_p95_ns"] = percentile(
                [latency for sample in rows for latency in sample["event_upper_ns"]])
            errors = []
            if (value["errors"] or not value["started"] or value["started"] != value["completed"] or
                    value["started"] != len(rows) or value["active_after"] != 0 or value["elapsed_ns"] < seconds * 1e9):
                errors.append("started/completed/interval/drain mismatch")
            if any(row["protocol"] != protocol or row["provider_protocol"] != 2 or row["provider_alpn"] != "h2"
                   or not row["tls"] or not row["provider_tls"] or row["attempts"] != 1 or not row["drained"]
                   or row.get("error") or len(row["event_upper_ns"]) != 10 for row in rows):
                errors.append("TLS/protocol/attempt/event correctness failed")
            if not observed["known"]:
                errors.append("gateway resource evidence incomplete")
            for key in ("rss_bytes", "fd_count", "average_cpu_cores"):
                value = observed.get(key)
                if not isinstance(value, (float, int)) or value > phase["limits"][key]:
                    errors.append(key + " unknown or above predeclared budget")
            for key, value in phase["observed_p95_ns"].items():
                if value is None or value > phase["limits"][key]:
                    errors.append(key + " unknown or above predeclared budget")
            phase["errors"] = errors
            phase["passed"] = not errors
            metrics = self.api("/metrics")
            until(lambda: all(f"{name} 0\n" in self.api("/metrics") for name in
                              ("gateway_admission_requests_active", "gateway_admission_attempts_active")),
                  "soak production admission did not drain")
            assert "gateway_admission_requests_active" in metrics
            print(json.dumps({"docker_protocol_soak": {key: phase[key] for key in
                  ("protocol", "requested_seconds", "workers", "limits", "resources", "observed_p95_ns", "errors", "passed")},
                  "started": phase["report"]["started"], "completed": phase["report"]["completed"]}), flush=True)
            assert phase["passed"], "Docker protocol soak failed; full samples retained in output report"

    def api(self, path, key="", body=None, method="GET", status=200):
        response = json.loads(self.helper("http", method=method, path=path, key=key,
                                          payload="" if body is None else json.dumps(body)).stdout)
        assert response["status"] == status, (path, response)
        return response["body"]

    def ready(self):
        def probe():
            response = self.helper("http", path="/healthz", check=False)
            return response.returncode == 0 and json.loads(response.stdout)["status"] == 200
        until(probe, "actual gateway main behind proxy did not become ready")

    def inspect(self):
        return json.loads(run(["docker", "inspect", self.gateway], capture=True).stdout)[0]

    def stats(self):
        return json.loads(self.helper("http", base="http://127.0.0.1:9911", path="/stats").stdout)["body"]

    def chat_http2(self):
        for number, name in enumerate(("h2-complete", "h2-cancel", "h2-recovery"), 1):
            report = json.loads(self.helper("chat", key=CLIENT_KEY, payload=name).stdout)
            assert report == {"input": name, "events": 1 if name == "h2-cancel" else 3,
                              "done": name != "h2-cancel"}, report
            until(lambda: len((state := self.stats())["chat"] or []) == number and state["chat_active"] == 0,
                  "Chat upstream stream did not drain")
        state = self.stats()
        rows = state["chat"]
        assert [row["input"] for row in rows] == ["h2-complete", "h2-cancel", "h2-recovery"], rows
        assert all(row["protocol"] == 2 for row in rows), rows
        assert len({row["peer"] for row in rows}) == 1, "default upstream TLS connection was not reused"
        assert not state["errors"], state["errors"]
        def drained():
            metrics = self.api("/metrics")
            return all(f"{name} 0\n" in metrics for name in
                       ("gateway_admission_requests_active", "gateway_admission_attempts_active"))
        until(drained, "Chat production admission did not drain")
        print("PASS: shipped default transport negotiated HTTP/2, normal/cancel/recovery SSE shared one TLS connection; 3 sends, all upstream streams drained", flush=True)

    def wait_sends(self, number):
        return until(lambda: (state if len((state := self.stats())["requests"] or []) == number and state["active"] == 0 else None),
                     "upstream sends or active connections differ")

    def dual_tls_baseline(self):
        response = json.loads(self.helper("http", base="http://127.0.0.1:9911", method="POST",
                                          path="/baseline", payload="{}").stdout)
        assert response["status"] == 200, response
        report = response["body"]
        report["source"] = {
            "commit": run(["git", "rev-parse", "HEAD"], capture=True).stdout.strip(),
            "dirty": bool(run(["git", "status", "--porcelain"], capture=True).stdout.strip()),
            "sha256": {path: hashlib.sha256((ROOT / path).read_bytes()).hexdigest() for path in
                       ("tests/upstream/nativews/main.go", "tests/upstream/nativews/baseline.go",
                        "tests/smoke/native_ws_delivery.py")},
        }
        def observed(values):
            ordered = sorted(values)
            return {"count": len(ordered), "p50": ordered[math.ceil(len(ordered) * 0.5) - 1],
                    "p95": ordered[math.ceil(len(ordered) * 0.95) - 1], "maximum": ordered[-1]} if ordered else None
        report["observed_summary_ns"] = {
            field: observed(report[field]) for field in
            ("paired_first_body_ns", "paired_first_text_ns", "paired_complete_ns")}
        report["observed_summary_ns"]["gateway_event_upper_ns"] = observed(
            [value for row in report["samples"] if row["target"] == "gateway" for value in row["event_upper_ns"]])
        # Print failed samples as well as successful ones before assertions.
        print("dual TLS/H2 paired baseline:", json.dumps(report, sort_keys=True), flush=True)
        assert not report["errors"] and report["planned"] == report["started"] == report["completed"] == 40, report
        assert report["paired_success_count"] == 20 and len(report["samples"]) == 40
        for field in ("paired_first_body_ns", "paired_first_text_ns", "paired_complete_ns"):
            assert len(report[field]) == 20
        for row in report["samples"]:
            assert row["protocol"] == row["provider_protocol"] == 2 and row["alpn"] == row["provider_alpn"] == "h2", row
            assert row["tls"] and row["provider_tls"] and row["status"] == 200, row
            assert row["attempts"] == 1 and row["drained"] and not row.get("error"), row
            assert len(row["event_lower_ns"]) == len(row["event_upper_ns"]) == 10
            assert all(low <= high for low, high in zip(row["event_lower_ns"], row["event_upper_ns"]))
        assert self.api("/metrics").count("gateway_admission_requests_active 0\n") == 1
        assert self.api("/metrics").count("gateway_admission_attempts_active 0\n") == 1
        usage_ids = {row.get("usage_id", "") for row in report["samples"] if row["target"] == "gateway"}
        assert len(usage_ids) == 20 and all(value.startswith("usage_") and len(value) == 38
                                           and int(value[6:], 16) >= 0 for value in usage_ids), usage_ids
        assert not any(row.get("usage_id") for row in report["samples"] if row["target"] == "direct")
        return usage_ids

    def ws_request(self, parent, value, tools=False):
        body = {"type": "response.create", "model": "alias", "input": value, "store": True}
        if parent:
            body["previous_response_id"] = parent
        if tools:
            body["tools"] = [{"type": "function", "name": "weather", "parameters": {"type": "object"}}]
        return body

    def ws(self, parent, value, *, tools=False, refused=False):
        events = [json.loads(line) for line in self.helper("ws", key=CLIENT_KEY,
                  payload=json.dumps(self.ws_request(parent, value, tools)),
                  until="error" if refused else "terminal").stdout.splitlines()]
        if refused:
            assert events[-1]["type"] == "error" and not any(event["type"] == "response.completed" for event in events)
            return None
        assert events[0]["type"] == "response.created" and events[-1]["type"] == "response.completed", events
        result = events[-1]["response"]
        assert result["status"] == "completed" and result["model"] == "alias" and result["id"].startswith("resp_")
        assert events[0]["response"]["id"] == result["id"]
        if tools:
            args = "".join(event["delta"] for event in events if event["type"] == "response.function_call_arguments.delta")
            assert json.loads(args) == {} and result["output"][0]["name"] == "weather"
        else:
            assert "".join(event["delta"] for event in events if event["type"] == "response.output_text.delta") == "你好🌏"
        self.api("/v1/responses/" + result["id"], CLIENT_KEY)
        return result

    def restart(self, *, kill=False):
        run(["docker", "kill", "--signal", "KILL", self.gateway] if kill else ["docker", "stop", "--time", "15", self.gateway])
        state = self.inspect()["State"]
        assert not state["Running"] and not state["OOMKilled"]
        if not kill:
            assert state["ExitCode"] == 0, state
        run(["docker", "start", self.gateway])
        self.ready()

    def usage(self, session, number):
        def read():
            page = self.api("/api/me/usage-records?limit=100", session)
            assert not page["has_more"], "fixture usage exceeds one explicitly bounded page"
            rows = page["data"]
            return rows if len(rows) == number and all(row["state"] in ("sealed", "interrupted") for row in rows) else None
        return until(read, "durable usage did not finish")

    def crash_in_flight(self, parent):
        output = self.work / "blocked-client.log"
        command = ["docker", "exec", self.provider, "/smoke-nativews", "-mode", "ws", "-key", CLIENT_KEY,
                   "-payload", json.dumps(self.ws_request(parent, "block")), "-until", "disconnect"]
        with output.open("w") as log:
            client = subprocess.Popen(command, cwd=ROOT, env=ENV, stdout=log, stderr=subprocess.STDOUT, text=True)
            try:
                def created():
                    assert client.poll() is None, "in-flight observer exited early"
                    for line in output.read_text().splitlines():
                        try:
                            event = json.loads(line)
                        except json.JSONDecodeError:
                            continue  # The child may still be writing this line.
                        if event.get("type") == "response.created":
                            return event["response"]["id"]
                    return None
                public_id = until(created, "client did not observe in-flight public ID", seconds=15)
                state = self.stats()
                assert len(state["requests"]) == 5 and state["active"] == 1
                self.restart(kill=True)
                assert client.wait(timeout=10) == 0
            finally:
                if client.poll() is None:
                    client.kill()
                    client.wait(timeout=10)
        events = [json.loads(line) for line in output.read_text().splitlines()]
        assert events[-1] == {"disconnected": True}
        assert not any(event.get("type") == "response.completed" for event in events)
        return public_id

    def sdk(self, image):
        settings = self.work / "sdk.json"
        settings.write_text(json.dumps({"base": "http://nativews-fixture:8081", "key": CLIENT_KEY}))
        settings.chmod(0o644)
        name = self.name + "-sdk"
        run(["docker", "create", "--name", name, "--label", self.label,
             "--network", self.network, "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m",
             "--mount", f"type=bind,source={ROOT / 'tests' / 'smoke'},target=/smoke,readonly",
             "--mount", f"type=bind,source={self.work},target=/fixtures,readonly",
             image, "/smoke/ws_sdk_conformance.py", "--client", "/fixtures/sdk.json", "--deployed"])
        self.containers.append(name)
        state = json.loads(run(["docker", "inspect", name], capture=True).stdout)[0]
        assert set(state["NetworkSettings"]["Networks"]) == {self.network}
        assert state["Config"]["User"] == "65532:65532" and state["HostConfig"]["ReadonlyRootfs"]
        result = run(["docker", "start", "--attach", name], capture=True, timeout=25)
        state = json.loads(run(["docker", "inspect", name], capture=True).stdout)[0]
        assert not state["State"]["Running"] and state["State"]["ExitCode"] == 0, result.stdout
        print(result.stdout.strip(), flush=True)
        report = json.loads(result.stdout.splitlines()[-1])
        assert report["cases"] == 12 and len(set(report["ids"])) == 12
        self.api("/v1/responses/" + report["unknown"], CLIENT_KEY, status=409)
        records = self.usage(self.api("/api/auth/login", body=ADMIN, method="POST")["session_token"], 18)
        assert sum(row["state"] == "interrupted" for row in records) == 1
        state = self.wait_sends(18)
        assert not state["errors"]
        requests = state["requests"][6:]
        assert [len(row["input"]) for row in requests] == [1, 3, 5, 7, 1, 1, 1, 1, 1, 3, 1, 4]
        assert all(row["transport"] == "ws" and row["model"] == "m" and not row["store"] and not row["previous"] for row in requests)
        assert requests[2]["input"][3]["call_id"] == requests[2]["input"][4]["call_id"]
        custom = requests[9]["input"]
        assert custom[1]["type"] == "custom_tool_call" and custom[2]["type"] == "custom_tool_call_output"
        assert custom[1]["input"] == "raw 雪🌏\nsecond" and custom[2]["output"] == "custom-result"
        assert custom[1]["call_id"] == custom[2]["call_id"] and custom[1]["namespace"] == custom[2]["namespace"] == "text"
        search = requests[11]["input"]
        assert search[1]["type"] == "tool_search_call" and search[1]["arguments"] == {"query": "雪🌏"}
        assert search[2]["type"] == "tool_search_output" and search[1]["call_id"] == search[2]["call_id"]
        assert search[3]["type"] == "additional_tools" and search[2]["tools"] == search[3]["tools"]
        return report["unknown"]

    def codex(self, image, executable, before):
        settings = self.work / "codex.json"
        settings.write_text(json.dumps({"base": "http://nativews-fixture:8081", "key": CLIENT_KEY}))
        settings.chmod(0o644)
        name = self.name + "-codex"
        previous = self.wait_sends(before)
        # The shared client imports modules whose root is two parents up.
        run(["docker", "create", "--name", name, "--label", self.label,
             "--network", self.network, "--read-only", "--tmpfs", "/tmp:rw,nosuid,size=64m",
             "--mount", f"type=bind,source={ROOT / 'tests' / 'smoke'},target=/workspace/tests/smoke,readonly",
             "--mount", f"type=bind,source={self.work},target=/fixtures,readonly",
             "--mount", f"type=bind,source={executable},target=/codex,readonly",
             image, "/workspace/tests/smoke/codex_ws_conformance.py", "--codex", "/codex", "--deployed", "/fixtures/codex.json"])
        self.containers.append(name)
        state = json.loads(run(["docker", "inspect", name], capture=True).stdout)[0]
        assert set(state["NetworkSettings"]["Networks"]) == {self.network}
        assert state["Config"]["User"] == "65532:65532" and state["HostConfig"]["ReadonlyRootfs"]
        result = run(["docker", "start", "--attach", name], capture=True, timeout=100)
        state = json.loads(run(["docker", "inspect", name], capture=True).stdout)[0]
        assert not state["State"]["Running"] and state["State"]["ExitCode"] == 0, result.stdout
        print(result.stdout.strip(), flush=True)
        assert json.loads(result.stdout.splitlines()[-1])["cases"] == ["text", "function", "custom", "search"]
        state = self.wait_sends(before + 11)
        assert (not state["errors"] and state["http_posts"] == previous["http_posts"]
                and state["proxy_posts"] == previous["proxy_posts"]), "HTTP fallback or fixture boundary failure"
        assert state["proxy_upgrades"] - previous["proxy_upgrades"] == 4
        requests = state["requests"][before:]
        groups = []
        for model, size in (("native-cli-text", 2), ("native-cli-function-result", 3),
                            ("native-cli-custom", 3), ("native-cli-tool-search", 3)):
            group, requests = requests[:size], requests[size:]
            assert len(group) == size and all(row["model"] == model and row["transport"] == "ws" and not row["store"] for row in group)
            assert len({row["connection"] for row in group}) == 1
            assert group[0]["generate"] is False and not group[0]["previous"]
            for index, row in enumerate(group[1:], 1):
                assert row.get("generate") is not False and row["previous"] == group[index - 1]["response_id"]
            if size == 3:
                expected = {"native-cli-function-result": "function_call_output", "native-cli-custom": "custom_tool_call_output", "native-cli-tool-search": "tool_search_output"}[model]
                assert any(item.get("type") == expected and item.get("call_id") == "call_codex_fixture" for item in group[-1]["input"])
                assert not any(item.get("type") == "reasoning" for item in group[-1]["input"]), "native continuation duplicated context"
            groups.append(group)
        assert not requests and len({group[0]["connection"] for group in groups}) == 4
        session = self.api("/api/auth/login", body=ADMIN, method="POST")["session_token"]
        records = self.usage(session, before + 11)
        rows = [row for row in records if row["model"] in ("cli-text", "cli-function-result", "cli-custom", "gpt-5.5")]
        assert len(rows) == 11 and all(row["state"] == "sealed" and row["transport"] == "websocket" for row in rows)
        totals = []
        for row in rows:
            attempts = self.api("/api/me/usage-records/" + row["id"], session)["data"]["attempts"]
            assert len(attempts) == 1 and attempts[0]["may_dispatch"]
            totals.append(attempts[0]["usage"]["total_tokens"])
        assert sorted(totals) == ["0"] * 4 + ["3"] * 7
        print("PASS: deployed observer requires 11 native creates, 4 prewarms/connections, no HTTP fallback, exact parents and 11 usage receipts", flush=True)

    def final_snapshot(self, unknowns, count, history_count=None):
        run(["docker", "stop", "--time", "15", self.gateway])
        assert self.inspect()["State"]["ExitCode"] == 0
        copy = self.work / "snapshot"
        copy.mkdir()
        run(["docker", "cp", self.gateway + ":/app/data/.", str(copy)])
        with contextlib.closing(sqlite3.connect(copy / "gateway.db")) as db:
            assert db.execute("SELECT COUNT(*) FROM response_histories").fetchone() == (count if history_count is None else history_count,)
            assert db.execute("SELECT COUNT(*) FROM usage_invocations").fetchone() == (count,)
            assert db.execute("SELECT state,COUNT(*) FROM usage_invocations GROUP BY state ORDER BY state").fetchall() == [("interrupted", 1), ("sealed", count - 1)]
            assert db.execute("SELECT COUNT(*) FROM response_histories WHERE busy='uncertain'").fetchone() == (len(unknowns),)
            for unknown in unknowns:
                assert db.execute("SELECT busy FROM response_histories WHERE id=?", (unknown,)).fetchone() == ("uncertain",)
            for table in ("users", "tokens"):
                assert db.execute(f"SELECT COUNT(*) FROM {table} WHERE used_quota != 0").fetchone() == (0,)
            for table in ("billing_reservations", "billing_attempts"):
                assert db.execute(f"SELECT COUNT(*) FROM {table}").fetchone() == (0,)
            assert db.execute("SELECT COUNT(*) FROM billing_balances WHERE used_units != 0 OR reserved_units != 0").fetchone() == (0,)
        logs = run(["docker", "logs", self.gateway], capture=True).stdout
        secrets = [CLIENT_KEY, PROVIDER_KEY, ADMIN["password"], self.settings["admin_key"], *self.key_material]
        assert not any(secret in logs for secret in secrets), "credential appeared in gateway log"

    def cleanup(self):
        for name in reversed(self.containers):
            # The shipped image declares /app/data as a VOLUME. The helper
            # container also inherits it, so remove its anonymous volume too.
            run(["docker", "rm", "-f", "-v", name], check=False)
        if self.created_volume:
            run(["docker", "volume", "rm", self.volume])
        if self.created_network:
            run(["docker", "network", "rm", self.network])


def exercise(installation, sdk_image=None, codex_image=None, codex=None):
    i = installation
    session = i.api("/api/auth/login", body=ADMIN, method="POST")["session_token"]
    first = i.api("/v1/responses", CLIENT_KEY, {"model": "alias", "input": "http-root", "store": True}, "POST")
    assert first["status"] == "completed"
    second = i.ws(first["id"], "tool-call", tools=True)
    third = i.api("/v1/responses", CLIENT_KEY, {"model": "alias", "store": True, "previous_response_id": second["id"],
                 "input": [{"type": "function_call_output", "call_id": second["output"][0]["call_id"], "output": "sunny"}]}, "POST")
    i.usage(session, 3)
    i.wait_sends(3)
    i.restart()
    for result in (first, second, third):
        recovered = i.api("/v1/responses/" + result["id"], CLIENT_KEY)
        assert recovered["id"] == result["id"] and recovered["status"] == "completed"
    fourth = i.ws(third["id"], "after-restart")
    i.usage(session, 4)
    i.wait_sends(4)
    unknown = i.crash_in_flight(fourth["id"])
    i.api("/v1/responses/" + unknown, CLIENT_KEY, status=409)
    i.ws(unknown, "must-not-replay", refused=True)
    i.wait_sends(5)
    records = i.usage(session, 5)
    interrupted = next(row for row in records if row["state"] == "interrupted")
    detail = i.api("/api/me/usage-records/" + interrupted["id"], session)["data"]
    assert len(detail["attempts"]) == 1 and detail["attempts"][0]["may_dispatch"]
    assert detail["attempts"][0]["usage"].get("total_tokens") is None
    sixth = i.ws(fourth["id"], "new-after-kill")
    assert sixth["id"] not in {unknown, fourth["id"]}
    i.usage(session, 6)
    state = i.wait_sends(6)
    assert not state["errors"] and state["proxy_upgrades"] >= 5
    requests = state["requests"]
    assert [row["transport"] for row in requests] == ["http", "ws", "http", "ws", "ws", "ws"]
    assert all(row["input_kind"] == "array" for row in requests)
    assert [len(row["input"]) for row in requests] == [1, 3, 5, 7, 9, 9]
    assert all(row["model"] == "m" and not row["previous"] for row in requests)
    # HTTP preserves its explicit store policy; persistent WS privately stores
    # locally and requests store=false from its native upstream connection.
    assert [row["store"] for row in requests] == [True, False, True, False, False, False]
    function = requests[2]["input"]
    assert function[-2]["type"] == "function_call" and function[-1]["type"] == "function_call_output"
    assert function[-2]["call_id"] == function[-1]["call_id"] == second["output"][0]["call_id"]
    unknowns, count = [unknown], 6
    if sdk_image:
        unknowns.append(i.sdk(sdk_image))
        count = 18
    history_count = count
    if codex:
        i.codex(codex_image, codex, count)
        count += 11
    i.settings["responses_ws"]["persistent_history"] = False
    i.save()
    i.restart()
    i.api("/v1/responses/" + sixth["id"], CLIENT_KEY)
    i.api("/v1/responses/" + unknown, CLIENT_KEY, status=409)
    i.ws(sixth["id"], "persistent-disabled", refused=True)
    i.usage(session, count)
    assert len(i.wait_sends(count)["requests"]) == count
    i.chat_http2()
    records = i.usage(session, count + 3)
    chat = [row for row in records if row["operation"] == "chat"]
    assert len(chat) == 3 and all(row["state"] == "sealed" for row in chat), chat
    count += 3
    baseline_ids = i.dual_tls_baseline()
    count += len(baseline_ids)
    records = i.usage(session, count)
    chat = [row for row in records if row["operation"] == "chat"]
    assert len(chat) == 23 and all(row["state"] == "sealed" for row in chat), chat
    baseline_receipts, original_complete, original_unknown = 0, 0, 0
    for row in chat:
        detail = i.api("/api/me/usage-records/" + row["id"], session)["data"]
        assert len(detail["attempts"]) == 1 and detail["attempts"][0]["may_dispatch"], detail
        usage = detail["attempts"][0]["usage"]
        names = ("input_tokens", "output_tokens", "total_tokens", "cached_input_tokens",
                 "cache_write_input_tokens", "reasoning_output_tokens")
        if row["id"] in baseline_ids:
            assert usage["completeness"] == "complete" and usage["source"] == "provider_reported", usage
            assert usage["profile"] == "chat_native_tokens_v1", usage
            assert [usage[name] for name in names] == ["2", "8", "10", None, None, None], usage
            baseline_receipts += 1
        elif usage["completeness"] == "complete":
            assert usage["source"] == "provider_reported" and usage["profile"] == "chat_native_tokens_v1", usage
            assert [usage[name] for name in names] == ["2", "1", "3", None, None, None], usage
            original_complete += 1
        else:
            assert usage["completeness"] == usage["source"] == "unknown" and usage["profile"] == "", usage
            assert all(usage[name] is None for name in names), usage
            original_unknown += 1
    assert baseline_receipts == 20, baseline_receipts
    assert (original_complete, original_unknown) == (2, 1), (original_complete, original_unknown)
    i.final_snapshot(unknowns, count, history_count)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--sdk", action="store_true", help="also run the fixed official Python SDK inside the isolated network")
    parser.add_argument("--codex", type=Path, help="also run official Codex0.163.0 Linux musl in the isolated network")
    parser.add_argument("--soak-seconds", type=int, default=0, help="0 or 5..1800 continuous seconds per TLS HTTP/1.1 and HTTP/2 profile")
    parser.add_argument("--soak-workers", type=int, default=2, help="1..2 clients; fixed existing key admission remains unchanged")
    parser.add_argument("--output", type=Path, help="complete Docker protocol soak samples and resource evidence")
    args = parser.parse_args()
    if not (args.soak_seconds == 0 or 5 <= args.soak_seconds <= 1800) or not 1 <= args.soak_workers <= 2:
        parser.error("soak-seconds must be 0 or 5..1800; soak-workers must be 1..2")
    if args.soak_seconds and args.output is None:
        parser.error("--output is required for a protocol soak")
    assert os.name != "nt", "requires Linux Docker; local harness tests are separate evidence"
    assert run(["docker", "info", "--format", "{{.OSType}}"], capture=True).stdout.strip() == "linux"
    image = "txapi-nativews-smoke:" + uuid.uuid4().hex[:12]
    sdk_image = image + "-sdk" if args.sdk or args.codex else None
    codex = args.codex.resolve() if args.codex else None
    report = {"schema": 1, "source": {"sha": run(["git", "rev-parse", "HEAD"], capture=True).stdout.strip(),
              "dirty": bool(run(["git", "status", "--porcelain"], capture=True).stdout.strip()),
              "sha256": {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in
                         ("tests/upstream/nativews/main.go", "tests/upstream/nativews/baseline.go",
                          "tests/upstream/nativews/soak.go", "tests/smoke/native_ws_delivery.py")}},
              "configuration": {"soak_seconds_per_protocol": args.soak_seconds, "workers": args.soak_workers,
                  "billing": "disabled", "usage_history_during_soak": False,
                  "auth": "production static API Key and admission", "retries": 0,
                  "gateway_container_limits": {"cpus": 2, "memory_bytes": 512 * 1024 * 1024, "pids": 128} if args.soak_seconds else "unchanged",
                  "scope": "actual non-root shipped image; isolated internal Docker network; TLS reverse proxy HTTP1.1/HTTP2 and verified upstream HTTP2; no external provider"},
              "protocol_soaks": [], "delivery_regression_passed": False}
    if codex:
        assert codex.is_file() and os.access(codex, os.X_OK), "explicit executable fixed Codex binary required"
    with tempfile.TemporaryDirectory(prefix="txapi-nativews-") as temporary:
        work = Path(temporary)
        helper = work / "nativews"
        run([args.go, "build", "-o", str(helper), "./tests/upstream/nativews"], timeout=180,
            env=dict(ENV, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0"))
        helper.chmod(0o755)
        fixtures = work / "fixtures"
        run([str(helper), "-mode", "prepare", "-directory", str(fixtures)])
        installation = Installation(fixtures, image, helper)
        if args.soak_seconds:
            installation.resource_limits = ["--cpus", "2", "--memory", "512m", "--pids-limit", "128"]
        try:
            run(["docker", "build", "--tag", image, str(ROOT)], timeout=600)
            if sdk_image:
                dockerfile = work / "SDK.Dockerfile"
                dockerfile.write_text('FROM python:3.13-slim\nCOPY tests/smoke/requirements-ws-sdk.txt /tmp/requirements.txt\n'
                                     'RUN pip install --no-cache-dir -r /tmp/requirements.txt\n'
                                     'ENV PYTHONDONTWRITEBYTECODE=1 PYTHONNOUSERSITE=1\n'
                                     'USER 65532:65532\nENTRYPOINT ["python"]\n')
                run(["docker", "build", "--file", str(dockerfile), "--tag", sdk_image, str(ROOT)], timeout=600)
            installation.create()
            if args.soak_seconds:
                host_config = installation.inspect()["HostConfig"]
                assert host_config["NanoCpus"] == 2_000_000_000 and host_config["Memory"] == 512 * 1024 * 1024 and host_config["PidsLimit"] == 128
                installation.settings["usage_history"]["enabled"] = False
                installation.save()
                installation.restart()
                installation.sustained_protocols(args.soak_seconds, args.soak_workers, report["protocol_soaks"])
                installation.settings["usage_history"]["enabled"] = True
                installation.save()
                installation.restart()
            exercise(installation, sdk_image if args.sdk else None, sdk_image if codex else None, codex)
            report["delivery_regression_passed"] = True
            print("PASS: actual non-root image, verified fixture TLS, reverse-proxy Upgrade, HTTP/WS tool history, restart, in-flight SIGKILL unknown protection and durable usage; isolated Docker only", flush=True)
        except BaseException as error:
            report["fatal_error"] = type(error).__name__ + ": " + str(error)[:300]
            with contextlib.suppress(Exception):
                # Stats contain only these synthetic requests and fixed fixture
                # error reasons, never the authentication headers or key ring.
                print("fixture observations:", json.dumps(installation.stats(), ensure_ascii=False), flush=True)
            for name in installation.containers:
                result = run(["docker", "inspect", "--format", "{{json .State}}", name], capture=True, check=False)
                print(name, result.stdout.strip(), flush=True)
                logs = run(["docker", "logs", "--tail", "30", name], capture=True, check=False).stdout
                for secret in [CLIENT_KEY, PROVIDER_KEY, ADMIN["password"], installation.settings["admin_key"], *installation.key_material]:
                    logs = logs.replace(secret, "[fixture-secret]")
                print(logs, flush=True)
            raise
        finally:
            try:
                installation.cleanup()
                report["cleanup_complete"] = True
            finally:
                if args.output:
                    args.output.parent.mkdir(parents=True, exist_ok=True)
                    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
                run(["docker", "image", "rm", image], check=False)
                if sdk_image:
                    run(["docker", "image", "rm", sdk_image], check=False)


if __name__ == "__main__":
    main()
