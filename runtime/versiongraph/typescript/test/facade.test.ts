// The facade's conversions a generated facade over the fixture does not
// reach: fixture-version-graph-db's only to-one relations are the root and
// the ref, which the engine writes, so no generated upsert writes a
// relation. This facade's graph is built here over the fixture's
// descriptor, with the ingredient's step_key as a writable to-one relation
// to the step, keyed by the step's entity key.
import { afterAll, beforeAll, expect, test } from "bun:test";
import { VersionGraphFacade, type Descriptor, type FacadeGraph } from "../dist/facade.js";
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

interface Kinds {
  step: Step;
  ingredient: Ingredient;
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
  },
};

const actor = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";
const root = "00000000-0000-4000-8000-000000000001";
let scratch: Scratch | undefined;

beforeAll(async () => {
  if (dsn === "") {
    return;
  }
  scratch = await scratchSchema("vg_facade_ts");
  await scratch.pool.query("INSERT INTO recipe (id, title, created_by) VALUES ($1::uuid, 'Bread', $2::uuid)", [
    root,
    actor,
  ]);
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
