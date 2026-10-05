package pygen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestVersionGraphFacadeOnPostgres generates fixture-version-graph-db's
// Python types package, whose versiongraph_recipe.py is the Recipe graph's
// typed facade over the version graph's Python engine, and runs two pytest
// tests that import it against the Postgres
// SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names, in the Python
// package's uv environment (runtime/versiongraph/python), which holds the
// engine, psycopg and pydantic. The first saves a Tasting, whose columns hold
// a value of every class a descriptor names, commits it and reads it back
// from the save and from the commit: each field comes back as the typed
// value of its canonical form. The second merges, resolves a conflict,
// diffs, releases and rolls back, rebases and sweeps through the facade.
func TestVersionGraphFacadeOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated facade in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to run the Python version graph facade against Postgres")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv not available; skipping the generated Python facade")
	}
	const service = "fixture-version-graph-db"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, service))
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	output, err := Generate(schema, Options{SchemaName: service, Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	root := t.TempDir()
	outDir := filepath.Join(root, output.PythonModuleName)
	paths := testpaths.Local(t)
	if err := SetVersionGraphPath(output, paths, outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	ddl := filepath.Join(root, "create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(testpaths.RepoRoot(t), "runtime", "versiongraph", "testdata", "fixture", "create.sql"), ddl)
	test := filepath.Join(root, "test_graph_facade.py")
	source := strings.ReplaceAll(versionGraphFacadeTest, "MODULE", output.PythonModuleName)
	if err := os.WriteFile(test, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(uv, "run", "--project", paths.VersionGraphPython, "python", "-m", "pytest", "-v", "-p", "no:cacheprovider", test)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+outDir,
		"FACADE_DDL="+ddl,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the generated facade's tests: %v\n%s", err, out)
	}
	for _, name := range []string{"test_facade_keeps_every_class", "test_facade_merges_releases_rebases_and_sweeps"} {
		if !strings.Contains(string(out), name+" PASSED") {
			t.Fatalf("the generated package did not pass %s:\n%s", name, out)
		}
	}
	t.Logf("the generated package passed test_facade_keeps_every_class and test_facade_merges_releases_rebases_and_sweeps against Postgres")
}

// versionGraphFacadeTest is the pytest file that imports the generated
// package; MODULE is its module name.
const versionGraphFacadeTest = `import json
import os
import time
from datetime import datetime, timedelta, timezone

import psycopg
import pytest

from MODULE.types import Step, Tasting
from MODULE.versiongraph_recipe import RecipeEdits, RecipeGraph
from superschematic_versiongraph.facade import (
    CommitOptions,
    NoParentError,
    NotTaggedError,
    Resolution,
    SweepOptions,
    TypedKindEdits,
    error_code,
    psycopg_client,
)

DSN = os.environ["SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL"]
COOK = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"
JANITOR = "3c9a7e21-6b4d-4f8a-9e2c-5d1b7a3f6e08"
BREAD = "00000000-0000-4000-8000-00000000b0b0"


@pytest.fixture
def graph():
    """A schema of its own holding the fixture's DDL and a Bread recipe, the
    Recipe graph over a connection to it, and a connection for SQL."""
    schema = f"vg_py_facade_{os.getpid()}_{time.time_ns()}"
    with psycopg.connect(DSN, autocommit=True) as admin:
        admin.execute(f"CREATE SCHEMA {schema}")
    options = f"-c search_path={schema},public"
    sql = psycopg.connect(DSN, autocommit=True, options=options)
    engine_connection = psycopg.connect(DSN, autocommit=True, options=options)
    try:
        with open(os.environ["FACADE_DDL"], encoding="utf-8") as ddl:
            sql.execute(ddl.read())
        sql.execute("INSERT INTO recipe (id, title, created_by) VALUES (%s::uuid, 'Bread', %s::uuid)", [BREAD, COOK])
        yield RecipeGraph(psycopg_client(engine_connection), actor=COOK), sql
    finally:
        sql.close()
        engine_connection.close()
        with psycopg.connect(DSN, autocommit=True) as admin:
            admin.execute(f"DROP SCHEMA {schema} CASCADE")


def base62(hyphenated):
    n = int(hyphenated.replace("-", ""), 16)
    alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
    out = ""
    while n:
        n, digit = divmod(n, 62)
        out = alphabet[digit] + out
    return out or "0"


def fields(model):
    return model.model_dump(mode="json", by_alias=True)


def test_facade_keeps_every_class(graph):
    g, _ = graph
    main = g.create_primary(BREAD, "main")
    draft = g.branch(main.id, "tastings")
    taster = base62("0e7d4b1a-3c2f-4a6e-8b9d-1f2e3d4c5b6a")
    tasting = Tasting.model_validate_json(json.dumps({
        "taster": taster,
        "salty": True,
        "score": 4.5,
        "servings": 9007199254740991,
        "tastedOn": "2026-09-01",
        "tastedAt": "2026-09-01T12:30:00.25+02:00",
        "servedAt": "18:30",
        "rested": "1.5ms",
        "verdict": "tweak",
        "remarks": {"crumb": "open", "crust": [1, 2.5]},
        "tags": ["sour", "a \"quoted\" tag"],
        "helpers": [COOK],
        "bites": [[1, 2], [3]],
    }))
    # Each field comes back as the typed value of its canonical form: an
    # instant in UTC, a time of day as HH:MM:SS, a duration in the scalar
    # core's form, a UUID in base62; the rest as it was.
    want = fields(tasting)
    want.update({
        "tastedAt": "2026-09-01T10:30:00.250000+00:00",
        "servedAt": "18:30:00",
        "rested": "1500us",
        "helpers": [base62(COOK)],
        "_version": 1,
    })
    saved = g.save(draft.id, draft.version, RecipeEdits(tasting=TypedKindEdits(upsert=[tasting])))
    committed = g.commit(draft.id, saved.ref.version)
    assert committed.commit is not None
    tree = g.materialize(committed.commit.id)
    assert tree.content_hash == committed.commit.content_hash
    assert tree.findings == []
    for what, got in (("saved", saved.saved.tasting), ("materialized", tree.tasting)):
        assert len(got) == 1, (what, got)
        have = fields(got[0])
        for field in ("taster", "salty", "score", "servings", "tastedOn", "tastedAt", "servedAt", "rested",
                      "verdict", "remarks", "tags", "helpers", "bites", "deletedOnRef", "_version"):
            assert have[field] == want[field], (what, field, have[field], want[field])
        assert isinstance(have["entityKey"], str) and have["entityKey"], (what, have)
        assert isinstance(have["id"], str) and have["id"], (what, have)
        assert have["recipe"] is None and have["ref"] is None, (what, have)


PLACEHOLDER = "2026-01-01T00:00:00Z"


def step(position, instruction, entity_key=None):
    value = {
        "position": position,
        "instruction": instruction,
        "timings": {},
        "createdAt": PLACEHOLDER,
        "createdBy": COOK,
        "updatedAt": PLACEHOLDER,
        "updatedBy": COOK,
    }
    if entity_key is not None:
        value["entityKey"] = entity_key
    return Step.model_validate_json(json.dumps(value))


def steps(*values):
    return RecipeEdits(step=TypedKindEdits(upsert=list(values)))


def version(g, ref):
    return g.engine.storage.transact(lambda tx: tx.read_ref(ref)).version


def land(g, main, name, edits, options=CommitOptions()):
    """Saves edits on a new change set of main, commits it and merges it into
    main, returning the merge."""
    draft = g.branch(main, name)
    saved = g.save(draft.id, draft.version, edits)
    g.commit(draft.id, saved.ref.version)
    return g.merge(draft.id, main, version(g, main), options=options)


def test_facade_merges_releases_rebases_and_sweeps(graph):
    g, sql = graph
    main = g.create_primary(BREAD, "main").id
    one = land(g, main, "one", steps(step(1, "Mix")), CommitOptions("v1", True))
    v1 = one.commit
    assert (v1.sequence, v1.message) == (1, "v1")
    mix = g.compose(main).step[0].entity_key

    # Two change sets edit one step: the second merge conflicts until a
    # resolution settles it.
    a = g.branch(main, "a")
    b = g.branch(main, "b")
    for ref, instruction in ((a, "Mix well"), (b, "Mix fast")):
        saved = g.save(ref.id, ref.version, steps(step(1, instruction, mix)))
        g.commit(ref.id, saved.ref.version)
    g.merge(a.id, main, version(g, main))
    conflicted = g.merge(b.id, main, version(g, main))
    assert len(conflicted.conflicts) == 1, conflicted
    conflict = conflicted.conflicts[0]
    assert (conflict.kind, conflict.entity_key, conflict.path) == ("step", mix, "/instruction")
    assert (json.loads(conflict.ours), json.loads(conflict.theirs)) == ("Mix well", "Mix fast")
    settled = g.merge(b.id, main, version(g, main), [Resolution("step", mix, "/instruction", take="theirs")], CommitOptions("v2", True))
    v2 = settled.commit
    changes = g.diff(v1.id, v2.id)
    assert [(c.kind, c.entity_key, c.operation) for c in changes] == [("step", mix, "UPDATE")]

    # The release pointer names a tagged commit; a rollback moves it back.
    released = g.release(BREAD, v2.id, 0)
    read = g.released(BREAD)
    assert (read.release.version, read.tree.step[0].instruction) == (1, "Mix fast")
    g.release(BREAD, v1.id, released.version)
    assert g.released(BREAD).tree.content_hash == v1.content_hash
    untagged = land(g, main, "rest", steps(step(2, "Rest")))
    with pytest.raises(NotTaggedError) as refused:
        g.release(BREAD, untagged.commit.id, 2)
    assert error_code(refused.value) == "not_tagged"

    # A rebase moves a change set onto its parent's newer head.
    c = g.branch(main, "c")
    saved = g.save(c.id, c.version, steps(step(3, "Bake")))
    land(g, main, "cool", steps(step(4, "Cool")))
    rebased = g.rebase(c.id, saved.ref.version)
    assert rebased.conflicts == [] and rebased.commit is not None, rebased
    assert [s.instruction for s in g.compose(c.id).step] == ["Mix fast", "Rest", "Bake", "Cool"]
    with pytest.raises(NoParentError):
        g.rebase(main, version(g, main))

    # A sweep writes as its actor: the rows it collects record it.
    x = g.branch(main, "dropped")
    x_saved = g.save(x.id, x.version, steps(step(9, "Garnish")))
    # The adapter writes the audit columns, whatever the typed value holds.
    garnish = x_saved.saved.step[0]
    placeholder = datetime(2026, 1, 1, tzinfo=timezone.utc)
    assert garnish.created_at != placeholder and garnish.updated_at != placeholder, garnish
    g.discard(x.id, x_saved.ref.version)
    sql.execute("UPDATE recipe_ref SET deleted_at = now() - interval '8 days' WHERE id = (SELECT id FROM recipe_ref WHERE name = 'dropped')")
    report = g.sweep(SweepOptions(JANITOR, discard_grace=timedelta(0)))
    assert (report.skipped, report.collected_refs, report.collected_rows.get("step")) == (False, 1, 1), report
    row = sql.execute(
        "SELECT operation, data->>'updated_by' FROM step_history WHERE (data->>'ref_id')::uuid = "
        "(SELECT id FROM recipe_ref WHERE name = 'dropped') ORDER BY _version DESC LIMIT 1"
    ).fetchone()
    assert row == ("DELETE", JANITOR)
`
