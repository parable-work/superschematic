// Shared test setup: every test opens real SQLite files under a fresh
// temporary directory (not :memory:, so write-ahead logging behaves as it
// does in a deployment), and cleanup closes and removes them. With
// ENGINE_RECORD set, the engines they open record the test's calls
// (recorder.ts). With ENGINE_IMPL=rust they are the Rust engine, through
// its Node-API addon (D58; scripts/engine-napi.sh builds it, and
// scripts/rust-ratchet.mjs runs the suite so against rust-pending.txt).
// A test that loops over backends runs on SQLite, and with ENGINE_IMPL=rust
// and ENGINE_TEST_PG_URL set on Postgres too, through openEngineAsync, as a
// TypeScript server opens the engine there.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import {
  allowAll,
  isBun,
  openEngine as openDistEngine,
  openEngineAsync,
  type DriverName,
  type Engine,
  type EngineOptions,
  type InstanceFields,
  type InstanceRecord,
  type Principal,
} from '../dist/index.js';
import { openNativeEngine } from '../dist/native/index.js';
import { endCase, recordOpen, registerDirectory, type TestContextLike } from './recorder.ts';

/**
 * The engine the tests open: the TypeScript engine, or with
 * ENGINE_IMPL=rust the Rust engine, behind the same API.
 */
export const engineImpl: 'typescript' | 'rust' = implementation(process.env.ENGINE_IMPL);

function implementation(given: string | undefined): 'typescript' | 'rust' {
  if (given === undefined || given === '' || given === 'typescript') {
    return 'typescript';
  }
  if (given === 'rust') {
    return 'rust';
  }
  throw new Error(`ENGINE_IMPL is typescript (the default) or rust, got ${given}`);
}

/**
 * Why a test that passes a value driver written in TypeScript
 * (values.driver) skips on the Rust engine: the TypeScript engine's alone,
 * dropped at the cutover (D58), since the Rust engine's drivers are Rust
 * ValueDrivers compiled into it, which its own tests hold
 * (runtime/engine/rust/engine/tests/values.rs).
 */
export const TYPESCRIPT_DRIVER = engineImpl === 'rust' && 'a value driver written in TypeScript (values.driver), dropped at the cutover (D58)';

const openImpl: (options: EngineOptions) => Engine = engineImpl === 'rust' ? (options) => openNativeEngine(options) as unknown as Engine : openDistEngine;

/** The principal most tests act as; openTestEngine allows it everything. */
export const alice: Principal = { subject: 'alice', permissions: [] };

/** The adapters this runtime opens: node:sqlite everywhere, bun:sqlite too on Bun. */
export const drivers: DriverName[] = isBun() ? ['bun', 'node'] : ['node'];

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
 * openEngine is the engine's openEngine (the Rust engine's with
 * ENGINE_IMPL=rust), recording the engine's calls when ENGINE_RECORD is
 * set; tests open engines through it or openTestEngine.
 */
export function openEngine(options: EngineOptions): Engine {
  return recordOpen(options, openImpl);
}

export function openTestEngine(options: Partial<EngineOptions> = {}): Engine {
  return track(openEngine({ path: freshPath(), policy: allowAll, ...options }));
}

/**
 * A database a test runs on: SQLite, a file of its own as openTestEngine
 * opens it, or with ENGINE_IMPL=rust and ENGINE_TEST_PG_URL set a schema of
 * its own of that Postgres database, through openEngineAsync, as a
 * TypeScript server opens the engine there (D58). The engine open returns
 * is the API every test uses, whose every call on Postgres returns a
 * promise: a test that loops over backends awaits each call, which on
 * SQLite awaits a value. openEngineAsync takes its options as data, so a
 * test that passes a function (a behavior written in TypeScript, a policy,
 * ids) skips on Postgres, with its reason (unlessOn).
 */
export interface Backend {
  readonly name: 'sqlite' | 'postgres';
  open(options?: Partial<EngineOptions>): Promise<Engine>;
}

const postgresUrl = engineImpl === 'rust' ? process.env.ENGINE_TEST_PG_URL : undefined;

/** The backends a test that loops over them runs on: SQLite, and Postgres when ENGINE_TEST_PG_URL names one on the Rust engine. */
export const backends: Backend[] = [
  { name: 'sqlite', open: async (options = {}) => openTestEngine(options) },
  ...(postgresUrl
    ? [
        {
          name: 'postgres' as const,
          open: async (options: Partial<EngineOptions> = {}) =>
            track(
              await openEngineAsync({
                postgres: { url: postgresUrl, schema: `engine_test_${randomUUID().replaceAll('-', '').slice(0, 16)}` },
                policy: allowAll,
                ...(options as object),
              } as never)
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
 * its fields, its states and its transitions, and its fields' titles and
 * icons.
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
