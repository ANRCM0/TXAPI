"""Fixed Codex CLI against the production gateway and disposable local provider.

Requires the official 0.163.0 binary; CI verifies the release archive digest.
Named functions, namespaces and native custom tools are exercised. No user settings or service
credentials are loaded, and the fixture never asks the client to run a command.
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
import signal
import sqlite3
import subprocess
import tempfile
import threading

from local import ROOT, ports, ready
from sdk_conformance import clean_environment, frame, gateway_process, response_document


VERSION = "codex-cli 0.163.0"
CASES = ("cli-text", "cli-function-result", "cli-failed", "cli-incomplete", "cli-truncated", "cli-namespace", "cli-custom")
ANSWER = "TXAPI fixture 你好🌏"
INVALID_PATCH = "TXAPI deliberately invalid patch 雪🌏; no paths or patch commands"


def client_model(case):
    return case


class WindowsJob:
    """Close the child's job, including helpers inheriting its log handles."""

    def __init__(self):
        self.handle = None
        if os.name != "nt":
            return
        import ctypes
        from ctypes import wintypes

        class Basic(ctypes.Structure):
            _fields_ = [("ProcessTime", ctypes.c_int64), ("JobTime", ctypes.c_int64),
                        ("Flags", wintypes.DWORD), ("MinWorkingSet", ctypes.c_size_t),
                        ("MaxWorkingSet", ctypes.c_size_t), ("ActiveProcessLimit", wintypes.DWORD),
                        ("Affinity", ctypes.c_size_t), ("PriorityClass", wintypes.DWORD),
                        ("SchedulingClass", wintypes.DWORD)]

        class IO(ctypes.Structure):
            _fields_ = [(name, ctypes.c_uint64) for name in
                        ("ReadOps", "WriteOps", "OtherOps", "ReadBytes", "WriteBytes", "OtherBytes")]

        class Extended(ctypes.Structure):
            _fields_ = [("Basic", Basic), ("IO", IO), ("ProcessMemory", ctypes.c_size_t),
                        ("JobMemory", ctypes.c_size_t), ("PeakProcessMemory", ctypes.c_size_t),
                        ("PeakJobMemory", ctypes.c_size_t)]

        self.api = ctypes.WinDLL("kernel32", use_last_error=True)
        self.api.CreateJobObjectW.argtypes = [ctypes.c_void_p, wintypes.LPCWSTR]
        self.api.CreateJobObjectW.restype = wintypes.HANDLE
        self.api.SetInformationJobObject.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.DWORD]
        self.api.AssignProcessToJobObject.argtypes = [wintypes.HANDLE, wintypes.HANDLE]
        self.api.CloseHandle.argtypes = [wintypes.HANDLE]
        self.handle = self.api.CreateJobObjectW(None, None)
        if not self.handle:
            raise ctypes.WinError(ctypes.get_last_error())
        limits = Extended()
        limits.Basic.Flags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        if not self.api.SetInformationJobObject(self.handle, 9, ctypes.byref(limits), ctypes.sizeof(limits)):
            self.close()
            raise ctypes.WinError(ctypes.get_last_error())

    def assign(self, process):
        if self.handle and not self.api.AssignProcessToJobObject(self.handle, int(process._handle)):
            import ctypes
            raise ctypes.WinError(ctypes.get_last_error())

    def close(self):
        if self.handle:
            self.api.CloseHandle(self.handle)
            self.handle = None


def run_cli(binary, arguments, work, env, seconds=20):
    out, err = work / "stdout.log", work / "stderr.log"
    job = WindowsJob()
    process = None
    try:
        # Files avoid a descendant holding communicate()'s pipe readers open.
        with out.open("w", encoding="utf-8") as stdout, err.open("w", encoding="utf-8") as stderr:
            process = subprocess.Popen([str(binary), *arguments], cwd=work, env=env,
                                       stdin=subprocess.DEVNULL, stdout=stdout, stderr=stderr,
                                       start_new_session=os.name != "nt")
            job.assign(process)
            result = process.wait(timeout=seconds)
    finally:
        if process is not None:
            if os.name != "nt":
                with contextlib.suppress(ProcessLookupError):
                    os.killpg(process.pid, signal.SIGKILL)
            elif process.poll() is None:
                process.kill()
            job.close()
            process.wait(timeout=5)
        else:
            job.close()
    return result, out.read_text(encoding="utf-8", errors="replace"), err.read_text(encoding="utf-8", errors="replace")


class Fixture:
    def __init__(self):
        self.requests = []
        self.errors = []
        self.key = "codex-provider-fixture-only"
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_POST(self):
                try:
                    self.connection.settimeout(5)
                    assert self.path == "/v1/responses"
                    assert self.headers.get("Authorization") == "Bearer " + owner.key
                    length = int(self.headers["Content-Length"])
                    assert 0 < length <= 1 << 20
                    body = json.loads(self.rfile.read(length))
                    case = body["model"].removeprefix("native-")
                    assert case in CASES and body["stream"] and body["store"] is False
                    assert body["input"]
                    assert all(tool["type"] in ("function", "namespace", "custom") for tool in body["tools"])
                    if case == "cli-namespace":
                        namespace = next(tool for tool in body["tools"] if tool["type"] == "namespace" and
                                         any(member["name"] == "wait_agent" for member in tool["tools"]))
                        assert namespace["name"] == "multi_agent_v1", namespace
                        wait_tool = next(member for member in namespace["tools"] if member["name"] == "wait_agent")
                        assert wait_tool["parameters"]["required"] == ["targets"], wait_tool
                    elif case == "cli-custom":
                        members = [member for tool in body["tools"] for member in
                                   (tool["tools"] if tool["type"] == "namespace" else [tool])]
                        custom = next(tool for tool in members if tool["type"] == "custom" and tool["name"] == "apply_patch")
                        assert custom["format"]["type"] == "grammar" and custom["format"]["syntax"] == "lark", custom
                    else:
                        assert all(tool["type"] == "function" for tool in body["tools"])
                    owner.requests.append(body)
                    value = response_document()
                    value["model"], value["output"][0]["content"][0]["text"] = body["model"], ANSWER
                    function_result = any(item.get("type") in ("function_call_output", "custom_tool_call_output") for item in body["input"])
                    if case == "cli-function-result" and not function_result:
                        assert any(tool["name"] == "request_user_input" for tool in body["tools"])
                        value["output"] = [{"type": "function_call", "id": "fc_cli", "call_id": "call_cli",
                                            "name": "request_user_input", "arguments": json.dumps({"questions": [{
                                            "header": "Fixture", "id": "fixture", "question": "Synthetic question",
                                            "options": [{"label": "One", "description": "Fixture one"},
                                                        {"label": "Two", "description": "Fixture two"}]}]})}]
                    elif case == "cli-namespace" and not function_result:
                        # A nonexistent agent ID returns a local error; no agent
                        # is created, messaged or given work by this fixture.
                        value["output"] = [{"type": "function_call", "id": "fc_namespace", "call_id": "call_namespace",
                                            "namespace": namespace["name"], "name": "wait_agent",
                                            "arguments": json.dumps({"targets": ["00000000-0000-0000-0000-000000000001"],
                                                                     "timeout_ms": 1000})}]
                    elif case == "cli-custom" and not function_result:
                        # This string is not a patch and contains no file path.
                        # The real CLI parser returns an error before file I/O.
                        value["output"] = [{"type": "custom_tool_call", "id": "ctc_cli", "call_id": "call_custom",
                                            "name": "apply_patch", "input": INVALID_PATCH}]
                    elif case == "cli-failed":
                        value["status"] = "failed"
                        value["error"] = {"code": "server_error", "message": "synthetic CLI provider failure"}
                        value["usage"] = None
                    elif case == "cli-incomplete":
                        value["status"] = "incomplete"
                        value["incomplete_details"] = {"reason": "max_output_tokens"}
                        value["usage"] = None
                    events = []

                    def add(kind, **fields):
                        events.append(frame({"type": kind, "sequence_number": len(events), **fields}, kind))

                    created = copy.deepcopy(value)
                    created.update(status="in_progress", output=[], usage=None, error=None, incomplete_details=None)
                    add("response.created", response=created)
                    item = value["output"][0]
                    pending = copy.deepcopy(item)
                    pending["status"] = "in_progress"
                    if item["type"] == "function_call":
                        pending["arguments"] = ""
                        add("response.output_item.added", output_index=0, item=pending)
                        add("response.function_call_arguments.delta", item_id=item["id"], output_index=0, delta=item["arguments"])
                        add("response.function_call_arguments.done", item_id=item["id"], output_index=0, arguments=item["arguments"])
                    elif item["type"] == "custom_tool_call":
                        pending["input"] = ""
                        add("response.output_item.added", output_index=0, item=pending)
                        for chunk in (INVALID_PATCH[:30], INVALID_PATCH[30:]):
                            add("response.custom_tool_call_input.delta", item_id=item["id"], output_index=0, delta=chunk)
                        add("response.custom_tool_call_input.done", item_id=item["id"], output_index=0, input=INVALID_PATCH)
                    else:
                        pending["content"] = []
                        add("response.output_item.added", output_index=0, item=pending)
                        add("response.content_part.added", item_id=item["id"], output_index=0, content_index=0,
                            part={"type": "output_text", "text": "", "annotations": []})
                        add("response.output_text.delta", item_id=item["id"], output_index=0, content_index=0, delta=ANSWER)
                        add("response.output_text.done", item_id=item["id"], output_index=0, content_index=0, text=ANSWER)
                        add("response.content_part.done", item_id=item["id"], output_index=0, content_index=0, part=item["content"][0])
                    add("response.output_item.done", output_index=0, item=item)
                    if case != "cli-truncated":
                        add("response." + value["status"], response=value)
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Connection", "close")
                    self.end_headers()
                    self.close_connection = True
                    for event in events:
                        # Cut inside UTF-8 code points without changing SSE events.
                        for index in range(0, len(event), 3):
                            self.wfile.write(event[index:index + 3])
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                    pass
                except BaseException as error:
                    owner.errors.append(type(error).__name__ + ": " + str(error))

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        assert not self.thread.is_alive()


def configure(home, model, base):
    # Retain named functions and enable the client's real function namespace
    # registry for its dedicated case. The custom case uses actual client
    # declarations, including the apply_patch freeform grammar.
    catalog_option = ""
    if model == "cli-custom":
        # Explicit local-provider capability profile, not the bundled default.
        # Default catalog models also advertise tool_search / Code Mode, which
        # this batch deliberately rejects. No upstream model instructions copied.
        catalog = home / "custom-models.json"
        catalog.write_text(json.dumps({"models": [{
            "slug": model, "display_name": "TXAPI custom fixture", "description": None,
            "supported_reasoning_levels": [], "shell_type": "unified_exec", "visibility": "none",
            "supported_in_api": True, "priority": 1, "support_verbosity": False,
            "apply_patch_tool_type": "freeform", "supports_search_tool": False,
            "truncation_policy": {"mode": "bytes", "limit": 10000},
            "experimental_supported_tools": [], "input_modalities": ["text"],
            "context_window": 272000, "include_apps_usage_instructions": False,
            "base_instructions": "Follow the user's request and consume the synthetic fixture response. Do not execute commands.",
        }]}), encoding="utf-8")
        catalog_option = "model_catalog_json = " + json.dumps(str(catalog.resolve())) + "\n"
    (home / "config.toml").write_text(f'''model = "{client_model(model)}"
{catalog_option}model_provider = "txapi_fixture"
approval_policy = "never"
sandbox_mode = "read-only"
web_search = "disabled"
cli_auth_credentials_store = "file"
project_root_markers = []
[features]
apply_patch_freeform = {str(model == 'cli-custom').lower()}
multi_agent = {str(model in ('cli-namespace', 'cli-custom')).lower()}
goals = {str(model == 'cli-custom').lower()}
apps = false
[model_providers.txapi_fixture]
name = "TXAPI loopback fixture"
base_url = "{base}"
env_key = "TXAPI_TEST_CODEX_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
stream_idle_timeout_ms = 3000
''', encoding="utf-8")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", required=True, type=Path)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    cli = args.codex.resolve()
    env = {key: value for key, value in clean_environment().items()
           if not key.upper().startswith(("CODEX_", "OTEL_"))}
    env["GOARCH"] = "amd64"
    with tempfile.TemporaryDirectory(prefix="txapi-codex-") as temporary, contextlib.ExitStack() as stack:
        work = Path(temporary)
        cli_home = work / "codex-home"
        cli_home.mkdir()
        env.update(CODEX_HOME=str(cli_home), RUST_LOG="off")
        result, version, _ = run_cli(cli, ["--version"], work, env)
        assert result == 0 and version.strip() == VERSION, version
        gateway = work / ("gateway.exe" if os.name == "nt" else "gateway")
        subprocess.run([args.go, "build", "-o", str(gateway), "./cmd/gateway"], cwd=ROOT, env=env, check=True, timeout=180)
        fixture = Fixture()
        stack.callback(fixture.close)
        port = ports()[0]
        for name in ("credentials", "content"):
            path = work / (name + ".json")
            path.write_text(json.dumps({"active_key": "test", "keys": {"test": base64.b64encode(os.urandom(32)).decode()},
                                        "digest_key": base64.b64encode(os.urandom(32)).decode()}))
            path.chmod(0o600)
        key = "codex-client-fixture-only"
        database = work / "gateway.db"
        config = {"server": {"addr": f"127.0.0.1:{port}", "timeout_seconds": 10, "retry_times": 0},
                  "admin_key": "codex-admin-fixture-only", "database": {"path": str(database),
                  "credential_keys_file": str(work / "credentials.json")}, "billing": {"mode": "disabled"},
                  "responses": {"enabled": True, "keys_file": str(work / "content.json")},
                  "usage_history": {"enabled": True, "instance_records": 32, "owner_records": 32, "retention_seconds": 3600},
                  "channels": [{"name": case, "type": "openai", "base_url": f"http://127.0.0.1:{fixture.server.server_port}",
                                "key": fixture.key, "models": [client_model(case)], "model_mapping": {client_model(case): "native-" + case}} for case in CASES],
                  "tokens": [{"name": "codex-client", "key": key, "models": [client_model(case) for case in CASES]}]}
        config_file = work / "gateway.json"
        config_file.write_text(json.dumps(config))
        for flags, command_env in [(["-init-admin"], dict(env, TXAPI_ADMIN_USERNAME="codex-admin", TXAPI_ADMIN_PASSWORD="codex-fixture-only")),
                                   (["-seed-only"], env), (["-claim-token", "1", "-owner-user", "1"], env)]:
            subprocess.run([str(gateway), "-config", str(config_file), *flags], cwd=ROOT, env=command_env,
                           check=True, timeout=30, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with gateway_process(gateway, config_file, work / "gateway.log", env) as process:
            ready(process, port)
            for case in CASES:
                workspace = work / case
                workspace.mkdir()
                configure(cli_home, case, f"http://127.0.0.1:{port}/v1")
                result, output, diagnostic = run_cli(cli, ["exec", "--skip-git-repo-check", "--ephemeral", "--json",
                    "--color", "never", "-C", str(workspace), "Use the synthetic fixture response. Do not execute commands."],
                    workspace, dict(env, TXAPI_TEST_CODEX_KEY=key))
                events = [json.loads(line) for line in output.splitlines() if line.strip()]
                if case in ("cli-text", "cli-function-result", "cli-namespace", "cli-custom"):
                    assert result == 0 and events[-1]["type"] == "turn.completed", (case, result, output[-2000:], diagnostic[-1000:], fixture.errors)
                    assert any(event.get("item", {}).get("text") == ANSWER for event in events), (case, output)
                else:
                    assert result != 0 and any(event["type"] == "turn.failed" for event in events), (case, output)
                assert key not in output + diagnostic and fixture.key not in output + diagnostic
                assert {path.name for path in workspace.iterdir()} <= {"stdout.log", "stderr.log"}, list(workspace.iterdir())
                print("PASS:", case, flush=True)
        assert not fixture.errors, fixture.errors
        counts = collections.Counter(row["model"] for row in fixture.requests)
        assert counts == collections.Counter({"native-" + case: 2 if case in ("cli-function-result", "cli-namespace", "cli-custom") else 1
                                              for case in CASES}), counts
        function = [row for row in fixture.requests if row["model"] == "native-cli-function-result"]
        outputs = [item for item in function[1]["input"] if item.get("type") == "function_call_output"]
        assert len(outputs) == 1 and outputs[0]["call_id"] == "call_cli"
        assert "unavailable" in outputs[0]["output"].lower(), outputs
        namespaced = [row for row in fixture.requests if row["model"] == "native-cli-namespace"]
        call = next(item for item in namespaced[1]["input"] if item.get("type") == "function_call")
        result_item = next(item for item in namespaced[1]["input"] if item.get("type") == "function_call_output")
        assert call["namespace"] == "multi_agent_v1" and call["call_id"] == result_item["call_id"] == "call_namespace"
        assert result_item.get("namespace") in (None, "multi_agent_v1"), result_item
        assert result_item["output"] and "00000000-0000-0000-0000-000000000001" in result_item["output"], result_item
        custom_rows = [row for row in fixture.requests if row["model"] == "native-cli-custom"]
        custom_call = next(item for item in custom_rows[1]["input"] if item.get("type") == "custom_tool_call")
        custom_output = next(item for item in custom_rows[1]["input"] if item.get("type") == "custom_tool_call_output")
        assert custom_call["input"] == INVALID_PATCH and custom_call["name"] == "apply_patch", custom_call
        assert custom_output["call_id"] == custom_call["call_id"] == "call_custom", custom_output
        assert "patch" in custom_output["output"].lower() and "invalid" in custom_output["output"].lower(), custom_output
        with contextlib.closing(sqlite3.connect(database)) as db:
            assert db.execute("SELECT COUNT(*) FROM usage_invocations").fetchone() == (10,)
            assert db.execute("SELECT COUNT(*) FROM usage_invocations WHERE transport='http' AND state='sealed' AND attempt_count=1").fetchone() == (10,)
            assert db.execute("SELECT COUNT(*) FROM usage_attempt_receipts").fetchone() == (10,)
            assert db.execute("SELECT COUNT(*) FROM usage_attempt_receipts WHERE may_dispatch=1").fetchone() == (10,)
            assert db.execute("SELECT COUNT(*) FROM usage_attempt_receipts WHERE total_tokens=3").fetchone() == (7,)
            assert db.execute("SELECT COUNT(*) FROM usage_attempt_receipts WHERE input_tokens IS NULL AND output_tokens IS NULL AND total_tokens IS NULL").fetchone() == (3,)
            for table in ("billing_reservations", "billing_attempts"):
                assert db.execute(f"SELECT COUNT(*) FROM {table}").fetchone() == (0,)
            for table in ("users", "tokens"):
                assert db.execute(f"SELECT COUNT(*) FROM {table} WHERE used_quota != 0").fetchone() == (0,)
            assert db.execute("SELECT COUNT(*) FROM billing_balances WHERE used_units != 0 OR reserved_units != 0").fetchone() == (0,)
        logs = (work / "gateway.log").read_text(encoding="utf-8", errors="replace")
        assert key not in logs and fixture.key not in logs
    print("PASS: fixed Codex 0.163.0, production Responses HTTP/SSE, text/function-result/namespace/custom and failed/incomplete/truncated terminals; loopback only")


if __name__ == "__main__":
    main()
