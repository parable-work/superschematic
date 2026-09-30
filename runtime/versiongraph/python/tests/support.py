"""What the Postgres tests share: the database they run against, a schema of
their own per test that holds the fixture's DDL, and the fixture.

The tests that need Postgres skip without
SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL; make
versiongraph-scenarios-python fails without it."""

import os
import time
from pathlib import Path
from typing import Any, Callable, Iterator, List

import pytest

DATABASE_VARIABLE = "SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL"
DSN = os.environ.get(DATABASE_VARIABLE, "")

requires_database = pytest.mark.skipif(DSN == "", reason=f"set {DATABASE_VARIABLE} to run the Postgres tests")

TESTDATA = Path(__file__).resolve().parents[2] / "testdata"
FIXTURE = TESTDATA / "fixture"

DESCRIPTOR = (FIXTURE / "recipe.json").read_text(encoding="utf-8")
"""The scenarios' graph descriptor, fixture-version-graph-db's Recipe graph,
as JSON text."""

CREATE_SQL = (FIXTURE / "create.sql").read_text(encoding="utf-8")
"""The fixture's Postgres DDL."""

_counter = [0]


class Scratch:
    """A schema of its own holding the fixture's DDL. connect() opens a
    connection whose search path is the schema; the schema is dropped, and
    every connection closed, when the test ends."""

    def __init__(self, schema: str) -> None:
        self.schema = schema
        self._connections: List[Any] = []

    def connect(self, *, autocommit: bool = True) -> Any:
        import psycopg

        connection = psycopg.connect(DSN, autocommit=autocommit, options=f"-c search_path={self.schema},public")
        self._connections.append(connection)
        return connection

    def close(self) -> None:
        for connection in self._connections:
            connection.close()


def create_scratch(prefix: str) -> Scratch:
    import psycopg

    with psycopg.connect(DSN, autocommit=True) as admin:
        # The fixture's DDL creates pgcrypto if it is missing. An extension's
        # name is unique in the database, so create it once in public, where
        # every schema's search path finds it, before tests running in
        # parallel each try to create it in their own schema.
        try:
            admin.execute("CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public")
        except psycopg.errors.UniqueViolation:
            pass
        _counter[0] += 1
        schema = f"{prefix}_{os.getpid()}_{time.time_ns()}_{_counter[0]}"
        admin.execute(f"CREATE SCHEMA {schema}")
    scratch = Scratch(schema)
    scratch.connect().execute(CREATE_SQL)
    return scratch


def drop_scratch(scratch: Scratch) -> None:
    import psycopg

    scratch.close()
    with psycopg.connect(DSN, autocommit=True) as admin:
        admin.execute(f"DROP SCHEMA {scratch.schema} CASCADE")


@pytest.fixture
def scratch_schema() -> Iterator[Callable[[str], Scratch]]:
    """Makes scratch schemas, each dropped when the test ends."""
    made: List[Scratch] = []

    def make(prefix: str) -> Scratch:
        scratch = create_scratch(prefix)
        made.append(scratch)
        return scratch

    yield make
    for scratch in made:
        drop_scratch(scratch)


BASE62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"


def hyphenated(id: str) -> str:
    """A UUID given in its canonical form (base62) as the hyphenated text
    Postgres reads."""
    n = 0
    for c in id:
        n = n * 62 + BASE62.index(c)
    text = "%032x" % n
    return f"{text[0:8]}-{text[8:12]}-{text[12:16]}-{text[16:20]}-{text[20:32]}"
