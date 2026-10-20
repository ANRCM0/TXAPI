"""Pinned Codex 0.163.0 through the assembled native WebSocket execution chain.

All credentials, content, SQLite state and providers are disposable fixtures.
The Go observer independently requires real WS creates and zero HTTP fallback.
"""

import argparse
import contextlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

from codex_conformance import ANSWER, VERSION, configure, run_cli
from local import ROOT
from sdk_conformance import clean_environment


CASES = (("text", "cli-text"), ("function", "cli-function-result"),
         ("custom", "cli-custom"), ("search", "cli-tool-search"))


def client(cli, home, workspace, env, settings, case, model):
    configure(home, model, settings["base"] + "/v1")
    config = home / "config.toml"
    config.write_text(config.read_text().replace("supports_websockets = false", "supports_websockets = true"))
    result, output, diagnostic = run_cli(cli, ["exec", "--skip-git-repo-check", "--ephemeral", "--json",
        "--color", "never", "-C", str(workspace), "Use the synthetic WS fixture response. Do not execute commands."],
        workspace, dict(env, CODEX_HOME=str(home), TXAPI_TEST_CODEX_KEY=settings["key"]))
    events = [json.loads(line) for line in output.splitlines() if line.strip()]
    assert result == 0 and events[-1]["type"] == "turn.completed", (case, result, output[-3000:], diagnostic[-1000:])
    assert any(event.get("item", {}).get("text") == ANSWER for event in events), (case, output)
    assert settings["key"] not in output + diagnostic and "upstream-main-fixture" not in output + diagnostic
    assert {path.name for path in workspace.iterdir()} <= {"stdout.log", "stderr.log"}


def deployed(cli, root, env, settings):
    """Only client behavior; the deployment observer verifies transport/usage."""
    for case, model in CASES:
        home, workspace = root / (case + "-home"), root / (case + "-workspace")
        home.mkdir()
        workspace.mkdir()
        client(cli, home, workspace, env, settings, case, model)
        print("PASS: deployed pinned Codex WS client", case, flush=True)
    return {"version": VERSION, "cases": [case for case, _ in CASES]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", required=True, type=Path)
    parser.add_argument("--go", default="go")
    parser.add_argument("--deployed", type=Path, help="run only the fixed client against isolated deployment settings")
    args = parser.parse_args()
    cli = args.codex.resolve()
    env = {key: value for key, value in clean_environment().items()
           if not key.upper().startswith(("CODEX_", "OTEL_"))}
    env.update(GOARCH="amd64", RUST_LOG="off", PYTHONIOENCODING="utf-8")
    with tempfile.TemporaryDirectory(prefix="txapi-codex-ws-") as temporary:
        root = Path(temporary)
        version_home = root / "version-home"
        version_home.mkdir()
        result, version, _ = run_cli(cli, ["--version"], root, dict(env, CODEX_HOME=str(version_home)))
        assert result == 0 and version.strip() == VERSION
        if args.deployed:
            report = deployed(cli, root, env, json.loads(args.deployed.read_text()))
            print(json.dumps(report), flush=True)
            return
        executable = root / ("gateway-codex-ws.test.exe" if os.name == "nt" else "gateway-codex-ws.test")
        subprocess.run([args.go, "test", "-c", "-o", str(executable), "./cmd/gateway"],
                       cwd=ROOT, env=env, check=True, timeout=180)
        for case, model in CASES:
            work = root / case
            work.mkdir()
            home = work / "home"
            home.mkdir()
            ready = work / "ready.json"
            with (work / "fixture.log").open("w+", encoding="utf-8") as log:
                child = subprocess.Popen([str(executable), "-test.run=^TestResponsesCodexWSProcess$", "-test.timeout=45s"],
                    cwd=ROOT, env=dict(env, TXAPI_TEST_CODEX_WS_DIRECTORY=str(work), TXAPI_TEST_CODEX_WS_CASE=case),
                    stdout=log, stderr=subprocess.STDOUT)
                try:
                    deadline = time.monotonic() + 15
                    while not ready.exists():
                        assert child.poll() is None and time.monotonic() < deadline, "native CLI fixture startup failed"
                        time.sleep(.02)
                    settings = json.loads(ready.read_text())
                    assert settings["base"].startswith("http://127.0.0.1:")
                    workspace = work / "workspace"
                    workspace.mkdir()
                    client(cli, home, workspace, env, settings, case, model)
                    (work / "done").touch()
                    assert child.wait(timeout=5) == 0, "native creates/reuse/parent/usage/no-fallback postconditions failed"
                    print("PASS: pinned Codex WS", case, "with real prewarm and no HTTP fallback", flush=True)
                except BaseException:
                    log.flush()
                    details = (work / "fixture.log").read_text(encoding="utf-8", errors="replace")
                    if ready.exists():
                        with contextlib.suppress(Exception):
                            details = details.replace(json.loads(ready.read_text())["key"], "[fixture-key]")
                    print(details[-10000:], flush=True)
                    raise
                finally:
                    if child.poll() is None:
                        child.kill()
                        child.wait(timeout=5)
    print("PASS: fixed Codex native WS text/function/custom/client-search, reasoning, actual prewarm, exact creates and disabled usage", flush=True)


if __name__ == "__main__":
    main()
