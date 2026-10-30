import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  RulesRefusal,
  execute,
  readRow,
  type CoreAnswered,
  type CoreCommand,
  type CoreRefusal,
  type CoreTypeSpec,
  type RulesColumn,
  type RulesLayout,
  type RulesOwnedField,
  type RulesTable,
  type RulesUse,
} from './rules';
import { bumpVersion, columnTypes, core, packageCore, postgresDatabase, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';

// The recorded scenarios are the specification (D103): the transcript
// superschematic-rules-testkit writes from core-workflow.json
// (runtime/engine/testdata/rules/core-workflow.transcript.json, whose
// format its transcript module states) replayed through this adapter on
// bun:sqlite and on Postgres. Each scenario gets a table per type and
// database, its own fields' columns beside the owned columns the layout
// rule gives each use of any version and _version; each check runs on the
// core alone, answering whether the type has rows from its table; each
// command runs through execute in a transaction with the version live at
// it, its refusal compared whole and the row it names read back after it
// and compared; and each raw step writes owned columns with SQL past the
// adapter. The Postgres replay is skipped without
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.
//
// The tables here follow the layout rule as the core's README states it,
// restated by this test (the corpus's uses are type-anchored: Workflow's
// workflow_status and workflow_entered_at). The layout the generator
// writes (internal/generator/behaviorlayout) is held by the generated
// packages' own end-to-end test, not by this one.

/** The steps the transcript holds, the testkit's floor (tests/transcript.rs): each dialect replays every one, so a regression cannot pass by skipping. */
const FLOOR = 96;

interface Expect {
  readonly refused?: CoreRefusal;
  readonly result?: unknown;
  readonly key?: string;
  readonly row?: { readonly own: Record<string, unknown>; readonly owned: Record<string, Record<string, unknown>> };
}

type Step =
  | { kind: 'command'; step: number; call: string; database: string; type: string; version: number; command: CoreCommand; expect: Expect }
  | { kind: 'check'; step: number; call: string; database: string; spec: CoreTypeSpec; command: CoreCommand; expect: Expect }
  | { kind: 'raw'; step: number; call: string; database: string; type: string; key: string; owned: Record<string, Record<string, unknown>> };

interface Scenario {
  readonly name: string;
  readonly types: readonly { name: string; fields: readonly { name: string; type: string; required?: boolean }[]; versions: readonly CoreTypeSpec[] }[];
  readonly steps: readonly Step[];
  readonly skipped: readonly { step: number; call: string; reason: string }[];
}

interface Transcript {
  readonly file: string;
  readonly behaviors: readonly string[];
  readonly scenarios: readonly Scenario[];
}

const transcript = JSON.parse(readFileSync(new URL('../../../engine/testdata/rules/core-workflow.transcript.json', import.meta.url), 'utf8')) as Transcript;

/** A behavior's storage key: its name lowercased, each run of other characters one _ (the README's layout rule). */
function storageKey(behavior: string): string {
  return behavior.toLowerCase().replace(/[^a-z0-9]+/g, '_');
}

/**
 * A field's name in snake case as the generators write it (codegen.ToSnakeCase,
 * the core README's layout rule): a `_` before each capital that follows a
 * lowercase letter or a digit, each `-` a `_`, the whole lowercased. enteredAt
 * is entered_at, lastIPAddress last_ipaddress.
 */
function snake(name: string): string {
  return name
    .replaceAll('-', '_')
    .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
    .toLowerCase();
}

/** A column's name in camel case, the IR field name a type anchor's owned column takes here. */
function camel(column: string): string {
  return column.replace(/_([a-z0-9])/g, (_, c: string) => c.toUpperCase());
}

/** The IR type of an own field of a JSON type. */
function irType(jsonType: string): string {
  switch (jsonType) {
    case 'string':
    case 'number':
    case 'boolean':
      return jsonType;
    case 'integer':
      return 'number';
    default:
      return 'Generic.JSON';
  }
}

/** The SQL type of a column of an IR type in a dialect. */
function sqlType(dialect: 'sqlite' | 'postgres', column: RulesColumn): string {
  const t = columnTypes(dialect);
  if (column.convert === 'epochMillis') return t.timestamp;
  switch (column.type) {
    case 'string':
      return t.text;
    case 'number':
      return t.number;
    case 'boolean':
      return t.boolean;
    default:
      return t.json;
  }
}

/** The uses of a type across its versions, each with the owned columns the layout rule gives it and its declared operations. */
function usesOf(type: Scenario['types'][number]): RulesUse[] {
  const declarations = core().declarations();
  const uses = new Map<string, RulesUse>();
  for (const version of type.versions) {
    for (const use of version.uses ?? []) {
      const anchor = use.anchor === undefined || use.anchor === 'type' ? undefined : use.anchor.field;
      const id = anchor ?? use.behavior;
      if (uses.has(id)) continue;
      const declaration = declarations.find(d => d.name === use.behavior);
      if (declaration === undefined) throw new Error(`the core declares no behavior ${use.behavior}`);
      const fields: RulesOwnedField[] = (declaration.fields ?? [])
        .filter(field => field.type !== undefined)
        .map(field => {
          const value = anchor !== undefined && field.name === declaration.value;
          const column = anchor === undefined ? `${storageKey(use.behavior)}_${snake(field.name)}` : value ? snake(anchor) : `${snake(anchor)}_${snake(field.name)}`;
          const convert = field.type === 'Temporal.DateTime' ? ({ convert: 'epochMillis' } as const) : {};
          return { name: field.name, field: camel(column), column, type: field.type!, ...(value ? { value: true } : {}), ...convert };
        });
      const operations = (declaration.operations ?? []).map(operation => ({ name: operation.name, writes: operation.writes === true }));
      uses.set(id, { id, behavior: use.behavior, ...(anchor !== undefined ? { anchor } : {}), fields, operations });
    }
  }
  return [...uses.values()];
}

const dialects: { name: string; skip: boolean; open: () => Promise<TestDatabase> }[] = [
  { name: 'bun:sqlite', skip: packageCore === undefined, open: async () => sqliteDatabase() },
  { name: 'Postgres', skip: packageCore === undefined || rulesDsn === '', open: postgresDatabase },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(`${transcript.file}'s transcript on ${dialect.name}`, () => {
    let db: TestDatabase;
    const counts = { command: 0, check: 0, raw: 0 };
    let skipped = 0;

    beforeAll(async () => {
      db = await dialect.open();
    });
    afterAll(async () => db?.close());

    transcript.scenarios.forEach((scenario, index) => {
      test(scenario.name, async () => {
        skipped += scenario.skipped.length;
        const databases = [...new Set(scenario.steps.map(step => step.database))];
        const created = new Set<string>();
        const p = (n: number) => (db.dialect === 'postgres' ? `$${n}` : `?${n}`);

        /** The layout of a type's table in one of the scenario's databases, with a version's spec. */
        const tableOf = (database: string, typeName: string, spec?: CoreTypeSpec): RulesTable => {
          const type = scenario.types.find(t => t.name === typeName);
          const name = `s${index}_d${databases.indexOf(database)}_${snake(typeName)}`;
          return {
            type: typeName,
            table: name,
            key: { field: 'id', column: 'id', type: 'string' },
            version: '_version',
            own: (type?.fields ?? []).map(field => ({ field: field.name, column: field.name, type: irType(field.type) })),
            uses: type ? usesOf(type) : [],
            spec: spec ?? type?.versions[0] ?? { name: typeName, uses: [] },
          };
        };

        /** The table, created the first time a step needs it. */
        const ensure = async (table: RulesTable): Promise<void> => {
          if (created.has(table.table)) return;
          created.add(table.table);
          const t = columnTypes(db.dialect);
          const columns = [`id ${t.key}`, ...[...table.own, ...table.uses.flatMap(use => use.fields)].map(column => `"${column.column}" ${sqlType(db.dialect, column)}`), `_version ${t.version}`];
          await db.exec(`CREATE TABLE "${table.table}" (${columns.join(', ')})`);
          if (db.dialect === 'postgres') for (const ddl of bumpVersion(table.table)) await db.exec(ddl);
        };

        const mismatches: { step: number; call: string; what: string; expected: unknown; actual: unknown }[] = [];
        const compare = (step: Step, what: string, expected: unknown, actual: unknown) => {
          if (!Bun.deepEquals(actual, expected, true)) mismatches.push({ step: step.step, call: step.call, what, expected, actual });
        };

        for (const step of scenario.steps) {
          counts[step.kind]++;
          if (step.kind === 'check') {
            // On the core alone: a publish asks whether the type has rows,
            // which its table in the step's database answers.
            const table = tableOf(step.database, step.spec.name);
            await ensure(table);
            const reads: CoreAnswered[] = [];
            let refused: CoreRefusal | undefined;
            for (let round = 0; ; round++) {
              if (round > 16) throw new Error(`step ${step.step}: the check never settled`);
              const run = core().run(step.spec, step.command, { reads });
              if ('done' in run) {
                refused = 'refused' in run.done ? run.done.refused : undefined;
                break;
              }
              if (!('read' in run)) throw new Error(`step ${step.step}: a check asked for ${JSON.stringify(run)}`);
              for (const request of run.read) {
                if (request.kind !== 'hasRows') throw new Error(`step ${step.step}: a check asked for ${JSON.stringify(request)}`);
                const rows = await db.rows(`SELECT 1 AS one FROM "${tableOf(step.database, request.type).table}" LIMIT 1`);
                reads.push({ request, answer: { hasRows: rows.length > 0 }, held: 0 });
              }
            }
            compare(step, 'refused', step.expect.refused, refused);
            continue;
          }
          const type = scenario.types.find(t => t.name === step.type)!;
          if (step.kind === 'raw') {
            // Past the adapter, as an operator's repair or the test's own
            // SQL: the escape hatch D103 keeps open.
            const table = tableOf(step.database, step.type);
            await ensure(table);
            const sets: string[] = [];
            const values: unknown[] = [];
            for (const [useId, fields] of Object.entries(step.owned)) {
              const use = table.uses.find(u => u.id === useId)!;
              for (const [name, value] of Object.entries(fields)) {
                const field = use.fields.find(f => f.name === name)!;
                values.push(value === null ? null : field.convert === 'epochMillis' ? (db.dialect === 'postgres' ? new Date(value as number) : new Date(value as number).toISOString()) : value);
                sets.push(`"${field.column}" = ${p(values.length)}`);
              }
            }
            values.push(step.key);
            compare(step, 'raw.changes', 1, await db.exec(`UPDATE "${table.table}" SET ${sets.join(', ')} WHERE id = ${p(values.length)}`, values));
            continue;
          }
          const table = tableOf(step.database, step.type, type.versions[step.version]);
          await ensure(table);
          const layout: RulesLayout = { types: { [step.type]: table } };
          let refused: CoreRefusal | undefined;
          let result: unknown = null;
          let key: string | null = null;
          try {
            const applied = await db.transaction(store => execute({ core: core(), layout, store }, step.type, step.command));
            result = applied.result;
            key = applied.key;
          } catch (error) {
            if (!(error instanceof RulesRefusal)) throw error;
            refused = error.refusal;
          }
          compare(step, 'refused', step.expect.refused, refused);
          compare(step, 'result', step.expect.result ?? null, refused ? null : result);
          if (step.expect.key !== undefined) compare(step, 'key', step.expect.key, key);
          const action = step.command.action;
          const rowKey = action.kind === 'create' ? action.id : 'key' in action ? action.key : undefined;
          if (rowKey) {
            const row = await db.transaction(store => readRow({ core: core(), layout, store }, step.type, rowKey));
            compare(step, 'row', step.expect.row, row === null ? undefined : { own: row.own, owned: row.owned });
          }
        }
        expect(mismatches).toEqual([]);
      });
    });

    test('replays every step the transcript holds, at least the floor', () => {
      const replayed = counts.command + counts.check + counts.raw;
      console.log(
        `${transcript.file} on ${dialect.name}: ${transcript.scenarios.length} scenarios, ${replayed} steps replayed (${counts.command} commands, ${counts.check} checks, ${counts.raw} raw writes), ${skipped} left out by the transcript with its reasons`
      );
      expect(replayed).toBeGreaterThanOrEqual(FLOOR);
    });
  });
}
