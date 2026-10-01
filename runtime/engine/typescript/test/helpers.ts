// Shared test setup: every test opens real SQLite files under a fresh
// temporary directory (not :memory:, so write-ahead logging behaves as it
// does in a deployment), and cleanup closes and removes them.
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { allowAll, isBun, openEngine, type DriverName, type Engine, type EngineOptions, type Principal } from '../dist/index.js';

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
  return join(directory, 'engine.db');
}

/** track closes item at cleanup. */
export function track<T extends { close(): void }>(item: T): T {
  open.push(item);
  return item;
}

export function openTestEngine(options: Partial<EngineOptions> = {}): Engine {
  return track(openEngine({ path: freshPath(), policy: allowAll, ...options }));
}

/** cleanup closes what the test opened and removes its files; register it with afterEach. */
export function cleanup(): void {
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
