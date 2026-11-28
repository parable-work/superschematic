#!/usr/bin/env python3
"""Tests for rules_rust_shards.py, and of ci.yml: its shards are the scripts', its shared caches have one writer, and its full tier is the release's call.

  python3 -m unittest discover -s scripts -p 'test_*.py'

Python 3.9+, standard library only.
"""
import re
import subprocess
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import rules_rust_shards  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
CI = ROOT / ".github" / "workflows" / "ci.yml"

NAMES = [
    "access", "core", "host", "job", "lease_scenarios", "postgres", "replay", "sqlite", "transcript",
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
        self.assertEqual(rules_rust_shards.test_targets(data), ["abi", "replay"])

    def test_a_kind_no_shard_selects_is_refused(self):
        for kind in ("example", "bench"):
            with self.assertRaisesRegex(ValueError, f"target demo is of kind {kind}"):
                rules_rust_shards.test_targets(metadata(("demo", [kind])))


class TestPartition(unittest.TestCase):
    def test_every_name_is_in_exactly_one_shard(self):
        shards = rules_rust_shards.partition(NAMES)
        self.assertEqual(sorted(name for names in shards.values() for name in names), NAMES)
        self.assertEqual(shards["transcript"], ["transcript"])

    def test_what_no_pattern_matches_is_in_rest(self):
        # rules-runtime's job, postgres and sqlite need the binaries the
        # rest shard's job builds.
        self.assertEqual(
            rules_rust_shards.partition(NAMES)["rest"],
            ["access", "core", "host", "job", "lease_scenarios", "postgres", "replay", "sqlite"],
        )

    def test_a_pattern_that_matches_nothing_is_refused(self):
        with self.assertRaisesRegex(ValueError, "matches no test target .*transcript: transcript"):
            rules_rust_shards.partition([name for name in NAMES if name != "transcript"])

    def test_a_name_in_two_shards_is_refused(self):
        rules_rust_shards.NAMED["scenarios"] = ["*_scenarios", "transcript"]
        rules_rust_shards.SHARDS.insert(-1, "scenarios")
        try:
            with self.assertRaisesRegex(ValueError, "transcript is in more than one shard: scenarios, transcript"):
                rules_rust_shards.partition(NAMES)
        finally:
            del rules_rust_shards.NAMED["scenarios"]
            rules_rust_shards.SHARDS.remove("scenarios")


class TestCommands(unittest.TestCase):
    def test_a_named_shard_runs_its_targets_over_the_whole_workspace(self):
        self.assertEqual(
            rules_rust_shards.commands("transcript", NAMES),
            [["cargo", "test", "--workspace", "--all-features", "--test", "transcript"]],
        )

    def test_rest_runs_the_unit_tests_and_the_doc_tests_too(self):
        first, second = rules_rust_shards.commands("rest", NAMES)
        self.assertEqual(first[:6], ["cargo", "test", "--workspace", "--all-features", "--lib", "--bins"])
        self.assertEqual(first[6:], ["--test", "access", "--test", "core", "--test", "host", "--test", "job", "--test", "lease_scenarios",
                                     "--test", "postgres", "--test", "replay", "--test", "sqlite"])
        self.assertEqual(second, ["cargo", "test", "--workspace", "--all-features", "--doc"])

    def test_the_shards_together_select_every_target_once(self):
        selected = []
        for shard in rules_rust_shards.SHARDS:
            for command in rules_rust_shards.commands(shard, NAMES):
                selected += [command[i + 1] for i, arg in enumerate(command) if arg == "--test"]
        self.assertEqual(sorted(selected), NAMES)

    def test_an_unknown_shard_is_refused(self):
        with self.assertRaisesRegex(ValueError, "unknown shard nope"):
            rules_rust_shards.commands("nope", NAMES)


class TestCIRunsEveryShard(unittest.TestCase):
    """A shard a job's matrix leaves out is tests that never run."""

    def test_rules_rust(self):
        self.assertEqual(sorted(matrix_shards("rules-rust")), sorted(rules_rust_shards.SHARDS))

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


class TestCISuperscalarCache(unittest.TestCase):
    """Every job reads the superscalar checkout under one key; a job that builds part of it must not save it."""

    def test_only_a_job_that_builds_all_of_it_saves_it(self):
        text = CI.read_text()
        jobs = re.split(r"\n(?=  [a-z][a-z0-9-]*:\n)", text[text.index("\njobs:\n"):])
        saving = [job for job in jobs if re.search(r"uses: actions/cache@[^\n]*\n(?:[^\n]*\n){1,6}?[^\n]*key: superscalar-", job)]
        self.assertTrue(saving)
        for job in saving:
            name = job.split(":", 1)[0].strip()
            # The script with no argument, on a line of its own.
            self.assertRegex(job, r"\n +(run: )?scripts/superscalar-dep\.sh\n", f"{name} saves the checkout and does not build all of it")


class TestCITiers(unittest.TestCase):
    """The full tier is the release's call, never an event: a release's event is push, as a merge to main's is."""

    def test_no_step_or_job_runs_on_the_event_not_being_a_pull_request(self):
        # The cache saves do (a merge's run saves them too), as save-if or
        # beside !cancelled(), never as an if of their own.
        self.assertNotRegex(CI.read_text(), r"\n +if: github\.event_name != 'pull_request'")

    def test_the_go_tests_are_short_unless_the_tier_is_full(self):
        self.assertIn("GO_TEST_SHORT: ${{ inputs.tier != 'full' }}", CI.read_text())

    def test_the_input_is_full_unless_said(self):
        self.assertRegex(CI.read_text(), r"\n  workflow_call:\n    inputs:\n      tier:\n(?: {8}[^\n]*\n)*? {8}default: full\n")

    def test_the_release_asks_for_the_full_tier(self):
        release = (ROOT / ".github" / "workflows" / "release.yml").read_text()
        self.assertRegex(release, r"uses: \./\.github/workflows/ci\.yml\n(?: +#[^\n]*\n)* +with:\n +tier: full\n")

    def test_a_merge_to_main_runs_the_workflow(self):
        self.assertRegex(CI.read_text(), r"\n  push:\n    branches: \[main\]\n")


if __name__ == "__main__":
    unittest.main()
