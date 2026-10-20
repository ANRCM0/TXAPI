"""Pinned Codex automatic local/V2 compaction through the production gateway.

The custom provider keeps default client features, tools and bundled metadata.
Only the V2 case declares remote_compaction="v2". Context window/threshold and
synthetic high usage are explicit trigger conditions, not capacity measurements.
The provider, credentials, database, opaque content and CLI homes are disposable;
no real provider, search engine, user configuration or account is used.
The ephemeral client's get_goal call exercises real function dispatch and its
fixed persistent-thread refusal; it does not prove a successful goal read.
"""

import argparse
import base64
import collections
import contextlib
import copy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import threading

from codex_conformance import VERSION, run_cli
from codex_standard_conformance import message, sse_events, tool_members
from local import ROOT, ports, ready
from sdk_conformance import clean_environment, frame, gateway_process, response_document


MODEL = "gpt-5.5"
MODES = ("default-local", "explicit-v2")
CONTEXT_WINDOW = 200000
COMPACT_THRESHOLD = 100000
HIGH_INPUT_TOKENS = 150000
SUMMARY = "TXAPI disposable local compaction summary."
ANSWER = "TXAPI compaction fixture \u4f60\u597d\U0001f30f"
OPAQUE = "txapi-fixture-opaque-compaction-no-real-provider-content"
COMPACTION = {"type": "compaction", "id": "cmp_fixture", "encrypted_content": OPAQUE}


def configure(home, base, mode):
    config = f'''model = "{MODEL}"
model_provider = "txapi_fixture"
model_context_window = {CONTEXT_WINDOW}
model_auto_compact_token_limit = {COMPACT_THRESHOLD}
approval_policy = "never"
sandbox_mode = "read-only"
cli_auth_credentials_store = "file"
[model_providers.txapi_fixture]
name = "TXAPI disposable loopback compaction fixture"
base_url = "{base}"
env_key = "TXAPI_TEST_CODEX_KEY"
wire_api = "responses"
requires_openai_auth = false
'''
    if mode == "explicit-v2":
        config += '[model_providers.txapi_fixture.capabilities]\nremote_compaction = "v2"\n'
    (home / "config.toml").write_text(config, encoding="utf-8")


def compaction_events(value):
    created = copy.deepcopy(value)
    created.update(status="in_progress", output=[], usage=None)
    item = value["output"][0]
    events = (("response.created", {"response": created}),
              ("response.output_item.added", {"output_index": 0, "item": item}),
              ("response.output_item.done", {"output_index": 0, "item": item}),
              ("response.completed", {"response": value}))
    return [frame({"type": kind, "sequence_number": index, **fields}, kind)
            for index, (kind, fields) in enumerate(events)]


def texts(body):
    return [part.get("text", "") for item in body["input"]
            for part in item.get("content", []) if isinstance(part, dict)]


def assert_default_tools(tools):
    members = tool_members(tools)
    assert any(tool.get("name") == "get_goal" for tool in members), members
    assert any(tool["type"] == "custom" and tool.get("name") == "apply_patch" for tool in members)
    assert any(tool["type"] == "tool_search" and tool["execution"] == "client" for tool in members)
    searches = [tool for tool in tools if tool["type"] == "web_search"]
    assert len(searches) == 1 and searches[0]["external_web_access"] is False, searches


class Fixture:
    def __init__(self):
        self.mode = None
        self.requests = []
        self.errors = []
        self.key = "codex-compaction-provider-fixture-only"
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                owner.errors.append("unexpected provider GET: " + self.path)
                self.send_error(404)

            def do_POST(self):
                try:
                    self.connection.settimeout(5)
                    assert self.path == "/v1/responses", self.path
                    assert self.headers.get("Authorization") == "Bearer " + owner.key
                    # Pinned gpt-5.5 does not enable Responses Lite, including
                    # its V2 trigger request. Trigger alone must not add it.
                    assert self.headers.get("x-openai-internal-codex-responses-lite") is None
                    size = int(self.headers["Content-Length"])
                    assert 0 < size <= 1 << 20, size
                    body = json.loads(self.rfile.read(size))
                    assert body["model"] == "native-" + MODEL, body["model"]
                    assert body["stream"] is True and body["store"] is False
                    assert "context_management" not in body
                    mode = owner.mode
                    assert mode in MODES, mode
                    owner.requests.append((mode, body))
                    stage = sum(previous_mode == mode for previous_mode, _request in owner.requests)
                    assert 1 <= stage <= 3, (mode, stage)
                    value = response_document()
                    value.update(id="resp_" + mode + "_" + str(stage), model=body["model"])
                    value["output"] = [message("msg_" + mode + "_" + str(stage), ANSWER)]
                    triggers = [item for item in body["input"] if item.get("type") == "compaction_trigger"]
                    compacted = [item for item in body["input"] if item.get("type") == "compaction"]
                    if stage == 1:
                        assert not triggers and not compacted
                        assert_default_tools(body["tools"])
                        value["output"] = [{"type": "function_call", "id": "fc_" + mode,
                            "call_id": "call_" + mode, "name": "get_goal", "arguments": "{}"}]
                        value["usage"] = {"input_tokens": HIGH_INPUT_TOKENS, "output_tokens": 1,
                                          "total_tokens": HIGH_INPUT_TOKENS + 1}
                    elif stage == 2:
                        results = [item for item in body["input"] if item.get("type") == "function_call_output"]
                        assert len(results) == 1 and results[0]["call_id"] == "call_" + mode, results
                        assert results[0]["output"] == "Goal tools require a persistent thread.", results
                        if mode == "explicit-v2":
                            assert triggers == [{"type": "compaction_trigger"}]
                            assert body["input"][-1] == triggers[0]
                            assert not compacted
                            assert_default_tools(body["tools"])
                            value["output"] = [copy.deepcopy(COMPACTION)]
                        else:
                            assert not triggers and not compacted and body["tools"] == []
                            assert "CONTEXT CHECKPOINT COMPACTION" in texts(body)[-1]
                            value["output"] = [message("msg_local_summary", SUMMARY)]
                    else:
                        assert not triggers
                        assert_default_tools(body["tools"])
                        if mode == "explicit-v2":
                            assert compacted == [COMPACTION], compacted
                            assert not any(item.get("type") in ("function_call", "function_call_output")
                                           for item in body["input"]), body["input"]
                        else:
                            assert not compacted and any(SUMMARY in text for text in texts(body))
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Connection", "close")
                    self.end_headers()
                    self.close_connection = True
                    events = compaction_events(value) if mode == "explicit-v2" and stage == 2 else sse_events(value)
                    for event in events:
                        for offset in range(0, len(event), 3):
                            self.wfile.write(event[offset:offset + 3])
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                    pass
                except BaseException as error:
                    owner.errors.append(type(error).__name__ + ": " + str(error))
                    with contextlib.suppress(OSError):
                        self.send_error(500, "synthetic fixture assertion failed")

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        assert not self.thread.is_alive()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", required=True, type=Path)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    cli = args.codex.resolve()
    env = {key: value for key, value in clean_environment().items()
           if not key.upper().startswith(("CODEX_", "OTEL_"))}
    env["GOARCH"] = "amd64"
    with tempfile.TemporaryDirectory(prefix="txapi-codex-compaction-") as temporary, contextlib.ExitStack() as stack:
        work = Path(temporary)
        version_dir = work / "version"
        version_dir.mkdir()
        version_home = work / "version-home"
        version_home.mkdir()
        result, version, _diagnostic = run_cli(cli, ["--version"], version_dir,
                                             dict(env, CODEX_HOME=str(version_home), RUST_LOG="off"))
        assert result == 0 and version.strip() == VERSION, version
        gateway = work / ("gateway.exe" if os.name == "nt" else "gateway")
        subprocess.run([args.go, "build", "-o", str(gateway), "./cmd/gateway"], cwd=ROOT,
                       env=env, check=True, timeout=180)
        fixture = Fixture()
        stack.callback(fixture.close)
        port = ports()[0]
        for name in ("credentials", "content"):
            path = work / (name + ".json")
            path.write_text(json.dumps({"active_key": "test", "keys": {"test": base64.b64encode(os.urandom(32)).decode()},
                                        "digest_key": base64.b64encode(os.urandom(32)).decode()}), encoding="utf-8")
            path.chmod(0o600)
        key = "codex-compaction-client-fixture-only"
        database = work / "gateway.db"
        config = {"server": {"addr": f"127.0.0.1:{port}", "timeout_seconds": 15, "retry_times": 0},
                  "admin_key": "codex-compaction-admin-fixture-only", "database": {"path": str(database),
                  "credential_keys_file": str(work / "credentials.json")}, "billing": {"mode": "disabled"},
                  "responses": {"enabled": True, "keys_file": str(work / "content.json")},
                  "usage_history": {"enabled": True, "instance_records": 32, "owner_records": 32, "retention_seconds": 3600},
                  "channels": [{"name": "codex-compaction", "type": "openai",
                                "base_url": f"http://127.0.0.1:{fixture.server.server_port}", "key": fixture.key,
                                "models": [MODEL], "model_mapping": {MODEL: "native-" + MODEL}}],
                  "tokens": [{"name": "codex-compaction-client", "key": key, "models": [MODEL]}]}
        config_file = work / "gateway.json"
        config_file.write_text(json.dumps(config), encoding="utf-8")
        for flags, command_env in [(["-init-admin"], dict(env, TXAPI_ADMIN_USERNAME="codex-compaction-admin",
                                                        TXAPI_ADMIN_PASSWORD="codex-compaction-fixture-only")),
                                   (["-seed-only"], env), (["-claim-token", "1", "-owner-user", "1"], env)]:
            subprocess.run([str(gateway), "-config", str(config_file), *flags], cwd=ROOT, env=command_env,
                           check=True, timeout=30, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with gateway_process(gateway, config_file, work / "gateway.log", env) as process:
            ready(process, port)
            for mode in MODES:
                fixture.mode = mode
                workspace = work / mode
                workspace.mkdir()
                home = work / (mode + "-home")
                home.mkdir()
                configure(home, f"http://127.0.0.1:{port}/v1", mode)
                try:
                    result, output, diagnostic = run_cli(cli, ["exec", "--skip-git-repo-check", "--ephemeral", "--json",
                        "--color", "never", "-C", str(workspace), "Consume only the disposable fixture response. "
                        "get_goal is read-only. Do not execute commands, edit files, search the web or call any other tool."],
                        workspace, dict(env, CODEX_HOME=str(home), TXAPI_TEST_CODEX_KEY=key, RUST_LOG="off"), seconds=45)
                except subprocess.TimeoutExpired:
                    for name in ("stdout.log", "stderr.log"):
                        print(mode, name, (workspace / name).read_text(encoding="utf-8", errors="replace")[-4000:], flush=True)
                    print("provider requests:", len(fixture.requests), "errors:", fixture.errors, flush=True)
                    raise
                events = [json.loads(line) for line in output.splitlines() if line.strip()]
                assert result == 0 and events[-1]["type"] == "turn.completed", (
                    mode, result, output[-3000:], diagnostic[-1000:], fixture.errors)
                assert any(event.get("item", {}).get("text") == ANSWER for event in events), (mode, output)
                for secret in (key, fixture.key, OPAQUE):
                    assert secret not in output + diagnostic
                assert {path.name for path in workspace.iterdir()} <= {"stdout.log", "stderr.log"}, list(workspace.iterdir())
                assert not (home / "auth.json").exists()
                print("PASS:", mode, "three Responses requests, final text completed", flush=True)
        assert not fixture.errors, fixture.errors
        assert collections.Counter(mode for mode, _body in fixture.requests) == collections.Counter({mode: 3 for mode in MODES})
        with contextlib.closing(sqlite3.connect(database)) as db:
            assert db.execute("SELECT COUNT(*) FROM usage_invocations").fetchone() == (6,)
            assert db.execute("SELECT COUNT(*) FROM usage_invocations WHERE transport='http' AND state='sealed' AND attempt_count=1").fetchone() == (6,)
            assert db.execute("SELECT COUNT(*) FROM usage_attempt_receipts").fetchone() == (6,)
            assert db.execute("SELECT COUNT(*) FROM usage_attempt_receipts WHERE may_dispatch=1").fetchone() == (6,)
            assert db.execute("SELECT COUNT(*) FROM usage_attempt_receipts WHERE input_tokens=? AND output_tokens=1 AND total_tokens=?",
                              (HIGH_INPUT_TOKENS, HIGH_INPUT_TOKENS + 1)).fetchone() == (2,)
            assert db.execute("SELECT COUNT(*) FROM usage_attempt_receipts WHERE input_tokens=2 AND output_tokens=1 AND total_tokens=3").fetchone() == (4,)
            for table in ("billing_reservations", "billing_attempts"):
                assert db.execute(f"SELECT COUNT(*) FROM {table}").fetchone() == (0,)
            for table in ("users", "tokens"):
                assert db.execute(f"SELECT COUNT(*) FROM {table} WHERE used_quota != 0").fetchone() == (0,)
            assert db.execute("SELECT COUNT(*) FROM billing_balances WHERE used_units != 0 OR reserved_units != 0").fetchone() == (0,)
        logs = (work / "gateway.log").read_text(encoding="utf-8", errors="replace")
        for secret in (key, fixture.key, OPAQUE):
            assert secret not in logs
    print("PASS: fixed Codex 0.163.0 default local summary and explicitly declared remote V2, "
          "real threshold-triggered compaction and opaque continuation; loopback only, not a capacity result")


if __name__ == "__main__":
    main()
