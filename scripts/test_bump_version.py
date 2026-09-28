#!/usr/bin/env python3
"""Tests for bump_version.py.

  python3 -m unittest discover -s scripts -p 'test_*.py'

Each test copies every version site into a temporary directory, writes its
own CHANGELOG.md there and points the script at it, so the checkout is
never written and the tests do not depend on the checkout's changelog.

Python 3.9+, standard library only.
"""
import contextlib
import io
import re
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import bump_version  # noqa: E402

UV_LOCK = Path("runtime", "schema", "python", "uv.lock")
PYPROJECT = Path("runtime", "schema", "python", "pyproject.toml")

# The lockfile entry for the runtime itself. The lockfile carries other
# packages at the same version (superscalar is 0.1.0), which must not move.
UV_LOCK_ENTRY = re.compile(r'(\[\[package\]\]\nname = "superschematic-schema-runtime"\nversion = ")([^"]+)(")')

CHANGELOG = """# Changelog

## [Unreleased]

### Fixed

- A fix. Patch.

[Unreleased]: {url}/commits/main
""".format(url=bump_version.REPO_URL)


class BumpVersionTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        rels = {path.relative_to(bump_version.ROOT) for path, _, _ in bump_version.sites()}
        for rel in rels | {UV_LOCK}:
            (self.root / rel).parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(bump_version.ROOT / rel, self.root / rel)
        (self.root / "CHANGELOG.md").write_text(CHANGELOG, encoding="utf-8")
        for name, value in (("ROOT", self.root), ("CHANGELOG", self.root / "CHANGELOG.md")):
            patcher = mock.patch.object(bump_version, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def read(self, rel: Path) -> str:
        return (self.root / rel).read_text(encoding="utf-8")

    def lock_version(self) -> str:
        found = UV_LOCK_ENTRY.findall(self.read(UV_LOCK))
        self.assertEqual(len(found), 1, "one superschematic-schema-runtime entry in uv.lock")
        return found[0][1]

    def test_the_copy_agrees(self):
        version = bump_version.check()
        self.assertEqual(self.lock_version(), bump_version.pep440(version))

    def test_check_refuses_a_stale_uv_lock(self):
        version = bump_version.current_version()
        text, n = UV_LOCK_ENTRY.subn(r"\g<1>99.0.0\g<3>", self.read(UV_LOCK))
        self.assertEqual(n, 1)
        (self.root / UV_LOCK).write_text(text, encoding="utf-8")
        with self.assertRaises(bump_version.VersionError) as caught:
            bump_version.check()
        self.assertIn(f"{UV_LOCK}: 99.0.0 (want {bump_version.pep440(version)})", str(caught.exception))

    def test_set_writes_the_uv_lock_in_pep440_form(self):
        before = self.read(UV_LOCK).splitlines()
        with contextlib.redirect_stdout(io.StringIO()):
            status = bump_version.main(["set", "1.2.3-beta.4", "--date", "2026-01-02"])
        self.assertEqual(status, 0)
        self.assertEqual(self.lock_version(), "1.2.3b4")
        self.assertIn('\nversion = "1.2.3b4"\n', self.read(PYPROJECT))
        # Only the runtime's own version line moves.
        after = self.read(UV_LOCK).splitlines()
        self.assertEqual(len(before), len(after))
        changed = [a for b, a in zip(before, after) if b != a]
        self.assertEqual(changed, ['version = "1.2.3b4"'])
        self.assertEqual(bump_version.check(expect="1.2.3-beta.4"), "1.2.3-beta.4")


if __name__ == "__main__":
    unittest.main()
