// Runs every scenario in runtime/versiongraph/testdata/scenarios through the
// TypeScript engine and its Postgres adapter, each in a schema of its own
// that holds the fixture's DDL (runtime/versiongraph/README.md,
// "Scenarios"). The TypeScript counterpart of the Go engine's
// scenario_test.go: the same files, the same rules for reading them, the
// same checks. It needs the Postgres SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL
// names, and skips without it; make versiongraph-scenarios-ts fails
// without it.
import { beforeAll, expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import type pg from "pg";
import {
  compareCodePoints,
  Engine,
  errorCode,
  isJsonObject,
  jsonEqual,
  JsonNumber,
  parseJson,
  stringifyJson,
  uuidHyphenated,
  type Commit,
  type Edits,
  type JsonObject,
  type JsonValue,
  type Ref,
  type Release,
  type Resolution,
  type SweepOptions,
  type SweepReport,
  type TreeResult,
} from "../dist/engine.js";
import { init, type VersionGraph } from "../dist/index.js";
import { PostgresAdapter, pgClient, pgPool } from "../dist/postgres.js";
import { dsn, descriptor, rawTypes, scratchSchema, type Scratch } from "./postgres.js";

// The schema epoch and snapshot interval the fixture's Recipe graph declares,
// which the engine of every step runs at unless the step names another.
const fixtureSchemaEpoch = 1;
const fixtureSnapshotEvery = 3;

// The actor of a step that names none: "Cook", a UUID in its canonical form.
const defaultActor = "Cook";

// The backend this runner runs the scenarios on. A step that lists its
// backends runs here only when it lists this one, and an sql step runs its
// statement for this backend.
const backend = "postgres";

// The backends a scenario may name.
const knownBackends: readonly string[] = ["postgres", "sqlite"];

const scenarioDir = new URL("../../testdata/scenarios/", import.meta.url);
const files = readdirSync(scenarioDir)
  .filter((name) => name.endsWith(".json"))
  .sort();

// The members each object of a scenario may have; any other is refused, as
// the Go runner's decoder refuses it.
const members = {
  scenario: ["name", "description", "roots", "steps"],
  step: [
    "op",
    "backends",
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
  ],
  kindEdits: ["upsert", "delete", "unset"],
  sweep: ["discardGraceSeconds", "abandonAfterSeconds", "pruneBatch"],
  sqlArg: ["uuid", "ref", "commit"],
  resolution: ["kind", "entityKey", "path", "take", "value"],
  expect: [
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
  ],
  refExpect: ["version", "sealed", "name", "parent", "base", "head"],
  commitExpect: ["ref", "parent", "message", "sequence", "schemaEpoch", "contentHash", "contentHashOf"],
  releaseExpect: ["commit", "version"],
} as const;

function object(value: JsonValue | undefined, kind: keyof typeof members, where: string): JsonObject {
  if (!isJsonObject(value)) {
    throw new Error(`${where}: a ${kind} is a JSON object`);
  }
  for (const name of value.keys()) {
    if (!(members[kind] as readonly string[]).includes(name)) {
      throw new Error(`${where}: unknown ${kind} member ${JSON.stringify(name)}`);
    }
  }
  return value;
}

function str(value: JsonValue | undefined): string {
  return typeof value === "string" ? value : "";
}

function num(value: JsonValue | undefined): number | undefined {
  return value instanceof JsonNumber ? Number(value.text) : undefined;
}

/** A plain value (a report, a conflict, a snapshot entry) as a JSON value, numbers kept as their text. */
function toJson(value: unknown): JsonValue {
  if (value === null || value === undefined) {
    return null;
  }
  if (typeof value === "number") {
    return new JsonNumber(String(value));
  }
  if (typeof value === "string" || typeof value === "boolean") {
    return value;
  }
  if (Array.isArray(value)) {
    return value.map(toJson);
  }
  const out: JsonObject = new Map();
  for (const [name, member] of Object.entries(value as Record<string, unknown>)) {
    if (member !== undefined) {
      out.set(name, toJson(member));
    }
  }
  return out;
}

/** A value that carries JSON text in some members (a conflict's units, a change's row) as a JSON value. */
function withJson(value: object, jsonMembers: readonly string[]): JsonValue {
  const out = toJson(value) as JsonObject;
  for (const name of jsonMembers) {
    const text = (value as Record<string, unknown>)[name];
    if (typeof text === "string") {
      out.set(name, parseJson(text));
    }
  }
  return out;
}

/** A scenario as the format says: its name, its roots, and its steps, each checked. */
interface Scenario {
  name: string;
  roots: string[];
  steps: JsonObject[];
}

/** A list of distinct strings, or the reason it is not one. */
function names(value: JsonValue, what: string): string[] {
  if (!Array.isArray(value)) {
    throw new Error(`${what} is a list`);
  }
  const out: string[] = [];
  for (const name of value) {
    if (typeof name !== "string") {
      throw new Error(`${what} lists ${stringifyJson(name)}, not a name`);
    }
    if (out.includes(name)) {
      throw new Error(`${what} lists ${JSON.stringify(name)} twice`);
    }
    out.push(name);
  }
  return out;
}

/**
 * Reads a scenario as the format says (runtime/versiongraph/README.md,
 * "Scenarios") for a runner of `runnerBackend`. It refuses an unknown
 * member; a scenario with no steps or no roots; a list of roots or backends
 * that holds a value other than a name or names one twice; an empty
 * backends list or one that names a backend no runner knows; a statement,
 * on any step, that is not an object of one string per backend or that
 * names an unknown backend; and an sql step that runs on `runnerBackend`
 * with no statement for it. A null `backends` or `statement` is none.
 */
function readScenario(text: string, where: string, runnerBackend: string): Scenario {
  const scenario = object(parseJson(text), "scenario", where);
  const steps = scenario.get("steps");
  if (!Array.isArray(steps) || steps.length === 0) {
    throw new Error(`${where}: a scenario has steps`);
  }
  const roots = scenario.get("roots");
  if (roots === undefined || roots === null) {
    throw new Error(`${where}: a scenario names its roots`);
  }
  let rootNames: string[];
  try {
    rootNames = names(roots, "roots");
  } catch (err) {
    throw new Error(`${where}: ${(err as Error).message}`);
  }
  if (rootNames.length === 0) {
    throw new Error(`${where}: a scenario names at least one root`);
  }
  const checked = steps.map((value, i) => {
    const stepWhere = `${where} step ${i}`;
    const step = object(value, "step", stepWhere);
    try {
      checkStep(step, runnerBackend);
    } catch (err) {
      throw new Error(`${stepWhere} (${str(step.get("op"))}): ${(err as Error).message}`);
    }
    return step;
  });
  return { name: str(scenario.get("name")), roots: rootNames, steps: checked };
}

/** Checks a step's backends and statement. */
function checkStep(step: JsonObject, runnerBackend: string): void {
  const backends = step.get("backends");
  if (backends !== undefined && backends !== null) {
    const listed = names(backends, "backends");
    if (listed.length === 0) {
      throw new Error("backends lists no backend");
    }
    for (const name of listed) {
      if (!knownBackends.includes(name)) {
        throw new Error(`backends lists unknown backend ${JSON.stringify(name)}`);
      }
    }
  }
  const statement = step.get("statement");
  if (statement !== undefined && statement !== null) {
    if (!isJsonObject(statement)) {
      throw new Error(`a statement is an object of one statement per backend, not ${stringifyJson(statement)}`);
    }
    for (const [name, text] of [...statement].sort(([a], [b]) => compareCodePoints(a, b))) {
      if (!knownBackends.includes(name)) {
        throw new Error(`a statement for unknown backend ${JSON.stringify(name)}`);
      }
      if (typeof text !== "string") {
        throw new Error(`a statement is an object of one statement per backend, not ${stringifyJson(statement)}`);
      }
    }
  }
  if (str(step.get("op")) === "sql" && runsOn(step, runnerBackend)) {
    if (!isJsonObject(statement) || !statement.has(runnerBackend)) {
      throw new Error(`the sql step has no ${runnerBackend} statement`);
    }
  }
}

/** Whether a step runs on the backend: a step runs on every backend unless it lists the ones it runs on. */
function runsOn(step: JsonObject, runnerBackend: string): boolean {
  const backends = step.get("backends");
  return backends === undefined || backends === null || (backends as JsonValue[]).includes(runnerBackend);
}

/**
 * Opens a runner, seeds the scenario's roots, and runs each step that runs on
 * the runner's backend, in order; `then` runs on the runner before it closes.
 */
async function runScenario(scenario: Scenario, then?: (runner: Runner) => Promise<void>): Promise<void> {
  const runner = await Runner.open();
  try {
    await runner.seed(scenario.roots);
    for (const [i, step] of scenario.steps.entries()) {
      if (!runsOn(step, backend)) {
        continue;
      }
      runner.where = `${scenario.name} step ${i} (${str(step.get("op"))})`;
      await runner.run(step);
    }
    await then?.(runner);
  } finally {
    await runner.close();
  }
}

let core: VersionGraph;

beforeAll(async () => {
  core = await init();
});

test("the scenario directory holds scenarios", () => {
  expect(files.length).toBeGreaterThan(0);
});

test("every scenario file reads as the format says", () => {
  for (const file of files) {
    const scenario = readScenario(readFileSync(new URL(file, scenarioDir), "utf8"), file, backend);
    expect(scenario.name).toBe(file.replace(/\.json$/, ""));
  }
});

/** A scenario of the given roots (raw JSON, or "" for none) and steps. */
function formatScenario(roots: string, ...steps: string[]): string {
  const member = roots === "" ? "" : `"roots": ${roots}, `;
  return `{"name": "format", "description": "", ${member}"steps": [${steps.join(", ")}]}`;
}

const createPrimaryStep = `{"op": "createPrimary", "root": "Bread", "name": "main"}`;

// Scenarios that each break one rule of the format, with a part of the error
// each is refused with, and ones that keep them ("" for none).
const formatCases: [string, string, string][] = [
  ["a statement per backend", formatScenario(`["Bread"]`, `{"op": "sql", "statement": {"postgres": "SELECT 1", "sqlite": "SELECT 1"}}`), ""],
  ["a plain string statement", formatScenario(`["Bread"]`, `{"op": "sql", "statement": "SELECT 1"}`), "a statement is an object of one statement per backend"],
  ["a statement that is not text", formatScenario(`["Bread"]`, `{"op": "sql", "statement": {"postgres": 1}}`), "a statement is an object of one statement per backend"],
  ["an sql step without the runner's statement", formatScenario(`["Bread"]`, `{"op": "sql", "statement": {"sqlite": "SELECT 1"}}`), "the sql step has no postgres statement"],
  ["an sql step with no statement", formatScenario(`["Bread"]`, `{"op": "sql"}`), "the sql step has no postgres statement"],
  ["a null statement for the runner's backend", formatScenario(`["Bread"]`, `{"op": "sql", "statement": {"postgres": null}}`), "a statement is an object of one statement per backend"],
  ["a null statement on a step that is not sql", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "statement": null}`), ""],
  ["a statement for an unknown backend on a step that is not sql", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "statement": {"mysql": "x"}}`), `a statement for unknown backend "mysql"`],
  ["a plain string statement on a step that is not sql", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "statement": "x"}`), "a statement is an object of one statement per backend"],
  ["a statement for an unknown backend", formatScenario(`["Bread"]`, `{"op": "sql", "statement": {"postgres": "SELECT 1", "mysql": "SELECT 1"}}`), `a statement for unknown backend "mysql"`],
  ["an sql step for another backend, without the runner's statement", formatScenario(`["Bread"]`, `{"op": "sql", "backends": ["sqlite"], "statement": {"sqlite": "SELECT 1"}}`), ""],
  ["backends listing the runner's", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backends": ["sqlite", "postgres"]}`), ""],
  ["backends listing an unknown backend", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backends": ["postgres", "mysql"]}`), `backends lists unknown backend "mysql"`],
  ["an empty backends", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backends": []}`), "backends lists no backend"],
  ["null backends", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backends": null}`), ""],
  ["backends listing a backend twice", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backends": ["postgres", "postgres"]}`), `backends lists "postgres" twice`],
  ["no roots", formatScenario("", createPrimaryStep), "a scenario names its roots"],
  ["null roots", formatScenario("null", createPrimaryStep), "a scenario names its roots"],
  ["empty roots", formatScenario("[]", createPrimaryStep), "a scenario names at least one root"],
  ["a root named twice", formatScenario(`["Bread", "Soup", "Bread"]`, createPrimaryStep), `roots lists "Bread" twice`],
  ["a null root", formatScenario("[null]", createPrimaryStep), "roots lists null, not a name"],
  ["no steps", formatScenario(`["Bread"]`), "a scenario has steps"],
  ["an unknown scenario member", `{"name": "format", "description": "", "roots": ["Bread"], "backend": "postgres", "steps": [${createPrimaryStep}]}`, `unknown scenario member "backend"`],
  ["an unknown step member", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backend": "postgres"}`), `unknown step member "backend"`],
];

for (const [name, text, refused] of formatCases) {
  test(`the scenario format: ${name}`, () => {
    if (refused === "") {
      readScenario(text, "format", backend);
    } else {
      expect(() => readScenario(text, "format", backend)).toThrow(refused);
    }
  });
}

// A scenario whose steps list their backends: a save listed for SQLite alone
// is skipped and leaves no row, and a save listed for Postgres too writes its
// row.
test.skipIf(dsn === "")("a step whose backends leave out the runner's is skipped", async () => {
  await runScenario(
    readScenario(
      formatScenario(
        `["Bread"]`,
        `{"op": "createPrimary", "root": "Bread", "name": "main", "as": "main"}`,
        `{"op": "branch", "from": "main", "name": "mix", "as": "mix"}`,
        `{"op": "save", "ref": "mix", "backends": ["sqlite"], "edits": {"step": {"upsert": [{"entity_key": "Mix", "position": 1, "instruction": "Mix", "timings": {}}]}}}`,
        `{"op": "rows", "ref": "mix", "kind": "step", "expect": {"rows": []}}`,
        `{"op": "save", "ref": "mix", "backends": ["sqlite", "postgres"], "edits": {"step": {"upsert": [{"entity_key": "Rest", "position": 2, "instruction": "Rest", "timings": {}}]}}}`,
        `{"op": "rows", "ref": "mix", "kind": "step", "expect": {"rows": [{"entity_key": "Rest"}]}}`,
      ),
      "backends",
      backend,
    ),
  );
});

// A scenario of two roots: each has the recipe row the seeding sql steps gave
// it (its id, its name as the title and the default actor as its creator) and
// no other root has one, so a primary line of a root the scenario does not
// name fails on the foreign key from recipe_ref.root_id.
test.skipIf(dsn === "")("a scenario's roots are seeded, and only they", async () => {
  await runScenario(
    readScenario(
      formatScenario(
        `["Soup", "Pie"]`,
        `{"op": "sql", "statement": {"postgres": "SELECT title, CASE id WHEN $1::uuid THEN 'Soup' WHEN $2::uuid THEN 'Pie' ELSE id::text END AS id, CASE created_by WHEN $3::uuid THEN 'Cook' ELSE created_by::text END AS created_by FROM recipe ORDER BY title"},
          "args": [{"uuid": "Soup"}, {"uuid": "Pie"}, {"uuid": "Cook"}],
          "expect": {"rows": [{"title": "Pie", "id": "Pie", "created_by": "Cook"}, {"title": "Soup", "id": "Soup", "created_by": "Cook"}]}}`,
        `{"op": "createPrimary", "root": "Soup", "name": "main"}`,
        `{"op": "createPrimary", "root": "Pie", "name": "main"}`,
      ),
      "roots",
      backend,
    ),
    async (runner) => {
      const error = await runner.engine.createPrimary(defaultActor, "Bread", "main").then(
        () => undefined,
        (err: unknown) => err,
      );
      expect((error as { code?: string } | undefined)?.code).toBe("23503");
    },
  );
});

for (const file of files) {
  const name = file.replace(/\.json$/, "");
  test.skipIf(dsn === "")(
    `scenario ${name}`,
    async () => {
      const scenario = readScenario(readFileSync(new URL(file, scenarioDir), "utf8"), file, backend);
      expect(scenario.name).toBe(name);
      await runScenario(scenario);
    },
    120_000,
  );
}

/** One scenario's database, engine and named results. */
class Runner {
  where = "";
  readonly refs = new Map<string, Ref>();
  readonly commits = new Map<string, Commit>();
  readonly releases = new Map<string, Release>();
  // The connection whose transaction holds the graph's sweep lock, between
  // holdSweepLock and releaseSweepLock.
  holder: pg.PoolClient | undefined;

  constructor(
    readonly scratch: Scratch,
    readonly adapter: PostgresAdapter,
    readonly engine: Engine,
  ) {}

  static async open(): Promise<Runner> {
    const scratch = await scratchSchema("vg_scenario_ts");
    const adapter = new PostgresAdapter(descriptor);
    const engine = new Engine(core, descriptor, adapter.storage(pgPool(scratch.pool)), {
      schemaEpoch: fixtureSchemaEpoch,
      snapshotEvery: fixtureSnapshotEvery,
    });
    return new Runner(scratch, adapter, engine);
  }

  async close(): Promise<void> {
    if (this.holder !== undefined) {
      await this.holder.query("ROLLBACK");
      this.holder.release();
      this.holder = undefined;
    }
    await this.scratch.close();
  }

  /**
   * Gives each root the row a root has on Postgres: a recipe whose id is the
   * root, whose title is the root's name and whose creator is the default
   * actor, all in one statement.
   */
  async seed(roots: string[]): Promise<void> {
    const args = [uuidHyphenated(defaultActor)];
    const values = roots.map((root) => {
      args.push(uuidHyphenated(root), root);
      return `($${args.length - 1}::uuid, $${args.length}, $1::uuid)`;
    });
    await this.scratch.pool.query(`INSERT INTO recipe (id, title, created_by) VALUES ${values.join(", ")}`, args);
  }

  fail(message: string): never {
    throw new Error(`${this.where}: ${message}`);
  }

  /** A ref named by an earlier step's "as", or a literal id written "id:<uuid>". */
  refID(name: string): string {
    if (name.startsWith("id:")) {
      return name.slice(3);
    }
    const ref = this.refs.get(name);
    if (ref === undefined) {
      this.fail(`no ref is named ${JSON.stringify(name)}`);
    }
    return ref.id;
  }

  /** A commit named by an earlier step's "as", or a literal id written "id:<uuid>". */
  commitID(name: string): string {
    if (name.startsWith("id:")) {
      return name.slice(3);
    }
    const commit = this.commits.get(name);
    if (commit === undefined) {
      this.fail(`no commit is named ${JSON.stringify(name)}`);
    }
    return commit.id;
  }

  /** The step's version, else the named ref's current one. */
  version(step: JsonObject, name: string): number {
    const given = num(step.get("version"));
    if (given !== undefined) {
      return given;
    }
    const ref = this.refs.get(name);
    if (ref === undefined) {
      this.fail(`no ref is named ${JSON.stringify(name)}`);
    }
    return ref.version;
  }

  /** Records a ref's new state under every name bound to it. */
  trackRef(ref: Ref): void {
    for (const [name, known] of this.refs) {
      if (known.id === ref.id) {
        this.refs.set(name, ref);
      }
    }
  }

  /** The scenario's engine, at the step's walk ceiling, schema epoch and snapshot interval when it names them. */
  engineFor(step: JsonObject): Engine {
    let engine = this.engine;
    const schemaEpoch = num(step.get("schemaEpoch"));
    const snapshotEvery = num(step.get("snapshotEvery")) ?? 0;
    if (schemaEpoch !== undefined || snapshotEvery !== 0) {
      engine = new Engine(core, descriptor, this.adapter.storage(pgPool(this.scratch.pool)), {
        schemaEpoch: schemaEpoch ?? fixtureSchemaEpoch,
        snapshotEvery: snapshotEvery !== 0 ? snapshotEvery : fixtureSnapshotEvery,
      });
    }
    const walkCeiling = num(step.get("walkCeiling")) ?? 0;
    if (walkCeiling !== 0) {
      engine = engine.withWalkCeiling(walkCeiling);
    }
    return engine;
  }

  async run(step: JsonObject): Promise<void> {
    const engine = this.engineFor(step);
    const op = str(step.get("op"));
    const as = str(step.get("as"));
    const actorValue = step.get("actor");
    const actor = typeof actorValue === "string" ? actorValue : defaultActor;
    const x = object(step.get("expect") ?? new Map(), "expect", this.where);

    let ref: Ref | undefined;
    let commit: Commit | null = null;
    let commitAware = false;
    let tree: TreeResult | undefined;
    let saved: Record<string, string[]> | undefined;
    let conflicts: unknown[] = [];
    let changes: unknown[] = [];
    let history: Commit[] = [];
    let rows: string[] = [];
    let patches: { kind: string; entityKey: string; operation: string; entityVersion: number }[] = [];
    let entries: { kind: string; entityKey: string; entityVersion: number }[] = [];
    let release: Release | undefined;
    let report: SweepReport | undefined;
    let error: unknown;

    try {
      switch (op) {
        case "createPrimary":
        case "branch": {
          const created =
            op === "createPrimary"
              ? await engine.createPrimary(actor, str(step.get("root")), str(step.get("name")))
              : await engine.branch(actor, this.refID(str(step.get("from"))), str(step.get("name")));
          ref = created;
          if (as !== "") {
            this.refs.set(as, created);
          }
          break;
        }
        case "save": {
          const result = await engine.save(actor, this.refID(str(step.get("ref"))), this.version(step, str(step.get("ref"))), this.edits(step));
          ref = result.ref;
          saved = result.saved;
          break;
        }
        case "commit":
        case "seal":
        case "revert": {
          const refName = str(step.get("ref"));
          const id = this.refID(refName);
          const version = this.version(step, refName);
          const result =
            op === "commit"
              ? await engine.commit(actor, id, version, { message: str(step.get("message")), tag: step.get("tag") === true })
              : op === "seal"
                ? await engine.seal(actor, id, version)
                : await engine.revert(actor, id, version, this.commitID(str(step.get("toCommit"))));
          ref = result.ref;
          commit = result.commit;
          commitAware = true;
          break;
        }
        case "merge":
        case "rebase": {
          const resolutions = this.resolutions(step);
          const result =
            op === "merge"
              ? await engine.merge(
                  actor,
                  this.refID(str(step.get("source"))),
                  this.refID(str(step.get("target"))),
                  this.version(step, str(step.get("target"))),
                  resolutions,
                  { message: str(step.get("message")), tag: step.get("tag") === true },
                )
              : await engine.rebase(actor, this.refID(str(step.get("ref"))), this.version(step, str(step.get("ref"))), resolutions);
          ref = result.ref;
          commit = result.commit;
          conflicts = result.conflicts;
          commitAware = true;
          break;
        }
        case "release": {
          const root = str(step.get("root"));
          const version = num(step.get("version")) ?? this.releases.get(root)?.version ?? 0;
          release = await engine.release(actor, root, this.commitID(str(step.get("commit"))), version);
          this.releases.set(root, release);
          break;
        }
        case "released": {
          const result = await engine.released(str(step.get("root")));
          release = result.release;
          tree = result;
          break;
        }
        case "sweep": {
          const options: SweepOptions = { actor };
          const given = step.get("sweep");
          if (given !== undefined && given !== null) {
            const o = object(given, "sweep", this.where);
            options.discardGrace = (num(o.get("discardGraceSeconds")) ?? 0) * 1000;
            options.abandonAfter = (num(o.get("abandonAfterSeconds")) ?? 0) * 1000;
            options.pruneBatch = num(o.get("pruneBatch")) ?? 0;
          }
          report = await engine.sweep(options);
          break;
        }
        case "holdSweepLock":
          await this.holdSweepLock();
          break;
        case "releaseSweepLock": {
          if (this.holder === undefined) {
            this.fail("no sweep lock is held");
          }
          const holder = this.holder;
          this.holder = undefined;
          await holder.query("ROLLBACK");
          holder.release();
          break;
        }
        case "snapshot":
          entries = await this.adapter
            .storage(pgPool(this.scratch.pool))
            .transact((tx) => tx.snapshot(this.commitID(str(step.get("commit")))));
          break;
        case "materialize":
          tree = await engine.materialize(this.commitID(str(step.get("commit"))));
          break;
        case "compose":
          tree = await engine.compose(this.refID(str(step.get("ref"))));
          break;
        case "diff":
          changes = await engine.diff(this.commitID(str(step.get("from"))), this.commitID(str(step.get("to"))));
          break;
        case "history":
          history = await engine.history(this.refID(str(step.get("ref"))));
          break;
        case "discard":
          await engine.discard(actor, this.refID(str(step.get("ref"))), this.version(step, str(step.get("ref"))));
          break;
        case "rows":
          rows = await this.adapter
            .storage(pgPool(this.scratch.pool))
            .transact((tx) => tx.rows(str(step.get("kind")), this.refID(str(step.get("ref")))));
          break;
        case "patches":
          patches = await this.adapter
            .storage(pgPool(this.scratch.pool))
            .transact((tx) => tx.patches([this.commitID(str(step.get("commit")))]));
          break;
        case "sql": {
          const args = ((step.get("args") as JsonValue[] | undefined) ?? []).map((arg) => this.sqlArg(arg));
          const statement = str((step.get("statement") as JsonObject).get(backend));
          const result = await this.scratch.pool.query({ text: statement, values: args, types: rawTypes });
          // A query's rows, in the order it returns them, every column as
          // the text Postgres writes.
          rows = result.rows.map((row: Record<string, string | null>) => JSON.stringify(row));
          break;
        }
        default:
          this.fail(`unknown op ${JSON.stringify(op)}`);
      }
    } catch (err) {
      if (err instanceof Error && err.message.startsWith(this.where + ":")) {
        throw err;
      }
      error = err;
    }

    const wantError = str(x.get("error"));
    if (wantError !== "") {
      if (error === undefined) {
        this.fail(`succeeded, want error ${wantError}`);
      }
      const code = errorCode(error);
      if (code !== wantError) {
        this.fail(`error ${JSON.stringify(code)} (${String(error)}), want ${wantError}`);
      }
      return;
    }
    if (error !== undefined) {
      this.fail(error instanceof Error ? (error.stack ?? error.message) : String(error));
    }
    if (ref !== undefined) {
      this.trackRef(ref);
    }
    if (commit !== null && as !== "" && op !== "createPrimary" && op !== "branch") {
      this.commits.set(as, commit);
    }

    const wantRef = x.get("ref");
    if (wantRef !== undefined && wantRef !== null) {
      if (ref === undefined) {
        this.fail(`expects a ref, and ${op} returns none`);
      }
      this.checkRef(ref, object(wantRef, "refExpect", this.where));
    }
    const wantCommit = x.get("commit");
    if (wantCommit !== undefined) {
      if (!commitAware) {
        this.fail(`expects a commit, and ${op} returns none`);
      }
      this.checkCommit(commit, wantCommit);
    }
    const wantSaved = x.get("saved");
    if (wantSaved !== undefined && wantSaved !== null) {
      this.checkTree("saved", saved ?? {}, wantSaved);
    }
    const wantTree = x.get("tree");
    const wantFindings = x.get("findings");
    if (
      (wantTree !== undefined && wantTree !== null) ||
      str(x.get("contentHash")) !== "" ||
      str(x.get("contentHashOf")) !== "" ||
      (wantFindings !== undefined && wantFindings !== null)
    ) {
      if (tree === undefined) {
        this.fail(`expects a tree, and ${op} returns none`);
      }
      if (wantTree !== undefined && wantTree !== null) {
        this.checkTree("tree", tree.tree, wantTree);
      }
      this.checkHash(tree.contentHash, str(x.get("contentHash")), str(x.get("contentHashOf")));
      if (wantFindings !== undefined && wantFindings !== null) {
        this.checkList("findings", tree.findings.map(toJson), wantFindings);
      }
    }
    const wantConflicts = x.get("conflicts");
    if (wantConflicts !== undefined && wantConflicts !== null) {
      this.checkList(
        "conflicts",
        conflicts.map((c) => withJson(c as object, ["base", "ours", "theirs", "oursAuthor", "theirsAuthor"])),
        wantConflicts,
      );
    } else if (conflicts.length > 0) {
      this.fail(`merge left conflicts ${JSON.stringify(conflicts)}, and the step expects none`);
    }
    const wantChanges = x.get("changes");
    if (wantChanges !== undefined && wantChanges !== null) {
      this.checkList(
        "changes",
        changes.map((c) => withJson(c as object, ["row"])),
        wantChanges,
      );
    }
    const wantCommits = x.get("commits");
    if (wantCommits !== undefined && wantCommits !== null) {
      const got = history.map((c) => this.commitName(c.id));
      const want = (wantCommits as JsonValue[]).map((v) => str(v));
      if (JSON.stringify(got) !== JSON.stringify(want)) {
        this.fail(`history ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
      }
    }
    const wantRows = x.get("rows");
    if (wantRows !== undefined && wantRows !== null) {
      const key = (row: string) => stringifyJson((parseJson(row) as JsonObject).get("entity_key") ?? null);
      const sorted = op === "sql" ? rows : [...rows].sort((a, b) => compareCodePoints(key(a), key(b)));
      this.checkList("rows", sorted.map(parseJson), wantRows);
    }
    const wantSnapshot = x.get("snapshot");
    if (wantSnapshot !== undefined && wantSnapshot !== null) {
      const sorted = [...entries].sort((a, b) => compareCodePoints(a.kind, b.kind) || compareCodePoints(a.entityKey, b.entityKey));
      this.checkList(
        "snapshot",
        sorted.map((e) => toJson({ kind: e.kind, entityKey: e.entityKey, entityVersion: e.entityVersion })),
        wantSnapshot,
      );
    }
    const wantRelease = x.get("release");
    if (wantRelease !== undefined && wantRelease !== null) {
      if (release === undefined) {
        this.fail(`expects a release, and ${op} returns none`);
      }
      const w = object(wantRelease, "releaseExpect", this.where);
      const got = this.commitName(release.commit);
      if (got !== str(w.get("commit"))) {
        this.fail(`the release names commit ${JSON.stringify(got)}, want ${JSON.stringify(str(w.get("commit")))}`);
      }
      const version = num(w.get("version"));
      if (version !== undefined && release.version !== version) {
        this.fail(`release version ${release.version}, want ${version}`);
      }
    }
    const wantReport = x.get("report");
    if (wantReport !== undefined && wantReport !== null) {
      if (report === undefined) {
        this.fail(`expects a report, and ${op} returns none`);
      }
      this.checkList("report", [toJson(report)], [wantReport]);
    }
    const wantPatches = x.get("patches");
    if (wantPatches !== undefined && wantPatches !== null) {
      const sorted = [...patches].sort((a, b) => compareCodePoints(a.kind, b.kind) || compareCodePoints(a.entityKey, b.entityKey));
      this.checkList(
        "patches",
        sorted.map((p) =>
          toJson({ kind: p.kind, entityKey: p.entityKey, operation: p.operation, entityVersion: p.entityVersion }),
        ),
        wantPatches,
      );
    }
  }

  edits(step: JsonObject): Edits {
    const out: Record<string, { upsert: string[]; delete: string[]; unset: string[] }> = {};
    const given = step.get("edits");
    if (given === undefined || given === null) {
      return out;
    }
    if (!isJsonObject(given)) {
      this.fail("edits are a JSON object");
    }
    for (const [kind, value] of given) {
      const e = object(value, "kindEdits", this.where);
      out[kind] = {
        upsert: ((e.get("upsert") as JsonValue[] | undefined) ?? []).map(stringifyJson),
        delete: ((e.get("delete") as JsonValue[] | undefined) ?? []).map((key) => str(key)),
        unset: ((e.get("unset") as JsonValue[] | undefined) ?? []).map((key) => str(key)),
      };
    }
    return out;
  }

  resolutions(step: JsonObject): Resolution[] {
    const given = step.get("resolutions");
    if (given === undefined || given === null) {
      return [];
    }
    return (given as JsonValue[]).map((value) => {
      const r = object(value, "resolution", this.where);
      const base = { kind: str(r.get("kind")), entityKey: str(r.get("entityKey")), path: str(r.get("path")) };
      const out: Record<string, unknown> = { ...base };
      if (r.has("take")) {
        out.take = str(r.get("take"));
      }
      if (r.has("value")) {
        out.value = stringifyJson(r.get("value")!);
      }
      return out as Resolution;
    });
  }

  /**
   * Takes the graph's sweep lock through the adapter in a transaction of
   * another connection, and keeps it open until releaseSweepLock.
   */
  async holdSweepLock(): Promise<void> {
    if (this.holder !== undefined) {
      this.fail("the sweep lock is already held");
    }
    const holder = await this.scratch.pool.connect();
    this.holder = holder;
    await holder.query("BEGIN");
    const locked = await this.adapter.storage(pgClient(holder, { savepoint: true })).transact((tx) => tx.sweepLock());
    if (!locked) {
      this.fail("take the sweep lock: another transaction holds it");
    }
  }

  sqlArg(value: JsonValue): string {
    const arg = object(value, "sqlArg", this.where);
    let id: string;
    if (str(arg.get("uuid")) !== "") {
      id = str(arg.get("uuid"));
    } else if (str(arg.get("ref")) !== "") {
      id = this.refID(str(arg.get("ref")));
    } else if (str(arg.get("commit")) !== "") {
      id = this.commitID(str(arg.get("commit")));
    } else {
      this.fail("an sql argument names a uuid, a ref or a commit");
    }
    return uuidHyphenated(id);
  }

  /** The name an earlier step bound a commit to, or its id. */
  commitName(id: string): string {
    for (const [name, commit] of this.commits) {
      if (commit.id === id) {
        return name;
      }
    }
    return "id:" + id;
  }

  refName(id: string): string {
    for (const [name, ref] of this.refs) {
      if (ref.id === id) {
        return name;
      }
    }
    return "id:" + id;
  }

  /** Compares an id with an expectation that names a ref or a commit, or is null for none. */
  checkName(what: string, id: string | null, want: JsonValue | undefined, name: (id: string) => string): void {
    if (want === undefined) {
      return;
    }
    if (want === null) {
      if (id !== null) {
        this.fail(`${what} is ${name(id)}, want none`);
      }
      return;
    }
    if (id === null || name(id) !== want) {
      this.fail(`${what} is ${JSON.stringify(id === null ? "" : name(id))}, want ${JSON.stringify(want)}`);
    }
  }

  checkRef(got: Ref, want: JsonObject): void {
    const version = num(want.get("version"));
    if (version !== undefined && got.version !== version) {
      this.fail(`ref version ${got.version}, want ${version}`);
    }
    const sealed = want.get("sealed");
    if (typeof sealed === "boolean" && got.sealed !== sealed) {
      this.fail(`ref sealed ${got.sealed}, want ${sealed}`);
    }
    const name = want.get("name");
    if (typeof name === "string" && got.name !== name) {
      this.fail(`ref name ${JSON.stringify(got.name)}, want ${JSON.stringify(name)}`);
    }
    this.checkName("the ref's parent", got.parent, want.get("parent"), (id) => this.refName(id));
    this.checkName("the ref's base", got.base, want.get("base"), (id) => this.commitName(id));
    this.checkName("the ref's head", got.head, want.get("head"), (id) => this.commitName(id));
  }

  checkCommit(got: Commit | null, raw: JsonValue): void {
    if (raw === null) {
      if (got !== null) {
        this.fail(`wrote commit ${got.id}, want none`);
      }
      return;
    }
    if (got === null) {
      this.fail("wrote no commit, want one");
    }
    const want = object(raw, "commitExpect", this.where);
    const ref = want.get("ref");
    if (typeof ref === "string" && this.refName(got.ref) !== ref) {
      this.fail(`commit ref ${JSON.stringify(this.refName(got.ref))}, want ${JSON.stringify(ref)}`);
    }
    this.checkName("the commit's parent", got.parent, want.get("parent"), (id) => this.commitName(id));
    const message = want.get("message");
    if (typeof message === "string" && got.message !== message) {
      this.fail(`commit message ${JSON.stringify(got.message)}, want ${JSON.stringify(message)}`);
    }
    const sequence = want.get("sequence");
    if (sequence !== undefined) {
      const gotSequence = got.sequence === null ? "null" : String(got.sequence);
      if (gotSequence !== stringifyJson(sequence)) {
        this.fail(`commit sequence ${gotSequence}, want ${stringifyJson(sequence)}`);
      }
    }
    const schemaEpoch = num(want.get("schemaEpoch"));
    if (schemaEpoch !== undefined && got.schemaEpoch !== schemaEpoch) {
      this.fail(`commit schema epoch ${got.schemaEpoch}, want ${schemaEpoch}`);
    }
    this.checkHash(got.contentHash, str(want.get("contentHash")), str(want.get("contentHashOf")));
  }

  checkHash(got: string, want: string, wantOf: string): void {
    if (want !== "" && got !== want) {
      this.fail(`content hash ${got}, want ${want}`);
    }
    if (wantOf !== "") {
      const commit = this.commits.get(wantOf);
      if (commit === undefined) {
        this.fail(`no commit is named ${JSON.stringify(wantOf)}`);
      }
      if (got !== commit.contentHash) {
        this.fail(`content hash ${got}, want ${wantOf}'s, ${commit.contentHash}`);
      }
    }
  }

  /**
   * Compares every kind of a tree with the expected rows: the same kinds, and
   * per kind the same number of rows in the same order, each with the listed
   * columns' values.
   */
  checkTree(what: string, got: Record<string, string[]>, wantValue: JsonValue): void {
    if (!isJsonObject(wantValue)) {
      this.fail(`${what} expectation is a JSON object`);
    }
    for (const kind of Object.keys(got)) {
      if (!wantValue.has(kind)) {
        this.fail(`${what} has ${kind} rows ${JSON.stringify(got[kind])}, and the step expects none`);
      }
    }
    for (const [kind, rows] of wantValue) {
      this.checkList(`${what} ${kind}`, (got[kind] ?? []).map(parseJson), rows);
    }
  }

  /** Compares a list of JSON objects with expected ones: the same length, and each object with the listed members' values. */
  checkList(what: string, got: JsonValue[], wantValue: JsonValue): void {
    if (!Array.isArray(wantValue)) {
      this.fail(`${what} expectation is a JSON array`);
    }
    if (got.length !== wantValue.length) {
      this.fail(`${what}: ${got.length}, want ${wantValue.length}: [${got.map(stringifyJson).join(",")}]`);
    }
    wantValue.forEach((wantItem, i) => {
      const item = got[i]!;
      if (!isJsonObject(item) || !isJsonObject(wantItem)) {
        this.fail(`${what}[${i}] is not an object`);
      }
      for (const [column, value] of wantItem) {
        const have = item.get(column);
        if (have === undefined && value === null) {
          continue;
        }
        if (have === undefined || !jsonEqual(have, value)) {
          this.fail(
            `${what}[${i}].${column} is ${have === undefined ? "absent" : stringifyJson(have)}, want ${stringifyJson(value)} (${stringifyJson(item)})`,
          );
        }
      }
    });
  }
}
