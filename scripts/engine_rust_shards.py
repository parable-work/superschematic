#!/usr/bin/env python3
"""The Rust engine workspace's tests in shards, for CI's engine-rust job.

  python3 scripts/engine_rust_shards.py run <shard>     # run a shard's tests
  python3 scripts/engine_rust_shards.py print <shard>   # print its cargo commands
  python3 scripts/engine_rust_shards.py shards          # print the shards' names

`cargo test --workspace --all-features` in runtime/engine/rust runs the
workspace's test binaries one after another, half an hour of them on a CI
runner. The engine-rust job runs the shards in jobs of their own instead,
and this script says which tests a shard runs, so that the shards together
run what the one command runs (`make engine-rust` still runs the one
command).

A shard is a set of integration test target names (a crate's tests/*.rs),
not a set of crates. Every command keeps --workspace --all-features, so a
shard builds each crate with the features the one command builds it with,
and a name two crates share runs in the shard that has the name. The last
shard, rest, takes every name no other shard's patterns match, and the
unit tests and the doc-tests with them: a new test file lands there, and
none is left out.

The script refuses to guess. A pattern that matches no target, a name two
shards match, and a target cargo test would build or run that no shard
selects (an example, a bench) each fail it.

Python 3.9+, standard library only.
"""
import fnmatch
import json
import shlex
import subprocess
import sys
from pathlib import Path

WORKSPACE = Path(__file__).resolve().parent.parent / "runtime" / "engine" / "rust"

# The shards that name their targets, by fnmatch patterns over the
# integration test target names. The test kit's replay of the scenario
# corpus, one test of seven minutes; the behaviors' core's transcripts; the
# test kit's tests of the core behaviors, with its replay of the HTTP
# corpus; the test kit's tests of the rest. Balanced on the engine-rust
# job's own timings: seven, five, six and a half and five minutes of
# tests, and four for rest.
NAMED = {
    "replay": ["replay"],
    "transcript": ["transcript"],
    "core": ["core_*", "http_replay"],
    "behaviors": ["behavior_*", "commerce_*", "replicas_*", "workqueue_*"],
}

# The shard of everything else.
REST = "rest"

SHARDS = [*NAMED, REST]

# The target kinds the commands below cover: --lib and --bins run the unit
# tests, --test the integration tests, and a build script is built with its
# crate. Anything else cargo test would touch has no shard.
KNOWN_KINDS = {"lib", "rlib", "dylib", "cdylib", "staticlib", "proc-macro", "bin", "test", "custom-build"}

CARGO_TEST = ["cargo", "test", "--workspace", "--all-features"]


def test_targets(metadata):
    """The distinct integration test target names of `cargo metadata --no-deps`, sorted."""
    names = set()
    for package in metadata["packages"]:
        for target in package["targets"]:
            unknown = set(target["kind"]) - KNOWN_KINDS
            if unknown:
                raise ValueError(
                    f"{package['name']}: target {target['name']} is of kind {', '.join(sorted(unknown))}, "
                    "which no shard selects; say in scripts/engine_rust_shards.py which shard runs it"
                )
            if "test" in target["kind"]:
                names.add(target["name"])
    return sorted(names)


def partition(names):
    """Each shard's integration test target names: every name in exactly one shard."""
    shards = {shard: [] for shard in SHARDS}
    unmatched = {(shard, pattern) for shard, patterns in NAMED.items() for pattern in patterns}
    for name in names:
        matches = [
            (shard, pattern)
            for shard, patterns in NAMED.items()
            for pattern in patterns
            if fnmatch.fnmatchcase(name, pattern)
        ]
        owners = sorted({shard for shard, _ in matches})
        if len(owners) > 1:
            raise ValueError(f"test target {name} is in more than one shard: {', '.join(owners)}")
        unmatched -= set(matches)
        shards[owners[0] if owners else REST].append(name)
    if unmatched:
        stale = ", ".join(f"{shard}: {pattern}" for shard, pattern in sorted(unmatched))
        raise ValueError(f"a shard's pattern matches no test target ({stale})")
    return shards


def commands(shard, names):
    """The cargo commands that run a shard, given every integration test target name."""
    if shard not in SHARDS:
        raise ValueError(f"unknown shard {shard}; the shards are {', '.join(SHARDS)}")
    selected = [arg for name in partition(names)[shard] for arg in ("--test", name)]
    if shard != REST:
        return [[*CARGO_TEST, *selected]]
    # The unit tests of every crate with the integration tests no other
    # shard names, then the doc-tests, which cargo selects only alone.
    return [[*CARGO_TEST, "--lib", "--bins", *selected], [*CARGO_TEST, "--doc"]]


def workspace_metadata():
    out = subprocess.run(
        ["cargo", "metadata", "--no-deps", "--format-version", "1"],
        cwd=WORKSPACE, check=True, stdout=subprocess.PIPE,
    ).stdout
    return json.loads(out)


def main(argv):
    if argv == ["shards"]:
        print("\n".join(SHARDS))
        return 0
    if len(argv) != 2 or argv[0] not in ("run", "print"):
        print(__doc__, file=sys.stderr)
        return 2
    try:
        todo = commands(argv[1], test_targets(workspace_metadata()))
    except ValueError as error:
        print(f"engine_rust_shards: {error}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError:
        # cargo said why above. Without the superscalar checkout it cannot
        # read the workspace at all.
        print(
            "engine_rust_shards: cargo metadata failed; the workspace needs the superscalar sources "
            "(scripts/superscalar-dep.sh --checkout)",
            file=sys.stderr,
        )
        return 1
    for command in todo:
        print(("+ " if argv[0] == "run" else "") + shlex.join(command), flush=True)
        if argv[0] == "run":
            status = subprocess.run(command, cwd=WORKSPACE).returncode
            if status != 0:
                return status
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
