"""Runs every canonical vector in runtime/versiongraph/testdata/canonical
through the package's canonical rules (canonical_value and canonical_row),
and, when SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names a Postgres,
checks each vector's postgres member against what Postgres renders and runs
the rules on that rendering. The Python counterpart of the Go module's
canonical_test.go and canonical_postgres_test.go."""

import json
from typing import Any, Dict, Iterator, List, Tuple

import pytest
from support import DSN, TESTDATA, requires_database

from superschematic_versiongraph.canonical import (
    ELEMENT_CLASSES,
    CanonicalError,
    UnknownClassError,
    canonical_row,
    canonical_value,
    duration_nanos,
    format_duration,
    uuid_canonical,
    uuid_hyphenated,
)

VECTORS = TESTDATA / "canonical"


def _read() -> Tuple[List[Dict[str, Any]], List[Dict[str, Any]]]:
    values: List[Dict[str, Any]] = []
    rows: List[Dict[str, Any]] = []
    for path in sorted(VECTORS.glob("*.json")):
        doc = json.loads(path.read_text(encoding="utf-8"))
        for case in doc.get("cases", []):
            element = case["class"].replace("[]", "")
            assert element == path.stem, f"{path.name}: case {case['name']} has class {case['class']}"
        values.extend(doc.get("cases", []))
        rows.extend(doc.get("rows", []))
    return values, rows


VALUES, ROWS = _read()


def test_every_element_class_has_a_value_a_list_a_null_and_a_refused_vector() -> None:
    seen = {c["class"] for c in VALUES}
    for element in ELEMENT_CLASSES:
        assert element in seen and element + "[]" in seen, element
        own = [c for c in VALUES if c["class"].replace("[]", "") == element]
        assert any(c["postgres"] == "null" for c in own), element
        assert any("error" in c for c in own), element
    for element in ("string", "integer", "number", "uuid", "dateTime", "duration", "json"):
        assert element + "[][]" in seen, element
    assert ROWS


@pytest.mark.parametrize("case", VALUES, ids=[f"{c['class']}/{c['name']}" for c in VALUES])
def test_value_vector(case: Dict[str, Any]) -> None:
    if "error" in case:
        with pytest.raises(CanonicalError):
            canonical_value(case["class"], case["postgres"])
        return
    got = canonical_value(case["class"], case["postgres"])
    assert got == case["canonical"]
    # The canonical form is a fixed point.
    assert canonical_value(case["class"], got) == case["canonical"]


@pytest.mark.parametrize("case", ROWS, ids=[c["name"] for c in ROWS])
def test_row_vector(case: Dict[str, Any]) -> None:
    if "error" in case:
        with pytest.raises(CanonicalError):
            canonical_row(case["columns"], case["postgres"])
        return
    assert canonical_row(case["columns"], case["postgres"]) == case["canonical"]


def test_an_unknown_class_is_refused() -> None:
    for value_class in ("decimal", "uuid[][][]", "", "[]"):
        with pytest.raises(UnknownClassError):
            canonical_value(value_class, '"x"')
    with pytest.raises(UnknownClassError):
        canonical_row({"id": "decimal"}, '{"id":1}')


def test_input_that_is_not_one_json_value_is_refused() -> None:
    for text in ("", "1 2", '{"a":', "NaN", "Infinity"):
        with pytest.raises(CanonicalError):
            canonical_value("number", text)
    with pytest.raises(CanonicalError):
        canonical_row({}, "[1]")


def test_negative_zero_is_zero() -> None:
    # -0, which no Postgres rendering carries (to_jsonb writes 0) but a stored
    # JSON value can, is 0 in both numeric classes.
    assert canonical_value("integer", "-0") == "0"
    assert canonical_value("number", "-0") == "0"


def test_uuid_forms_round_trip() -> None:
    hyphenated = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"
    assert uuid_hyphenated(uuid_canonical(hyphenated)) == hyphenated
    assert uuid_canonical("0") == "0"
    with pytest.raises(CanonicalError):
        uuid_canonical("not-a-uuid")


def test_durations_read_as_go_and_postgres_write_them() -> None:
    # Go's time.ParseDuration forms, which a JSON value the ORM stored keeps,
    # and the overflow and unit rules it has.
    assert duration_nanos("1h30m0s") == 5400 * 10**9
    assert duration_nanos("1.5ms") == 1_500_000
    assert duration_nanos("µs".join(["1", ""])) == 1_000
    assert duration_nanos("-0") == 0
    assert duration_nanos(".5s") == 500_000_000
    assert duration_nanos("9223372036854775807ns") == 2**63 - 1
    assert duration_nanos("-9223372036854775808ns") == -(2**63)
    for refused in ("", "1", "1x", ".s", "9223372036854775808ns", "1 hour", "3 mons"):
        with pytest.raises(CanonicalError):
            duration_nanos(refused)
    assert format_duration(-(2**63)) == "-2562047h47m16.854775808s"


@pytest.fixture(scope="module")
def postgres() -> Iterator[Any]:
    import psycopg

    with psycopg.connect(DSN, autocommit=True) as connection:
        yield connection


def _render(connection: Any, time_zone: str, query: str) -> str:
    """Renders a query's one text column under a session time zone, as the
    text Postgres wrote."""
    import psycopg

    connection.execute("SELECT set_config('TimeZone', %s, false)", [time_zone or "UTC"])
    cursor = psycopg.RawCursor(connection)
    cursor.execute(query)
    assert cursor.pgresult is not None
    value = cursor.pgresult.get_value(0, 0)
    assert value is not None
    return bytes(value).decode()


@requires_database
@pytest.mark.parametrize("case", VALUES, ids=[f"{c['class']}/{c['name']}" for c in VALUES])
def test_value_vector_renders_as_postgres_renders_it(postgres: Any, case: Dict[str, Any]) -> None:
    got = _render(postgres, case.get("timeZone", ""), f"SELECT to_jsonb(t)::text FROM (SELECT {case['sql']} AS v) AS t")
    # The member's JSON text as Postgres wrote it: the rendering is
    # {"v": <value>}.
    assert got.startswith('{"v": ') and got.endswith("}")
    assert got[len('{"v": ') : -1] == case["postgres"]
    # The rules run on what Postgres returned, not only on text written into
    # a file.
    if "error" in case:
        with pytest.raises(CanonicalError):
            canonical_row({"v": case["class"]}, got)
        return
    assert canonical_row({"v": case["class"]}, got) == '{"v":' + case["canonical"] + "}"


@requires_database
@pytest.mark.parametrize("case", ROWS, ids=[c["name"] for c in ROWS])
def test_row_vector_renders_as_postgres_renders_it(postgres: Any, case: Dict[str, Any]) -> None:
    got = _render(postgres, case.get("timeZone", ""), f"SELECT to_jsonb(t)::text FROM (SELECT {case['sql']}) AS t")
    assert got == case["postgres"]
