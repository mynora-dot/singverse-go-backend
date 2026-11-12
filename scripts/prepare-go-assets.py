#!/usr/bin/env python3
"""Embed a complete explicit local dist, or the pinned frontend release."""
import argparse
import importlib.util
import json
from pathlib import Path
import shutil
import tempfile

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--frontend-dist", type=Path)
args = parser.parse_args()
destination = ROOT / "internal/web/dist"
def install(source):
    for name in ("admin/index.html", "party/index.html", "downloads/index.html"):
        if not (source / name).is_file() or not (source / name).stat().st_size:
            raise SystemExit("Complete frontend dist required (including downloads): " + name)
    if source.resolve() == destination.resolve():
        return
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=destination.parent) as temporary:
        stage = Path(temporary) / "dist"
        shutil.copytree(source, stage)
        if destination.exists():
            shutil.rmtree(destination)
        stage.rename(destination)
if args.frontend_dist:
    install(args.frontend_dist)
else:
    spec = importlib.util.spec_from_file_location("platform_assets", ROOT / "scripts/prepare-platform.py")
    assets = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(assets)
    component = json.loads((ROOT / "components.json").read_text())["frontend"]
    release = assets.resolve("frontend", component)
    with tempfile.TemporaryDirectory() as temporary:
        work = Path(temporary)
        records = {}
        for filename, maximum in (("singverse-frontend.tar.gz", 128 << 20), ("SHA256SUMS", 65536)):
            records[filename] = assets.download(component["repository"], release, filename, work / filename, maximum)
        assets.verify_checksums(work / "SHA256SUMS", {"singverse-frontend.tar.gz": records["singverse-frontend.tar.gz"]})
        assets.extract_frontend(work / "singverse-frontend.tar.gz", work / "dist")
        install(work / "dist")
print("Prepared complete frontend assets for go:embed.")
