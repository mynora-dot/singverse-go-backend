#!/usr/bin/env python3
"""Verify bundled download bytes with no external network or deployment volumes."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
def check(image):
    name = "singverse-apk-check-" + uuid.uuid4().hex
    subprocess.run(["docker", "run", "-d", "--name", name, "--network", "none", "--read-only",
                    "--tmpfs", "/data:uid=10001,gid=10001", "--tmpfs", "/secrets:uid=10001,gid=10001",
                    "--tmpfs", "/tmp:mode=1777", image], check=True, stdout=subprocess.DEVNULL)
    try:
        for attempt in range(100):
            ready = subprocess.run(["docker", "exec", name, "wget", "-q", "-O-", "http://127.0.0.1:8080/api/health"], capture_output=True)
            if ready.returncode == 0 and json.loads(ready.stdout) == {"status": "UP"}:
                break
            time.sleep(.2)
        else:
            details = subprocess.run(["docker", "logs", "--tail", "20", name], capture_output=True, text=True)
            raise RuntimeError("Isolated offline container did not become healthy: " + details.stderr[-4000:] + details.stdout[-4000:])
        lock = json.loads((ROOT / ".build/platform/components.lock.json").read_text())
        for platform in ("mobile", "tv"):
            raw = subprocess.check_output(["docker", "exec", name, "wget", "-q", "-O-",
                                           "http://127.0.0.1:8080/api/v1/client-releases/" + platform])
            manifest = json.loads(raw)
            expected = lock["components"][platform]["manifest"]
            if not manifest.get("available") or any(manifest.get(k) != v for k, v in expected.items()):
                raise ValueError("Container release differs from fixed build input")
            payload = subprocess.check_output(["docker", "exec", name, "wget", "-q", "-O-",
                                               "http://127.0.0.1:8080" + manifest["url"]])
            if len(payload) != expected["size"] or hashlib.sha256(payload).hexdigest() != expected["sha256"]:
                raise ValueError("Offline APK download differs from release")

            url = "http://127.0.0.1:8080" + manifest["url"]
            head = subprocess.run(["docker", "exec", name, "wget", "-S", "--spider", url], capture_output=True, text=True)
            if head.returncode or ("Content-Length: " + str(expected["size"])).lower() not in head.stderr.lower():
                raise ValueError("APK HEAD length differs from bundled bytes")
            request = ("GET " + manifest["url"] + " HTTP/1.1\r\nHost: 127.0.0.1:8080\r\nRange: bytes=0-3\r\nConnection: close\r\n\r\n").encode()
            response = subprocess.run(["docker", "exec", "-i", name, "nc", "-w", "10", "127.0.0.1", "8080"], input=request, capture_output=True, check=True).stdout
            headers, prefix = response.split(b"\r\n\r\n", 1)
            if not headers.startswith(b"HTTP/1.1 206 ") or ("Content-Range: bytes 0-3/" + str(expected["size"])).encode().lower() not in headers.lower():
                raise ValueError("APK Range status or bounds mismatch")
            local_apk = ROOT / ".build/platform/releases" / platform / ("singverse-" + platform + ".apk")
            with local_apk.open("rb") as source:
                if prefix != source.read(4):
                    raise ValueError("APK Range response differs from bundled bytes")
        page = subprocess.check_output(["docker", "exec", name, "wget", "-q", "-O-", "http://127.0.0.1:8080/downloads/"])
        if b'<div id="app">' not in page:
            raise ValueError("Anonymous downloads page is missing")
        print("Passed: anonymous page and both APKs GET/HEAD/Range verified without external networking.")
    finally:
        subprocess.run(["docker", "rm", "-f", name], check=True, stdout=subprocess.DEVNULL)
if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    check(parser.parse_args().image)
