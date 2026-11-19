#!/usr/bin/env python3
"""Stage explicitly selected, signed Android packages for the local source image."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import stat
import tempfile

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / ".build/local-releases"
LOCAL_NOTE = "本地联调安装包，非正式发行版本。签名可能与已安装版本不同，不能保证覆盖升级。"


def platform_tools():
    spec = importlib.util.spec_from_file_location("prepare_platform", ROOT / "scripts/prepare-platform.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def checksum(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mobile-release", type=Path, required=True)
    parser.add_argument("--tv-release", type=Path, required=True)
    parser.add_argument("--apksigner", default=os.environ.get("APKSIGNER", "apksigner"))
    parser.add_argument("--aapt", default=os.environ.get("AAPT", "aapt"))
    args = parser.parse_args()
    tools = platform_tools()
    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    # Both packages must pass before replacing the previous build context.
    with tempfile.TemporaryDirectory(prefix="local-releases-", dir=OUTPUT.parent) as temporary:
        stage = Path(temporary) / "releases"
        stage.mkdir()
        for platform, source in (("mobile", args.mobile_release), ("tv", args.tv_release)):
            source = source.resolve(strict=True)
            destination = stage / platform
            destination.mkdir()
            manifest_file = source / "release.json"
            info = manifest_file.lstat()
            if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= 65536:
                raise ValueError("Invalid local release manifest: " + platform)
            metadata = json.loads(manifest_file.read_text())
            if not isinstance(metadata, dict) or not isinstance(metadata.get("versionName"), str):
                raise ValueError("Invalid local release manifest: " + platform)
            packages = tools.manifest_packages(metadata, metadata["versionName"], platform)
            names = [name for name, _ in packages] + ["release.json", "SHA256SUMS"]
            for name in names:
                path = source / name
                info = path.lstat()
                maximum = tools.MAX_APK if name.endswith(".apk") else 65536
                if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= maximum:
                    raise ValueError("Invalid local release file: " + str(path))
                shutil.copyfile(path, destination / name)
            records = {name: {"sha256": checksum(destination / name)} for name in names[:-1]}
            tools.verify_checksums(destination / "SHA256SUMS", records)
            for filename, item in packages:
                tools.verify_apk(destination, platform, {"tag_name": "v" + metadata["versionName"]},
                                 args.apksigner, args.aapt, filename, item)
            metadata["notes"] = LOCAL_NOTE + ("\n\n" + metadata["notes"] if metadata["notes"] else "")
            if len(metadata["notes"].encode("utf-8")) > 16000:
                raise ValueError("Local release notes exceed 16000 bytes")
            (destination / "release.json").write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + "\n")
            (destination / "SHA256SUMS").write_text("".join(
                checksum(destination / name) + "  " + name + "\n" for name in names[:-1]))
            print("已校验本地联调包：" + platform + " " + metadata["versionName"])
        if OUTPUT.is_symlink():
            raise ValueError("Local release output must not be a symlink")
        if OUTPUT.exists():
            shutil.rmtree(OUTPUT)
        stage.rename(OUTPUT)
    print("已准备本地镜像输入；执行 make docker-local-up 后在 /downloads/ 下载。")


if __name__ == "__main__":
    main()
