// The facade's conversions a generated facade over the fixture does not
// reach: fixture-version-graph-db's only to-one relations are the root and
// the ref, which the engine writes, so no generated upsert writes a
// relation. This facade's graph is built here over the fixture's
// descriptor, with the ingredient's step_key as a writable to-one relation
// to the step, keyed by the step's entity key, and the part's parent_key as
// one to its parent part. It also holds the base's arrange and orderKeys
// over the fixture's part, the kind a string key orders, and its
// entityHistory.
import { afterAll, beforeAll, expect, test } from "bun:test";
import { errorCode, orderKeys, VersionGraphFacade, type Descriptor, type FacadeGraph } from "../dist/facade.js";
import { pgPool } from "../dist/postgres.js";
import { descriptor, dsn, scratchSchema, type Scratch } from "./postgres.js";

interface Step {
  entityKey?: string;
  position: number;
  instruction: string;
  timings: Record<string, number>;
}

interface Ingredient {
  entityKey?: string;
  step: { entityKey: string } | null;
  quantity: string;
  substitutes: unknown;
}

interface Part {
  entityKey?: string;
  parent: { entityKey: string } | null;
  rank: string;
  heading: string;
}

interface Kinds {
  step: Step;
  ingredient: Ingredient;
  part: Part;
}

const graph: FacadeGraph<Kinds> = {
  descriptor: JSON.parse(descriptor) as Descriptor,
  schemaEpoch: 1,
  snapshotEvery: 3,
  historyActorSetting: "superschematic.history_actor_id",
  kinds: {
    step: {
      kind: "step",
      columns: [
        { column: "position", field: "position", write: true },
        { column: "instruction", field: "instruction", write: true },
        { column: "timings", field: "timings", write: true },
        { column: "entity_key", field: "entityKey", write: true },
      ],
      parse: (json) => json as Step,
    },
    ingredient: {
      kind: "ingredient",
      columns: [
        { column: "step_key", field: "step", relationKey: "entityKey", write: true },
        { column: "quantity", field: "quantity", write: true },
        { column: "substitutes", field: "substitutes", write: true },
        { column: "entity_key", field: "entityKey", write: true },
      ],
      parse: (json) => json as Ingredient,
    },
    part: {
      kind: "part",
      columns: [
        { column: "parent_key", field: "parent", relationKey: "entityKey", write: true },
        { column: "rank", field: "rank", write: true },
        { column: "heading", field: "heading", write: true },
        { column: "entity_key", field: "entityKey", write: true },
      ],
      parse: (json) => json as Part,
    },
  },
};

/** A facade that names, as its fourth type argument, the kinds a string key orders. */
class RecipeGraph extends VersionGraphFacade<Kinds, string, "ADD" | "UPDATE" | "DELETE", "part"> {}

const actor = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";
const root = "00000000-0000-4000-8000-000000000001";
const arrangedRoot = "00000000-0000-4000-8000-000000000002";
const historyRoot = "00000000-0000-4000-8000-000000000003";
let scratch: Scratch | undefined;

beforeAll(async () => {
  if (dsn === "") {
    return;
  }
  scratch = await scratchSchema("vg_facade_ts");
  await scratch.pool.query(
    "INSERT INTO recipe (id, title, created_by) VALUES ($1::uuid, 'Bread', $4::uuid), ($2::uuid, 'Rolls', $4::uuid), ($3::uuid, 'Buns', $4::uuid)",
    [root, arrangedRoot, historyRoot, actor],
  );
});

afterAll(async () => {
  await scratch?.close();
});

// An upsert writes a to-one relation as its target's key, and a read gives
// the relation back as an object holding that key.
test.skipIf(dsn === "")("the facade writes a to-one relation as its target's key", async () => {
  const pool = scratch!.pool;
  const g = new VersionGraphFacade(graph, pgPool(pool), { actor });
  const main = await g.createPrimary(root, "main");
  const draft = await g.branch(main.id, "flour");
  const withStep = await g.save(draft.id, draft.version, {
    step: { upsert: [{ position: 1, instruction: "Mix", timings: {} }] },
  });
  const stepKey = withStep.saved.step[0]!.entityKey!;
  const saved = await g.save(draft.id, withStep.ref.version, {
    ingredient: { upsert: [{ step: { entityKey: stepKey }, quantity: "500 g", substitutes: [] }] },
  });
  expect(saved.saved.ingredient.map((i) => i.step)).toEqual([{ entityKey: stepKey }]);

  const stored = await pool.query("SELECT step_key::text AS step_key FROM ingredient");
  const steps = await pool.query("SELECT entity_key::text AS entity_key FROM step");
  expect(stored.rows.map((r) => r.step_key)).toEqual(steps.rows.map((r) => r.entity_key));

  const tree = await g.compose(draft.id);
  expect(tree.findings).toEqual([]);
  expect(tree.ingredient.map((i) => [i.step, i.quantity])).toEqual([[{ entityKey: stepKey }, "500 g"]]);
});

// orderKeys makes the keys new siblings are saved with, and arrange gives a
// new key to the one child that moved: the rest keep theirs, so the ref
// gains one row's write.
test.skipIf(dsn === "")("the facade arranges a parent's children and makes order keys", async () => {
  const g = new RecipeGraph(graph, pgPool(scratch!.pool), { actor });
  const main = await g.createPrimary(arrangedRoot, "main");
  const draft = await g.branch(main.id, "parts");
  const ranks = await g.orderKeys(null, undefined, 3);
  expect(ranks).toEqual(["F", "V", "k"]);
  expect(await orderKeys("", "", 3)).toEqual(ranks);

  const top = await g.save(draft.id, draft.version, {
    part: { upsert: [{ parent: null, rank: ranks[1]!, heading: "The dough" }] },
  });
  const dough = top.saved.part[0]!.entityKey!;
  const children = await g.save(draft.id, top.ref.version, {
    part: {
      upsert: ["Mix", "Knead", "Rest"].map((heading, i) => ({ parent: { entityKey: dough }, rank: ranks[i]!, heading })),
    },
  });
  const [mix, knead, rest] = children.saved.part.map((p) => p.entityKey!);

  const arranged = await g.arrange(draft.id, children.ref.version, "part", dough, [rest!, mix!, knead!]);
  expect(arranged.ref.version).toBe(children.ref.version + 1);
  expect(arranged.saved.part.map((p) => [p.entityKey, p.heading, p.parent])).toEqual([[rest!, "Rest", { entityKey: dough }]]);
  const moved = arranged.saved.part[0]!.rank;
  expect(moved < ranks[0]!).toBe(true);
  expect(arranged.saved.step).toEqual([]);

  // The rows with no parent, as they stand: nothing is written, and the
  // ref's version moves.
  const still = await g.arrange(draft.id, arranged.ref.version, "part", null, [dough]);
  expect(still.ref.version).toBe(arranged.ref.version + 1);
  expect(still.saved.part).toEqual([]);

  const tree = await g.compose(draft.id);
  expect(tree.findings).toEqual([]);
  expect(tree.part.filter((p) => p.parent !== null).map((p) => [p.heading, p.rank])).toEqual([
    ["Rest", moved],
    ["Mix", ranks[0]!],
    ["Knead", ranks[1]!],
  ]);

  // A key between the moved row's and the next is where an insert goes.
  const [between] = await g.orderKeys(moved, ranks[0], 1);
  expect(moved < between! && between! < ranks[0]!).toBe(true);

  const code = (operation: Promise<unknown>) =>
    operation.then(
      () => "",
      (err: unknown) => errorCode(err),
    );
  expect(await code(g.arrange(draft.id, still.ref.version, "part", dough, [rest!, mix!]))).toBe("invalid_arrangement");
  expect(await code(g.arrange(draft.id, arranged.ref.version, "part", dough, [rest!, mix!, knead!]))).toBe("version_conflict");
  expect(await code(g.orderKeys(ranks[1], ranks[0], 1))).toBe("no_order_key");
  // @ts-expect-error arrange takes the kinds a string key orders; the core refuses another too.
  expect(await code(g.arrange(draft.id, still.ref.version, "step", null, []))).toBe("order_not_a_key");
});

// entityHistory lists the commits of the root's primary line that changed
// an entity, newest first, each with what it did; the change set's own
// commits, and another kind's entity of the same key, are not in it.
test.skipIf(dsn === "")("the facade lists an entity's history on its root's primary line", async () => {
  const g = new RecipeGraph(graph, pgPool(scratch!.pool), { actor });
  expect(await g.entityHistory(historyRoot, "part", "00000000-0000-4000-8000-00000000000a")).toEqual([]);
  let main = await g.createPrimary(historyRoot, "main");
  const merges = [];
  let key = "";
  for (const heading of ["Shape", "Shape the buns"]) {
    const draft = await g.branch(main.id, heading);
    const saved = await g.save(draft.id, draft.version, {
      part: { upsert: [{ ...(key === "" ? {} : { entityKey: key }), parent: null, rank: "V", heading }] },
    });
    key = saved.saved.part[0]!.entityKey!;
    await g.commit(draft.id, saved.ref.version);
    const merged = await g.merge(draft.id, main.id, main.version);
    main = merged.ref;
    merges.push(merged.commit!);
  }
  const history = await g.entityHistory(historyRoot, "part", key);
  expect(history.map((change) => [change.commit.id, change.operation])).toEqual([
    [merges[1]!.id, "UPDATE"],
    [merges[0]!.id, "ADD"],
  ]);
  // Each commit is the commit history gives: the line wrote both.
  expect(history.map((change) => change.commit)).toEqual(await g.history(main.id));
  expect(await g.entityHistory(historyRoot, "step", key)).toEqual([]);
  expect(await g.entityHistory(root, "part", key)).toEqual([]);
  await expect(g.entityHistory(historyRoot, "crumb", key)).rejects.toThrow('engine: unknown kind "crumb"');
});
