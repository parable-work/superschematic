"""Runs every vector in runtime/versiongraph/testdata/vectors through the
package, three ways.

1. ``run`` takes the vector's input as JSON text, and its output text, or
   the error document of the VersionGraphError it raises, must equal the
   vector's ``expect``: every member in the same order, every number with the
   same digits.
2. The typed operation of a ``VersionGraph`` built with an exact codec (below)
   takes the decoded input, and its output, or the raised error, must equal
   ``expect`` the same way.
3. The module-level typed operation, with the default codec, takes the input
   when that codec reads it without loss, and its output must equal
   ``expect`` as Python values.

A vector whose ``expect`` is an output also checks its input and output
against the contract's types (superschematic_versiongraph/contract.py):
each TypedDict's required members present and no member it lacks, and each
literal inside its union. The last test fails when a member or a literal of a
type never appears in any vector. Together they fail when a type in
contract.py has a member or literal missing, extra or misnamed.

Each TypedDict's required and optional members are checked too: an input
type's against the members the core refuses an input for missing, and an
output type's against the members every vector's output holds.
"""

import copy
import json
import typing
from pathlib import Path
from typing import Any, Dict, List, Set, Tuple

import pytest

import superschematic_versiongraph as vg
from superschematic_versiongraph import contract

VECTORS = Path(__file__).resolve().parents[2] / "testdata" / "vectors"
FILES = sorted(VECTORS.glob("*.json"))


# --- the exact codec -----------------------------------------------------------
#
# json reads a number with a fraction or an exponent as a float, which does
# not keep 12345678901234567890.25. Number keeps the text as written, and
# exact_dumps writes it back unchanged; integers stay Python ints, which keep
# every digit.


class Number(str):
    """A JSON number with a fraction or an exponent, as written."""


def exact_loads(text: str) -> Any:
    return json.loads(text, parse_float=Number)


def exact_dumps(value: Any) -> str:
    if isinstance(value, Number):
        return str(value)
    if isinstance(value, dict):
        return "{" + ",".join(exact_dumps(k) + ":" + exact_dumps(v) for k, v in value.items()) + "}"
    if isinstance(value, list):
        return "[" + ",".join(exact_dumps(v) for v in value) + "]"
    return json.dumps(value, ensure_ascii=False)


def ordered(text: str) -> Any:
    """A JSON document with member order kept and every number as its text,
    so == compares both."""
    return json.loads(
        text,
        object_pairs_hook=lambda pairs: ("object", pairs),
        parse_float=lambda s: ("number", s),
        parse_int=lambda s: ("number", s),
    )


def load(path: Path) -> Dict[str, Any]:
    return exact_loads(path.read_text(encoding="utf-8"))


# --- the contract check ----------------------------------------------------------
#
# What each type declares and what the vectors showed it, for the coverage
# test at the end: members for a TypedDict, values for a literal union.


def _public_types() -> Dict[str, Any]:
    found = {}
    for name in contract.__all__:
        value = getattr(contract, name)
        if isinstance(value, type) and hasattr(value, "__required_keys__"):
            found[name] = value
        elif typing.get_origin(value) is typing.Literal:
            found[name] = value
    return found


TYPES = _public_types()
NAMES = {id(value): name for name, value in TYPES.items()}
DECLARED: Dict[str, List[str]] = {}
SEEN: Dict[str, Set[str]] = {}
for _name, _value in TYPES.items():
    if typing.get_origin(_value) is typing.Literal:
        DECLARED[_name] = [str(arg) for arg in typing.get_args(_value)]
    else:
        DECLARED[_name] = list(typing.get_type_hints(_value))
    SEEN[_name] = set()

# Contract members no vector can carry: a vector is JSON, so the core never
# answers one with invalid_json (a test below sends it text that is not), and
# internal is the core's own failure.
UNREACHABLE = {"ErrorCode": {"internal"}}


def conforms(value: Any, annotation: Any, where: str) -> None:
    """Fails unless value fits annotation, recording what it used. None passes
    through, since a row column may hold it."""
    if value is None or annotation is Any:
        return
    origin = typing.get_origin(annotation)
    name = NAMES.get(id(annotation))
    if origin is typing.Literal:
        allowed = typing.get_args(annotation)
        assert value in allowed and type(value) in {type(a) for a in allowed}, (
            f"{where}: {value!r} is not in {name or annotation}"
        )
        if name is not None:
            SEEN[name].add(str(value))
        if name == "ValueClass":
            # A value class is an element class with [] or [][] after it.
            element, _, suffix = value.partition("[")
            assert suffix in {"", "]", "][]"}, f"{where}: {value!r} is not a value class"
            conforms(element, contract.ElementClass, where)
        return
    if origin is typing.Union:
        fits = [a for a in typing.get_args(annotation) if _keys_fit(value, a)]
        assert len(fits) == 1, f"{where}: {value!r} fits {len(fits)} members of {annotation}"
        conforms(value, fits[0], where)
        return
    if origin is list:
        assert isinstance(value, list), f"{where}: {value!r} is not a list"
        (item,) = typing.get_args(annotation)
        for i, element in enumerate(value):
            conforms(element, item, f"{where}[{i}]")
        return
    if origin is dict:
        assert isinstance(value, dict), f"{where}: {value!r} is not an object"
        _, item = typing.get_args(annotation)
        for key, element in value.items():
            assert isinstance(key, str)
            conforms(element, item, f"{where}.{key}")
        return
    if hasattr(annotation, "__required_keys__"):
        assert isinstance(value, dict), f"{where}: {value!r} is not an object"
        hints = typing.get_type_hints(annotation)
        for member, element in value.items():
            assert member in hints, f"{where}: {name} has no member {member}"
            conforms(element, hints[member], f"{where}.{member}")
            if name is not None:
                SEEN[name].add(member)
        for member in annotation.__required_keys__:
            assert member in value, f"{where}: {name} is missing its required member {member}"
        return
    if annotation is bool:
        assert isinstance(value, bool), f"{where}: {value!r} is not a boolean"
        return
    if annotation is int:
        assert isinstance(value, int) and not isinstance(value, bool), f"{where}: {value!r} is not an integer"
        return
    if annotation is str:
        assert isinstance(value, str) and not isinstance(value, Number), f"{where}: {value!r} is not a string"
        return
    raise AssertionError(f"{where}: no rule for {annotation}")


def _keys_fit(value: Any, annotation: Any) -> bool:
    if not isinstance(value, dict) or not hasattr(annotation, "__required_keys__"):
        return False
    keys = set(value)
    return annotation.__required_keys__ <= keys <= set(typing.get_type_hints(annotation))


# The input and output type of each operation, and the typed method.
OPERATIONS: Dict[str, Tuple[Any, Any, str]] = {
    "compose": (contract.ComposeInput, contract.ComposeOutput, "compose"),
    "merge": (contract.MergeInput, contract.MergeOutput, "merge"),
    "diff": (contract.DiffInput, contract.DiffOutput, "diff"),
    "content_hash": (contract.TreeInput, contract.ContentHashOutput, "content_hash"),
    "validate": (contract.TreeInput, contract.ValidateOutput, "validate"),
}

EXACT = vg.VersionGraph(loads=exact_loads, dumps=exact_dumps)


def error_document(error: vg.VersionGraphError) -> Dict[str, Any]:
    return {"error": {"code": error.code, "message": error.message}}


def default_codec_is_exact(text: str) -> bool:
    """Whether json.loads reads every number in text as written."""
    lossy = []

    def parse_float(s: str) -> float:
        if repr(float(s)) != s:
            lossy.append(s)
        return float(s)

    json.loads(text, parse_float=parse_float)
    return not lossy


def test_vectors_exist() -> None:
    assert FILES


@pytest.mark.parametrize("path", FILES, ids=[path.stem for path in FILES])
def test_vector(path: Path) -> None:
    vector = load(path)
    assert f"{vector['name']}.json" == path.name
    conforms(vector["op"], contract.OperationName, "op")
    input_type, output_type, method = OPERATIONS[vector["op"]]
    input_text = exact_dumps(vector["input"])
    expect = ordered(exact_dumps(vector["expect"]))
    refused = "error" in vector["expect"]

    # 1. JSON text through run.
    try:
        output = vg.run(vector["op"], input_text)
    except vg.VersionGraphError as error:
        assert refused, f"refused: {error}"
        output = json.dumps(error_document(error), ensure_ascii=False)
    assert ordered(output) == expect

    # 2. The typed operation, with the exact codec.
    if refused:
        with pytest.raises(vg.VersionGraphError) as caught:
            getattr(EXACT, method)(vector["input"])
        document = error_document(caught.value)
        conforms(document, contract.ErrorDocument, "expect")
        assert ordered(json.dumps(document, ensure_ascii=False)) == expect
        return
    conforms(vector["input"], input_type, "input")
    result = getattr(EXACT, method)(vector["input"])
    conforms(result, output_type, "output")
    assert ordered(exact_dumps(result)) == expect

    # 3. The module-level typed operation, with the default codec.
    if default_codec_is_exact(input_text):
        result = getattr(vg, method)(json.loads(input_text))
        assert result == json.loads(exact_dumps(vector["expect"]))


def test_a_document_that_is_not_json_is_invalid_json() -> None:
    with pytest.raises(vg.VersionGraphError) as caught:
        vg.run("validate", "{not json")
    conforms(caught.value.code, contract.ErrorCode, "code")
    assert caught.value.code == "invalid_json"


# --- required and optional members ---------------------------------------------
#
# Each TypedDict's required and optional members must be the core's. For an
# input type the core is asked: each member of each object an accepted
# vector's input holds is deleted in turn, and the member is required exactly
# when the core then refuses the input for missing it. For an output type the
# vectors' outputs say: a member is required exactly when every object of the
# type holds it.


def typed_objects(value: Any, annotation: Any, where: Tuple[Any, ...] = ()) -> Any:
    """Yields (annotation, path, object) for each object of value that a
    TypedDict describes, walking as conforms does."""
    if value is None or annotation is Any:
        return
    origin = typing.get_origin(annotation)
    if origin is typing.Union:
        fits = [a for a in typing.get_args(annotation) if _keys_fit(value, a)]
        if len(fits) == 1:
            yield from typed_objects(value, fits[0], where)
    elif origin is list:
        (item,) = typing.get_args(annotation)
        for i, element in enumerate(value):
            yield from typed_objects(element, item, where + (i,))
    elif origin is dict:
        _, item = typing.get_args(annotation)
        for key, element in value.items():
            yield from typed_objects(element, item, where + (key,))
    elif hasattr(annotation, "__required_keys__"):
        yield annotation, where, value
        hints = typing.get_type_hints(annotation)
        for member, element in value.items():
            yield from typed_objects(element, hints[member], where + (member,))


def reachable(annotation: Any, found: Set[str]) -> Set[str]:
    """The names of the TypedDicts annotation is or holds."""
    origin = typing.get_origin(annotation)
    if origin in (typing.Union, list, dict):
        for arg in typing.get_args(annotation):
            reachable(arg, found)
    elif hasattr(annotation, "__required_keys__") and NAMES[id(annotation)] not in found:
        found.add(NAMES[id(annotation)])
        for hint in typing.get_type_hints(annotation).values():
            reachable(hint, found)
    return found


def missing(message: str, member: str) -> bool:
    """Whether the core refused an input for missing member."""
    return (
        f"missing field `{member}`" in message
        or f'missing member "{member}"' in message
        # A resolution is a take or a value by the one of them it holds.
        or (member in ("take", "value") and "give exactly one of take and value" in message)
    )


def keys_of(annotation: Any) -> Tuple[Set[str], Set[str]]:
    return set(annotation.__required_keys__), set(annotation.__optional_keys__)


def test_the_input_types_require_what_the_core_requires() -> None:
    required: Dict[str, Dict[str, bool]] = {}
    for path in FILES:
        vector = load(path)
        if "error" in vector["expect"]:
            continue
        input_type = OPERATIONS[vector["op"]][0]
        for annotation, where, value in typed_objects(vector["input"], input_type):
            probed = required.setdefault(NAMES[id(annotation)], {})
            for member in value:
                if member in probed:
                    continue
                document = copy.deepcopy(vector["input"])
                target = document
                for step in where:
                    target = target[step]
                del target[member]
                try:
                    vg.run(vector["op"], exact_dumps(document))
                    probed[member] = False
                except vg.VersionGraphError as error:
                    probed[member] = missing(error.message, member)
    inputs: Set[str] = set()
    for input_type, _, _ in OPERATIONS.values():
        reachable(input_type, inputs)
    assert set(required) == inputs
    for name, probed in required.items():
        annotation = TYPES[name]
        assert set(probed) == set(typing.get_type_hints(annotation)), f"{name}: no accepted input holds every member"
        core = ({m for m, r in probed.items() if r}, {m for m, r in probed.items() if not r})
        assert keys_of(annotation) == core, f"{name}: (required, optional) is not the core's"


def test_the_output_types_require_what_the_core_always_writes() -> None:
    held: Dict[str, List[Set[str]]] = {}
    for path in FILES:
        vector = load(path)
        output_type = contract.ErrorDocument if "error" in vector["expect"] else OPERATIONS[vector["op"]][1]
        for annotation, _, value in typed_objects(vector["expect"], output_type):
            held.setdefault(NAMES[id(annotation)], []).append(set(value))
    outputs = reachable(contract.ErrorDocument, set())
    for _, output_type, _ in OPERATIONS.values():
        reachable(output_type, outputs)
    assert set(held) == outputs
    for name, objects in held.items():
        annotation = TYPES[name]
        always = set.intersection(*objects)
        assert keys_of(annotation) == (always, set(typing.get_type_hints(annotation)) - always), (
            f"{name}: (required, optional) is not what every output holds"
        )


def test_every_typed_dict_is_an_input_or_an_output() -> None:
    inputs: Set[str] = set()
    outputs = reachable(contract.ErrorDocument, set())
    for input_type, output_type, _ in OPERATIONS.values():
        reachable(input_type, inputs)
        reachable(output_type, outputs)
    typed_dicts = {name for name, value in TYPES.items() if hasattr(value, "__required_keys__")}
    assert inputs | outputs == typed_dicts
    assert not inputs & outputs


# Runs last: every member and literal of every contract type appeared in some
# vector (or above), so none is extra or misnamed without failing.
def test_the_vectors_use_every_member_of_the_contract_types() -> None:
    unused = [
        f"{name}.{member}"
        for name, members in DECLARED.items()
        for member in members
        if member not in SEEN[name] and member not in UNREACHABLE.get(name, set())
    ]
    assert unused == []
