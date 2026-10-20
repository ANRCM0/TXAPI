"""Pinned HTTP SDK tool rounds against an assembled managed-account fixture.

The weather function runs in this client against a fixed result map. No real
provider, ambient credentials, proxies or SDK retries participate.
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


def client_phase(path):
    import httpx
    import openai
    from openai.types.responses import ResponseFunctionToolCall

    assert openai.__version__ == "2.11.0" and httpx.__version__ == "0.28.1"
    settings = json.loads(path.read_text())
    assert settings["base"].startswith("http://127.0.0.1:")
    city = "\u4e0a\u6d77"
    tools = [{"type": "function", "name": "weather", "description": "Read fixture weather",
              "parameters": {"type": "object", "properties": {"city": {"type": "string"}},
                             "required": ["city"], "additionalProperties": False}, "strict": True}]
    user = {"role": "user", "content": "Weather for " + city}
    public_ids = []

    def round_trip(client, inputs, function=False):
        arguments, argument_done, text, created, completed = "", None, "", None, None
        call_done = None
        with client.responses.create(model="synthetic-model", input=inputs, tools=tools,
                                     tool_choice="auto", store=False, stream=True,
                                     extra_headers={"Cookie": "sdk-private-cookie",
                                                    "X-Forwarded-For": "192.0.2.11"}) as stream:
            count = 0
            for event in stream:
                count += 1
                assert count <= 32, "unbounded fixture events"
                assert event.type not in ("error", "response.failed", "response.incomplete"), event.type
                if event.type == "response.created":
                    created = event.response.id
                elif event.type == "response.function_call_arguments.delta":
                    arguments += event.delta
                elif event.type == "response.function_call_arguments.done":
                    assert argument_done is None
                    argument_done = event.arguments
                elif event.type == "response.output_item.done" and event.item.type == "function_call":
                    call_done = event.item
                elif event.type == "response.output_text.delta":
                    text += event.delta
                elif event.type == "response.completed":
                    assert completed is None
                    completed = event.response
        assert completed is not None and completed.status == "completed"
        assert completed.id == created and completed.id.startswith("resp_")
        assert completed.id not in public_ids and completed.model == "synthetic-model"
        public_ids.append(completed.id)
        assert completed.usage is not None
        if function:
            assert len(completed.output) == 1
            call = completed.output[0]
            assert isinstance(call, ResponseFunctionToolCall) and isinstance(call_done, ResponseFunctionToolCall)
            assert call.name == "weather" and call.status == "completed"
            assert call.call_id == call_done.call_id == "call_managed_weather"
            assert call.id == call_done.id and call.arguments == call_done.arguments == argument_done == arguments
            assert json.loads(arguments) == {"city": city}
            assert completed.usage.input_tokens == 5 and completed.usage.output_tokens == 7
            return call
        assert text == city + ": sunny, 22 C." and completed.output_text == text
        assert completed.usage.input_tokens == 11 and completed.usage.output_tokens == 13

    with openai.OpenAI(api_key=settings["key"], base_url=settings["base"] + "/v1", max_retries=0,
                       timeout=5, http_client=openai.DefaultHttpxClient(trust_env=False)) as client:
        call = round_trip(client, [user], function=True)
        weather = {city: {"city": city, "forecast": "sunny", "temperature_c": 22}}
        result = weather[json.loads(call.arguments)["city"]]
        round_trip(client, [user, call.model_dump(exclude_none=True),
                            {"type": "function_call_output", "call_id": call.call_id,
                             "output": json.dumps(result, ensure_ascii=False)}])
    print("PASS: openai 2.11.0 managed HTTP function deltas, client execution and self-contained second round", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--client", type=Path)
    args = parser.parse_args()
    if args.client:
        client_phase(args.client)
        return
    from local import ROOT

    env = {key: value for key, value in os.environ.items()
           if not key.upper().startswith(("OPENAI_", "TXAPI_", "GATEWAY_"))}
    env.update(GOARCH="amd64")
    with tempfile.TemporaryDirectory(prefix="txapi-managed-function-sdk-") as temporary:
        work = Path(temporary)
        executable = work / ("gateway-sdk.test.exe" if os.name == "nt" else "gateway-sdk.test")
        subprocess.run([args.go, "test", "-c", "-o", str(executable), "./cmd/gateway"],
                       cwd=ROOT, env=env, check=True, timeout=180)
        with (work / "fixture.log").open("w+") as log:
            child = subprocess.Popen([str(executable), "-test.run=^TestManagedFunctionPythonSDKProcess$", "-test.timeout=45s"],
                                     cwd=ROOT, env=dict(env, TXAPI_TEST_MANAGED_FUNCTION_SDK_DIRECTORY=str(work)),
                                     stdout=log, stderr=subprocess.STDOUT)
            ready = work / "ready.json"
            try:
                deadline = time.monotonic() + 15
                while not ready.exists():
                    assert child.poll() is None and time.monotonic() < deadline, "managed SDK fixture startup failed"
                    time.sleep(0.02)
                subprocess.run([sys.executable, __file__, "--client", str(ready)], cwd=ROOT, env=env,
                               check=True, timeout=20)
                (work / "done").touch()
                assert child.wait(timeout=5) == 0, "managed wire/usage/financial postconditions failed"
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
