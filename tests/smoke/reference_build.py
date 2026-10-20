"""Build the fixed new-api reference in a new directory outside this repository."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tarfile
import urllib.request
import zipfile

from local import ROOT

REFERENCE_SHA = "25372e30b1f94255b1f818aeaaf006f042572580"
BUN_VERSION = "1.4.0"
BUN_ARCHIVES = {
    "Windows": ("bun-windows-x64.zip", "e6f093d39da486b20262ca8cdd5ed6a9e8bc9c2f275b78e6d3a0c5b28cc95901"),
    "Linux": ("bun-linux-x64.zip", "2d03fb5fb83ac8b567aca0a281b2ce1a1a19d488f56c2968d88c3f25e92fe452"),
}


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def build(source, work, go):
    source, work = source.resolve(), work.resolve()
    if work.is_relative_to(ROOT) or work == source or work.is_relative_to(source):
        raise ValueError("reference build directory must be outside the repository and source checkout")
    if subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=source, text=True).strip() != REFERENCE_SHA:
        raise ValueError("reference source is not the registered fixed SHA")
    if subprocess.check_output(["git", "status", "--porcelain"], cwd=source, text=True).strip():
        raise ValueError("reference source has local changes")
    origin = subprocess.check_output(["git", "remote", "get-url", "origin"], cwd=source, text=True).strip()
    if origin not in ("https://github.com/QuantumNous/new-api.git", "https://github.com/QuantumNous/new-api"):
        raise ValueError("reference origin is not the registered repository")
    if platform.system() not in BUN_ARCHIVES or platform.machine().lower() not in ("amd64", "x86_64"):
        raise ValueError("reference build supports Windows/Linux x64 only")
    work.mkdir(parents=True, exist_ok=False)
    log = work / "build.log"
    metadata = {"repository": origin, "sha": REFERENCE_SHA, "source_dirty": False,
                "platform": platform.platform(), "bun_version": BUN_VERSION,
                "install_hooks": "disabled", "web_assets": "full production build",
                "go_flags": ["CGO_ENABLED=0", "GOARCH=amd64", "GOWORK=off", "GOEXPERIMENT=greenteagc", "-ldflags=-s -w"],
                "source_changes": "none; generated web assets only"}
    try:
        metadata["stage"] = "export_source"
        archive = work / "source.tar"
        subprocess.run(["git", "archive", "--format=tar", "--output", str(archive), REFERENCE_SHA],
                       cwd=source, check=True, timeout=45)
        metadata["source_archive_sha256"] = digest(archive)
        exported = work / "source"
        exported.mkdir()
        with tarfile.open(archive) as files:
            files.extractall(exported, filter="data")
        asset, expected_hash = BUN_ARCHIVES[platform.system()]
        metadata["stage"] = "download_bun"
        bun_zip = work / "bun.zip"
        url = f"https://github.com/oven-sh/bun/releases/download/bun-v{BUN_VERSION}/{asset}"
        with urllib.request.urlopen(url, timeout=45) as response, bun_zip.open("wb") as output:
            copied = 0
            while data := response.read(1 << 20):
                copied += len(data)
                if copied > 100 << 20:
                    raise ValueError("Bun archive exceeded finite download limit")
                output.write(data)
        if digest(bun_zip) != expected_hash:
            raise ValueError("Bun archive digest mismatch")
        metadata["bun_archive_sha256"] = expected_hash
        with zipfile.ZipFile(bun_zip) as files:
            executable = "bun.exe" if os.name == "nt" else "bun"
            member = next(item for item in files.infolist() if Path(item.filename).name == executable)
            bun = work / executable
            bun.write_bytes(files.read(member))
            bun.chmod(0o700)
        blocked = ("OPENAI_", "GATEWAY_", "TXAPI_", "SQL_", "LOG_SQL_", "REDIS_", "SESSION_", "CRYPTO_")
        env = {key: value for key, value in os.environ.items() if not key.upper().startswith(blocked)}
        env.update(GOARCH="amd64", CGO_ENABLED="0", GOWORK="off", GOEXPERIMENT="greenteagc",
                   PATH=str(work) + os.pathsep + env.get("PATH", ""), DISABLE_ESLINT_PLUGIN="true",
                   VITE_REACT_APP_VERSION=(exported / "VERSION").read_text().strip())
        with log.open("w", encoding="utf-8") as output:
            def run(command, cwd, timeout):
                subprocess.run(command, cwd=cwd, env=env, stdout=output, stderr=output,
                               check=True, timeout=timeout)
            metadata["go_version"] = subprocess.check_output([go, "version"], env=env, text=True).strip()
            metadata["stage"] = "install_web_dependencies"
            run([str(bun), "install", "--frozen-lockfile", "--ignore-scripts"], exported / "web", 600)
            metadata["stage"] = "build_web_assets"
            run([str(bun), "run", "build"], exported / "web", 1200)
            metadata["stage"] = "download_go_modules"
            run([go, "mod", "download"], exported, 900)
            metadata["stage"] = "build_reference_main"
            pgo_dir = subprocess.check_output([go, "list", "-m", "-f", "{{.Dir}}", "github.com/Calcium-Ion/moejs"],
                                              cwd=exported, env=env, text=True, timeout=60).strip()
            binary = work / ("new-api.exe" if os.name == "nt" else "new-api")
            run([go, "build", "-pgo=" + str(Path(pgo_dir) / "default.pgo"), "-ldflags", "-s -w",
                 "-o", str(binary), "."], exported, 1800)
        metadata["binary"] = str(binary)
        metadata["binary_sha256"] = digest(binary)
        metadata["status"] = "built"
        metadata["stage"] = "complete"
    except BaseException as error:
        metadata.update(status="failed", error=type(error).__name__, log=str(log))
        raise
    finally:
        (work / "build.json").write_text(json.dumps(metadata, indent=2), encoding="utf-8")
    return metadata


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    print(json.dumps(build(args.source, args.work, args.go)))


if __name__ == "__main__":
    main()
