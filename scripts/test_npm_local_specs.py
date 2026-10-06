#!/usr/bin/env python3
"""Tests for npm_local_specs.py, and that release.yml packs no local spec.

  python3 -m unittest discover -s scripts -p 'test_*.py'

Python 3.9+, standard library only.
"""
import contextlib
import io
import json
import re
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import npm_local_specs  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
RELEASE = ROOT / ".github" / "workflows" / "release.yml"


def tarball(directory, manifest):
    path = Path(directory) / "package.tgz"
    data = json.dumps(manifest).encode()
    with tarfile.open(path, "w:gz") as tar:
        info = tarfile.TarInfo("package/package.json")
        info.size = len(data)
        tar.addfile(info, io.BytesIO(data))
    return str(path)


class NpmLocalSpecsTest(unittest.TestCase):
    def test_a_local_spec_of_an_installed_section_is_refused(self):
        manifest = {
            "dependencies": {"ajv": "8.20.0", "@superschematic/versiongraph": "file:../../versiongraph/typescript"},
            "peerDependencies": {"hono": "4.13.8", "@acme/x": "link:../x", "@acme/y": "workspace:*"},
            "devDependencies": {"@acme/dev": "file:../dev"},
        }
        self.assertEqual(
            npm_local_specs.local_specs(manifest),
            [
                "dependencies.@superschematic/versiongraph=file:../../versiongraph/typescript",
                "peerDependencies.@acme/x=link:../x",
                "peerDependencies.@acme/y=workspace:*",
            ],
        )
        with tempfile.TemporaryDirectory() as directory, contextlib.redirect_stderr(io.StringIO()) as errors:
            self.assertEqual(npm_local_specs.main([tarball(directory, manifest)]), 1)
            self.assertIn("dependencies.@superschematic/versiongraph=file:../../versiongraph/typescript would not install from the tarball", errors.getvalue())
            self.assertEqual(npm_local_specs.main([tarball(directory, {"dependencies": {"ajv": "8.20.0"}})]), 0)

    def test_release_rewrites_every_local_spec_before_it_packs(self):
        # Each package release.yml packs, by its directory: a local spec of
        # one must be one the pack step sets to the release's version.
        release = RELEASE.read_text()
        step = release[release.index("- name: Pack the npm tarballs") :]
        step = step[: step.index("- name:", 1)]
        directories = re.findall(r"\(cd ([\w./-]+) && ", step)
        directories += [f"packages/{pkg}" for pkg in re.search(r"for pkg in ([\w -]+); do", step).group(1).split()]
        self.assertEqual(len(directories), 10, directories)
        for directory in directories:
            manifest = json.loads((ROOT / directory / "package.json").read_text())
            for spec in npm_local_specs.local_specs(manifest):
                section, rest = spec.split(".", 1)
                name = rest.split("=", 1)[0]
                self.assertIn(f'npm pkg set "{section}.{name}=$VERSION"', release, f"{directory}: {spec}")
        self.assertIn("python3 scripts/npm_local_specs.py release-assets/npm/*.tgz", release)


if __name__ == "__main__":
    unittest.main()
