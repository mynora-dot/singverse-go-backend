#!/usr/bin/env python3
"""Advertise a running Docker entry from its macOS LAN host until Ctrl-C."""
import argparse
import ipaddress
import json
import platform
import shutil
import subprocess
import time
import urllib.error
import urllib.request
import uuid


DEFAULT_CONTAINER = "singverse-go-app-singverse-go-backend-1"
MAX_BODY = 8192
PRIVATE_NETWORKS = tuple(ipaddress.ip_network(value) for value in
                         ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"))


def published_entry(ports):
    if not isinstance(ports, dict):
        raise ValueError("The Go container has no published HTTP entry.")
    entries = set()
    for binding in ports.get("8080/tcp") or []:
        try:
            address = ipaddress.IPv4Address(binding["HostIp"])
            port = int(binding["HostPort"])
        except (KeyError, TypeError, ValueError):
            continue
        if any(address in network for network in PRIVATE_NETWORKS) and 1 <= port <= 65535:
            entries.add((str(address), port))
    if len(entries) != 1:
        raise ValueError("Bind the Go HTTP port to one private LAN IP with SINGVERSE_HTTP_BIND, then redeploy.")
    return next(iter(entries))


def container_entry(container):
    result = subprocess.run(
        ["docker", "inspect", "--format", "{{json .NetworkSettings.Ports}}", container],
        check=True, capture_output=True, text=True, timeout=5,
    )
    return published_entry(json.loads(result.stdout))


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        return None


def server_identity(address, port):
    # Ignore shell/system proxies and refuse redirects to a different destination.
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    request = urllib.request.Request(
        f"http://{address}:{port}/api/v1/server-info",
        headers={"Accept": "application/json"},
    )
    with client.open(request, timeout=3) as response:
        body = response.read(MAX_BODY + 1)
    if len(body) > MAX_BODY:
        raise ValueError("The LAN entry returned an oversized server identity.")
    identity = json.loads(body)
    if (not isinstance(identity, dict) or identity.get("service") != "singverse"
            or type(identity.get("apiVersion")) is not int or identity["apiVersion"] != 1):
        raise ValueError("The published HTTP entry is not a SingVerse API v1 server.")
    instance = identity.get("instanceId")
    if not isinstance(instance, str):
        raise ValueError("The LAN entry returned an invalid instance ID.")
    return str(uuid.UUID(instance))


def verified_target(container):
    address, port = container_entry(container)
    return address, port, server_identity(address, port)


def publisher_command(target):
    address, port, instance = target
    host = "singverse-" + uuid.UUID(instance).hex[:12] + ".local."
    # Proxy registration publishes the actual port binding, not every Mac NIC.
    return ["dns-sd", "-P", "SingVerse NAS", "_singverse._tcp", "local.",
            str(port), host, address, "api=1", "scheme=http"]


def stop_publisher(process):
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def advertise(container, target):
    process = None
    current = None
    waiting = False
    try:
        while True:
            if target != current:
                stop_publisher(process)
                process = None
                if target is not None:
                    print(f"Advertising SingVerse at http://{target[0]}:{target[1]} (Ctrl-C to stop)", flush=True)
                    process = subprocess.Popen(publisher_command(target))
                current = target
            if process is not None and process.poll() is not None:
                raise RuntimeError("The host mDNS publisher exited; check the dns-sd output above.")
            time.sleep(5)
            try:
                target = verified_target(container)
                waiting = False
            except (OSError, ValueError, TypeError, subprocess.SubprocessError):
                target = None
                if not waiting:
                    print("Withdrawing discovery until the LAN entry is healthy again.", flush=True)
                    waiting = True
    finally:
        stop_publisher(process)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", default=DEFAULT_CONTAINER)
    parser.add_argument("--check", action="store_true", help="Verify the published LAN entry without advertising.")
    args = parser.parse_args()
    if platform.system() != "Darwin":
        parser.exit(1, "Run this on the macOS Docker host; Linux NAS hosts can publish with Avahi.\n")
    if not shutil.which("docker") or not shutil.which("dns-sd"):
        parser.exit(1, "docker and the macOS dns-sd command are required.\n")
    try:
        target = verified_target(args.container)
        if args.check:
            print(f"Verified SingVerse LAN entry: http://{target[0]}:{target[1]}")
            return
        advertise(args.container, target)
    except KeyboardInterrupt:
        print("\nStopped SingVerse LAN discovery.")
    except (OSError, ValueError, TypeError, subprocess.SubprocessError, RuntimeError) as error:
        parser.exit(1, f"LAN discovery unavailable: {error}\n")


if __name__ == "__main__":
    main()
