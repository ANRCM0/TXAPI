"""Exercise the shipped images/Compose with synthetic, disposable databases.

Requires Linux Docker, Compose v2, Go and the S3 commit in local Git history.
Never reads config.yaml, .env, deployment secrets or existing Docker volumes.
"""

import argparse
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tarfile
import tempfile
import time
import urllib.request
import uuid

from local import HTTP, ROOT, control_workflow, request


S3_BASELINE = "b0a69a72039414aa1a1103a74d484d24f8123c0c"
ADMIN = {"username": "smoke-admin", "password": "smoke-admin-password-only"}
ENV = {key: value for key, value in os.environ.items()
       if not key.startswith(("GATEWAY_", "TXAPI_ADMIN_", "COMPOSE_"))}
ENV.update(GATEWAY_ADMIN_KEY="delivery-test-admin-only",
           TXAPI_ADMIN_USERNAME=ADMIN["username"],
           TXAPI_ADMIN_PASSWORD=ADMIN["password"])


def run(command, *, capture=False, check=True, cwd=ROOT, env=ENV, timeout=300):
    return subprocess.run(command, cwd=cwd, env=env, check=check, timeout=timeout,
                          stdout=subprocess.PIPE if capture else None,
                          stderr=subprocess.STDOUT if capture else None,
                          text=True)


def api(base, path, session=None, payload=None, method=None, expected=200):
    status, raw = request(base + path, session, payload, method)
    assert status == expected, (path, status, raw)
    return json.loads(raw) if raw else {}


def login(base, username=ADMIN["username"], password=ADMIN["password"]):
    return api(base, "/api/auth/login", payload={
        "username": username, "password": password,
    })["session_token"]


def call(base, key, *, stream=False):
    payload = {"model": "upgrade-model", "stream": stream,
               "messages": [{"role": "user", "content": "delivery test only"}]}
    result = request(base + "/v1/chat/completions", key, payload)
    assert result[0] == 200, result
    if stream:
        assert b"data: [DONE]" in result[1], result
    else:
        assert json.loads(result[1])["model"] == "mapped-model", result


def usage(base, session, expected):
    deadline = time.monotonic() + 10
    while True:
        value = api(base, "/api/me/usage", session)["data"]["usage"]["total_requests"]
        if value == expected:
            return
        assert time.monotonic() < deadline, (value, expected)
        time.sleep(0.1)


def snapshot(path):
    with contextlib.closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as db:
        assert db.execute("PRAGMA integrity_check").fetchall() == [("ok",)]
        tables = ["users", "tokens", "channels", "credentials", "logs", "schema_migrations"]
        result = {table: db.execute(f'SELECT * FROM "{table}" ORDER BY id').fetchall()
                  for table in tables if table != "schema_migrations"}
        result["schema_migrations"] = db.execute(
            "SELECT * FROM schema_migrations ORDER BY version").fetchall()
        return result


def archive_baseline(directory):
    data = subprocess.run(["git", "archive", "--format=tar", S3_BASELINE], cwd=ROOT,
                          check=True, stdout=subprocess.PIPE, timeout=30).stdout
    with tarfile.open(fileobj=io.BytesIO(data)) as archive:
        for entry in archive:
            target = (directory / entry.name).resolve()
            assert target.is_relative_to(directory.resolve()), entry.name
            if entry.isdir():
                target.mkdir(parents=True, exist_ok=True)
            elif entry.isfile():
                target.parent.mkdir(parents=True, exist_ok=True)
                with archive.extractfile(entry) as source:
                    target.write_bytes(source.read())
                target.chmod(entry.mode & 0o777)
            else:
                raise AssertionError(f"Unexpected archive entry: {entry.name}")


class Installation:
    def __init__(self, work, project, image, upstream, *, old=False):
        assert project.startswith("txapi-s4-smoke-")
        self.project = project
        self.work = work
        work.mkdir()
        self.config = work / "config.json"
        self.empty_env = work / "empty.env"
        self.empty_env.write_text("", encoding="utf-8")
        self.compose_path = work / "compose.json"
        self.set_database_path(old)
        command = ["docker", "compose", "--env-file", str(self.empty_env), "-p", project,
                   "-f", str(ROOT / "docker-compose.yml"), "config", "--format", "json"]
        self.definition = json.loads(run(command, capture=True).stdout)
        gateway = self.definition["services"]["gateway"]
        assert gateway["environment"]["GATEWAY_DATABASE_PATH"] == "/app/data/gateway.db"
        gateway.update(image=image, container_name=project + "-gateway", restart="no",
                       ports=[{"target": 8080, "published": "0", "host_ip": "127.0.0.1",
                               "protocol": "tcp"}])
        for volume in gateway["volumes"]:
            if volume["target"] == "/app/config.yaml":
                volume.update(source=str(self.config), read_only=True)
        # Keep the image's real HEALTHCHECK command, only accelerate its polling.
        gateway["healthcheck"] = {"interval": "1s", "timeout": "2s",
                                  "start_period": "1s", "retries": 10}
        self.definition["services"]["upstream"] = {
            "image": image, "entrypoint": ["/smoke-upstream"],
            "command": ["0.0.0.0:9911"], "restart": "no",
            "healthcheck": {"disable": True},
            "volumes": [{"type": "bind", "source": str(upstream),
                         "target": "/smoke-upstream", "read_only": True}],
        }
        self.save()

    def set_database_path(self, old):
        self.config.write_text(json.dumps({
            "server": {"addr": "127.0.0.1:1", "timeout_seconds": 10},
            "database": {"driver": "sqlite", "path":
                         "/app/data/gateway.db" if old else "/unwritable/not-the-volume.db"},
            "channels": [], "tokens": [],
        }), encoding="utf-8")

    def save(self):
        self.compose_path.write_text(json.dumps(self.definition), encoding="utf-8")

    def command(self, *args):
        return ["docker", "compose", "--env-file", str(self.empty_env),
                "-p", self.project, "-f", str(self.compose_path), *args]

    def compose(self, *args, **kwargs):
        return run(self.command(*args), **kwargs)

    def init_admin(self):
        args = ("run", "--rm", "--no-deps", "-e", "TXAPI_ADMIN_USERNAME",
                "-e", "TXAPI_ADMIN_PASSWORD", "gateway", "-config",
                "/app/config.yaml", "-init-admin")
        self.compose(*args)
        rejected = self.compose(*args, capture=True, check=False)
        assert rejected.returncode != 0 and "admin initialization failed" in rejected.stdout

    def container(self):
        return self.compose("ps", "--all", "--quiet", "gateway", capture=True).stdout.strip()

    def inspect(self):
        return json.loads(run(["docker", "inspect", self.container()], capture=True).stdout)[0]

    def start(self, *, image=None, old=False):
        if image:
            self.definition["services"]["gateway"]["image"] = image
            self.set_database_path(old)
            self.save()
        self.compose("up", "-d", "--no-build", "upstream", "gateway")
        deadline = time.monotonic() + 45
        while True:
            state = self.inspect()
            assert state["State"]["Running"], state["State"]
            if state["State"].get("Health", {}).get("Status") == "healthy":
                break
            assert time.monotonic() < deadline, state["State"]
            time.sleep(0.5)
        assert state["Config"]["User"] not in ("", "root", "0"), state["Config"]["User"]
        uid = self.compose("exec", "-T", "gateway", "id", "-u", capture=True).stdout.strip()
        assert uid == "10001", uid
        volume = next(item for item in state["Mounts"] if item["Destination"] == "/app/data")
        assert volume["Type"] == "volume" and volume["Name"].startswith(self.project + "_")
        self.volume = volume["Name"]
        binding = state["NetworkSettings"]["Ports"]["8080/tcp"][0]
        assert binding["HostIp"] == "127.0.0.1", binding
        self.base = "http://127.0.0.1:" + binding["HostPort"]
        assert api(self.base, "/healthz")["status"] == "ok"

    def stop(self, stream_key=None):
        if stream_key:
            body = json.dumps({"model": "upgrade-model", "stream": True,
                               "smoke_chunk_delay_ms": 1000,
                               "messages": [{"role": "user", "content": "shutdown test"}]}).encode()
            req = urllib.request.Request(self.base + "/v1/chat/completions", data=body,
                                         headers={"Authorization": "Bearer " + stream_key,
                                                  "Content-Type": "application/json"})
            with HTTP.open(req, timeout=15) as response:
                stopper = subprocess.Popen(self.command("stop", "gateway"), env=ENV,
                                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                           text=True)
                try:
                    assert b"data: [DONE]" in response.read(), "SIGTERM truncated the in-flight SSE"
                finally:
                    output, _ = stopper.communicate(timeout=45)
                assert stopper.returncode == 0, output
        else:
            self.compose("stop", "gateway")
        state = self.inspect()["State"]
        assert state["ExitCode"] == 0 and not state["OOMKilled"], state

    def backup(self, name):
        assert not self.inspect()["State"]["Running"], "Back up only after stop/flush"
        destination = self.work / name
        destination.mkdir()
        run(["docker", "cp", self.container() + ":/app/data/.", str(destination)])
        snapshot(destination / "gateway.db")
        return destination

    def restore(self, backup):
        assert not self.inspect()["State"]["Running"], "Never restore a running database"
        assert self.volume.startswith(self.project + "_")
        labels = json.loads(run(["docker", "volume", "inspect", self.volume],
                                capture=True).stdout)[0]["Labels"]
        assert labels["com.docker.compose.project"] == self.project
        # Only this fixture's labelled volume is writable; backup input is read-only.
        script = """
set -eu
rm -f /data/gateway.db /data/gateway.db-wal /data/gateway.db-shm
for file in gateway.db gateway.db-wal gateway.db-shm; do
  if [ -f "/backup/$file" ]; then cp "/backup/$file" "/data/$file"; fi
done
chown 10001:10001 /data/gateway.db*
"""
        run(["docker", "run", "--rm", "--network", "none", "--user", "0",
             "--mount", f"type=volume,source={self.volume},target=/data",
             "--mount", f"type=bind,source={backup},target=/backup,readonly",
             "--entrypoint", "/bin/sh", self.definition["services"]["gateway"]["image"],
             "-c", script])

    def cleanup(self):
        self.compose("down", "--volumes", "--remove-orphans", timeout=90)

    def diagnostics(self):
        self.compose("logs", "--no-color", "--tail", "60", check=False)


def previous_workflow(installation):
    base = installation.base
    admin = login(base)
    user = api(base, "/api/admin/users", admin, {
        "username": "upgrade-user", "password": "upgrade-test-password-only",
        "role": "user", "model_mode": "allowlist", "models": ["upgrade-model"],
    }, expected=201)["data"]
    channel = api(base, "/api/admin/channels", admin, {
        "name": "upgrade-upstream", "type": "openai", "base_url": "http://upstream:9911",
        "api_key": "test-upstream-upgrade-only", "models": ["upgrade-model"],
        "model_mapping": {"upgrade-model": "mapped-model"},
    }, expected=201)["data"]
    session = login(base, "upgrade-user", "upgrade-test-password-only")
    key = api(base, "/api/me/keys", session, {
        "name": "upgrade-client", "model_mode": "allowlist", "models": ["upgrade-model"],
    }, expected=201)["data"]
    call(base, key["key"])
    call(base, key["key"], stream=True)
    usage(base, session, 2)
    return {"user": user, "channel": channel, "key": key}


def preserved(installation, data):
    base = installation.base
    admin = login(base)
    user = api(base, f"/api/admin/users/{data['user']['id']}", admin)["data"]
    assert user["username"] == "upgrade-user"
    channel = api(base, f"/api/admin/channels/{data['channel']['id']}", admin)["data"]
    assert channel["credentials"][0]["id"] == data["channel"]["credentials"][0]["id"]
    session = login(base, "upgrade-user", "upgrade-test-password-only")
    key = api(base, f"/api/me/keys/{data['key']['id']}", session)["data"]
    assert key["key_prefix"] == data["key"]["key_prefix"] and "key" not in key
    assert api(base, "/v1/models", data["key"]["key"])["data"][0]["id"] == "upgrade-model"
    usage(base, session, 2)
    return admin


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    assert run(["docker", "info", "--format", "{{.OSType}}"], capture=True).stdout.strip() == "linux"
    run(["docker", "compose", "version"])
    suffix = uuid.uuid4().hex[:12]
    current_image, previous_image = f"txapi-s4-smoke:{suffix}", f"txapi-s3-smoke:{suffix}"
    fixtures = []
    with tempfile.TemporaryDirectory(prefix="txapi-delivery-") as directory:
        try:
            work = Path(directory)
            previous = work / "previous"
            previous.mkdir()
            archive_baseline(previous)
            print(f"Delivery upgrade baseline: {S3_BASELINE}", flush=True)
            upstream = work / "upstream"
            run([args.go, "build", "-o", str(upstream), "./tests/upstream/openai"],
                env=dict(ENV, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64"))
            run(["docker", "build", "--tag", current_image, str(ROOT)], timeout=600)
            run(["docker", "build", "--tag", previous_image, str(previous)], timeout=600)
            for image in (current_image, previous_image):
                config = json.loads(run(["docker", "image", "inspect", image], capture=True).stdout)[0]["Config"]
                assert config["Healthcheck"]["Test"] and config["User"] == "app"

            fresh = Installation(work / "fresh", "txapi-s4-smoke-" + suffix + "-fresh",
                                 current_image, upstream)
            fixtures.append(fresh)
            fresh.init_admin()
            fresh.start()
            key = control_workflow(fresh.base, "http://upstream:9911")
            fresh.stop()
            backup = fresh.backup("installed")
            assert len(snapshot(backup / "gateway.db")["logs"]) == 1
            fresh.start()
            session = login(fresh.base, "console-user", "console-user-test-password")
            assert api(fresh.base, "/api/me/keys", session)["data"][0]["id"] == key["id"]
            assert api(fresh.base, "/v1/models", key["key"])["data"][0]["id"] == "console-model"
            usage(fresh.base, session, 1)
            fresh.stop()
            print("PASS: fresh Compose install, non-root, healthcheck, console workflow and volume restart", flush=True)

            upgrade = Installation(work / "upgrade", "txapi-s4-smoke-" + suffix + "-upgrade",
                                   previous_image, upstream, old=True)
            fixtures.append(upgrade)
            upgrade.init_admin()
            upgrade.start()
            data = previous_workflow(upgrade)
            upgrade.stop()
            before = upgrade.backup("before-s4")
            original = snapshot(before / "gateway.db")
            upgrade.start(image=current_image)
            admin = preserved(upgrade, data)
            assert request(upgrade.base + "/")[0] == 200
            status = api(upgrade.base, "/api/admin/config-status", admin)["data"]
            assert status["channels"]["desired_revision"] == status["channels"]["applied_revision"]
            api(upgrade.base, "/api/admin/users", admin, {
                "username": "post-upgrade", "password": "post-upgrade-test-password",
                "role": "user", "model_mode": "allowlist", "models": [],
            }, expected=201)
            call(upgrade.base, data["key"]["key"])
            upgrade.stop(stream_key=data["key"]["key"])
            after = upgrade.backup("after-s4")
            upgraded = snapshot(after / "gateway.db")
            assert len(upgraded["logs"]) == 4, "Graceful stop did not flush accepted access logs"
            previous_steps = len(original["schema_migrations"])
            assert upgraded["schema_migrations"][:previous_steps] == original["schema_migrations"], "Released migrations changed"
            assert [row[0] for row in upgraded["schema_migrations"][previous_steps:]] == [5, 6], "Missing S5 billing migrations"
            with contextlib.closing(sqlite3.connect((after / "gateway.db").as_uri() + "?mode=ro", uri=True)) as db:
                history = dict(db.execute("SELECT version,payload FROM billing_price_versions").fetchall())
                assert history, "Published price history was not persisted"
                for version, payload in history.items():
                    assert hashlib.sha256(payload.encode()).hexdigest() == version
                versions = db.execute("SELECT price_version FROM logs ORDER BY id").fetchall()
                assert versions[:2] == [("",), ("",)], "Historical logs were assigned invented prices"
                assert all(version in history for version, in versions[2:]), "New requests lack persisted price versions"
            print("PASS: S3/v4 -> current/v6, original identities/keys/usage, price history, in-flight SSE and log drain", flush=True)
            upgrade.restore(before)
            restored = upgrade.backup("restored")
            assert snapshot(restored / "gateway.db") == original, "Restore did not recover the exact baseline data"
            upgrade.start(image=previous_image, old=True)
            admin = preserved(upgrade, data)
            users = api(upgrade.base, "/api/admin/users", admin)["data"]
            assert all(user["username"] != "post-upgrade" for user in users)
            upgrade.stop()
            print("PASS: offline consistent backup/restore, preserved post-upgrade backup and S3 rollback", flush=True)
            for fixture in reversed(fixtures):
                fixture.cleanup()
            fixtures.clear()
        finally:
            try:
                for fixture in reversed(fixtures):
                    try:
                        fixture.diagnostics()
                    finally:
                        fixture.cleanup()
            finally:
                run(["docker", "image", "rm", current_image, previous_image], check=False, timeout=90)


if __name__ == "__main__":
    main()
