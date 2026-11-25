"""The facade's conversions a generated facade over the fixture does not
reach: fixture-version-graph-db's only to-one relations are the root and the
ref, which the engine writes, so no generated upsert writes a relation. This
facade's graph is built here over the fixture's descriptor, with the
ingredient's step_key as a writable to-one relation to the step, keyed by
the step's entity key, and plain mappings as its typed values. It also holds
the facade's arrange, order_keys and entity_history to the engine's, over the
fixture's part, the kind ordered by a string key."""

import json
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List

import pytest
from support import DESCRIPTOR, Scratch, hyphenated, requires_database

from superschematic_versiongraph import VersionGraphError
from superschematic_versiongraph.contract import Finding
from superschematic_versiongraph.facade import (
    Column,
    FacadeGraph,
    FacadeKind,
    NoActorError,
    TypedKindEdits,
    VersionGraphFacade,
    error_code,
    order_keys,
    psycopg_client,
)


@dataclass
class Tree:
    step: List[Dict[str, Any]] = field(default_factory=list)
    ingredient: List[Dict[str, Any]] = field(default_factory=list)
    part: List[Dict[str, Any]] = field(default_factory=list)
    content_hash: str = ""
    findings: List[Finding] = field(default_factory=list)


@dataclass
class Edits:
    step: TypedKindEdits[Dict[str, Any]] = field(default_factory=TypedKindEdits)
    ingredient: TypedKindEdits[Dict[str, Any]] = field(default_factory=TypedKindEdits)
    part: TypedKindEdits[Dict[str, Any]] = field(default_factory=TypedKindEdits)


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
        FacadeKind(
            "part",
            "part",
            (
                Column("parent_key", "parentKey", write=True),
                Column("rank", "rank", write=True),
                Column("heading", "heading", write=True),
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
    with pytest.raises(NoActorError):
        graph.arrange(ROOT, 1, "part", None, [])
    with pytest.raises(ValueError, match="unknown kind"):
        graph.with_actor(ACTOR).arrange(ROOT, 1, "stew", None, [])
    # A read takes no actor, and an unknown kind is refused before the
    # client too.
    with pytest.raises(ValueError, match="unknown kind"):
        graph.entity_history(ROOT, "stew", ROOT)


def test_the_facades_order_keys_are_the_cores() -> None:
    # The keys need no client and no actor: the class makes them, and so
    # does a graph.
    assert VersionGraphFacade.order_keys(None, None, 3) == ["F", "V", "k"]
    graph: VersionGraphFacade[Tree, Edits] = VersionGraphFacade(GRAPH, Unreachable())
    assert graph.order_keys("V", "W", 1) == ["VV"]
    assert graph.order_keys("z", None, 2) == order_keys("z", None, 2)
    with pytest.raises(VersionGraphError) as refused:
        graph.order_keys("W", "V", 1)
    assert error_code(refused.value) == "no_order_key"


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


@requires_database
def test_the_facade_arranges_a_parents_children(scratch_schema: Callable[[str], Scratch]) -> None:
    # Parts saved with the keys order_keys makes read in that order. Arrange
    # writes the one part that moved, and returns it typed, as save does.
    scratch = scratch_schema("vg_facade_py")
    connection = scratch.connect()
    connection.execute("INSERT INTO recipe (id, title, created_by) VALUES (%s::uuid, 'Bread', %s::uuid)", [ROOT, ACTOR])
    graph: VersionGraphFacade[Tree, Edits] = VersionGraphFacade(GRAPH, psycopg_client(connection), actor=ACTOR)
    main = graph.create_primary(ROOT, "main")
    draft = graph.branch(main.id, "write")
    ranks = graph.order_keys(None, None, 3)
    written = graph.save(
        draft.id,
        draft.version,
        Edits(part=TypedKindEdits(upsert=[{"rank": r, "heading": h} for r, h in zip(ranks, ["Mix", "Knead", "Rest"])])),
    )
    mix, knead, rest = [p["entityKey"] for p in written.saved.part]

    arranged = graph.arrange(draft.id, written.ref.version, "part", None, [rest, mix, knead])
    assert arranged.ref.version == written.ref.version + 1
    assert arranged.saved.part == [dict(written.saved.part[2], rank="7")]
    assert (arranged.saved.step, arranged.saved.content_hash) == ([], "")
    assert [(p["heading"], p["rank"]) for p in graph.compose(draft.id).part] == [("Rest", "7"), ("Mix", "F"), ("Knead", "V")]

    # A parent's children are arranged apart from the rows with no parent,
    # and the parent and the keys read hyphenated as they read canonical.
    nested = graph.save(
        draft.id,
        arranged.ref.version,
        Edits(
            part=TypedKindEdits(
                upsert=[{"parentKey": mix, "rank": r, "heading": h} for r, h in zip(ranks, ["Weigh", "Stir", "Scrape"])]
            )
        ),
    )
    weigh, stir, scrape = [p["entityKey"] for p in nested.saved.part]
    moved = graph.arrange(draft.id, nested.ref.version, "part", hyphenated(mix), [hyphenated(stir), weigh, scrape])
    assert [(p["heading"], p["rank"], p["parentKey"]) for p in moved.saved.part] == [("Stir", "7", mix)]

    # Arranged as it stands, nothing is written and the ref's version moves.
    still = graph.arrange(draft.id, moved.ref.version, "part", mix, [stir, weigh, scrape])
    assert (still.saved.part, still.ref.version) == ([], moved.ref.version + 1)
    stored = connection.execute(
        'SELECT heading, rank FROM part ORDER BY parent_key NULLS FIRST, rank COLLATE "C"'
    ).fetchall()
    assert stored == [("Rest", "7"), ("Mix", "F"), ("Knead", "V"), ("Stir", "7"), ("Weigh", "F"), ("Scrape", "k")]

    # The core's refusal keeps its code, and writes nothing.
    for kind, parent, keys, code in [
        ("part", mix, [stir, weigh], "invalid_arrangement"),
        ("part", None, [rest, mix, knead, stir], "invalid_arrangement"),
        ("step", None, [], "order_not_a_key"),
    ]:
        with pytest.raises(VersionGraphError) as refused:
            graph.arrange(draft.id, still.ref.version, kind, parent, keys)
        assert error_code(refused.value) == code
    assert graph.compose(draft.id).findings == []
    assert connection.execute("SELECT _version FROM recipe_ref WHERE name = 'write'").fetchall() == [(still.ref.version,)]


@requires_database
def test_the_facade_lists_an_entitys_history(scratch_schema: Callable[[str], Scratch]) -> None:
    # The commits of the root's primary line that changed a part, newest
    # first, each the engine's commit with what it did to the part. The kind
    # is named as the descriptor names it; a change set's own commit, and a
    # commit that changed another part, are not in it.
    scratch = scratch_schema("vg_facade_py")
    connection = scratch.connect()
    connection.execute("INSERT INTO recipe (id, title, created_by) VALUES (%s::uuid, 'Bread', %s::uuid)", [ROOT, ACTOR])
    graph: VersionGraphFacade[Tree, Edits] = VersionGraphFacade(GRAPH, psycopg_client(connection), actor=ACTOR)
    main = graph.create_primary(ROOT, "main")
    line = [main]

    def change(name: str, parts: List[Dict[str, Any]]) -> Any:
        # Merges a change set's parts into the primary line: the parts as
        # saved, and the commit the merge wrote.
        draft = graph.branch(main.id, name)
        saved = graph.save(draft.id, draft.version, Edits(part=TypedKindEdits(upsert=parts)))
        graph.commit(saved.ref.id, saved.ref.version)
        merged = graph.merge(draft.id, main.id, line[0].version)
        assert merged.commit is not None
        line[0] = merged.ref
        return saved.saved.part, merged.commit

    written, added = change("write", [{"rank": "F", "heading": "Mix"}, {"rank": "k", "heading": "Bake"}])
    mix, bake = [p["entityKey"] for p in written]
    _, edited = change("edit", [{"entityKey": mix, "rank": "F", "heading": "Mix well"}])
    _, baked = change("bake", [{"entityKey": bake, "rank": "k", "heading": "Bake hot"}])

    history = graph.entity_history(ROOT, "part", mix)
    assert [(c.commit.id, c.operation) for c in history] == [(edited.id, "UPDATE"), (added.id, "ADD")]
    assert history[0].commit == graph.history(main.id)[1]
    assert [(c.commit.id, c.operation) for c in graph.entity_history(ROOT, "part", hyphenated(bake))] == [
        (baked.id, "UPDATE"),
        (added.id, "ADD"),
    ]
    assert graph.entity_history(ROOT, "step", mix) == []
    assert graph.entity_history(ACTOR, "part", mix) == []
