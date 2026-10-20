"""Pinned official Python SDK against an isolated production gateway; loopback only.

Install requirements-sdk.txt in a disposable venv, then run this file. No real
Provider, credentials, personal configuration or SDK retry is involved.
"""

import argparse
import base64
import collections
import contextlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.metadata
import json
import os
from pathlib import Path
import select
import socket
import struct
import subprocess
import tempfile
import threading
import time

from local import ROOT, ports, ready

CASES = ["chat-json", "responses-json", "chat-text-open-terminal", "chat-tools-open-terminal",
         "responses-completed-open-terminal", "responses-failed-open-terminal", "responses-incomplete-open-terminal"]
CASES += [operation + suffix for operation in ("chat", "responses")
          for suffix in ("-truncated", "-cancel", "-while-busy", "-after-close")]
EMBEDDING_CASES = ("embeddings-float", "embeddings-base64", "embeddings-default")
CASES += list(EMBEDDING_CASES)
EMBEDDING_VECTORS = [[0.25, -0.5, 1.0], [-1.0, 0.0, 2.0]]


def embedding_base64(vector):
    return base64.b64encode(struct.pack("<3f", *vector)).decode("ascii")


def clean_environment():
    blocked = ("OPENAI_", "GATEWAY_", "TXAPI_")
    proxies = {"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}
    return {key: value for key, value in os.environ.items()
            if not key.upper().startswith(blocked) and key.upper() not in proxies}


def until(predicate, message):
    deadline = time.monotonic() + 8
    while not predicate():
        assert time.monotonic() < deadline, message
        time.sleep(0.02)


def frame(value, event=None):
    # A JSON split at a comma gives legal multiline SSE data; UTF-8 bytes are
    # separately split again by the fixture's socket writer below.
    payload = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    first, separator, rest = payload.partition(",")
    prefix = f"event: {event}\n" if event else ""
    return (prefix + "data: " + first + separator + "\n" +
            ("data: " + rest + "\n" if separator else "") + "\n").encode()


def response_document(status="completed"):
    value = {"id": "resp_fixture", "object": "response", "created_at": 1,
             "model": "m", "status": status, "output": [{"id": "msg_fixture", "type": "message",
             "role": "assistant", "status": "completed", "content": [{"type": "output_text",
             "text": "你好🌏", "annotations": []}]}], "error": None,
             "incomplete_details": None, "usage": {"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}}
    if status == "failed":
        value["error"] = {"code": "server_error", "message": "synthetic failure"}
    if status == "incomplete":
        value["incomplete_details"] = {"reason": "max_output_tokens"}
    return value


class Fixture:
    def __init__(self):
        self.lock = threading.Lock()
        self.hits = collections.Counter()
        self.closed = collections.Counter()
        self.errors = []
        self.release = threading.Event()
        self.key = "sk-sdk-loopback-upstream-only"
        owner = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_args):
                pass

            def do_POST(self):
                case = "unknown"
                try:
                    self.connection.settimeout(5)
                    assert self.headers.get("Authorization") == "Bearer " + owner.key
                    assert self.path in ("/v1/chat/completions", "/v1/responses", "/v1/embeddings")
                    length = int(self.headers["Content-Length"])
                    assert 0 < length < 16384
                    body = json.loads(self.rfile.read(length))
                    inputs = body.get("input") or body.get("messages", [{}])[0].get("content")
                    case = inputs[0] if isinstance(inputs, list) else inputs
                    with owner.lock:
                        owner.hits[case] += 1
                    if self.path == "/v1/embeddings":
                        assert case in EMBEDDING_CASES and body["model"] == "sdk-embedding-actual"
                        assert body["dimensions"] == 3 and not body.get("stream")
                        floating = case == "embeddings-float"
                        assert body["encoding_format"] == ("float" if floating else "base64")
                        assert inputs == ([case, "second input"] if floating else case)
                        vectors = EMBEDDING_VECTORS if floating else EMBEDDING_VECTORS[:1]
                        value = {"object": "list", "model": "sdk-embedding-actual",
                                 "data": [{"object": "embedding", "index": index,
                                           "embedding": vector if floating else embedding_base64(vector),
                                           "extension": {"preserved": True}}
                                          for index, vector in enumerate(vectors)],
                                 "usage": {"prompt_tokens": 7, "total_tokens": 7},
                                 "extension": {"preserved": True}}
                        raw = json.dumps(value, separators=(",", ":")).encode()
                        self.send_response(200)
                        self.send_header("Content-Type", "application/json")
                        self.send_header("Content-Length", str(len(raw)))
                        self.send_header("Connection", "close")
                        self.end_headers()
                        self.close_connection = True
                        self.wfile.write(raw)
                        return
                    chat = self.path.endswith("chat/completions")
                    streaming = body.get("stream", False)
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream" if streaming else "application/json")
                    self.send_header("Connection", "close")
                    self.end_headers()
                    self.close_connection = True
                    if not streaming:
                        value = response_document()
                        if chat:
                            value = {"id": "chat_fixture", "object": "chat.completion", "created": 1, "model": "m",
                                     "choices": [{"index": 0, "message": {"role": "assistant", "content": "你好🌏"},
                                                  "finish_reason": "stop"}], "usage": {"prompt_tokens": 2,
                                                  "completion_tokens": 1, "total_tokens": 3}}
                        self.wfile.write(json.dumps(value, ensure_ascii=False).encode())
                        return
                    if chat:
                        def chunk(delta, finish=None):
                            return frame({"id": "chat_fixture", "object": "chat.completion.chunk", "created": 1,
                                          "model": "m", "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]})
                        data = chunk({"role": "assistant", "content": "你好🌏"})
                        if "tools" in case:
                            data += chunk({"tool_calls": [{"index": 0, "id": "call_fixture", "type": "function",
                                "function": {"name": "weather", "arguments": '{"city":"'}}]})
                            data += chunk({"tool_calls": [{"index": 0, "function": {"arguments": '上海"}'}}]})
                        if "truncated" not in case and "cancel" not in case:
                            data += chunk({}, "tool_calls" if "tools" in case else "stop") + b"data: [DONE]\n\n"
                    else:
                        data = frame({"type": "response.output_text.delta", "sequence_number": 0,
                                      "item_id": "msg_fixture", "output_index": 0, "content_index": 0,
                                      "delta": "你好🌏"}, "response.output_text.delta")
                        if "truncated" not in case and "cancel" not in case:
                            status = "failed" if "failed" in case else "incomplete" if "incomplete" in case else "completed"
                            kind = "response." + status
                            data += frame({"type": kind, "sequence_number": 1, "response": response_document(status)}, kind)
                    for byte in data:
                        self.wfile.write(bytes([byte]))
                        self.wfile.flush()
                    if "cancel" in case or "open-terminal" in case:
                        # An open provider response must be actively closed by the
                        # gateway on downstream close or protocol completion.
                        while not owner.release.is_set():
                            readable, _, _ = select.select([self.connection], [], [], 0.1)
                            if readable and self.connection.recv(1) == b"":
                                break
                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                    pass
                except BaseException as error:
                    with owner.lock:
                        owner.errors.append(type(error).__name__)
                finally:
                    with owner.lock:
                        owner.closed[case] += 1

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.release.set()
        self.server.shutdown()
        self.server.server_close()


@contextlib.contextmanager
def gateway_process(binary, config, log, env):
    # Never dump the process log on error: seed/bootstrap diagnostics can contain
    # synthetic credentials. The temporary directory is removed on every exit.
    with log.open("w", encoding="utf-8") as output:
        process = subprocess.Popen([str(binary), "-config", str(config)], cwd=ROOT,
                                   env=env, stdout=output, stderr=output)
        try:
            yield process
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)


def exercise(client, fixture):
    import openai
    from openai.types.chat import ChatCompletion, ChatCompletionChunk
    from openai.types.responses import Response, ResponseTextDeltaEvent
    from openai.types.create_embedding_response import CreateEmbeddingResponse
    from openai.types.embedding import Embedding

    def chat(case, stream=False, **kwargs):
        return client.chat.completions.create(model=case, messages=[{"role": "user", "content": case}],
                                              stream=stream, **kwargs)

    def responses(case, stream=False):
        return client.responses.create(model=case, input=case, store=False, stream=stream)

    result = chat("chat-json")
    assert isinstance(result, ChatCompletion) and result.choices[0].message.content == "你好🌏"
    result = responses("responses-json")
    assert isinstance(result, Response) and result.status == "completed" and result.output_text == "你好🌏"
    for case in EMBEDDING_CASES:
        kwargs = {"dimensions": 3}
        if case != "embeddings-default":
            kwargs["encoding_format"] = "float" if case == "embeddings-float" else "base64"
        inputs = [case, "second input"] if case == "embeddings-float" else case
        result = client.embeddings.create(model=case, input=inputs, **kwargs)
        assert isinstance(result, CreateEmbeddingResponse) and result.object == "list"
        assert result.model == "sdk-embedding-actual"
        assert result.usage.prompt_tokens == 7 and result.usage.total_tokens == 7
        assert not hasattr(result.usage, "completion_tokens")
        assert result.extension == {"preserved": True}
        expected = EMBEDDING_VECTORS if case == "embeddings-float" else EMBEDDING_VECTORS[:1]
        assert len(result.data) == len(expected)
        for index, (item, vector) in enumerate(zip(result.data, expected)):
            assert isinstance(item, Embedding) and item.object == "embedding" and item.index == index
            assert item.extension == {"preserved": True}
            assert item.embedding == (embedding_base64(vector) if case == "embeddings-base64" else vector)
        until(lambda: fixture.closed[case] == 1, "embeddings did not release upstream")
    for supplied in ({"extra_body": {"stream": True}}, {"invalid_key": True}):
        caller = client.with_options(api_key="sk-sdk-invalid-call-key") if supplied.get("invalid_key") else client
        try:
            caller.embeddings.create(model="embeddings-float", input="embeddings-float",
                                     **({"extra_body": supplied["extra_body"]} if "extra_body" in supplied else {}))
        except openai.APIStatusError as error:
            assert error.status_code == (401 if supplied.get("invalid_key") else 400)
        else:
            raise AssertionError("invalid embeddings request reached provider")
    assert all(fixture.hits[case] == 1 for case in EMBEDDING_CASES), "rejected embeddings call was sent"
    for case in ("chat-text-open-terminal", "chat-tools-open-terminal"):
        kwargs = {}
        if "tools" in case:
            kwargs["tools"] = [{"type": "function", "function": {"name": "weather", "parameters": {"type": "object"}}}]
        with chat(case, True, **kwargs) as stream:
            chunks = list(stream)
        assert chunks and all(isinstance(chunk, ChatCompletionChunk) for chunk in chunks)
        assert "".join(c.choices[0].delta.content or "" for c in chunks) == "你好🌏"
        assert chunks[-1].choices[0].finish_reason == ("tool_calls" if "tools" in case else "stop")
        if "tools" in case:
            calls = [call for c in chunks for call in (c.choices[0].delta.tool_calls or [])]
            assert calls[0].id == "call_fixture" and calls[0].function.name == "weather"
            assert json.loads("".join(call.function.arguments or "" for call in calls)) == {"city": "上海"}
        until(lambda: fixture.closed[case] == 1, "terminal did not release upstream")
    for status in ("completed", "failed", "incomplete"):
        case = "responses-" + status + "-open-terminal"
        with responses(case, True) as stream:
            events = list(stream)
        assert isinstance(events[0], ResponseTextDeltaEvent) and events[0].delta == "你好🌏"
        assert [e.type for e in events] == ["response.output_text.delta", "response." + status]
        assert events[-1].response.status == status
        assert isinstance(events[-1].response, Response)
        if status == "failed":
            assert events[-1].response.error is not None
        if status == "incomplete":
            assert events[-1].response.incomplete_details.reason == "max_output_tokens"
        until(lambda: fixture.closed[case] == 1, "Responses terminal did not release upstream")
    # This SDK version accepts a clean SSE EOF without independently enforcing a
    # terminal. Assert the visible partial result, never call that completion.
    for operation, call in (("chat", chat), ("responses", responses)):
        case = operation + "-truncated"
        with call(case, True) as stream:
            partial = list(stream)
        assert len(partial) == 1
        if operation == "chat":
            assert partial[0].choices[0].finish_reason is None and partial[0].choices[0].delta.content == "你好🌏"
        else:
            assert partial[0].type == "response.output_text.delta" and partial[0].delta == "你好🌏"
        case = operation + "-cancel"
        with call(case, True) as stream:
            first = next(iter(stream))
            assert first is not None
            # A second request on the same Key proves its only slot is occupied.
            try:
                chat(operation + "-while-busy")
            except openai.APIStatusError as error:
                assert error.status_code == 429
            else:
                raise AssertionError("single Key concurrency slot was not held")
        until(lambda: fixture.closed[case] == 1, "SDK close did not release upstream")
        # Upstream close/join precedes slot release by a few instructions.
        # Probe admission with bounded 429-only retries; only one reaches upstream.
        deadline = time.monotonic() + 8
        while True:
            try:
                assert chat(operation + "-after-close").choices[0].finish_reason == "stop"
                break
            except openai.APIStatusError as error:
                assert error.status_code == 429 and time.monotonic() < deadline
                time.sleep(0.02)
    assert not fixture.errors, "loopback fixture rejected a request"
    assert set(fixture.hits) == {case for case in CASES if "while-busy" not in case}, "missing upstream scenario"
    assert all(count == 1 for count in fixture.hits.values()), "unexpected upstream retry"
    assert not any("while-busy" in case for case in fixture.hits), "rejected call reached provider"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--gateway", type=Path, help="Use an existing production binary")
    args = parser.parse_args()
    assert importlib.metadata.version("openai") == "2.11.0"
    assert importlib.metadata.version("httpx") == "0.28.1"
    env = clean_environment()
    # Imported SDK configuration must also be isolated inside this process.
    for key in list(os.environ):
        if key not in env:
            del os.environ[key]
    import httpx
    from openai import OpenAI
    with tempfile.TemporaryDirectory(prefix="txapi-sdk-") as temporary, contextlib.ExitStack() as stack:
        work = Path(temporary)
        binary = args.gateway.resolve() if args.gateway else work / ("gateway.exe" if os.name == "nt" else "gateway")
        if not args.gateway:
            subprocess.run([args.go, "build", "-o", str(binary), "./cmd/gateway"], cwd=ROOT,
                           env=env, check=True, timeout=240)
        fixture = Fixture()
        stack.callback(fixture.close)
        port = ports()[0]
        credential_keys, content_keys = work / "credentials.json", work / "content.json"
        for path in (credential_keys, content_keys):
            path.write_text(json.dumps({"active_key": "test", "keys": {"test": base64.b64encode(os.urandom(32)).decode()},
                                        "digest_key": base64.b64encode(os.urandom(32)).decode()}))
            path.chmod(0o600)
        key = "sk-sdk-loopback-client-only"
        config = {"server": {"addr": f"127.0.0.1:{port}", "timeout_seconds": 10, "retry_times": 2,
                    "concurrency": {"instance": 3, "user": 2, "key": 1, "channel": 2}},
                  "admin_key": "sdk-loopback-admin-only", "database": {"path": str(work / "gateway.db"),
                  "credential_keys_file": str(credential_keys)}, "billing": {"mode": "disabled"},
                  # Separate real credential rows keep error-case health cooldown
                  # from making the next independent scenario unavailable.
                  "channels": [{"name": case, "type": "openai", "base_url": f"http://127.0.0.1:{fixture.server.server_port}",
                                "key": fixture.key, "models": [case]} for case in CASES],
                  "tokens": [{"name": "sdk-client", "key": key, "models": CASES}],
                  "responses": {"enabled": True, "keys_file": str(content_keys)}}
        for channel in config["channels"]:
            if channel["name"] in EMBEDDING_CASES:
                channel["model_mapping"] = {channel["name"]: "sdk-embedding-actual"}
        config_file = work / "config.json"
        config_file.write_text(json.dumps(config))
        for flags, command_env in [(["-init-admin"], dict(env, TXAPI_ADMIN_USERNAME="sdk-admin", TXAPI_ADMIN_PASSWORD="sdk-password-only")),
                                   (["-seed-only"], env), (["-claim-token", "1", "-owner-user", "1"], env)]:
            subprocess.run([str(binary), "-config", str(config_file), *flags], cwd=ROOT, env=command_env,
                           check=True, timeout=30, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with gateway_process(binary, config_file, work / "gateway.log", env) as process:
            ready(process, port)
            with OpenAI(api_key=key, base_url=f"http://127.0.0.1:{port}/v1", max_retries=0,
                        timeout=8, http_client=httpx.Client(trust_env=False)) as client:
                exercise(client, fixture)
        process_log = (work / "gateway.log").read_text(encoding="utf-8", errors="replace")
        assert key not in process_log and fixture.key not in process_log, "synthetic credential appeared in service log"
    print("PASS: pinned Python SDK typed Chat/Responses JSON/SSE, embeddings float/base64/default decoding and model mapping, tools/UTF-8, terminals, truncation, close and admission release; loopback only")


if __name__ == "__main__":
    main()
