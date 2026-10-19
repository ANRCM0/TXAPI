"""Pinned official Python WS SDK against assembled and shipped TXAPI fixtures.

No real provider, credentials, ambient proxies, automatic reconnects or SDK
retries are used.
"""

import argparse
import contextlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time


def client_phase(path, deployed=False):
    import openai
    import websockets

    assert openai.__version__ == "3.8.0" and websockets.__version__ == "15.0.1"
    settings = json.loads(path.read_text())
    assert (settings["base"] == "http://nativews-fixture:8081" if deployed
            else settings["base"].startswith("http://127.0.0.1:"))
    expected_text = "你好🌏" if deployed else "hello"
    tool_name = "weather" if deployed else "lookup"
    public_ids = []
    options = {"proxy": None, "open_timeout": 3, "close_timeout": 1,
               "compression": None, "ping_interval": None, "max_size": 256 << 10}

    def connect(client):
        return client.responses.connect(max_retries=0, websocket_connection_options=options,
                                        extra_headers={"Cookie": "sdk-private-cookie",
                                                       "X-Forwarded-For": "192.0.2.11"})

    def finish(connection, status="completed", tools=False):
        text, arguments, created = "", "", None
        for _ in range(32):
            event = connection.recv()
            assert event.type != "error", event
            if event.type == "response.created":
                created = event.response.id
                assert event.response.model == "alias"
            elif event.type == "response.output_text.delta":
                text += event.delta
            elif event.type == "response.function_call_arguments.delta":
                arguments += event.delta
            elif event.type in ("response.completed", "response.failed", "response.incomplete"):
                response = event.response
                assert event.type == "response." + status and response.status == status
                assert created == response.id and response.model == "alias" and response.id.startswith("resp_")
                if tools:
                    assert json.loads(arguments) == {} and response.output[0].call_id
                    assert response.output[0].name == tool_name
                else:
                    assert text == expected_text
                assert response.id not in public_ids
                public_ids.append(response.id)
                return response
        raise AssertionError("bounded SDK terminal missing")

    with openai.OpenAI(api_key=settings["key"], base_url=settings["base"] + "/v1",
                       websocket_base_url="ws" + settings["base"][4:] + "/v1",
                       max_retries=0, timeout=3,
                       http_client=openai.DefaultHttpxClient(trust_env=False)) as client:
        with connect(client) as connection:
            connection.response.create(model="alias", input="SDK first", store=True)
            first = finish(connection)
            assert client.responses.retrieve(first.id).id == first.id
            connection.response.create(model="alias", input="SDK tool", store=True, previous_response_id=first.id,
                                       tools=[{"type": "function", "name": tool_name, "parameters": {"type": "object"}}])
            second = finish(connection, tools=True)
            connection.response.create(model="alias", store=True, previous_response_id=second.id,
                                       input=[{"type": "function_call_output", "call_id": second.output[0].call_id, "output": "sunny"}])
            third = finish(connection)
        with connect(client) as connection:
            connection.response.create(model="alias", input="SDK explicit reconnect", store=True, previous_response_id=third.id)
            fourth = finish(connection)
            assert client.responses.retrieve(fourth.id).status == "completed"
        for status in ("incomplete", "failed"):
            with connect(client) as connection:
                connection.response.create(model="alias", input=status, store=True)
                finish(connection, status)
        with connect(client) as connection:
            connection.response.create(model="alias", input="block", store=True)
            event = connection.recv()
            assert event.type == "response.created"
            unknown = event.response.id
            assert unknown not in public_ids
            public_ids.append(unknown)
        try:
            client.responses.retrieve(unknown)
            raise AssertionError("canceled SDK response became complete")
        except openai.ConflictError:
            pass
        with connect(client) as connection:
            connection.response.create(model="alias", input="must not replay", store=True, previous_response_id=unknown)
            assert connection.recv().type == "error"
        with connect(client) as connection:
            connection.response.create(model="alias", input="new after cancellation", store=True)
            finish(connection)
    assert len(public_ids) == 8
    print("PASS: openai 3.8.0 typed WS text/tools, same/new connections, HTTP retrieval, failed/incomplete and cancellation", flush=True)
    if deployed:
        print(json.dumps({"cases": 8, "unknown": unknown, "ids": public_ids}), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--client", type=Path)
    parser.add_argument("--deployed", action="store_true", help="only inside the isolated deployment SDK container")
    args = parser.parse_args()
    if args.client:
        client_phase(args.client, args.deployed)
        return
    assert not args.deployed, "deployed mode requires an explicit fixture client file"
    # Only the host-side assembly harness needs the repository. The deployed
    # client also runs from a shallow read-only mount without a source checkout.
    from local import ROOT

    env = {key: value for key, value in os.environ.items()
           if not key.upper().startswith(("OPENAI_", "TXAPI_", "GATEWAY_"))}
    env.update(GOARCH="amd64")
    with tempfile.TemporaryDirectory(prefix="txapi-ws-sdk-") as temporary:
        work = Path(temporary)
        executable = work / ("gateway-sdk.test.exe" if os.name == "nt" else "gateway-sdk.test")
        subprocess.run([args.go, "test", "-c", "-o", str(executable), "./cmd/gateway"],
                       cwd=ROOT, env=env, check=True, timeout=180)
        with (work / "fixture.log").open("w+") as log:
            child = subprocess.Popen([str(executable), "-test.run=^TestResponsesWSPythonSDKProcess$", "-test.timeout=45s"],
                                     cwd=ROOT, env=dict(env, TXAPI_TEST_WS_SDK_DIRECTORY=str(work)), stdout=log, stderr=subprocess.STDOUT)
            try:
                ready, deadline = work / "ready.json", time.monotonic() + 20
                while not ready.exists():
                    assert child.poll() is None and time.monotonic() < deadline, "SDK fixture startup failed"
                    time.sleep(0.02)
                # The independent process timeout bounds SDK recv, including
                # unexpected protocol stalls, without concurrent readers.
                subprocess.run([sys.executable, __file__, "--client", str(ready)], cwd=ROOT, env=env,
                               check=True, timeout=25)
                (work / "done").touch()
                assert child.wait(timeout=5) == 0, "wire/history/usage postconditions failed"
            except BaseException:
                log.flush()
                diagnostic = (work / "fixture.log").read_text(errors="replace")
                if ready.exists():
                    with contextlib.suppress(Exception):
                        diagnostic = diagnostic.replace(json.loads(ready.read_text())["key"], "[fixture-key]")
                print(diagnostic[-8000:], file=sys.stderr)
                raise
            finally:
                if child.poll() is None:
                    child.kill()
                    child.wait(timeout=5)


if __name__ == "__main__":
    main()
