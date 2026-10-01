"""The facade's conversions a generated facade over the fixture does not
reach: fixture-version-graph-db's only to-one relations are the root and the
ref, which the engine writes, so no generated upsert writes a relation. This
facade's graph is built here over the fixture's descriptor, with the
ingredient's step_key as a writable to-one relation to the step, keyed by
the step's entity key, and plain mappings as its typed values."""

import json
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List

import pytest
from support import DESCRIPTOR, Scratch, requires_database

from superschematic_versiongraph.contract import Finding
from superschematic_versiongraph.facade import (
    Column,
    FacadeGraph,
    FacadeKind,
    NoActorError,
    TypedKindEdits,
    VersionGraphFacade,
    psycopg_client,
)


@dataclass
class Tree:
    step: List[Dict[str, Any]] = field(default_factory=list)
    ingredient: List[Dict[str, Any]] = field(default_factory=list)
    content_hash: str = ""
    findings: List[Finding] = field(default_factory=list)


@dataclass
class Edits:
    step: TypedKindEdits[Dict[str, Any]] = field(default_factory=TypedKindEdits)
    ingredient: TypedKindEdits[Dict[str, Any]] = field(default_factory=TypedKindEdits)


GRAPH = FacadeGraph(
    descriptor=DESCRIPTOR,
    schema_epoch=1,
    snapshot_every=3,
    history_actor_setting="superschematic.history_actor_id",
    kinds=(
        FacadeKind(
            "step",
            "step",
            (
                Column("position", "position", write=True),
                Column("instruction", "instruction", write=True),
                Column("timings", "timings", write=True),
                Column("entity_key", "entityKey", write=True),
                Column("_version", "_version"),
            ),
            json.loads,
        ),
        FacadeKind(
            "ingredient",
            "ingredient",
            (
                Column("step_key", "step", relation_key="entityKey", write=True),
                Column("quantity", "quantity", write=True),
                Column("substitutes", "substitutes", write=True),
                Column("entity_key", "entityKey", write=True),
            ),
            json.loads,
        ),
    ),
    tree=Tree,
)

ACTOR = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"
ROOT = "00000000-0000-4000-8000-000000000001"


class Unreachable:
    def transact(self, fn: Any) -> Any:
        raise AssertionError("the facade reached its client")


def test_a_write_without_an_actor_is_refused_before_the_client() -> None:
    graph: VersionGraphFacade[Tree, Edits] = VersionGraphFacade(GRAPH, Unreachable())
    with pytest.raises(NoActorError):
        graph.create_primary(ROOT, "main")
    with pytest.raises(TypeError, match="a model or a mapping"):
        graph.with_actor(ACTOR).save(ROOT, 1, Edits(step=TypedKindEdits(upsert=["Mix"])))  # type: ignore[list-item]


@requires_database
def test_the_facade_writes_a_to_one_relation_as_its_targets_key(scratch_schema: Callable[[str], Scratch]) -> None:
    # An upsert writes a to-one relation as its target's key, and a read
    # leaves the relation out, since the row does not hold its target.
    scratch = scratch_schema("vg_facade_py")
    connection = scratch.connect()
    connection.execute("INSERT INTO recipe (id, title, created_by) VALUES (%s::uuid, 'Bread', %s::uuid)", [ROOT, ACTOR])
    graph: VersionGraphFacade[Tree, Edits] = VersionGraphFacade(GRAPH, psycopg_client(connection), actor=ACTOR)
    main = graph.create_primary(ROOT, "main")
    draft = graph.branch(main.id, "flour")
    with_step = graph.save(draft.id, draft.version, Edits(step=TypedKindEdits(upsert=[{"position": 1, "instruction": "Mix", "timings": {}}])))
    step_key = with_step.saved.step[0]["entityKey"]
    saved = graph.save(
        draft.id,
        with_step.ref.version,
        {"ingredient": TypedKindEdits(upsert=[{"step": {"entityKey": step_key}, "quantity": "500 g", "substitutes": []}])},  # type: ignore[arg-type]
    )
    assert [("step" in i, i["quantity"]) for i in saved.saved.ingredient] == [(False, "500 g")]

    stored = connection.execute("SELECT step_key::text FROM ingredient").fetchall()
    steps = connection.execute("SELECT entity_key::text FROM step").fetchall()
    assert stored == steps

    tree = graph.compose(draft.id)
    assert tree.findings == []
    assert [(i["quantity"], i["substitutes"]) for i in tree.ingredient] == [("500 g", [])]
    assert [(s["instruction"], s["_version"]) for s in tree.step] == [("Mix", 1)]
