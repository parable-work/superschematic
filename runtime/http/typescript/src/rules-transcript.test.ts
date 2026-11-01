import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { existsSync, readFileSync } from 'node:fs';
import {
  RulesRefusal,
  execute,
  readArmedDeadlines,
  readRow,
  readSubRows,
  type ArmedDeadline,
  type CoreAnswered,
  type CoreCommand,
  type CoreRefusal,
  type CoreSubRow,
  type CoreTypeSpec,
  type RulesColumn,
  type RulesDeadlines,
  type RulesHost,
  type RulesLayout,
  type RulesOwnedField,
  type RulesSubTable,
  type RulesTable,
  type RulesUse,
} from './rules';
import { bumpVersion, columnTypes, core, packageCore, postgresDatabase, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';

// The recorded scenarios are the specification (D103): each transcript
// superschematic-rules-testkit writes from a recording
// (runtime/engine/testdata/rules/<file>.transcript.json, whose format its
// transcript module states) replayed through this adapter on bun:sqlite
// and on Postgres: core-workflow's, and core-timers', whose uses keep
// sub-tables and arm deadlines. Each scenario gets a table per type and
// database, its own fields' columns beside the owned columns the layout
// rule gives each use of any version and _version, and, for a use whose
// behavior declares tables, each sub-table, and for a type whose
// behaviors arm deadlines, its deadline queue; each check runs on the core
// alone, answering whether the type has rows from its table; each command
// runs through execute in a transaction with the version live at it, its
// refusal compared whole and the row it names read back after it and
// compared, with the row's sub-table rows and armed deadlines; a deadline
// is a command whose action is deadline, which the adapter runs only
// while its arming is a live message of the queue; and each raw step
// writes owned columns with SQL past the adapter. The Postgres replay is
// skipped without SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.
//
// The tables here follow the layout rule as the core's README states it,
// restated by this test (the corpus's uses are type-anchored: Workflow's
// workflow_status and workflow_entered_at, Timers' sub-table
// <table>_timers_deadlines). The layout the generator writes
// (internal/generator/behaviorlayout) is held by the generated packages'
// own end-to-end tests, not by this one.

/** The steps core-timers' transcript holds, as the core's milestone states them. */
const TIMERS_FLOOR = 101;

/**
 * The transcripts, each with the steps it holds, the testkit's floor
 * (tests/transcript.rs): each dialect replays every one, so a regression
 * cannot pass by skipping.
 */
const TRANSCRIPTS: readonly { readonly file: string; readonly floor: number }[] = [
  { file: 'core-workflow', floor: 96 },
  { file: 'core-timers', floor: TIMERS_FLOOR },
];

interface Expect {
  readonly refused?: CoreRefusal;
  readonly result?: unknown;
  readonly key?: string;
  readonly row?: { readonly own: Record<string, unknown>; readonly owned: Record<string, Record<string, unknown>> };
  /** The row's sub-table rows after the step, by use id and table, each table's in key order; absent for none. */
  readonly subRows?: Record<string, Record<string, CoreSubRow[]>>;
  /** The row's armed deadlines after the step, by use id then name; absent for none. */
  readonly deadlines?: ArmedDeadline[];
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

/** A transcript as the testkit wrote it. */
function transcriptOf(file: string): Transcript {
  const url = new URL(`../../../engine/testdata/rules/${file}.transcript.json`, import.meta.url);
  if (!existsSync(url)) throw new Error(`rules: ${url.pathname} is missing; superschematic-rules-testkit writes it (UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test transcript)`);
  return JSON.parse(readFileSync(url, 'utf8')) as Transcript;
}

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

/** A name in camel case from a storage key: the member a type anchor's operations sit under (workflow, timers). */
function camelOfKey(key: string): string {
  return key.replace(/_([a-z0-9])/g, (_, c: string) => c.toUpperCase());
}

/** A declared field as the column the layout rule gives it in a sub-table: its name in snake case, epoch milliseconds for a date-time. */
function subField(field: { name: string; type?: string }): RulesOwnedField {
  const convert = field.type === 'Temporal.DateTime' ? ({ convert: 'epochMillis' } as const) : {};
  return { name: field.name, field: field.name, column: snake(field.name), type: field.type ?? 'string', ...convert };
}

/**
 * The uses of a type across its versions, each with the owned columns the
 * layout rule gives it, its declared operations, and its sub-tables, each
 * <the type's table>_<member in snake case>_<table in snake case> with
 * <type in snake case>_id; and whether any arms deadlines.
 */
function usesOf(type: Scenario['types'][number], table: string): { uses: RulesUse[]; armsDeadlines: boolean } {
  const declarations = core().declarations();
  const uses = new Map<string, RulesUse>();
  let armsDeadlines = false;
  for (const version of type.versions) {
    for (const use of version.uses ?? []) {
      const anchor = use.anchor === undefined || use.anchor === 'type' ? undefined : use.anchor.field;
      const id = anchor ?? use.behavior;
      if (uses.has(id)) continue;
      const declaration = declarations.find(d => d.name === use.behavior);
      if (declaration === undefined) throw new Error(`the core declares no behavior ${use.behavior}`);
      armsDeadlines ||= declaration.armsDeadlines === true;
      const member = anchor ?? camelOfKey(storageKey(use.behavior));
      const fields: RulesOwnedField[] = (declaration.fields ?? [])
        .filter(field => field.type !== undefined)
        .map(field => {
          const value = anchor !== undefined && field.name === declaration.value;
          const column = anchor === undefined ? `${storageKey(use.behavior)}_${snake(field.name)}` : value ? snake(anchor) : `${snake(anchor)}_${snake(field.name)}`;
          const convert = field.type === 'Temporal.DateTime' ? ({ convert: 'epochMillis' } as const) : {};
          return { name: field.name, field: camel(column), column, type: field.type!, ...(value ? { value: true } : {}), ...convert };
        });
      const operations = (declaration.operations ?? []).map(operation => ({ name: operation.name, writes: operation.writes === true }));
      const tables: RulesSubTable[] = (declaration.tables ?? []).map(declared => {
        const key = declared.fields.find(field => field.name === declared.key);
        if (key === undefined) throw new Error(`behavior ${use.behavior}'s table ${declared.name} has no key field ${declared.key}`);
        return {
          name: declared.name,
          table: `${table}_${snake(member)}_${snake(declared.name)}`,
          row: `${snake(type.name)}_id`,
          key: subField(key),
          fields: declared.fields.filter(field => field.name !== declared.key).map(subField),
        };
      });
      uses.set(id, { id, behavior: use.behavior, member, ...(anchor !== undefined ? { anchor } : {}), fields, operations, ...(tables.length > 0 ? { tables } : {}) });
    }
  }
  return { uses: [...uses.values()], armsDeadlines };
}

/** SQLite's default of a UUID column, sqlmigrate's sqliteUUIDDefault: a version 4 UUID in its lowercase text. */
const SQLITE_UUID =
  "(lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))))";

const dialects: { name: string; skip: boolean; open: () => Promise<TestDatabase> }[] = [
  { name: 'bun:sqlite', skip: packageCore === undefined, open: async () => sqliteDatabase() },
  { name: 'Postgres', skip: packageCore === undefined || rulesDsn === '', open: postgresDatabase },
];

for (const { file, floor } of TRANSCRIPTS) {
  for (const dialect of dialects) {
    describe.skipIf(dialect.skip)(`${file}'s transcript on ${dialect.name}`, () => {
      const transcript = transcriptOf(file);
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
            const { uses, armsDeadlines } = type ? usesOf(type, name) : { uses: [], armsDeadlines: false };
            const deadlines: RulesDeadlines | undefined = armsDeadlines
              ? { queue: `${typeName}Deadline`, table: `${name}_deadline_queue`, rowKey: 'row_key', use: 'use_id', name: 'name', at: 'at', payload: 'payload' }
              : undefined;
            return {
              type: typeName,
              table: name,
              key: { field: 'id', column: 'id', type: 'string' },
              version: '_version',
              own: (type?.fields ?? []).map(field => ({ field: field.name, column: field.name, type: irType(field.type) })),
              uses,
              ...(deadlines ? { deadlines } : {}),
              spec: spec ?? type?.versions[0] ?? { name: typeName, uses: [] },
            };
          };

          /** The table, its uses' sub-tables and its deadline queue, created the first time a step needs them. */
          const ensure = async (table: RulesTable): Promise<void> => {
            if (created.has(table.table)) return;
            created.add(table.table);
            const pg = db.dialect === 'postgres';
            const t = columnTypes(db.dialect);
            const columns = [`id ${t.key}`, ...[...table.own, ...table.uses.flatMap(use => use.fields)].map(column => `"${column.column}" ${sqlType(db.dialect, column)}`), `_version ${t.version}`];
            await db.exec(`CREATE TABLE "${table.table}" (${columns.join(', ')})`);
            if (pg) for (const ddl of bumpVersion(table.table)) await db.exec(ddl);
            for (const sub of table.uses.flatMap(use => use.tables ?? [])) {
              const subColumns = [
                `"${sub.row}" ${t.text} NOT NULL REFERENCES "${table.table}" (id) ON DELETE CASCADE`,
                `"${sub.key.column}" ${t.text} NOT NULL`,
                ...sub.fields.map(field => `"${field.column}" ${sqlType(db.dialect, field)}`),
                `PRIMARY KEY ("${sub.row}", "${sub.key.column}")`,
              ];
              await db.exec(`CREATE TABLE "${sub.table}" (${subColumns.join(', ')})`);
            }
            const q = table.deadlines;
            if (q !== undefined) {
              const now = pg ? 'TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP' : "TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))";
              await db.exec(`CREATE TABLE "${q.table}" (
    id ${pg ? 'UUID DEFAULT gen_random_uuid()' : `TEXT DEFAULT ${SQLITE_UUID}`} NOT NULL PRIMARY KEY,
    "${q.rowKey}" TEXT NOT NULL,
    "${q.use}" TEXT NOT NULL,
    "${q.name}" TEXT NOT NULL,
    "${q.at}" ${t.integer} NOT NULL,
    "${q.payload}" ${t.json},
    state TEXT DEFAULT 'ready' NOT NULL,
    attempts INTEGER DEFAULT 0 NOT NULL,
    due_at ${now} NOT NULL,
    claim_expires_at ${t.timestamp},
    claim_token ${pg ? 'UUID' : 'TEXT'},
    last_error TEXT,
    created_at ${now} NOT NULL,
    updated_at ${now} NOT NULL
  )`);
              await db.exec(`CREATE UNIQUE INDEX "${q.table}_live" ON "${q.table}" ("${q.rowKey}", "${q.use}", "${q.name}") WHERE state IN ('ready', 'claimed')`);
            }
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
              const host = (store: RulesHost['store']): RulesHost => ({ core: core(), layout, store });
              const row = await db.transaction(store => readRow(host(store), step.type, rowKey));
              compare(step, 'row', step.expect.row, row === null ? undefined : { own: row.own, owned: row.owned });
              // The row's sub-table rows, by use id and table, each absent when
              // it has none, and its armed deadlines, absent when none.
              const subRows: Record<string, Record<string, CoreSubRow[]>> = {};
              for (const use of table.uses) {
                for (const sub of use.tables ?? []) {
                  const rows = await db.transaction(store => readSubRows(host(store), step.type, rowKey, use.id, sub.name));
                  if (rows.length > 0) (subRows[use.id] ??= {})[sub.name] = rows;
                }
              }
              compare(step, 'subRows', step.expect.subRows, Object.keys(subRows).length > 0 ? subRows : undefined);
              if (table.deadlines !== undefined) {
                const armed = await db.transaction(store => readArmedDeadlines(host(store), step.type, rowKey));
                compare(step, 'deadlines', step.expect.deadlines, armed.length > 0 ? armed : undefined);
              } else {
                compare(step, 'deadlines', step.expect.deadlines, undefined);
              }
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
        expect(replayed).toBeGreaterThanOrEqual(floor);
      });
    });
  }
}
