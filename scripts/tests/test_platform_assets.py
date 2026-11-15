import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("platform_assets", Path(__file__).resolve().parents[1] / "prepare-platform.py")
assets = importlib.util.module_from_spec(spec)
spec.loader.exec_module(assets)

class PlatformAssetsTests(unittest.TestCase):
    def release(self, **values):
        return dict({"id": 42, "tag_name": "v0.1.0", "draft": False, "prerelease": False,
                     "assets": [{"name": "file", "id": 1, "state": "uploaded", "size": 3}]}, **values)

    def test_stable_releases_and_pinned_tag(self):
        for values in ({"draft": True}, {"prerelease": True}, {"tag_name": "v0.2.0-rc.1"}, {"tag_name": "main"}):
            with self.subTest(values=values), self.assertRaises(ValueError):
                assets.validate_release(self.release(**values))
        assets.validate_release(self.release(), "v0.1.0")
        with self.assertRaises(ValueError):
            assets.validate_release(self.release(), "v0.2.0")

    def test_latest_resolution_and_retry_use_release_id(self):
        for name in ("frontend", "mobile", "tv"):
            with self.subTest(component=name):
                info = {"repository": "owner/" + name}
                with patch.object(assets, "fetch", return_value=self.release()) as request:
                    self.assertEqual(assets.resolve(name, info)["tag_name"], "v0.1.0")
                    self.assertTrue(request.call_args.args[0].endswith("/releases/latest"))
                    locked = {"repository": info["repository"], "releaseId": 42, "tag": "v0.1.0"}
                    self.assertEqual(assets.resolve(name, info, locked)["id"], 42)
                    self.assertTrue(request.call_args.args[0].endswith("/releases/42"))
                    with self.assertRaises(ValueError):
                        assets.resolve(name, {"repository": "other/" + name}, locked)

    def test_explicit_tag_and_retry_lock_must_agree_for_every_component(self):
        for name in ("frontend", "mobile", "tv"):
            with self.subTest(component=name):
                info = {"repository": "owner/" + name, "tag": "v0.1.0"}
                locked = {"repository": info["repository"], "releaseId": 42, "tag": "v0.1.0"}
                with patch.object(assets, "fetch", return_value=self.release()) as request:
                    assets.resolve(name, info)
                    self.assertTrue(request.call_args.args[0].endswith("/releases/tags/v0.1.0"))
                    assets.resolve(name, info, locked)
                    self.assertTrue(request.call_args.args[0].endswith("/releases/42"))
                    with self.assertRaises(ValueError):
                        assets.resolve(name, dict(info, tag="v0.2.0"), locked)

    def test_missing_duplicate_and_replaced_assets(self):
        for items in ([], [self.release()["assets"][0]] * 2):
            with self.assertRaises(ValueError):
                assets.asset(self.release(assets=items), "file")
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / "file"
            with self.assertRaises(ValueError):
                assets.download("owner/mobile", self.release(), "file", target, 10,
                                {"assetId": 2, "size": 3, "sha256": "a" * 64})
            with patch.object(assets, "fetch", return_value={"size": 3, "sha256": "b" * 64}):
                with self.assertRaises(ValueError):
                    assets.download("owner/mobile", self.release(), "file", target, 10,
                                    {"assetId": 1, "size": 3, "sha256": "a" * 64})

    def test_checksums_and_android_version_bounds(self):
        with tempfile.TemporaryDirectory() as temporary:
            sums = Path(temporary) / "SHA256SUMS"
            sums.write_text("a" * 64 + "  file\n")
            assets.verify_checksums(sums, {"file": {"sha256": "a" * 64}})
            with self.assertRaises(ValueError):
                assets.verify_checksums(sums, {"file": {"sha256": "b" * 64}})
        self.assertEqual(assets.android_code("1.2.3"), 1002003)
        for version in ("0.0.0", "1.1000.0", "1.0.1000", "2101.0.0", "1.0.0-rc.1"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                assets.android_code(version)

    def tar(self, destination, entries):
        with tarfile.open(destination, "w:gz") as archive:
            for name, value, symlink in entries:
                member = tarfile.TarInfo(name)
                if symlink:
                    member.type = tarfile.SYMTYPE
                    member.linkname = value
                    archive.addfile(member)
                else:
                    content = value.encode()
                    member.size = len(content)
                    archive.addfile(member, io.BytesIO(content))

    def test_frontend_requires_all_entries_and_rejects_escape_or_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for index, entries in enumerate([
                [("../private", "x", False)],
                [("/absolute", "x", False)],
                [("link", "/etc/passwd", True)],
                [("admin/index.html", "admin", False)],
            ]):
                archive = root / ("%d.tar.gz" % index)
                self.tar(archive, entries)
                with self.assertRaises(ValueError):
                    assets.extract_frontend(archive, root / ("out%d" % index))
            archive = root / "ok.tar.gz"
            self.tar(archive, [(name + "/index.html", name, False) for name in ("admin", "party", "downloads")])
            assets.extract_frontend(archive, root / "ok")
            self.assertEqual((root / "ok/downloads/index.html").read_text(), "downloads")

    def test_apk_cryptographic_and_manifest_validation(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            payload = b"isolated APK fixture"
            (directory / "singverse-mobile.apk").write_bytes(payload)
            metadata = dict(schemaVersion=1, platform="mobile", packageName="com.mynora.singverse.mobile",
                            versionName="0.1.0", versionCode=1000, minSdk=24, abis=["arm64-v8a"],
                            size=len(payload), sha256=hashlib.sha256(payload).hexdigest(),
                            certificateSha256="a" * 64, notes="")
            (directory / "release.json").write_text(json.dumps(metadata))
            signature = subprocess.CompletedProcess([], 0, "Signer #1 certificate SHA-256 digest: " + "a" * 64, "")
            badging = subprocess.CompletedProcess([], 0, "package: name='com.mynora.singverse.mobile' versionCode='1000' versionName='0.1.0'\nsdkVersion:'24'\nnative-code: 'arm64-v8a'", "")
            with patch.object(assets.subprocess, "run", side_effect=[signature, badging]):
                assets.verify_apk(directory, "mobile", self.release(), "apksigner", "aapt")
            for key, bad_value in (("schemaVersion", True), ("versionCode", 1000.0), ("minSdk", 24.0), ("size", float(len(payload))), ("abis", "arm64-v8a")):
                bad_metadata = dict(metadata, **{key: bad_value})
                (directory / "release.json").write_text(json.dumps(bad_metadata))
                with patch.object(assets.subprocess, "run", side_effect=[signature, badging]), self.subTest(key=key), self.assertRaises(ValueError):
                    assets.verify_apk(directory, "mobile", self.release(), "apksigner", "aapt")
            (directory / "release.json").write_text(json.dumps(metadata))
            debug_signature = subprocess.CompletedProcess([], 0, signature.stdout + "\nSigner #1 certificate DN: CN=Android Debug, O=Android, C=US", "")
            with patch.object(assets.subprocess, "run", return_value=debug_signature), self.assertRaises(ValueError):
                assets.verify_apk(directory, "mobile", self.release(), "apksigner", "aapt")
            for bad in (subprocess.CompletedProcess([], 1, "", ""), subprocess.CompletedProcess([], 0, "Signer #1 certificate SHA-256 digest: " + "b" * 64, "")):
                with patch.object(assets.subprocess, "run", return_value=bad), self.assertRaises(ValueError):
                    assets.verify_apk(directory, "mobile", self.release(), "apksigner", "aapt")
            with patch.object(assets.subprocess, "run", side_effect=[signature, subprocess.CompletedProcess([], 0, badging.stdout.replace("versionCode='1000'", "versionCode='999'"), "")]), self.assertRaises(ValueError):
                assets.verify_apk(directory, "mobile", self.release(), "apksigner", "aapt")

    def test_only_github_https_is_contacted(self):
        for url in ("http://api.github.com/repos/a/b", "https://evil.example/file", "https://github.com.evil.example/file", "https://user:secret@github.com/file"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                assets.check_url(url)
        assets.check_url("https://release-assets.githubusercontent.com/file")


    def test_build_resolves_once_and_retry_reuses_all_asset_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "frontend.tar.gz"
            self.tar(archive, [(name + "/index.html", name, False) for name in ("admin", "party", "downloads")])
            config = {name: {"repository": "owner/" + name} for name in ("frontend", "mobile", "tv")}
            releases, payloads, manifests = {}, {}, {}
            for index, name in enumerate(("frontend", "mobile", "tv"), 1):
                if name == "frontend":
                    files = {"singverse-frontend.tar.gz": archive.read_bytes()}
                else:
                    apk = ("signed fixture " + name).encode()
                    metadata = dict(schemaVersion=1, platform=name, packageName="com.mynora.singverse." + name,
                                    versionName="0.1.0", versionCode=1000, minSdk=24 if name == "mobile" else 21,
                                    abis=["universal"], size=len(apk), sha256=hashlib.sha256(apk).hexdigest(),
                                    certificateSha256="a" * 64, notes="isolated test")
                    manifests[name] = metadata
                    files = {"singverse-" + name + ".apk": apk, "release.json": json.dumps(metadata).encode()}
                files["SHA256SUMS"] = "".join(hashlib.sha256(value).hexdigest() + "  " + key + "\n" for key, value in files.items()).encode()
                release = self.release(id=index, assets=[])
                for offset, (filename, value) in enumerate(files.items(), 1):
                    asset_id = index * 100 + offset
                    release["assets"].append(dict(id=asset_id, name=filename, state="uploaded", size=len(value)))
                    payloads["https://api.github.com/repos/owner/%s/releases/assets/%d" % (name, asset_id)] = value
                releases[name] = release
            requests = []
            def fetch(url, target=None, maximum=None, binary=False):
                requests.append(url)
                name = url.split("/")[5]
                if target is None:
                    return releases[name]
                value = payloads[url]
                self.assertLessEqual(len(value), maximum)
                target.write_bytes(value)
                return {"size": len(value), "sha256": hashlib.sha256(value).hexdigest()}
            with patch.object(assets, "ROOT", root), patch.object(assets, "fetch", side_effect=fetch), patch.object(assets, "verify_apk", side_effect=lambda directory, name, *args: manifests[name]):
                first = assets.prepare(config, root / ".build/first")
                self.assertEqual(sum(url.endswith("/releases/latest") for url in requests), 3)
                self.assertFalse(any("/releases/tags/" in url for url in requests))
                requests.clear()
                retry = assets.prepare(config, root / ".build/retry", first)
                self.assertEqual(retry, first)
                self.assertFalse(any(url.endswith("/releases/latest") for url in requests))
                for path in (root / ".build/first").rglob("*"):
                    if path.is_file():
                        self.assertEqual(path.read_bytes(), (root / ".build/retry" / path.relative_to(root / ".build/first")).read_bytes())
                requests.clear()
                # Replaced bytes of the same length fail before replacing staged inputs.
                key = next(url for url in payloads if "/mobile/" in url)
                payloads[key] = b"x" * len(payloads[key])
                with self.assertRaisesRegex(ValueError, "Locked asset content changed"):
                    assets.prepare(config, root / ".build/retry", first)
                self.assertEqual(json.loads((root / ".build/retry/components.lock.json").read_text()), first)

if __name__ == "__main__":
    unittest.main()
