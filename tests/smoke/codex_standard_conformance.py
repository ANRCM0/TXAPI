"""Pinned Codex with standard custom-provider settings and a loopback provider.

No model catalog or feature overrides are installed. The CLI keeps its default
tool declarations; the provider simulates cached search and never accesses a
search engine. All credentials, content and databases are disposable fixtures.
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
from local import ROOT, ports, ready
from sdk_conformance import clean_environment, frame, gateway_process, response_document


ALIAS = "txapi-standard-alias"
CATALOG_MODEL = "gpt-5.5"
CASES = (("alias-text", ALIAS), ("alias-function", ALIAS),
         ("catalog-text", CATALOG_MODEL), ("catalog-function", CATALOG_MODEL),
         ("catalog-search", CATALOG_MODEL))
ANSWER = "TXAPI standard fixture \u4f60\u597d\U0001f30f"
SEARCH_TEXT = "Cached loopback fixture result."
SEARCH_ITEM = {"type": "web_search_call", "id": "ws_standard", "status": "completed",
               "action": {"type": "search", "query": "TXAPI loopback fixture"}}
CITATION = {"type": "url_citation", "start_index": 0, "end_index": len(SEARCH_TEXT),
            "url": "https://fixture.invalid/cached-result", "title": "Disposable fixture"}
MARKER = "TXAPI_STANDARD_CASE:"


def configure(home, model, base):
    (home / "config.toml").write_text(f'''model = "{model}"
model_provider = "txapi_fixture"
approval_policy = "never"
sandbox_mode = "read-only"
cli_auth_credentials_store = "file"
[model_providers.txapi_fixture]
name = "TXAPI disposable loopback fixture"
base_url = "{base}"
env_key = "TXAPI_TEST_CODEX_KEY"
wire_api = "responses"
requires_openai_auth = false
''', encoding="utf-8")


def case_name(body):
    names = set()
    for item in body["input"]:
        for part in item.get("content", []) if isinstance(item.get("content"), list) else []:
            text = part.get("text", "")
            for case, _model in CASES:
                if MARKER + case + "." in text:
                    names.add(case)
    assert len(names) == 1, names
    return names.pop()


def tool_members(tools):
    return [member for tool in tools for member in
            (tool["tools"] if tool["type"] == "namespace" else [tool])]


def sse_events(value):
    events = []

    def add(kind, **fields):
        events.append(frame({"type": kind, "sequence_number": len(events), **fields}, kind))

    created = copy.deepcopy(value)
    created.update(status="in_progress", output=[], usage=None)
    add("response.created", response=created)
    for index, item in enumerate(value["output"]):
        pending = copy.deepcopy(item)
        pending["status"] = "in_progress"
        if item["type"] == "function_call":
            pending["arguments"] = ""
        elif item["type"] == "message":
            pending["content"] = []
        add("response.output_item.added", output_index=index, item=pending)
        common = {"item_id": item["id"], "output_index": index}
        if item["type"] == "function_call":
            add("response.function_call_arguments.delta", **common, delta=item["arguments"])
            add("response.function_call_arguments.done", **common, arguments=item["arguments"])
        elif item["type"] == "web_search_call":
            add("response.web_search_call.in_progress", **common)
            add("response.web_search_call.searching", **common)
            add("response.web_search_call.completed", **common)
        else:
            part = item["content"][0]
            add("response.content_part.added", **common, content_index=0,
                part={"type": "output_text", "text": "", "annotations": []})
            add("response.output_text.delta", **common, content_index=0, delta=part["text"])
            for annotation_index, annotation in enumerate(part["annotations"]):
                add("response.output_text.annotation.added", **common, content_index=0,
                    annotation_index=annotation_index, annotation=annotation)
            add("response.output_text.done", **common, content_index=0, text=part["text"])
            add("response.content_part.done", **common, content_index=0, part=part)
        add("response.output_item.done", output_index=index, item=item)
    add("response.completed", response=value)
    return events


def message(identifier, text, annotations=None):
    return {"id": identifier, "type": "message", "role": "assistant", "status": "completed",
            "content": [{"type": "output_text", "text": text, "annotations": annotations or []}]}


class Fixture:
    def __init__(self):
        self.requests = []
        self.errors = []
        self.key = "codex-standard-provider-fixture-only"
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
                    size = int(self.headers["Content-Length"])
                    assert 0 < size <= 1 << 20, size
                    body = json.loads(self.rfile.read(size))
                    case = case_name(body)
                    expected_model = dict(CASES)[case]
                    assert body["model"] == "native-" + expected_model, body["model"]
                    assert body["stream"] is True and body["store"] is False
                    search = [tool for tool in body["tools"] if tool["type"] == "web_search"]
                    assert len(search) == 1 and search[0]["external_web_access"] is False, search
                    if "search_content_types" in search[0]:
                        assert search[0]["search_content_types"] == ["text", "image"], search
                    members = tool_members(body["tools"])
                    assert any(tool.get("name") == "get_goal" for tool in members), members
                    if expected_model == ALIAS:
                        assert any(tool["type"] == "namespace" for tool in body["tools"])
                    else:
                        assert any(tool["type"] == "custom" and tool.get("name") == "apply_patch" for tool in members)
                        assert any(tool["type"] == "tool_search" and tool["execution"] == "client" for tool in members)
                    owner.requests.append((case, body))
                    outputs = [item for item in body["input"] if item.get("type") == "function_call_output"]
                    needs_function = case.endswith(("function", "search"))
                    value = response_document()
                    value.update(id="resp_" + case + ("_final" if outputs else "_first"), model=body["model"])
                    value["output"] = [message("msg_" + case, ANSWER)]
                    if needs_function and not outputs:
                        value["output"] = []
                        if case.endswith("search"):
                            value["output"].extend([copy.deepcopy(SEARCH_ITEM),
                                message("msg_cached_search", SEARCH_TEXT, [copy.deepcopy(CITATION)])])
                        value["output"].append({"type": "function_call", "id": "fc_" + case,
                            "call_id": "call_" + case, "name": "get_goal", "arguments": "{}"})
                    elif needs_function:
                        assert len(outputs) == 1 and outputs[0]["call_id"] == "call_" + case, outputs
                        assert outputs[0]["output"], outputs
                        assert "unavailable" not in outputs[0]["output"].lower(), outputs
                        if case.endswith("search"):
                            items = [item for item in body["input"] if item.get("type") == "web_search_call"]
                            assert len(items) == 1 and items[0] == SEARCH_ITEM, items
                            assert any(item.get("id") == "msg_cached_search" for item in body["input"]), body["input"]
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Connection", "close")
                    self.end_headers()
                    self.close_connection = True
                    for event in sse_events(value):
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
    with tempfile.TemporaryDirectory(prefix="txapi-codex-standard-") as temporary, contextlib.ExitStack() as stack:
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
        key = "codex-standard-client-fixture-only"
        database = work / "gateway.db"
        models = (ALIAS, CATALOG_MODEL)
        config = {"server": {"addr": f"127.0.0.1:{port}", "timeout_seconds": 15, "retry_times": 0},
                  "admin_key": "codex-standard-admin-fixture-only", "database": {"path": str(database),
                  "credential_keys_file": str(work / "credentials.json")}, "billing": {"mode": "disabled"},
                  "responses": {"enabled": True, "keys_file": str(work / "content.json")},
                  "usage_history": {"enabled": True, "instance_records": 32, "owner_records": 32, "retention_seconds": 3600},
                  "channels": [{"name": "standard-" + model, "type": "openai",
                                "base_url": f"http://127.0.0.1:{fixture.server.server_port}", "key": fixture.key,
                                "models": [model], "model_mapping": {model: "native-" + model}} for model in models],
                  "tokens": [{"name": "codex-standard-client", "key": key, "models": list(models)}]}
        config_file = work / "gateway.json"
        config_file.write_text(json.dumps(config), encoding="utf-8")
        for flags, command_env in [(["-init-admin"], dict(env, TXAPI_ADMIN_USERNAME="codex-standard-admin",
                                                        TXAPI_ADMIN_PASSWORD="codex-standard-fixture-only")),
                                   (["-seed-only"], env), (["-claim-token", "1", "-owner-user", "1"], env)]:
            subprocess.run([str(gateway), "-config", str(config_file), *flags], cwd=ROOT, env=command_env,
                           check=True, timeout=30, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with gateway_process(gateway, config_file, work / "gateway.log", env) as process:
            ready(process, port)
            for case, model in CASES:
                workspace = work / case
                workspace.mkdir()
                home = work / (case + "-home")
                home.mkdir()
                configure(home, model, f"http://127.0.0.1:{port}/v1")
                try:
                    result, output, diagnostic = run_cli(cli, ["exec", "--skip-git-repo-check", "--ephemeral", "--json",
                        "--color", "never", "-C", str(workspace), MARKER + case + ". Consume the synthetic fixture response; "
                        "get_goal is read-only. Do not execute commands, edit files, search the web or call any other tool."],
                        workspace, dict(env, CODEX_HOME=str(home), TXAPI_TEST_CODEX_KEY=key, RUST_LOG="off"))
                except subprocess.TimeoutExpired:
                    for name in ("stdout.log", "stderr.log"):
                        print(case, name, (workspace / name).read_text(encoding="utf-8", errors="replace")[-4000:], flush=True)
                    print("provider requests:", len(fixture.requests), "errors:", fixture.errors, flush=True)
                    raise
                events = [json.loads(line) for line in output.splitlines() if line.strip()]
                assert result == 0 and events[-1]["type"] == "turn.completed", (
                    case, result, output[-3000:], diagnostic[-1000:], fixture.errors)
                assert any(event.get("item", {}).get("text") == ANSWER for event in events), (case, output)
                if case.endswith("search"):
                    assert any(event.get("item", {}).get("type") == "web_search" for event in events), output
                    assert any(event.get("item", {}).get("text") == SEARCH_TEXT for event in events), output
                assert key not in output + diagnostic and fixture.key not in output + diagnostic
                assert {path.name for path in workspace.iterdir()} <= {"stdout.log", "stderr.log"}, list(workspace.iterdir())
                assert not (home / "auth.json").exists()
                print("PASS:", case, flush=True)
        assert not fixture.errors, fixture.errors
        counts = collections.Counter(case for case, _body in fixture.requests)
        assert counts == collections.Counter({case: 2 if case.endswith(("function", "search")) else 1
                                               for case, _model in CASES}), counts
        with contextlib.closing(sqlite3.connect(database)) as db:
            assert db.execute("SELECT COUNT(*) FROM usage_invocations").fetchone() == (8,)
            assert db.execute("SELECT COUNT(*) FROM usage_invocations WHERE transport='http' AND state='sealed' AND attempt_count=1").fetchone() == (8,)
            assert db.execute("SELECT COUNT(*) FROM usage_attempt_receipts WHERE may_dispatch=1 AND total_tokens=3").fetchone() == (8,)
            for table in ("billing_reservations", "billing_attempts"):
                assert db.execute(f"SELECT COUNT(*) FROM {table}").fetchone() == (0,)
            for table in ("users", "tokens"):
                assert db.execute(f"SELECT COUNT(*) FROM {table} WHERE used_quota != 0").fetchone() == (0,)
            assert db.execute("SELECT COUNT(*) FROM billing_balances WHERE used_units != 0 OR reserved_units != 0").fetchone() == (0,)
        logs = (work / "gateway.log").read_text(encoding="utf-8", errors="replace")
        assert key not in logs and fixture.key not in logs
    print("PASS: fixed Codex 0.163.0 standard provider settings, alias/bundled model, text/read-only function, "
          "default cached-search declaration, cited result and complete search-item continuation; loopback only")


if __name__ == "__main__":
    main()
