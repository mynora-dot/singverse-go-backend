#!/usr/bin/env python3
"""Deploy the local Go/frontend image with the explicitly selected native Mac node."""
import json
from pathlib import Path
import platform
import subprocess

ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ["docker", "compose", "--env-file", str(ROOT / ".env.local"),
           "--project-directory", str(ROOT),
           "-f", str(ROOT / "infra/docker/compose/compose.yaml"),
           "-f", str(ROOT / "infra/docker/compose/compose.local.yaml"),
           "-f", str(ROOT / "infra/docker/compose/compose.macos.yaml")]


def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def main():
    if platform.system() != "Darwin":
        raise SystemExit("Native Mac deployment requires macOS")
    config = json.loads(run(COMPOSE + ["--profile", "compute", "config", "--format", "json"],
                            capture_output=True, text=True).stdout)
    node = Path(config["services"]["singverse-compute"]["build"]["context"])
    python = node / ".venv/bin/python"
    launcher = node / "scripts/macos-compute.py"
    for path in (python, launcher, node / ".tools/jellyfin-ffmpeg/ffmpeg",
                 node / ".tools/jellyfin-ffmpeg/ffprobe"):
        if not path.is_file():
            raise SystemExit("Prepare the selected compute checkout with make compute-setup transcode-tools: " + str(node))
    auth = ROOT / "data/docker/compute-auth"
    shared_token = auth / "token"
    native_token = node / "data/macos-compute/token"
    if shared_token.exists() and native_token.exists() and shared_token.read_bytes() != native_token.read_bytes():
        raise SystemExit("Existing native and Docker credentials differ; preserve both and reconcile explicitly")

    (ROOT / ".build/local-releases").mkdir(parents=True, exist_ok=True)
    run(COMPOSE + ["build", "singverse-go-backend"])
    run(COMPOSE + ["run", "--rm", "--no-deps", "--pull", "never", "singverse-local-init"])
    run(COMPOSE + ["--profile", "compute", "stop", "singverse-compute"])
    command = [str(python), str(launcher), "restart"]
    if shared_token.exists():
        command += ["--token-file", str(shared_token)]
    run(command)
    if not shared_token.exists():
        # Transfer the node-generated credential privately on stdin; never print it.
        run(["docker", "run", "--rm", "-i", "--user", "0:0", "--entrypoint", "/bin/sh",
             "--mount", "type=bind,source=" + str(auth) + ",target=/auth", "singverse:local",
             "-ec", "umask 077; cat > /auth/token; chown 10001:10001 /auth/token"],
            input=native_token.read_bytes())
    run(COMPOSE + ["up", "-d", "--no-deps", "--force-recreate", "--no-build", "--pull", "never",
                   "--wait", "--wait-timeout", "180", "singverse-go-backend"])
    print("Local frontend/Go redeployed; native Mac compute is managed by launchd.")


if __name__ == "__main__":
    main()
