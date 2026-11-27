#!/usr/bin/env python3
"""Tests for engine_rust_shards.py, and that ci.yml runs every shard of it and of go-test-shard.sh and reads one Go build cache.

  python3 -m unittest discover -s scripts -p 'test_*.py'

Python 3.9+, standard library only.
"""
import re
import subprocess
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import engine_rust_shards  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
CI = ROOT / ".github" / "workflows" / "ci.yml"

NAMES = [
    "abi", "behavior_reach", "commerce_holds", "core", "core_links", "http_replay", "job", "postgres",
    "replay", "replicas_runner", "sqlite", "transcript", "workqueue_lease",
]


def metadata(*targets):
    return {"packages": [{"name": "a-crate", "targets": [{"name": name, "kind": kind} for name, kind in targets]}]}


def matrix_shards(job):
    """The shards a job's matrix in ci.yml lists."""
    text = CI.read_text()
    body = text[text.index(f"\n  {job}:\n"):]
    return re.search(r"\n        shard: \[([^\]]*)\]", body).group(1).replace(" ", "").split(",")


class TestTargets(unittest.TestCase):
    def test_distinct_integration_test_names_sorted(self):
        data = metadata(("a-crate", ["lib"]), ("replay", ["test"]), ("abi", ["test"]), ("build-script-build", ["custom-build"]))
        data["packages"].append({"name": "b-crate", "targets": [{"name": "replay", "kind": ["test"]}]})
        self.assertEqual(engine_rust_shards.test_targets(data), ["abi", "replay"])

    def test_a_kind_no_shard_selects_is_refused(self):
        for kind in ("example", "bench"):
            with self.assertRaisesRegex(ValueError, f"target demo is of kind {kind}"):
                engine_rust_shards.test_targets(metadata(("demo", [kind])))


class TestPartition(unittest.TestCase):
    def test_every_name_is_in_exactly_one_shard(self):
        shards = engine_rust_shards.partition(NAMES)
        self.assertEqual(sorted(name for names in shards.values() for name in names), NAMES)
        self.assertEqual(shards["replay"], ["replay"])
        self.assertEqual(shards["transcript"], ["transcript"])
        self.assertEqual(shards["core"], ["core_links", "http_replay"])
        self.assertEqual(shards["behaviors"], ["behavior_reach", "commerce_holds", "replicas_runner", "workqueue_lease"])

    def test_what_no_pattern_matches_is_in_rest(self):
        # The rules crate's `core` is not a `core_*`, and rules-runtime's
        # three need the binaries the rest shard's job builds.
        self.assertEqual(engine_rust_shards.partition(NAMES)["rest"], ["abi", "core", "job", "postgres", "sqlite"])

    def test_a_pattern_that_matches_nothing_is_refused(self):
        with self.assertRaisesRegex(ValueError, "matches no test target .*core: http_replay"):
            engine_rust_shards.partition([name for name in NAMES if name != "http_replay"])

    def test_a_name_in_two_shards_is_refused(self):
        patterns = engine_rust_shards.NAMED["replay"]
        patterns.append("core_links")
        try:
            with self.assertRaisesRegex(ValueError, "core_links is in more than one shard: core, replay"):
                engine_rust_shards.partition(NAMES)
        finally:
            patterns.remove("core_links")


class TestCommands(unittest.TestCase):
    def test_a_named_shard_runs_its_targets_over_the_whole_workspace(self):
        self.assertEqual(
            engine_rust_shards.commands("core", NAMES),
            [["cargo", "test", "--workspace", "--all-features", "--test", "core_links", "--test", "http_replay"]],
        )

    def test_rest_runs_the_unit_tests_and_the_doc_tests_too(self):
        first, second = engine_rust_shards.commands("rest", NAMES)
        self.assertEqual(first[:6], ["cargo", "test", "--workspace", "--all-features", "--lib", "--bins"])
        self.assertEqual(first[6:], ["--test", "abi", "--test", "core", "--test", "job", "--test", "postgres", "--test", "sqlite"])
        self.assertEqual(second, ["cargo", "test", "--workspace", "--all-features", "--doc"])

    def test_the_shards_together_select_every_target_once(self):
        selected = []
        for shard in engine_rust_shards.SHARDS:
            for command in engine_rust_shards.commands(shard, NAMES):
                selected += [command[i + 1] for i, arg in enumerate(command) if arg == "--test"]
        self.assertEqual(sorted(selected), NAMES)

    def test_an_unknown_shard_is_refused(self):
        with self.assertRaisesRegex(ValueError, "unknown shard nope"):
            engine_rust_shards.commands("nope", NAMES)


class TestCIRunsEveryShard(unittest.TestCase):
    """A shard a job's matrix leaves out is tests that never run."""

    def test_engine_rust(self):
        self.assertEqual(sorted(matrix_shards("engine-rust")), sorted(engine_rust_shards.SHARDS))

    def test_go(self):
        out = subprocess.run(
            [str(ROOT / "scripts" / "go-test-shard.sh"), "--shards"], check=True, stdout=subprocess.PIPE, text=True,
        ).stdout
        self.assertEqual(sorted(matrix_shards("go")), sorted(out.split()))


class TestCIGoBuildCache(unittest.TestCase):
    """The jobs that read the Go build cache must name one key, or each reads nothing."""

    def test_one_key(self):
        keys = re.findall(r"\n          key: (go-build-.*)\n", CI.read_text())
        self.assertEqual(len(keys), 3)
        self.assertEqual(len(set(keys)), 1)


if __name__ == "__main__":
    unittest.main()
