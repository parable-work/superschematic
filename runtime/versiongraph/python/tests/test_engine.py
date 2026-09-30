"""The engine's rules that need no database: its named errors and their
stable codes, what it refuses before it opens a transaction, and the exact
JSON its rows travel as."""

from typing import Any

import pytest
from support import DESCRIPTOR

import superschematic_versiongraph as vg
from superschematic_versiongraph import errors
from superschematic_versiongraph.canonical import CanonicalError
from superschematic_versiongraph.engine import CommitOptions, Engine, KindEdits, SweepOptions
from superschematic_versiongraph.exactjson import JsonNumber, dumps, json_equal, loads

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
