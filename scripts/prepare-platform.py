#!/usr/bin/env python3
"""Resolve stable GitHub assets once, verify, and stage immutable Docker inputs."""
import argparse
import hashlib
import json
import os
import sys
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import tarfile
import tempfile
from urllib.error import HTTPError
from urllib.parse import quote, urlparse
from urllib.request import Request, build_opener, HTTPRedirectHandler

sys.path.insert(0, str(Path(__file__).resolve().parent))
from apk_signature import release_certificate
from release_manifest import manifest_packages

ROOT = Path(__file__).resolve().parents[1]
MAX_APK = 256 << 20
VERSION = re.compile(r"^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
HEX = re.compile(r"^[a-f0-9]{64}$")
REPOSITORY = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")

class SafeRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        check_url(newurl)
        result = super().redirect_request(req, fp, code, msg, headers, newurl)
        if result is not None and urlparse(req.full_url).netloc != urlparse(newurl).netloc:
            result.remove_header("Authorization")
        return result

def check_url(url):
    u = urlparse(url)
    if u.scheme != "https" or u.username or u.password or not (
        u.hostname in ("api.github.com", "github.com") or
        (u.hostname or "").endswith(".githubusercontent.com") or
        (u.hostname or "").endswith(".github.com")
    ):
        raise ValueError("Unexpected GitHub asset destination")

def fetch(url, target=None, maximum=1 << 20, binary=False):
    check_url(url)
    headers = {"User-Agent": "SingVerse-release-builder", "Accept": "application/octet-stream" if binary else "application/vnd.github+json"}
    token = os.environ.get("CROSS_REPO_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if token and urlparse(url).hostname == "api.github.com":
        headers["Authorization"] = "Bearer " + token
    try:
        with build_opener(SafeRedirect()).open(Request(url, headers=headers), timeout=60) as response:
            count = 0
            digest = hashlib.sha256()
            chunks = []
            output = target.open("wb") if target else None
            try:
                while True:
                    chunk = response.read(1 << 20)
                    if not chunk:
                        break
                    count += len(chunk)
                    if count > maximum:
                        raise ValueError("Release asset exceeds size limit")
                    digest.update(chunk)
                    if output:
                        output.write(chunk)
                    else:
                        chunks.append(chunk)
            finally:
                if output:
                    output.close()
    except HTTPError as error:
        raise RuntimeError("GitHub release unavailable (HTTP %d); check formal releases and read permissions" % error.code) from None
    if target:
        return {"size": count, "sha256": digest.hexdigest()}
    return json.loads(b"".join(chunks))

def validate_release(release, expected_tag=None):
    if release.get("draft") or release.get("prerelease") or not VERSION.fullmatch(release.get("tag_name", "")):
        raise ValueError("Only stable, published semantic-version releases are accepted")
    if expected_tag and release["tag_name"] != expected_tag:
        raise ValueError("Pinned release tag changed")
    if type(release.get("id")) is not int or release["id"] <= 0:
        raise ValueError("Release ID missing")

def resolve(name, component, locked=None):
    repo = component["repository"]
    if not REPOSITORY.fullmatch(repo):
        raise ValueError("Invalid repository")
    base = "https://api.github.com/repos/" + repo
    if locked:
        if locked["repository"] != repo or type(locked.get("releaseId")) is not int or locked["releaseId"] <= 0:
            raise ValueError("Locked repository or release ID mismatch")
        release = fetch(base + "/releases/" + str(locked["releaseId"]))
        expected = locked["tag"]
    elif component.get("tag"):
        expected = component["tag"]
        release = fetch(base + "/releases/tags/" + quote(expected, safe=""))
    else:
        expected = None
        release = fetch(base + "/releases/latest")
    validate_release(release, expected)
    if locked and release["id"] != locked["releaseId"]:
        raise ValueError("Locked release ID changed")
    if component.get("tag") and release["tag_name"] != component["tag"]:
        raise ValueError("Locked release tag differs from components.json: " + name)
    return release

def asset(release, filename):
    found = [item for item in release.get("assets", []) if item.get("name") == filename and item.get("state") == "uploaded"]
    if len(found) != 1 or type(found[0].get("id")) is not int or found[0]["id"] <= 0:
        raise ValueError("Missing or ambiguous asset: " + filename)
    return found[0]

def download(repo, release, filename, output, maximum, locked=None):
    item = asset(release, filename)
    if locked and item["id"] != locked["assetId"]:
        raise ValueError("Locked asset ID changed: " + filename)
    result = fetch("https://api.github.com/repos/%s/releases/assets/%s" % (repo, item["id"]),
                   output, maximum, binary=True)
    if result["size"] != item.get("size"):
        raise ValueError("GitHub asset size mismatch: " + filename)
    if locked and (result["size"] != locked["size"] or result["sha256"] != locked["sha256"]):
        raise ValueError("Locked asset content changed: " + filename)
    result["assetId"] = item["id"]
    return result

def verify_checksums(path, files):
    entries = {}
    for line in path.read_text().splitlines():
        match = re.fullmatch(r"([a-f0-9]{64})  \*?([^/\\]+)", line)
        if not match or match[2] in entries:
            raise ValueError("Invalid checksum file")
        entries[match[2]] = match[1]
    for filename, record in files.items():
        if entries.get(filename) != record["sha256"]:
            raise ValueError("Release checksum mismatch: " + filename)

def android_code(version):
    match = VERSION.fullmatch(version)
    if not match:
        raise ValueError("Invalid Android version")
    major, minor, patch = map(int, match.groups())
    code = major * 1000000 + minor * 1000 + patch
    if minor > 999 or patch > 999 or not 1 <= code <= 2100000000:
        raise ValueError("Android version outside supported range")
    return code

def verify_apk(directory, platform, release, apksigner, aapt, filename=None, metadata=None):
    apk = directory / (filename or "singverse-" + platform + ".apk")
    if metadata is None:
        metadata = json.loads((directory / "release.json").read_text())
    name = release["tag_name"].removeprefix("v")
    expected = {"schemaVersion": 1, "platform": platform, "packageName": "com.mynora.singverse." + platform,
                "versionName": name, "versionCode": android_code(name), "minSdk": 24 if platform == "mobile" else 21}
    if not isinstance(metadata, dict) or any(type(metadata.get(k)) is not type(value) or metadata[k] != value for k, value in expected.items()):
        raise ValueError("APK metadata does not match release: " + platform)
    if type(metadata.get("size")) is not int or not 0 < apk.stat().st_size <= MAX_APK or metadata["size"] != apk.stat().st_size:
        raise ValueError("APK size mismatch")
    with apk.open("rb") as source:
        checksum = hashlib.sha256()
        for chunk in iter(lambda: source.read(1 << 20), b""):
            checksum.update(chunk)
        digest = checksum.hexdigest()
    if metadata.get("sha256") != digest or not isinstance(metadata.get("certificateSha256"), str) or not HEX.fullmatch(metadata["certificateSha256"]):
        raise ValueError("APK hash or certificate metadata invalid")
    signature = subprocess.run([apksigner, "verify", "--verbose", "--print-certs", str(apk)], capture_output=True, text=True)
    if signature.returncode:
        raise ValueError("APK signature verification failed: " + platform)
    certificate = release_certificate(signature.stdout)
    if certificate != metadata["certificateSha256"]:
        raise ValueError("APK signature verification failed: " + platform)
    badging = subprocess.run([aapt, "dump", "badging", str(apk)], capture_output=True, text=True)
    match = re.search(r"package: name='([^']+)' versionCode='(\d+)' versionName='([^']+)'", badging.stdout)
    sdk = re.search(r"sdkVersion:'(\d+)'", badging.stdout)
    if badging.returncode or not match or not sdk or (
        match[1], int(match[2]), match[3], int(sdk[1])
    ) != (expected["packageName"], expected["versionCode"], name, expected["minSdk"]):
        raise ValueError("APK manifest differs from metadata: " + platform)
    abi_line = re.search(r"native-code: (.+)", badging.stdout)
    abis = re.findall(r"'([^']+)'", abi_line[1]) if abi_line else ["universal"]
    if not isinstance(metadata.get("abis"), list) or any(not isinstance(item, str) for item in metadata["abis"]) or sorted(metadata["abis"]) != sorted(abis):
        raise ValueError("APK ABI metadata mismatch")
    if not isinstance(metadata.get("notes"), str) or len(metadata["notes"].encode()) > 16000:
        raise ValueError("Invalid release notes")
    return metadata

def extract_frontend(archive_path, destination):
    size = 0
    destination.mkdir(parents=True)
    with tarfile.open(archive_path, "r:gz") as archive:
        for index, member in enumerate(archive):
            path = PurePosixPath(member.name)
            if index > 10000 or path.is_absolute() or ".." in path.parts or "\\" in member.name:
                raise ValueError("Unsafe frontend archive path")
            if not member.isfile() and not member.isdir():
                raise ValueError("Frontend archive must contain regular files only")
            size += member.size
            if size > 512 << 20:
                raise ValueError("Frontend archive too large")
            target = destination.joinpath(*path.parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with archive.extractfile(member) as source, target.open("wb") as output:
                    shutil.copyfileobj(source, output)
    for entry in ("admin/index.html", "party/index.html", "downloads/index.html"):
        if not (destination / entry).is_file() or not (destination / entry).stat().st_size:
            raise ValueError("Incomplete frontend archive: " + entry)

def prepare(config, output, lock=None, apksigner="apksigner", aapt="aapt"):
    if lock is not None and (set(lock) != {"schemaVersion", "components"} or type(lock.get("schemaVersion")) is not int or lock["schemaVersion"] != 1 or set(lock.get("components", {})) != {"frontend", "mobile", "tv"}):
        raise ValueError("Invalid component lock")
    output = output.resolve()
    if not output.is_relative_to((ROOT / ".build").resolve()):
        raise ValueError("Staging output must be inside this repository .build directory")
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="platform-", dir=output.parent) as temporary:
        stage = Path(temporary) / "inputs"
        stage.mkdir()
        result = {"schemaVersion": 1, "components": {}}
        for name in ("frontend", "mobile", "tv"):
            component = config[name]
            old = lock["components"][name] if lock else None
            release = resolve(name, component, old)
            folder = Path(temporary) / name if name == "frontend" else stage / "releases" / name
            folder.mkdir(parents=True)
            records = {}
            packages = []
            if name == "frontend":
                filenames = ["singverse-frontend.tar.gz"]
            else:
                records["release.json"] = download(component["repository"], release, "release.json", folder / "release.json", 65536,
                                                    old["assets"]["release.json"] if old else None)
                metadata = json.loads((folder / "release.json").read_text())
                packages = manifest_packages(metadata, release["tag_name"].removeprefix("v"), name)
                filenames = [filename for filename, _ in packages]
            for filename in filenames + ["SHA256SUMS"]:
                maximum = MAX_APK if filename.endswith(".apk") else (128 << 20 if filename.endswith(".tar.gz") else 65536)
                records[filename] = download(component["repository"], release, filename, folder / filename, maximum,
                                             old["assets"][filename] if old else None)
            verify_checksums(folder / "SHA256SUMS", {k: v for k, v in records.items() if k != "SHA256SUMS"})
            record = {"repository": component["repository"], "releaseId": release["id"], "tag": release["tag_name"], "assets": records}
            if name == "frontend":
                extract_frontend(folder / filenames[0], stage / "frontend")
            else:
                for filename, item in packages:
                    verify_apk(folder, name, release, apksigner, aapt, filename, item)
                record["manifest"] = metadata
            result["components"][name] = record
        (stage / "components.lock.json").write_text(json.dumps(result, indent=2) + "\n")
        if output.exists():
            shutil.rmtree(output)
        stage.rename(output)
    return result

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=ROOT / "components.json")
    parser.add_argument("--output", type=Path, default=ROOT / ".build/platform")
    parser.add_argument("--lock", type=Path)
    parser.add_argument("--apksigner", default=os.environ.get("APKSIGNER", "apksigner"))
    parser.add_argument("--aapt", default=os.environ.get("AAPT", "aapt"))
    args = parser.parse_args()
    prepare(json.loads(args.config.read_text()), args.output,
            json.loads(args.lock.read_text()) if args.lock else None, args.apksigner, args.aapt)
    print("Staged verified frontend, mobile and TV releases; component lock written.")

if __name__ == "__main__":
    main()
