"""The engine's rules that need no database: its named errors and their
stable codes, what it refuses before it opens a transaction, and the exact
JSON its rows travel as."""

from typing import Any, List, Sequence

import pytest
from support import DESCRIPTOR

import superschematic_versiongraph as vg
from superschematic_versiongraph import errors
from superschematic_versiongraph.canonical import CanonicalError
from superschematic_versiongraph.engine import CommitOptions, Engine, KindEdits, SweepOptions, order_keys
from superschematic_versiongraph.exactjson import JsonNumber, dumps, json_equal, loads
from superschematic_versiongraph.storage import Commit, Patch, Ref

# The codes runtime/versiongraph/README.md ("Engine and storage") lists, which
# every language's engine shares.
CODES = {
    "NotFoundError": "not_found",
    "VersionConflictError": "version_conflict",
    "NameTakenError": "name_taken",
    "NoActorError": "no_actor",
    "RefSealedError": "ref_sealed",
    "NothingToCommitError": "nothing_to_commit",
    "WalkCeilingError": "walk_ceiling",
    "SchemaEpochError": "schema_epoch",
    "EntityNotFoundError": "entity_not_found",
    "HistoryMissingError": "history_missing",
    "InvalidTreeError": "invalid_tree",
    "RootMismatchError": "root_mismatch",
    "MergeIntoItselfError": "merge_into_itself",
    "PrimaryMergeOnlyError": "primary_merge_only",
    "NotTaggedError": "not_tagged",
    "NoParentError": "no_parent",
}


def test_every_named_error_has_its_code() -> None:
    named = {name for name in errors.__all__ if name.endswith("Error") and name != "EngineError"}
    assert named == set(CODES)
    for name, code in CODES.items():
        cls = getattr(errors, name)
        error = cls([]) if name == "InvalidTreeError" else cls()
        assert errors.error_code(error) == code, name
        assert str(error) != "", name


def test_a_version_conflict_is_a_not_found() -> None:
    assert isinstance(errors.VersionConflictError(), errors.NotFoundError)
    assert errors.error_code(errors.VersionConflictError()) == "version_conflict"


def test_the_cores_refusal_keeps_its_code() -> None:
    with pytest.raises(vg.VersionGraphError) as refused:
        Engine('{"version": 1}', Unreachable())
    assert errors.error_code(refused.value) == "invalid_descriptor"
    assert errors.error_code(ValueError("x")) == ""


class Unreachable:
    def transact(self, fn: Any) -> Any:
        raise AssertionError("the engine opened a transaction")


def test_bad_arguments_are_refused_before_a_transaction() -> None:
    engine = Engine(DESCRIPTOR, Unreachable())
    with pytest.raises(errors.NoActorError):
        engine.create_primary("", "Bread", "main")
    with pytest.raises(errors.NoActorError):
        engine.sweep(SweepOptions(""))
    with pytest.raises(CanonicalError):
        engine.commit("Cook", "not a uuid", 1, CommitOptions())
    with pytest.raises(ValueError, match="unknown kind"):
        engine.save("Cook", "Mix", 1, {"stew": KindEdits()})
    with pytest.raises(errors.NoActorError):
        engine.arrange("", "Mix", 1, "part", None, [])
    with pytest.raises(ValueError, match="unknown kind"):
        engine.arrange("Cook", "Mix", 1, "stew", None, [])
    with pytest.raises(CanonicalError, match="engine: id: "):
        engine.arrange("Cook", "Mix", 1, "part", "not a uuid", [])
    with pytest.raises(CanonicalError, match="engine: entity key: "):
        engine.arrange("Cook", "Mix", 1, "part", None, ["Knead", "not a uuid"])
    with pytest.raises(CanonicalError, match="engine: id: "):
        engine.entity_history("not a uuid", "part", "Mix")
    with pytest.raises(ValueError, match="unknown kind"):
        engine.entity_history("Bread", "stew", "Mix")
    with pytest.raises(CanonicalError, match="engine: entity key: "):
        engine.entity_history("Bread", "part", "not a uuid")


class Lines:
    """Storage that holds a root's two primary lines, one with no head and
    one of three commits, and one entity's patches, and records each read
    the engine makes of it. It has no other read, so a tree is not read."""

    def __init__(self) -> None:
        self.reads: List[Any] = []

    def transact(self, fn: Any) -> Any:
        return fn(self)

    def primary_refs(self, root: str) -> List[Ref]:
        self.reads.append(("primary_refs", root))
        return [
            Ref("Empty", root, None, None, None, "empty", False, False, 1),
            Ref("Main", root, None, None, "C3", "main", False, False, 4),
        ]

    def ref_commits(self, ref: str, head: str, limit: int) -> List[Commit]:
        self.reads.append(("ref_commits", ref, head, limit))
        ids = ["C3", "C2", "C1"][:limit]
        return [Commit(id, "1", ref, None, "", 1, "h", None, "2026-10-05T09:00:00Z", "Cook", False) for id in ids]

    def entity_patches(self, commits: Sequence[str], kind: str, entity_key: str) -> List[Patch]:
        self.reads.append(("entity_patches", list(commits), kind, entity_key))
        held = [Patch("C1", kind, entity_key, "Row", 1, "ADD"), Patch("C3", kind, entity_key, "Row", 3, "DELETE")]
        return [p for p in held if p.commit in commits]


def test_an_entitys_history_reads_each_lines_commits_and_the_entitys_patches_and_no_tree() -> None:
    # The root and the key read hyphenated and reach storage canonical; the
    # line with no head is passed over; the commits come back in the line's
    # order, newest first, each with its patch's operation, and one that did
    # not change the entity is left out.
    storage = Lines()
    engine = Engine(DESCRIPTOR, storage).with_walk_ceiling(7)
    history = engine.entity_history("00000000-0000-0000-0000-000000000001", "part", "00000000-0000-0000-0000-00000000003d")
    assert [(change.commit.id, change.commit.ref, change.operation) for change in history] == [
        ("C3", "Main", "DELETE"),
        ("C1", "Main", "ADD"),
    ]
    assert storage.reads == [
        ("primary_refs", "1"),
        ("ref_commits", "Main", "C3", 7),
        ("entity_patches", ["C3", "C2", "C1"], "part", "z"),
    ]
    # The ceiling bounds the commits of a line the history reads.
    assert [c.commit.id for c in engine.with_walk_ceiling(2).entity_history("1", "part", "z")] == ["C3"]


def test_order_keys_are_the_cores() -> None:
    assert order_keys(None, None, 3) == ["F", "V", "k"]
    assert order_keys("V", "W", 1) == ["VV"]
    assert order_keys("z", None, 0) == []
    # An empty bound is none, as the Go engine reads it.
    assert order_keys("", "", 1) == ["V"]
    with pytest.raises(vg.VersionGraphError) as refused:
        order_keys("W", "V", 1)
    assert errors.error_code(refused.value) == "no_order_key"
    with pytest.raises(vg.VersionGraphError) as refused:
        order_keys("a b", None, 1)
    assert errors.error_code(refused.value) == "invalid_request"


def test_rows_travel_as_exact_json() -> None:
    text = '{"b":12345678901234567890.123456789,"a":[1,-0,2.50e+3],"c":"\\ud800 \\ud83c\\udf6e"}'
    value = loads(text)
    assert value["b"] == JsonNumber("12345678901234567890.123456789")
    # A lone surrogate reads as U+FFFD, as Go reads it; a pair is one
    # character.
    assert value["c"] == "� \U0001f36e"
    assert dumps(value) == '{"b":12345678901234567890.123456789,"a":[1,-0,2.50e+3],"c":"� \U0001f36e"}'
    assert json_equal(value, loads(dumps(value)))
    assert not json_equal(loads("1"), loads("1.0"))
    assert not json_equal(loads("true"), loads("1"))
    for text in ("NaN", "1 2", "", '{"a":1,}'):
        with pytest.raises(ValueError):
            loads(text)
    with pytest.raises(TypeError):
        dumps(1.5)
