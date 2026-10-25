"""Tests for prebuilt_deps.py: the prebuilt dependencies a checkout without
Rust stages (D82).

Each test copies the script into a checkout of its own, with the pins it
keys on and stand-ins for every build product, packs them as the
prebuilt-deps workflow does, writes the manifests, removes what a build
left, and fetches through a file:// URL, as a published release is fetched
over https://. Run with `python3 -m unittest discover -s scripts -p 'test_*.py'`.
"""

import hashlib
import io
import json
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "prebuilt_deps.py"
PLATFORM = "linux-x64"

# The stand-ins for a build's products, by path in the checkout.
BUILT = {
    "third_party/superscalar/LICENSE": "Apache-2.0\n",
    "third_party/superscalar/go/go.mod": "module github.com/parable-work/superscalar/go\n",
    "third_party/superscalar/go/go.sum": "",
    "third_party/superscalar/go/binding.go": "package superscalar\n",
    "third_party/superscalar/go/binding_test.go": "package superscalar\n",
    "third_party/superscalar/go/include/superscalar.h": "/* the C ABI */\n",
    "third_party/superscalar/go/lib/linux_amd64/libsuperscalar_ffi.a": "superscalar archive\n",
    "third_party/superscalar/bindings/typescript/package.json": '{"name": "superscalar"}\n',
    "third_party/superscalar/bindings/typescript/src/index.ts": "export {};\n",
    "third_party/superscalar/bindings/typescript/dist/esm/validation.js": "export {};\n",
    "third_party/superscalar/bindings/typescript/dist/browser/backend.js": "export {};\n",
    "third_party/superscalar/bindings/typescript/dist/browser/wasm-inline.js": 'export default "AGFzbQ==";\n',
    "third_party/superscalar/bindings/typescript/dist/workerd/backend.js": "export {};\n",
    "third_party/superscalar/bindings/typescript/native/index.js": "module.exports = {};\n",
    "third_party/superscalar/bindings/typescript/native/index.d.ts": "export {};\n",
    "third_party/superscalar/bindings/typescript/native/superscalar-napi.linux-x64-gnu.node": "addon\n",
    "runtime/versiongraph/go/lib/linux_amd64/libsuperschematic_versiongraph.a": "version graph archive\n",
    "runtime/versiongraph/rust/target/wasm32-unknown-unknown/release/superschematic_versiongraph.wasm": "\0asm wasm\n",
}

# The sources a key reads.
SOURCES = {
    "tools.env": "RUST_VERSION=1.99.0\nWASM_PACK_VERSION=0.15.0\n",
    "superscalar.pin": "commit=62b300e78ba95b6d25365918575d2351be9d99f9\n",
    "runtime/versiongraph/rust/Cargo.toml": "[package]\nname = \"superschematic-versiongraph\"\n",
    "runtime/versiongraph/rust/Cargo.lock": "version = 4\n",
    "runtime/versiongraph/rust/src/lib.rs": "pub fn compose() {}\n",
}


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


class PrebuiltDepsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="prebuilt-test-"))
        self.addCleanup(shutil.rmtree, self.tmp)
        self.repo = self.tmp / "checkout"
        (self.repo / "scripts").mkdir(parents=True)
        shutil.copy(SCRIPT, self.repo / "scripts" / "prebuilt_deps.py")
        for files in (SOURCES, BUILT):
            for name, content in files.items():
                path = self.repo / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content)
        self.releases = self.tmp / "releases"
        self.env = dict(os.environ)
        for name in list(self.env):
            if name.startswith("SUPERSCHEMATIC_PREBUILT_"):
                del self.env[name]
        self.env.update({
            "SUPERSCHEMATIC_PREBUILT_PLATFORM": PLATFORM,
            "SUPERSCHEMATIC_PREBUILT_URL": self.releases.as_uri(),
        })

    def run_script(self, *args, check=True):
        result = subprocess.run(
            [sys.executable, str(self.repo / "scripts" / "prebuilt_deps.py"), *args],
            cwd=self.repo, env=self.env, capture_output=True, text=True,
        )
        if check and result.returncode != 0:
            self.fail(f"prebuilt_deps.py {' '.join(args)}: {result.returncode}\n{result.stdout}{result.stderr}")
        return result

    def publish(self):
        """Packs every built product for PLATFORM, writes both manifests, and
        removes what the build left, as a checkout without Rust has it."""
        self.run_script("pack", PLATFORM, str(self.releases), "--wasm")
        digests = {}
        for family in ("superscalar", "versiongraph"):
            manifest = Path(self.run_script("manifest", str(self.releases), family, "--platforms", PLATFORM).stdout.strip())
            digests[family] = sha256(manifest)
        for name in BUILT:
            (self.repo / name).unlink()
        return digests

    def trust(self, digests):
        for family, digest in digests.items():
            self.env[f"SUPERSCHEMATIC_PREBUILT_{family.upper()}_MANIFEST_SHA256"] = digest

    def tag(self, family):
        return self.run_script("tag", family).stdout.strip()

    def test_fetch_stages_every_build_product(self):
        self.trust(self.publish())
        self.run_script("fetch", "superscalar")
        self.run_script("fetch", "versiongraph")
        for name, content in BUILT.items():
            if name.endswith("_test.go"):
                self.assertFalse((self.repo / name).exists(), f"{name} is staged, but no test ships")
                continue
            if name.startswith("runtime/versiongraph/rust/target/"):
                name = "runtime/versiongraph/typescript/prebuilt/superschematic_versiongraph.wasm"
            self.assertEqual((self.repo / name).read_text(), content, name)
        stamp = (self.repo / "runtime/versiongraph/go/archive_stamp_linux_amd64.go").read_text()
        archive = self.repo / "runtime/versiongraph/go/lib/linux_amd64/libsuperschematic_versiongraph.a"
        self.assertIn(f'const archiveSHA256 = "{sha256(archive)}"', stamp)
        # A second fetch finds each staged and downloads no tarball.
        again = self.run_script("fetch", "superscalar")
        self.assertIn("staged already", again.stderr)

    def test_fetch_alone_stages_the_go_binding(self):
        self.trust(self.publish())
        self.run_script("fetch", "superscalar", "--only", "superscalar-go")
        self.assertTrue((self.repo / "third_party/superscalar/go/lib/linux_amd64/libsuperscalar_ffi.a").is_file())
        self.assertFalse((self.repo / "third_party/superscalar/bindings/typescript/dist/browser/backend.js").exists())

    def test_a_tarball_whose_digest_differs_is_refused(self):
        self.trust(self.publish())
        tarball = self.releases / self.tag("superscalar") / f"superscalar-go_{PLATFORM}.tar.gz"
        data = bytearray(tarball.read_bytes())
        data[len(data) // 2] ^= 0xFF
        tarball.write_bytes(bytes(data))
        result = self.run_script("fetch", "superscalar", check=False)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("not the manifest's", result.stderr)
        self.assertIn("refusing it", result.stderr)
        self.assertFalse((self.repo / "third_party/superscalar/go/lib/linux_amd64/libsuperscalar_ffi.a").exists())

    def test_a_manifest_whose_digest_differs_is_refused(self):
        digests = self.publish()
        manifest = self.releases / self.tag("versiongraph") / "manifest.json"
        manifest.write_text(manifest.read_text().replace('"artifacts"', '"artifacts" ', 1))
        self.trust(digests)
        result = self.run_script("fetch", "versiongraph", check=False)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("not the pinned", result.stderr)

    def test_the_pin_must_name_this_checkouts_key(self):
        self.publish()
        result = self.run_script("fetch", "superscalar", check=False)
        self.assertIn("prebuilt.pin pins no prebuilt superscalar yet", result.stderr)
        self.run_script("pin", "superscalar", "versiongraph")
        pin = (self.repo / "prebuilt.pin").read_text()
        self.assertIn("superscalar_key=" + self.run_script("key", "superscalar").stdout.strip(), pin)
        self.run_script("fetch", "superscalar")
        # Another superscalar commit is another key, which the pin does not
        # name.
        (self.repo / "superscalar.pin").write_text("commit=0000000000000000000000000000000000000000\n")
        result = self.run_script("fetch", "superscalar", check=False)
        self.assertEqual(result.returncode, 1)
        self.assertIn("but this checkout's inputs give", result.stderr)
        # An edit of the version graph's sources is another key too.
        (self.repo / "runtime/versiongraph/rust/src/lib.rs").write_text("pub fn compose() { todo!() }\n")
        result = self.run_script("fetch", "versiongraph", check=False)
        self.assertIn("but this checkout's inputs give", result.stderr)

    def test_a_tarball_that_leaves_its_directory_is_refused(self):
        digests = self.publish()
        tag = self.tag("superscalar")
        tarball = self.releases / tag / f"superscalar-go_{PLATFORM}.tar.gz"
        with tarfile.open(tarball, "w:gz") as tar:
            info = tarfile.TarInfo("../../escaped.txt")
            info.size = 3
            tar.addfile(info, io.BytesIO(b"bad"))
        # Its manifest lists the new tarball, so only the path refuses it.
        manifest = self.releases / tag / "manifest.json"
        data = json.loads(manifest.read_text())
        for entry in data["artifacts"]:
            if entry["file"] == tarball.name:
                entry["sha256"], entry["size"] = sha256(tarball), tarball.stat().st_size
        manifest.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")
        digests["superscalar"] = sha256(manifest)
        self.trust(digests)
        result = self.run_script("fetch", "superscalar", check=False)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("which is no plain file inside it", result.stderr)
        self.assertFalse((self.tmp / "escaped.txt").exists())

    def test_manifest_needs_every_platform(self):
        self.run_script("pack", PLATFORM, str(self.releases))
        result = self.run_script("manifest", str(self.releases), "superscalar", check=False)
        self.assertEqual(result.returncode, 1)
        self.assertIn("superscalar-go_linux-arm64.tar.gz is missing", result.stderr)


if __name__ == "__main__":
    unittest.main()
