"""Actual shipped gateway + reverse proxy + TLS WS fixture, isolated Docker only.

No host DNS/trust changes, production dial overrides, personal databases or real
provider traffic. All runtime containers share one labelled internal network.
"""

import argparse
import base64
import contextlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
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


class Installation:
    def __init__(self, work, image, helper):
        self.work, self.image = work, image
        self.name = "txapi-nativews-" + uuid.uuid4().hex[:12]
        self.network, self.volume = self.name + "-network", self.name + "-data"
        self.gateway, self.provider = self.name + "-gateway", self.name + "-provider"
        self.label = "txapi.nativews.fixture=" + self.name
        self.containers, self.created_volume, self.created_network = [], False, False
        self.config = work / "config.json"
        self.settings = {
            "server": {"addr": ":8080", "timeout_seconds": 30, "retry_times": 0,
                       "concurrency": {"instance": 8, "user": 4, "key": 2, "channel": 4}},
            "admin_key": "nativews-deployment-admin-key-fixture-only",
            "database": {"path": "/app/data/gateway.db", "credential_keys_file": "/fixtures/credentials.json"},
            "billing": {"mode": "disabled"},
            "responses": {"enabled": True, "keys_file": "/fixtures/content.json"},
            "responses_ws": {"enabled": True, "persistent_history": True, "models": ["m"]},
            "usage_history": {"enabled": True, "instance_records": 32, "owner_records": 32, "retention_seconds": 3600},
            "channels": [{"name": "official-fixture", "type": "openai", "base_url": "https://api.openai.com",
                          "key": PROVIDER_KEY, "models": ["alias"], "model_mapping": {"alias": "m"}}],
            "tokens": [{"name": "nativews-client", "key": CLIENT_KEY, "models": ["alias"]}],
        }
        self.key_material = []
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
             "--network", self.network, "--network-alias", "api.openai.com", "--user", "0",
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
             "--network", self.network, "--network-alias", "gateway", *self.mounts,
             "-e", "SSL_CERT_FILE=/fixtures/ca.pem", self.image, "-config", "/fixtures/config.json"])
        self.containers.append(self.gateway)
        run(["docker", "start", self.gateway])
        self.ready()
        for name in self.containers:
            state = json.loads(run(["docker", "inspect", name], capture=True).stdout)[0]
            assert set(state["NetworkSettings"]["Networks"]) == {self.network}, "runtime escaped the internal network"
        assert self.inspect()["Config"]["User"] == "app", "must use shipped non-root entrypoint"

    def helper(self, mode, *, base="http://127.0.0.1:8081", check=True, **fields):
        command = ["docker", "exec", self.provider, "/smoke-nativews", "-mode", mode, "-base", base]
        for key, value in fields.items():
            command += ["-" + key, value]
        return run(command, capture=True, check=check, timeout=40)

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

    def wait_sends(self, number):
        return until(lambda: (state if len((state := self.stats())["requests"] or []) == number and state["active"] == 0 else None),
                     "upstream sends or active connections differ")

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
            rows = self.api("/api/me/usage-records", session)["data"]
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

    def final_snapshot(self, unknown):
        run(["docker", "stop", "--time", "15", self.gateway])
        assert self.inspect()["State"]["ExitCode"] == 0
        copy = self.work / "snapshot"
        copy.mkdir()
        run(["docker", "cp", self.gateway + ":/app/data/.", str(copy)])
        with contextlib.closing(sqlite3.connect(copy / "gateway.db")) as db:
            assert db.execute("SELECT COUNT(*) FROM response_histories").fetchone() == (6,)
            assert db.execute("SELECT COUNT(*) FROM usage_invocations").fetchone() == (6,)
            assert db.execute("SELECT state,COUNT(*) FROM usage_invocations GROUP BY state ORDER BY state").fetchall() == [("interrupted", 1), ("sealed", 5)]
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


def exercise(installation):
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
    i.settings["responses_ws"]["persistent_history"] = False
    i.save()
    i.restart()
    i.api("/v1/responses/" + sixth["id"], CLIENT_KEY)
    i.api("/v1/responses/" + unknown, CLIENT_KEY, status=409)
    i.ws(sixth["id"], "persistent-disabled", refused=True)
    i.usage(session, 6)
    assert len(i.wait_sends(6)["requests"]) == 6
    i.final_snapshot(unknown)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    assert os.name != "nt", "requires Linux Docker; local harness tests are separate evidence"
    assert run(["docker", "info", "--format", "{{.OSType}}"], capture=True).stdout.strip() == "linux"
    image = "txapi-nativews-smoke:" + uuid.uuid4().hex[:12]
    with tempfile.TemporaryDirectory(prefix="txapi-nativews-") as temporary:
        work = Path(temporary)
        helper = work / "nativews"
        run([args.go, "build", "-o", str(helper), "./tests/upstream/nativews"], timeout=180,
            env=dict(ENV, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0"))
        helper.chmod(0o755)
        fixtures = work / "fixtures"
        run([str(helper), "-mode", "prepare", "-directory", str(fixtures)])
        installation = Installation(fixtures, image, helper)
        try:
            run(["docker", "build", "--tag", image, str(ROOT)], timeout=600)
            installation.create()
            exercise(installation)
            print("PASS: actual non-root image, verified fixture TLS, reverse-proxy Upgrade, HTTP/WS tool history, restart, in-flight SIGKILL unknown protection and durable usage; isolated Docker only", flush=True)
        except BaseException:
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
            finally:
                run(["docker", "image", "rm", image], check=False)


if __name__ == "__main__":
    main()
