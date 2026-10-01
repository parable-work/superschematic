"""Runs every scenario in runtime/versiongraph/testdata/scenarios through the
Python engine and its Postgres adapter, each in a schema of its own that
holds the fixture's DDL (runtime/versiongraph/README.md, "Scenarios"). The
Python counterpart of the Go engine's scenario_test.go: the same files, the
same rules for reading them, the same checks. It needs the Postgres
SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names, and skips without it;
make versiongraph-scenarios-python fails without it."""

from dataclasses import fields, is_dataclass
from datetime import timedelta
from typing import Any, Dict, List, Optional, Sequence

import pytest
from support import DESCRIPTOR, TESTDATA, Scratch, create_scratch, drop_scratch, hyphenated, requires_database

from superschematic_versiongraph.engine import (
    CommitOptions,
    Engine,
    KindEdits,
    Resolution,
    SweepOptions,
    SweepReport,
    TreeResult,
)
from superschematic_versiongraph.errors import error_code
from superschematic_versiongraph.exactjson import JsonNumber, dumps, json_equal, loads
from superschematic_versiongraph.postgres import PostgresAdapter, psycopg_client
from superschematic_versiongraph.storage import Commit, Ref, Release

# The schema epoch and snapshot interval the fixture's Recipe graph declares,
# which the engine of every step runs at unless the step names another.
FIXTURE_SCHEMA_EPOCH = 1
FIXTURE_SNAPSHOT_EVERY = 3

# The actor of a step that names none: "Cook", a UUID in its canonical form.
DEFAULT_ACTOR = "Cook"

SCENARIOS = TESTDATA / "scenarios"
FILES = sorted(SCENARIOS.glob("*.json"))

# The members each object of a scenario may have; any other is refused, as
# the Go runner's decoder refuses it.
MEMBERS = {
    "scenario": {"name", "description", "steps"},
    "step": {
        "op",
        "as",
        "actor",
        "root",
        "name",
        "ref",
        "from",
        "to",
        "source",
        "target",
        "commit",
        "toCommit",
        "version",
        "edits",
        "message",
        "tag",
        "resolutions",
        "walkCeiling",
        "schemaEpoch",
        "snapshotEvery",
        "sweep",
        "kind",
        "statement",
        "args",
        "expect",
    },
    "kindEdits": {"upsert", "delete", "unset"},
    "sweep": {"discardGraceSeconds", "abandonAfterSeconds", "pruneBatch"},
    "sqlArg": {"uuid", "ref", "commit"},
    "resolution": {"kind", "entityKey", "path", "take", "value"},
    "expect": {
        "error",
        "ref",
        "commit",
        "tree",
        "saved",
        "contentHash",
        "contentHashOf",
        "findings",
        "conflicts",
        "changes",
        "commits",
        "rows",
        "patches",
        "snapshot",
        "release",
        "report",
    },
    "refExpect": {"version", "sealed", "name", "parent", "base", "head"},
    "commitExpect": {"ref", "parent", "message", "sequence", "schemaEpoch", "contentHash", "contentHashOf"},
    "releaseExpect": {"commit", "version"},
}

# The engine's snake_case names of the members a scenario names in camelCase.
CAMEL = {
    "entity_key": "entityKey",
    "ours_author": "oursAuthor",
    "theirs_author": "theirsAuthor",
    "collected_refs": "collectedRefs",
    "collected_rows": "collectedRows",
    "entity_version": "entityVersion",
}


def obj(value: Any, kind: str, where: str) -> Dict[str, Any]:
    if not isinstance(value, dict):
        raise AssertionError(f"{where}: a {kind} is a JSON object")
    for name in value:
        if name not in MEMBERS[kind]:
            raise AssertionError(f"{where}: unknown {kind} member {name!r}")
    return value


def text(value: Any) -> str:
    return value if isinstance(value, str) else ""


def num(value: Any) -> Optional[int]:
    return int(value.text) if isinstance(value, JsonNumber) else None


def to_json(value: Any, json_members: Sequence[str] = ()) -> Any:
    """A plain value (a report, a conflict, a snapshot entry) as an exact
    JSON value, members named as a scenario names them. json_members carry
    JSON text, which is read as the value it holds."""
    if value is None or isinstance(value, (bool, str)):
        return value
    if isinstance(value, int):
        return JsonNumber(str(value))
    if isinstance(value, (list, tuple)):
        return [to_json(v) for v in value]
    if is_dataclass(value):
        out = {}
        for f in fields(value):
            member = getattr(value, f.name)
            if member is None:
                continue
            name = CAMEL.get(f.name, f.name)
            out[name] = loads(member) if name in json_members or f.name in json_members else to_json(member)
        return out
    if isinstance(value, dict):
        return {k: to_json(v) for k, v in value.items()}
    raise TypeError(f"{value!r} is not a plain value")


def test_the_scenario_directory_holds_scenarios() -> None:
    assert FILES


@requires_database
@pytest.mark.parametrize("path", FILES, ids=[p.stem for p in FILES])
def test_scenario(path: Any) -> None:
    scenario = obj(loads(path.read_text(encoding="utf-8")), "scenario", path.name)
    assert text(scenario.get("name")) == path.stem, f"{path.name}: the scenario's name is not the file's"
    steps = scenario.get("steps")
    if not isinstance(steps, list) or not steps:
        raise AssertionError(f"{path.name}: a scenario has steps")
    runner = Runner(create_scratch("vg_scenario_py"))
    try:
        for i, value in enumerate(steps):
            step = obj(value, "step", f"{path.name} step {i}")
            runner.where = f"{path.stem} step {i} ({text(step.get('op'))})"
            runner.run(step)
    finally:
        runner.close()


class Failure(AssertionError):
    pass


class Runner:
    """One scenario's database, engine and named results."""

    def __init__(self, scratch: Scratch) -> None:
        self.scratch = scratch
        self.where = ""
        self.connection = scratch.connect()
        self.adapter = PostgresAdapter(DESCRIPTOR)
        self.client = psycopg_client(self.connection)
        self.engine = Engine(
            DESCRIPTOR,
            self.adapter.storage(self.client),
            schema_epoch=FIXTURE_SCHEMA_EPOCH,
            snapshot_every=FIXTURE_SNAPSHOT_EVERY,
        )
        self.refs: Dict[str, Ref] = {}
        self.commits: Dict[str, Commit] = {}
        self.releases: Dict[str, Release] = {}
        # The connection whose transaction holds the graph's sweep lock,
        # between holdSweepLock and releaseSweepLock.
        self.holder: Any = None

    def close(self) -> None:
        if self.holder is not None:
            self.holder.execute("ROLLBACK")
            self.holder = None
        drop_scratch(self.scratch)

    def fail(self, message: str) -> Failure:
        return Failure(f"{self.where}: {message}")

    def ref_id(self, name: str) -> str:
        """A ref named by an earlier step's "as", or a literal id written
        "id:<uuid>"."""
        if name.startswith("id:"):
            return name[3:]
        if name not in self.refs:
            raise self.fail(f"no ref is named {name!r}")
        return self.refs[name].id

    def commit_id(self, name: str) -> str:
        """A commit named by an earlier step's "as", or a literal id written
        "id:<uuid>"."""
        if name.startswith("id:"):
            return name[3:]
        if name not in self.commits:
            raise self.fail(f"no commit is named {name!r}")
        return self.commits[name].id

    def version(self, step: Dict[str, Any], name: str) -> int:
        """The step's version, else the named ref's current one."""
        given = num(step.get("version"))
        if given is not None:
            return given
        if name not in self.refs:
            raise self.fail(f"no ref is named {name!r}")
        return self.refs[name].version

    def track_ref(self, ref: Ref) -> None:
        """Records a ref's new state under every name bound to it."""
        for name, known in list(self.refs.items()):
            if known.id == ref.id:
                self.refs[name] = ref

    def engine_for(self, step: Dict[str, Any]) -> Engine:
        """The scenario's engine, at the step's walk ceiling, schema epoch and
        snapshot interval when it names them."""
        engine = self.engine
        schema_epoch = num(step.get("schemaEpoch"))
        snapshot_every = num(step.get("snapshotEvery")) or 0
        if schema_epoch is not None or snapshot_every != 0:
            engine = Engine(
                DESCRIPTOR,
                self.adapter.storage(self.client),
                schema_epoch=schema_epoch if schema_epoch is not None else FIXTURE_SCHEMA_EPOCH,
                snapshot_every=snapshot_every or FIXTURE_SNAPSHOT_EVERY,
            )
        walk_ceiling = num(step.get("walkCeiling")) or 0
        if walk_ceiling != 0:
            engine = engine.with_walk_ceiling(walk_ceiling)
        return engine

    def run(self, step: Dict[str, Any]) -> None:
        engine = self.engine_for(step)
        op = text(step.get("op"))
        as_ = text(step.get("as"))
        actor = step["actor"] if isinstance(step.get("actor"), str) else DEFAULT_ACTOR
        x = obj(step.get("expect") or {}, "expect", self.where)

        ref: Optional[Ref] = None
        commit: Optional[Commit] = None
        commit_aware = False
        tree: Optional[TreeResult] = None
        saved: Optional[Dict[str, List[str]]] = None
        conflicts: List[Any] = []
        changes: List[Any] = []
        history: List[Commit] = []
        rows: List[str] = []
        patches: List[Any] = []
        entries: List[Any] = []
        release: Optional[Release] = None
        report: Optional[SweepReport] = None
        error: Optional[BaseException] = None

        try:
            if op in ("createPrimary", "branch"):
                if op == "createPrimary":
                    ref = engine.create_primary(actor, text(step.get("root")), text(step.get("name")))
                else:
                    ref = engine.branch(actor, self.ref_id(text(step.get("from"))), text(step.get("name")))
                if as_:
                    self.refs[as_] = ref
            elif op == "save":
                name = text(step.get("ref"))
                result = engine.save(actor, self.ref_id(name), self.version(step, name), self.edits(step))
                ref, saved = result.ref, result.saved
            elif op in ("commit", "seal", "revert"):
                name = text(step.get("ref"))
                id, version = self.ref_id(name), self.version(step, name)
                if op == "commit":
                    options = CommitOptions(text(step.get("message")), step.get("tag") is True)
                    committed = engine.commit(actor, id, version, options)
                elif op == "seal":
                    committed = engine.seal(actor, id, version)
                else:
                    committed = engine.revert(actor, id, version, self.commit_id(text(step.get("toCommit"))))
                ref, commit, commit_aware = committed.ref, committed.commit, True
            elif op in ("merge", "rebase"):
                resolutions = self.resolutions(step)
                if op == "merge":
                    target = text(step.get("target"))
                    merged = engine.merge(
                        actor,
                        self.ref_id(text(step.get("source"))),
                        self.ref_id(target),
                        self.version(step, target),
                        resolutions,
                        CommitOptions(text(step.get("message")), step.get("tag") is True),
                    )
                else:
                    name = text(step.get("ref"))
                    merged = engine.rebase(actor, self.ref_id(name), self.version(step, name), resolutions)
                ref, commit, conflicts, commit_aware = merged.ref, merged.commit, merged.conflicts, True
            elif op == "release":
                root = text(step.get("root"))
                given = num(step.get("version"))
                if given is None:
                    given = self.releases[root].version if root in self.releases else 0
                release = engine.release(actor, root, self.commit_id(text(step.get("commit"))), given)
                self.releases[root] = release
            elif op == "released":
                read = engine.released(text(step.get("root")))
                release, tree = read.release, read.tree
            elif op == "sweep":
                report = engine.sweep(self.sweep_options(step, actor))
            elif op == "holdSweepLock":
                self.hold_sweep_lock()
            elif op == "releaseSweepLock":
                if self.holder is None:
                    raise self.fail("no sweep lock is held")
                holder, self.holder = self.holder, None
                holder.execute("ROLLBACK")
            elif op == "snapshot":
                commit_id = self.commit_id(text(step.get("commit")))
                entries = self.adapter.storage(self.client).transact(lambda tx: tx.snapshot(commit_id))
            elif op == "materialize":
                tree = engine.materialize(self.commit_id(text(step.get("commit"))))
            elif op == "compose":
                tree = engine.compose(self.ref_id(text(step.get("ref"))))
            elif op == "diff":
                changes = engine.diff(self.commit_id(text(step.get("from"))), self.commit_id(text(step.get("to"))))
            elif op == "history":
                history = engine.history(self.ref_id(text(step.get("ref"))))
            elif op == "discard":
                name = text(step.get("ref"))
                engine.discard(actor, self.ref_id(name), self.version(step, name))
            elif op == "rows":
                kind, ref_id = text(step.get("kind")), self.ref_id(text(step.get("ref")))
                rows = self.adapter.storage(self.client).transact(lambda tx: tx.rows(kind, ref_id))
            elif op == "patches":
                commit_id = self.commit_id(text(step.get("commit")))
                patches = self.adapter.storage(self.client).transact(lambda tx: tx.patches([commit_id]))
            elif op == "sql":
                rows = self.query(text(step.get("statement")), [self.sql_arg(a) for a in step.get("args") or []])
            else:
                raise self.fail(f"unknown op {op!r}")
        except Failure:
            raise
        except Exception as caught:  # noqa: BLE001 - the step's expectation decides
            error = caught

        want_error = text(x.get("error"))
        if want_error:
            if error is None:
                raise self.fail(f"succeeded, want error {want_error}")
            code = error_code(error)
            if code != want_error:
                raise self.fail(f"error {code!r} ({error!r}), want {want_error}")
            return
        if error is not None:
            raise self.fail(f"{type(error).__name__}: {error}") from error
        if ref is not None:
            self.track_ref(ref)
        if commit is not None and as_ and op not in ("createPrimary", "branch"):
            self.commits[as_] = commit

        if x.get("ref") is not None:
            if ref is None:
                raise self.fail(f"expects a ref, and {op} returns none")
            self.check_ref(ref, obj(x["ref"], "refExpect", self.where))
        if "commit" in x:
            if not commit_aware:
                raise self.fail(f"expects a commit, and {op} returns none")
            self.check_commit(commit, x["commit"])
        if x.get("saved") is not None:
            self.check_tree("saved", saved or {}, x["saved"])
        want_tree, want_findings = x.get("tree"), x.get("findings")
        if want_tree is not None or text(x.get("contentHash")) or text(x.get("contentHashOf")) or want_findings is not None:
            if tree is None:
                raise self.fail(f"expects a tree, and {op} returns none")
            if want_tree is not None:
                self.check_tree("tree", tree.tree, want_tree)
            self.check_hash(tree.content_hash, text(x.get("contentHash")), text(x.get("contentHashOf")))
            if want_findings is not None:
                self.check_list("findings", [to_json(f) for f in tree.findings], want_findings)
        if x.get("conflicts") is not None:
            self.check_list(
                "conflicts",
                [to_json(c, ("base", "ours", "theirs", "oursAuthor", "theirsAuthor")) for c in conflicts],
                x["conflicts"],
            )
        elif conflicts:
            raise self.fail(f"merge left conflicts {conflicts}, and the step expects none")
        if x.get("changes") is not None:
            self.check_list("changes", [to_json(c, ("row",)) for c in changes], x["changes"])
        if x.get("commits") is not None:
            got = [self.commit_name(c.id) for c in history]
            want = [text(v) for v in x["commits"]]
            if got != want:
                raise self.fail(f"history {got}, want {want}")
        if x.get("rows") is not None:
            parsed = [loads(row) for row in rows]
            if op != "sql":
                parsed.sort(key=lambda row: dumps(row.get("entity_key")))
            self.check_list("rows", parsed, x["rows"])
        if x.get("snapshot") is not None:
            ordered = sorted(entries, key=lambda e: (e.kind, e.entity_key))
            self.check_list(
                "snapshot",
                [to_json({"kind": e.kind, "entityKey": e.entity_key, "entityVersion": e.entity_version}) for e in ordered],
                x["snapshot"],
            )
        if x.get("release") is not None:
            if release is None:
                raise self.fail(f"expects a release, and {op} returns none")
            w = obj(x["release"], "releaseExpect", self.where)
            got_commit = self.commit_name(release.commit)
            if got_commit != text(w.get("commit")):
                raise self.fail(f"the release names commit {got_commit!r}, want {text(w.get('commit'))!r}")
            want_version = num(w.get("version"))
            if want_version is not None and release.version != want_version:
                raise self.fail(f"release version {release.version}, want {want_version}")
        if x.get("report") is not None:
            if report is None:
                raise self.fail(f"expects a report, and {op} returns none")
            self.check_list("report", [to_json(report)], [x["report"]])
        if x.get("patches") is not None:
            ordered_patches = sorted(patches, key=lambda p: (p.kind, p.entity_key))
            self.check_list(
                "patches",
                [
                    to_json(
                        {
                            "kind": p.kind,
                            "entityKey": p.entity_key,
                            "operation": p.operation,
                            "entityVersion": p.entity_version,
                        }
                    )
                    for p in ordered_patches
                ],
                x["patches"],
            )

    def edits(self, step: Dict[str, Any]) -> Dict[str, KindEdits]:
        given = step.get("edits")
        if given is None:
            return {}
        if not isinstance(given, dict):
            raise self.fail("edits are a JSON object")
        out: Dict[str, KindEdits] = {}
        for kind, value in given.items():
            e = obj(value, "kindEdits", self.where)
            out[kind] = KindEdits(
                upsert=[dumps(row) for row in e.get("upsert") or []],
                delete=[text(key) for key in e.get("delete") or []],
                unset=[text(key) for key in e.get("unset") or []],
            )
        return out

    def resolutions(self, step: Dict[str, Any]) -> List[Resolution]:
        out: List[Resolution] = []
        for value in step.get("resolutions") or []:
            r = obj(value, "resolution", self.where)
            out.append(
                Resolution(
                    text(r.get("kind")),
                    text(r.get("entityKey")),
                    text(r.get("path")),
                    take=text(r.get("take")) if "take" in r else None,
                    value=dumps(r["value"]) if "value" in r else None,
                )
            )
        return out

    def sweep_options(self, step: Dict[str, Any], actor: str) -> SweepOptions:
        given = step.get("sweep")
        if given is None:
            return SweepOptions(actor)
        o = obj(given, "sweep", self.where)
        return SweepOptions(
            actor,
            discard_grace=timedelta(seconds=num(o.get("discardGraceSeconds")) or 0),
            abandon_after=timedelta(seconds=num(o.get("abandonAfterSeconds")) or 0),
            prune_batch=num(o.get("pruneBatch")) or 0,
        )

    def hold_sweep_lock(self) -> None:
        """Takes the graph's sweep lock through the adapter in a transaction
        of another connection, and keeps it open until releaseSweepLock."""
        if self.holder is not None:
            raise self.fail("the sweep lock is already held")
        holder = self.scratch.connect()
        holder.execute("BEGIN")
        self.holder = holder
        locked = self.adapter.storage(psycopg_client(holder)).transact(lambda tx: tx.sweep_lock())
        if not locked:
            raise self.fail("take the sweep lock: another transaction holds it")

    def query(self, statement: str, args: List[str]) -> List[str]:
        """An sql step's statement, and its rows in the order it returns
        them, each a JSON object of its columns read as text: the statement
        casts what it selects."""
        import psycopg

        cursor = psycopg.RawCursor(self.connection)
        cursor.execute(statement, args)
        result = cursor.pgresult
        out: List[str] = []
        if result is not None:
            names = [bytes(result.fname(j) or b"").decode() for j in range(result.nfields)]
            for i in range(result.ntuples):
                row = {}
                for j, name in enumerate(names):
                    value = result.get_value(i, j)
                    row[name] = None if value is None else bytes(value).decode()
                out.append(dumps(row))
        cursor.close()
        return out

    def sql_arg(self, value: Any) -> str:
        arg = obj(value, "sqlArg", self.where)
        if text(arg.get("uuid")):
            id = text(arg.get("uuid"))
        elif text(arg.get("ref")):
            id = self.ref_id(text(arg.get("ref")))
        elif text(arg.get("commit")):
            id = self.commit_id(text(arg.get("commit")))
        else:
            raise self.fail("an sql argument names a uuid, a ref or a commit")
        return hyphenated(id)

    def commit_name(self, id: str) -> str:
        """The name an earlier step bound a commit to, or its id."""
        for name, commit in self.commits.items():
            if commit.id == id:
                return name
        return "id:" + id

    def ref_name(self, id: str) -> str:
        for name, ref in self.refs.items():
            if ref.id == id:
                return name
        return "id:" + id

    def check_name(self, what: str, id: Optional[str], want: Any, has_want: bool, name: Any) -> None:
        """Compares an id with an expectation that names a ref or a commit, or
        is null for none."""
        if not has_want:
            return
        if want is None:
            if id is not None:
                raise self.fail(f"{what} is {name(id)}, want none")
            return
        if id is None or name(id) != want:
            raise self.fail(f"{what} is {name(id) if id is not None else ''!r}, want {want!r}")

    def check_ref(self, got: Ref, want: Dict[str, Any]) -> None:
        version = num(want.get("version"))
        if version is not None and got.version != version:
            raise self.fail(f"ref version {got.version}, want {version}")
        sealed = want.get("sealed")
        if isinstance(sealed, bool) and got.sealed != sealed:
            raise self.fail(f"ref sealed {got.sealed}, want {sealed}")
        name = want.get("name")
        if isinstance(name, str) and got.name != name:
            raise self.fail(f"ref name {got.name!r}, want {name!r}")
        self.check_name("the ref's parent", got.parent, want.get("parent"), "parent" in want, self.ref_name)
        self.check_name("the ref's base", got.base, want.get("base"), "base" in want, self.commit_name)
        self.check_name("the ref's head", got.head, want.get("head"), "head" in want, self.commit_name)

    def check_commit(self, got: Optional[Commit], raw: Any) -> None:
        if raw is None:
            if got is not None:
                raise self.fail(f"wrote commit {got.id}, want none")
            return
        if got is None:
            raise self.fail("wrote no commit, want one")
        want = obj(raw, "commitExpect", self.where)
        ref = want.get("ref")
        if isinstance(ref, str) and self.ref_name(got.ref) != ref:
            raise self.fail(f"commit ref {self.ref_name(got.ref)!r}, want {ref!r}")
        self.check_name("the commit's parent", got.parent, want.get("parent"), "parent" in want, self.commit_name)
        message = want.get("message")
        if isinstance(message, str) and got.message != message:
            raise self.fail(f"commit message {got.message!r}, want {message!r}")
        if "sequence" in want:
            got_sequence = "null" if got.sequence is None else str(got.sequence)
            if got_sequence != dumps(want["sequence"]):
                raise self.fail(f"commit sequence {got_sequence}, want {dumps(want['sequence'])}")
        schema_epoch = num(want.get("schemaEpoch"))
        if schema_epoch is not None and got.schema_epoch != schema_epoch:
            raise self.fail(f"commit schema epoch {got.schema_epoch}, want {schema_epoch}")
        self.check_hash(got.content_hash, text(want.get("contentHash")), text(want.get("contentHashOf")))

    def check_hash(self, got: str, want: str, want_of: str) -> None:
        if want and got != want:
            raise self.fail(f"content hash {got}, want {want}")
        if want_of:
            if want_of not in self.commits:
                raise self.fail(f"no commit is named {want_of!r}")
            if got != self.commits[want_of].content_hash:
                raise self.fail(f"content hash {got}, want {want_of}'s, {self.commits[want_of].content_hash}")

    def check_tree(self, what: str, got: Dict[str, List[str]], want: Any) -> None:
        """Compares every kind of a tree with the expected rows: the same
        kinds, and per kind the same number of rows in the same order, each
        with the listed columns' values."""
        if not isinstance(want, dict):
            raise self.fail(f"{what} expectation is a JSON object")
        for kind in got:
            if kind not in want:
                raise self.fail(f"{what} has {kind} rows {got[kind]}, and the step expects none")
        for kind, rows in want.items():
            self.check_list(f"{what} {kind}", [loads(row) for row in got.get(kind, [])], rows)

    def check_list(self, what: str, got: List[Any], want: Any) -> None:
        """Compares a list of JSON objects with expected ones: the same
        length, and each object with the listed members' values."""
        if not isinstance(want, list):
            raise self.fail(f"{what} expectation is a JSON array")
        if len(got) != len(want):
            raise self.fail(f"{what}: {len(got)}, want {len(want)}: [{','.join(dumps(g) for g in got)}]")
        for i, (item, want_item) in enumerate(zip(got, want)):
            if not isinstance(item, dict) or not isinstance(want_item, dict):
                raise self.fail(f"{what}[{i}] is not an object")
            for column, value in want_item.items():
                if column not in item and value is None:
                    continue
                if column not in item or not json_equal(item[column], value):
                    have = dumps(item[column]) if column in item else "absent"
                    raise self.fail(f"{what}[{i}].{column} is {have}, want {dumps(value)} ({dumps(item)})")
