// Shared test setup: every test opens real SQLite files under a fresh
// temporary directory (not :memory:, so write-ahead logging behaves as it
// does in a deployment), and cleanup closes and removes them. The engine is
// the Rust engine, through its Node-API addon (D58): the test addon, which
// scripts/engine-napi.sh --js-behaviors builds into native/, whose bridge
// runs the behaviors the tests write in TypeScript. With ENGINE_RECORD set,
// the engines they open record the test's calls (recorder.ts). A test that
// loops over backends runs on SQLite, and with ENGINE_TEST_PG_URL set on
// Postgres too, through openEngineAsync, as a TypeScript server opens the
// engine there.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import {
  allowAll,
  openEngine as openDistEngine,
  openEngineAsync,
  type Engine,
  type EngineOptions,
  type InstanceFields,
  type InstanceRecord,
  type Principal,
  type Row,
  type SqlValue,
} from '../dist/index.js';
import { endCase, recordOpen, registerDirectory, type TestContextLike } from './recorder.ts';

/**
 * The SQLite module a test names, as the TypeScript engine took one: node
 * (node:sqlite) or bun (bun:sqlite). The engine has a SQLite of its own,
 * so the suite's loop over drivers runs once, under the name the corpus's
 * cases carry, and openEngine leaves the option out.
 */
export type DriverName = 'node' | 'bun';

/** What the tests open an engine with: the engine's options, and a driver name the engine does not take. */
export type TestEngineOptions = EngineOptions & { driver?: DriverName };

/** isBun reports whether the tests run on Bun. */
export function isBun(): boolean {
  return typeof (globalThis as { Bun?: unknown }).Bun !== 'undefined';
}

/** A reader of SQLite rows: engine.storage, or a connection of a test's own on the file. */
export interface SqliteReader {
  get(sql: string, params?: readonly SqlValue[]): Row | undefined;
  all(sql: string, params?: readonly SqlValue[]): Row[];
}

/** A row of the engine's migration ledger (engine_migrations). */
export interface AppliedMigration {
  owner: string;
  version: number;
  name: string;
  appliedAt: number;
}

/** appliedMigrations lists an owner's rows of the engine's migration ledger, in version order. */
export function appliedMigrations(storage: SqliteReader, owner: string): AppliedMigration[] {
  const ledger = storage.get("SELECT 1 AS present FROM sqlite_master WHERE type = 'table' AND name = 'engine_migrations'");
  if (!ledger) {
    return [];
  }
  return storage
    .all('SELECT owner, version, name, applied_at FROM engine_migrations WHERE owner = ? ORDER BY version', [owner])
    .map((row) => ({ owner: String(row.owner), version: Number(row.version), name: String(row.name), appliedAt: Number(row.applied_at) }));
}

/** The principal most tests act as; openTestEngine allows it everything. */
export const alice: Principal = { subject: 'alice', permissions: [] };

/** The suite's loop over SQLite modules: the engine's own SQLite, once (DriverName). */
export const drivers: DriverName[] = ['node'];

const directories: string[] = [];
const open: Array<{ close(): void }> = [];

/** freshPath returns a database path in a new temporary directory. */
export function freshPath(): string {
  const directory = mkdtempSync(join(tmpdir(), 'superschematic-engine-'));
  directories.push(directory);
  registerDirectory(directory);
  return join(directory, 'engine.db');
}

/** track closes item at cleanup. */
export function track<T extends { close(): void }>(item: T): T {
  open.push(item);
  return item;
}

/**
 * openEngine is the engine's openEngine, recording the engine's calls when
 * ENGINE_RECORD is set; tests open engines through it or openTestEngine.
 */
export function openEngine(options: TestEngineOptions): Engine {
  const { driver: _driver, ...engineOptions } = options;
  return recordOpen(engineOptions, openDistEngine);
}

export function openTestEngine(options: Partial<TestEngineOptions> = {}): Engine {
  return track(openEngine({ path: freshPath(), policy: allowAll, ...options }));
}

/**
 * A database a test runs on: SQLite, a file of its own as openTestEngine
 * opens it, or with ENGINE_TEST_PG_URL set a schema of its own of that
 * Postgres database, through openEngineAsync, as a
 * TypeScript server opens the engine there (D58). The engine open returns
 * is the API every test uses, whose every call on Postgres returns a
 * promise: a test that loops over backends awaits each call, which on
 * SQLite awaits a value. openEngineAsync takes its options as data, so a
 * test that passes a function (a behavior written in TypeScript, a policy,
 * ids) skips on Postgres, with its reason (unlessOn).
 */
export interface Backend {
  readonly name: 'sqlite' | 'postgres';
  open(options?: Partial<TestEngineOptions>): Promise<Engine>;
}

const postgresUrl = process.env.ENGINE_TEST_PG_URL || undefined;

/** The backends a test that loops over them runs on: SQLite, and Postgres when ENGINE_TEST_PG_URL names one. */
export const backends: Backend[] = [
  { name: 'sqlite', open: async (options = {}) => openTestEngine(options) },
  ...(postgresUrl
    ? [
        {
          name: 'postgres' as const,
          open: async ({ driver: _driver, ...options }: Partial<TestEngineOptions> = {}) =>
            track(
              await openEngineAsync({
                postgres: { url: postgresUrl, schema: `engine_test_${randomUUID().replaceAll('-', '').slice(0, 16)}` },
                policy: allowAll,
                ...options,
              })
            ) as unknown as Engine,
        },
      ]
    : []),
];

/** The skip of a test on a backend that cannot run it, with why; false where it runs. */
export function unlessOn(backend: Backend, name: Backend['name'], why: string): string | false {
  return backend.name === name && why;
}

/**
 * cleanup closes what the test opened and removes its files; register it
 * with afterEach, which passes the test's context, under which a
 * recording names the test's scenario.
 */
export function cleanup(context?: TestContextLike): void {
  try {
    // A recording the recorder refuses fails the test, after the cleanup.
    endCase(context);
  } finally {
    for (const item of open.splice(0)) {
      try {
        item.close();
      } catch {
        // Closed by the test.
      }
    }
    for (const directory of directories.splice(0)) {
      rmSync(directory, { recursive: true, force: true });
    }
  }
}

/** thrown runs fn, asserts it throws an instance of type, and returns the error. */
export function thrown<T extends Error>(fn: () => unknown, type: new (...args: never[]) => T): T {
  try {
    fn();
  } catch (error) {
    assert.ok(error instanceof type, `expected ${type.name}, got ${error instanceof Error ? `${error.name}: ${error.message}` : String(error)}`);
    return error;
  }
  assert.fail(`expected ${type.name} to be thrown`);
}

/**
 * fieldsOf is an instance's fields as a read returns them, its own in data
 * and its behaviors' in behaviors, without the rest of its record;
 * undefined for none.
 */
export function fieldsOf(record: InstanceRecord | undefined): InstanceFields | undefined {
  return record === undefined ? undefined : { data: record.data, behaviors: record.behaviors };
}

export type Field = Record<string, unknown> & { name: string; typeRef: Record<string, unknown> };

/**
 * schemaDocument builds a General schema whose instance type is named like
 * the schema, with extra top-level members (other types, enums, scalars).
 */
export function schemaDocument(name: string, fields: Field[], extra: Record<string, unknown> = {}): Record<string, unknown> {
  const { types, ...rest } = extra as { types?: Record<string, unknown> };
  return {
    kind: 'General',
    name,
    ...rest,
    types: { [name]: { name, role: 'EmbeddedStruct', fields }, ...(types ?? {}) },
  };
}

/** An order: the schema most tests use. */
export function orderDocument(): Record<string, unknown> {
  return schemaDocument(
    'Order',
    [
      { name: 'title', typeRef: { name: 'string' }, required: true },
      { name: 'quantity', typeRef: { name: 'Generic.Int64' } },
      { name: 'status', typeRef: { name: 'OrderStatus' } },
      { name: 'lines', typeRef: { name: 'OrderLine', isArray: true } },
    ],
    {
      enums: {
        OrderStatus: {
          name: 'OrderStatus',
          values: [
            { name: 'OPEN', serializedAs: 'open' },
            { name: 'SHIPPED', serializedAs: 'shipped' },
          ],
        },
      },
      types: {
        OrderLine: {
          name: 'OrderLine',
          role: 'EmbeddedStruct',
          fields: [
            { name: 'sku', typeRef: { name: 'string' }, required: true, validateMaxLength: 12 },
            { name: 'count', typeRef: { name: 'number' }, validateMin: 1 },
          ],
        },
      },
    }
  );
}

/** clone deep-copies a JSON document so a test can change it. */
export function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

/**
 * documentsDocument is the General schema-file document the core binary
 * loads with no extension linked (make cli-smoke, fixture-behaviors-json):
 * its Document type composes Workflow, Comments and Revisions, and the
 * transition from review to published needs documents.publish.
 */
export function documentsDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-behaviors-json/src/document.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * ticketsDocument is the General schema-file document the core binary
 * loads with no extension linked (fixture-display-json, which
 * `superschematic format --to=json` wrote from its TypeScript twin): its
 * Ticket type composes Workflow and Comments and declares @display over
 * its fields, its states and its transitions, with an icon, and its
 * fields' titles, icons and multiline hint; its Priority enum's members
 * take titles.
 */
export function ticketsDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-display-json/src/ticket.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * tasksDocument is the General schema-file document the core binary loads
 * with no extension linked beside documentsDocument (make cli-smoke,
 * fixture-cross-instance-json): its Task type composes Workflow,
 * Dependencies, whose blockers are tasks or documents and whose gated
 * state is done, and Links, with spec pinned to a document's revision, a
 * required parent task and an optional project.
 */
export function tasksDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-cross-instance-json/src/task.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * projectsDocument is the General schema-file document the core binary
 * loads with no extension linked beside tasksDocument (make cli-smoke,
 * fixture-rollups-json): its Project type composes Workflow, from active
 * to done or dropped, and Rollups over the tasks whose project link points
 * at it: how many, how many in each status, and whether every one is in a
 * terminal state, which done waits for.
 */
export function projectsDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-rollups-json/src/project.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * notesDocument is the General schema-file document the core binary loads
 * with no extension linked beside the others (make cli-smoke,
 * fixture-search-json): its Note type composes Search over its title and
 * body, the title weighing three times the body.
 */
export function notesDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-search-json/src/note.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * projectTreeDocument is the General schema-file document the core binary
 * loads with no extension linked beside the others (make cli-smoke,
 * fixture-reactions-json), a schema named projects as projectsDocument's
 * is: its Project type composes Workflow, whose failed state is a
 * failure, Links, with a parent project, and Reactions, whose rules start
 * a project's parent when the project starts, finish a project once every
 * project under it is done, and fail it when one of them fails.
 */
export function projectTreeDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-reactions-json/src/project.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * stepsDocument is the General schema-file document the core binary loads
 * with no extension linked beside the others (make cli-smoke,
 * fixture-variants-json), a schema named Step like its instance type,
 * which composes Constants, which keeps the kind its create gives it, and
 * Variants, which types its result by its kind: a VerifyResult (with its
 * list of Checks) for a verify step, a ReviewResult for a review, and
 * none for a note.
 */
export function stepsDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-variants-json/src/step.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * numberedInvoicesDocument is the General schema-file document the core binary
 * loads with no extension linked beside the others (make cli-smoke,
 * fixture-numbering-json): its Invoice type composes Links, with the
 * project every create gives, and Numbering, which numbers each invoice
 * at its create, INV-2026-0001 on, in its project's count, starting
 * again each UTC year.
 */
export function numberedInvoicesDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-numbering-json/src/invoice.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * recipesDocument is the General schema-file document the core binary
 * loads with no extension linked beside the others (make cli-smoke,
 * fixture-branches-json), a schema named Recipe like its instance type,
 * which composes Branches: each recipe is a version graph's root, with
 * steps ordered by position, ingredients under a step and at most one
 * cover, and the sweep on.
 */
export function recipesDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-branches-json/src/recipe.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * invoicesDocument is the General schema-file document the core binary
 * loads with no extension linked beside the others (make cli-smoke,
 * fixture-staterules-json): its Invoice type composes Workflow, from
 * draft to sent, then paid or void, and StateRules, under which sent
 * requires a customer, an amount and the lines and freezes them, and
 * paid and void freeze every field, but for a caller with
 * invoices.amend.
 */
export function invoicesDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-staterules-json/src/invoice.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * expensesDocument is the General schema-file document the core binary
 * loads with no extension linked beside the others (make cli-smoke,
 * fixture-approvals-json), a schema named expenses: its Expense type
 * composes Workflow, from draft to submitted, approved and paid, or
 * rejected, and Approvals, whose approved state waits for two sign-offs by
 * principals with expenses.approve, never the claimant's, and whose paid
 * state, from approved, for one with expenses.pay.
 */
export function expensesDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-approvals-json/src/expense.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}

/**
 * requestsDocument is the General schema-file document the core binary
 * loads with no extension linked beside the others (make cli-smoke,
 * fixture-timers-json), a schema named requests: its Request type composes
 * Workflow, from open to waiting, escalated or resolved, and Timers, whose
 * respond escalates a request open four hours, whose lapse resolves one
 * waiting a week, each cancelled when the request leaves that state, and
 * whose remind fires a day before visitAt, with requests.timers to reset
 * or snooze one.
 */
export function requestsDocument(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-timers-json/src/request.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}
