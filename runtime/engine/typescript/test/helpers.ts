// Shared test setup: every test opens real SQLite files under a fresh
// temporary directory (not :memory:, so write-ahead logging behaves as it
// does in a deployment), and cleanup closes and removes them.
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
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
