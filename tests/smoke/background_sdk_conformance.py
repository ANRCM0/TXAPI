"""Pinned HTTP SDK background recovery against production main; loopback only."""

import argparse
import base64
import collections
import contextlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.metadata
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import threading
import time
from urllib.parse import parse_qs, urlsplit

from local import ROOT, ports, ready
from sdk_conformance import clean_environment, frame, gateway_process, response_document


class Fixture:
    def __init__(self):
        self.lock = threading.Lock()
        self.calls = collections.Counter()
        self.errors = []
        self.finished = False
        self.cancelled = set()
        self.key = "sk-background-sdk-upstream-only"
        owner = self

        def document(remote_id, status):
            value = response_document(status)
            value.update(id=remote_id, model="m")
            value["usage"].update(input_tokens_details={"cached_tokens": 1},
                                  output_tokens_details={"reasoning_tokens": 0})
            if status != "completed":
                value["output"] = []
            return value

        def event(sequence, remote_id, status):
            kind = "response.completed" if status == "completed" else "response.created"
            return frame({"type": kind, "sequence_number": sequence,
                          "response": document(remote_id, status)}, kind)

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def dispatch(self):
                try:
                    assert self.headers.get("Authorization") == "Bearer " + owner.key
                    url = urlsplit(self.path)
                    query = parse_qs(url.query)
                    data = None
                    if self.command == "POST" and url.path == "/v1/responses":
                        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                        assert body["model"] == "m" and "previous_response_id" not in body
                        if not body.get("background"):
                            assert not body.get("stream")
                            assert len(body["input"]) == 1
                            assert "sdk-history-parent" in json.dumps(body["input"][0])
                            with owner.lock:
                                owner.calls["parent"] += 1
                            data = document("resp_remote_parent", "completed")
                        elif isinstance(body["input"], list):
                            assert body["background"] is True and body["store"] is False
                            assert not body.get("stream") and len(body["input"]) == 3
                            assert "sdk-history-parent" in json.dumps(body["input"][0])
                            assert body["input"][1]["role"] == "assistant"
                            assert body["input"][1]["content"] == response_document()["output"][0]["content"]
                            assert body["input"][2]["role"] == "user"
                            assert body["input"][2]["content"] == "sdk-history-next"
                            assert all("id" not in item for item in body["input"])
                            with owner.lock:
                                owner.calls["background-history"] += 1
                            data = document("resp_remote_history", "completed")
                        else:
                            assert body["background"] is True and body["store"] is False
                            case = "sse" if body.get("stream") else "json"
                            assert body["input"] == "sdk-background-" + case
                            remote_id = "resp_remote_" + case
                            with owner.lock:
                                owner.calls["create-" + case] += 1
                            data = event(0, remote_id, "queued") if case == "sse" else document(remote_id, "in_progress")
                    else:
                        remote_id = url.path.split("/")[-2 if self.command == "POST" else -1]
                        assert remote_id in ("resp_remote_sse", "resp_remote_json")
                        with owner.lock:
                            if self.command == "POST":
                                assert url.path.endswith("/cancel") and not query
                                owner.calls["cancel"] += 1
                                owner.cancelled.add(remote_id)
                                status = "cancelled"
                            else:
                                assert self.command == "GET"
                                status = ("cancelled" if remote_id in owner.cancelled else
                                          "completed" if owner.finished and remote_id.endswith("sse") else "in_progress")
                            if query:
                                assert query == {"stream": ["true"], "starting_after": ["0"]}
                                assert remote_id.endswith("sse")
                                owner.calls["resume"] += 1
                        data = (event(1, remote_id, "completed") if status == "completed" else b"") if query else document(remote_id, status)
                    streaming = isinstance(data, bytes)
                    raw = data if streaming else json.dumps(data).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream" if streaming else "application/json")
                    self.send_header("Content-Length", str(len(raw)))
                    self.end_headers()
                    self.wfile.write(raw)
                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                    pass
                except BaseException as error:
                    with owner.lock:
                        owner.errors.append(type(error).__name__)
                    self.close_connection = True

            do_POST = dispatch
            do_GET = dispatch

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)
        assert not self.thread.is_alive()


def wait_for(read, predicate, message, seconds=10):
    deadline = time.monotonic() + seconds
    while True:
        value = read()
        if predicate(value):
            return value
        assert time.monotonic() < deadline, message
        time.sleep(0.05)


def exercise(binary, work, env, fixture):
    import httpx
    import openai
    from openai import OpenAI
    from openai.types.responses import Response, ResponseCreatedEvent, ResponseCompletedEvent

    port = ports()[0]
    credential_keys, content_keys = work / "credentials.json", work / "content.json"
    for path in (credential_keys, content_keys):
        path.write_text(json.dumps({"active_key": "test", "keys": {"test": base64.b64encode(os.urandom(32)).decode()},
                                    "digest_key": base64.b64encode(os.urandom(32)).decode()}))
        path.chmod(0o600)
    key, other_key = "sk-background-sdk-client-only", "sk-background-sdk-other-key"
    database = work / "gateway.db"
    config = {"server": {"addr": f"127.0.0.1:{port}", "timeout_seconds": 10},
              "admin_key": "background-sdk-admin-only", "database": {"path": str(database),
              "credential_keys_file": str(credential_keys)}, "billing": {"mode": "disabled"},
              "channels": [{"name": "native", "type": "openai", "base_url": f"http://127.0.0.1:{fixture.server.server_port}",
                            "key": fixture.key, "models": ["m"]}],
              "tokens": [{"name": name, "key": token, "models": ["m"]}
                         for name, token in (("client", key), ("other", other_key))],
              "responses": {"enabled": True, "keys_file": str(content_keys),
                            "background": {"enabled": True, "channel_ids": [1]}}}
    config_file = work / "config.json"
    config_file.write_text(json.dumps(config))
    commands = [(["-init-admin"], dict(env, TXAPI_ADMIN_USERNAME="sdk-admin", TXAPI_ADMIN_PASSWORD="sdk-password-only")),
                (["-seed-only"], env), (["-claim-token", "1", "-owner-user", "1"], env),
                (["-claim-token", "2", "-owner-user", "1"], env)]
    for flags, command_env in commands:
        subprocess.run([str(binary), "-config", str(config_file), *flags], cwd=ROOT, env=command_env,
                       check=True, timeout=30, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def sql(query, parameters=()):
        with contextlib.closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True, timeout=3)) as db:
            return db.execute(query, parameters).fetchall()

    def denied(call, status):
        try:
            call()
        except openai.APIStatusError as error:
            assert error.status_code == status, (error.status_code, status)
            assert "resp_remote_" not in error.response.text
        else:
            raise AssertionError("denied SDK operation succeeded")

    with contextlib.ExitStack() as stack:
        def client(token):
            return stack.enter_context(OpenAI(api_key=token, base_url=f"http://127.0.0.1:{port}/v1", max_retries=0,
                                              timeout=8, http_client=httpx.Client(trust_env=False)))
        owner, other = client(key), client(other_key)

        def isolated(public_id):
            denied(lambda: other.responses.retrieve(public_id), 404)
            denied(lambda: other.responses.cancel(public_id), 404)
            denied(lambda: other.responses.retrieve(public_id, stream=True, starting_after=0), 404)

        with gateway_process(binary, config_file, work / "first.log", env) as process:
            ready(process, port)
            with owner.responses.create(model="m", input="sdk-background-sse", background=True, store=False, stream=True) as stream:
                first = next(iter(stream))
                assert isinstance(first, ResponseCreatedEvent) and first.sequence_number == 0
                public_id = first.response.id
                assert public_id and "resp_remote_" not in public_id
            isolated(public_id)
            assert owner.responses.retrieve(public_id).status in ("queued", "in_progress")
            with fixture.lock:
                assert fixture.calls["create-sse"] == 1 and fixture.calls["cancel"] == 0
            process.kill()
            process.wait(timeout=5)

        with gateway_process(binary, config_file, work / "restart.log", env) as process:
            ready(process, port)
            assert owner.responses.retrieve(public_id).status in ("queued", "in_progress")
            isolated(public_id)
            with fixture.lock:
                fixture.finished = True
            # A killed owner retains its real 30-second claim; do not edit leases.
            complete = wait_for(lambda: owner.responses.retrieve(public_id), lambda value: value.status == "completed",
                                "SDK task did not recover after Kill/Wait", seconds=45)
            assert isinstance(complete, Response) and complete.id == public_id
            assert complete.output_text == response_document()["output"][0]["content"][0]["text"]
            with owner.responses.retrieve(public_id, stream=True, starting_after=0) as stream:
                events = list(stream)
            assert len(events) == 1 and isinstance(events[0], ResponseCompletedEvent)
            assert events[0].sequence_number == 1 and events[0].response.id == public_id
            assert events[0].response.status == "completed"
            with owner.responses.retrieve(public_id, stream=True, starting_after=1) as stream:
                assert list(stream) == []
            denied(lambda: owner.responses.retrieve(public_id, stream=True, starting_after=-1), 400)

            created = owner.responses.create(model="m", input="sdk-background-json", background=True, store=False)
            assert isinstance(created, Response) and created.status in ("queued", "in_progress")
            cancel_id = created.id
            wait_for(lambda: sql("SELECT stage FROM background_jobs WHERE public_id=?", (cancel_id,)),
                     lambda rows: rows == [("remote_active",)], "JSON task never became remote active")
            isolated(cancel_id)
            assert isinstance(owner.responses.cancel(cancel_id), Response)
            cancelled = wait_for(lambda: owner.responses.retrieve(cancel_id), lambda value: value.status == "cancelled",
                                 "SDK cancellation did not reach a remote terminal")
            assert cancelled.id == cancel_id
            assert owner.responses.cancel(cancel_id).status == "cancelled"
            denied(lambda: owner.responses.retrieve(cancel_id, stream=True), 409)
            owner.responses.delete(cancel_id)
            denied(lambda: owner.responses.retrieve(cancel_id), 404)
            usage_query = ("SELECT public_id,stage,deleted,usage_input_tokens,usage_output_tokens,usage_total_tokens,"
                           "usage_cached_input_tokens,usage_cache_write_input_tokens,usage_reasoning_output_tokens,"
                           "usage_completeness,usage_source,usage_profile FROM background_jobs")
            rows = {row[0]: row[1:] for row in sql(usage_query)}
            expected_usage = (2, 1, 3, 1, None, 0, "complete", "provider_reported", "responses_native_tokens_v2")
            assert rows == {public_id: ("terminal", 0, *expected_usage),
                            cancel_id: ("terminal", 1, *expected_usage)}, rows
            assert sql("SELECT COUNT(*) FROM background_events") == [(2,)]
            assert sql("SELECT COUNT(*) FROM background_jobs") == [(2,)]
        with gateway_process(binary, config_file, work / "sealed-restart.log", env) as process:
            ready(process, port)
            assert owner.responses.retrieve(public_id).model_dump() == complete.model_dump()
            denied(lambda: owner.responses.retrieve(cancel_id), 404)
            isolated(public_id)
            assert {row[0]: row[1:] for row in sql(usage_query)} == rows

            # Keep the original recovery/usage assertions above exact; this is
            # the independent SDK consumer of the local-history input bridge.
            parent = owner.responses.create(model="m", input="sdk-history-parent")
            assert isinstance(parent, Response) and parent.status == "completed"
            assert sql("SELECT COUNT(*) FROM response_histories WHERE id=? AND deleted=0",
                       (parent.id,)) == [(1,)]
            denied(lambda: other.responses.create(model="m", input="sdk-history-next", background=True,
                                                   store=False, previous_response_id=parent.id), 404)
            with fixture.lock:
                assert fixture.calls["parent"] == 1 and fixture.calls["background-history"] == 0
            continuation = dict(model="m", input="sdk-history-next", background=True, store=False,
                                previous_response_id=parent.id,
                                extra_headers={"Idempotency-Key": "sdk-history-once"})
            task = owner.responses.create(**continuation)
            assert isinstance(task, Response) and task.id != parent.id
            owner.responses.delete(parent.id)
            denied(lambda: owner.responses.retrieve(parent.id), 404)
            replay = owner.responses.create(**continuation)
            assert replay.id == task.id, "deleted parent broke accepted-task idempotency"
            terminal = wait_for(lambda: owner.responses.retrieve(task.id),
                                lambda value: value.status == "completed",
                                "SDK history background task did not complete")
            assert terminal.id == task.id and terminal.output_text == complete.output_text
            assert sql("SELECT COUNT(*) FROM response_histories WHERE id=?", (task.id,)) == [(0,)], \
                "background result was published as permanent history"
        with fixture.lock:
            assert fixture.calls["create-sse"] == fixture.calls["create-json"] == fixture.calls["cancel"] == 1
            assert fixture.calls["parent"] == fixture.calls["background-history"] == 1
            assert fixture.calls["resume"] >= 1 and not fixture.errors, fixture.errors
        for path in work.glob("*.log"):
            text = path.read_text(encoding="utf-8", errors="replace")
            assert all(secret not in text for secret in (key, other_key, fixture.key)), "credential appeared in service log"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--gateway", type=Path, help="Use an existing production binary")
    args = parser.parse_args()
    assert importlib.metadata.version("openai") == "2.11.0"
    assert importlib.metadata.version("httpx") == "0.28.1"
    env = clean_environment()
    for key in list(os.environ):
        if key not in env:
            del os.environ[key]
    with tempfile.TemporaryDirectory(prefix="txapi-background-sdk-") as temporary, contextlib.ExitStack() as stack:
        work = Path(temporary)
        binary = args.gateway.resolve() if args.gateway else work / ("gateway.exe" if os.name == "nt" else "gateway")
        if not args.gateway:
            subprocess.run([args.go, "build", "-o", str(binary), "./cmd/gateway"], cwd=ROOT, env=env, check=True, timeout=240)
        fixture = Fixture()
        stack.callback(fixture.close)
        exercise(binary, work, env, fixture)
    print("PASS: pinned HTTP SDK background typed create/retrieve/cursor, observer close, Kill/Wait recovery, cancel/delete, Key isolation and exact persisted usage; two original creates/one cancel plus one saved parent/one history background, parent deletion/idempotent replay and no permanent background history; loopback only")


if __name__ == "__main__":
    main()
