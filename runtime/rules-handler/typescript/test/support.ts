// What the package's tests share: the handler's module as dist/ holds it
// (bun run build puts it there), the checkout's paths, and a SQLite store
// over bun:sqlite, in memory or in a file of the test's own.
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { loadHandlerSync, SqliteStore, type HandlerModule } from '../dist/index.js';
import { openSqlite } from '../dist/sqlite.js';

/** The checkout's root. */
export const checkout = fileURLToPath(new URL('../../../../', import.meta.url));

/** The handler, from the module dist/ holds. */
export const handler: HandlerModule = loadHandlerSync();

/** A directory of the test's own, removed by the returned function. */
export function scratch(prefix: string): { dir: string; remove: () => void } {
  const dir = mkdtempSync(join(tmpdir(), `rules-handler-${prefix}-`));
  return { dir, remove: () => rmSync(dir, { recursive: true, force: true }) };
}

/** The development layout over bun:sqlite: a file, or ":memory:". */
export function sqlite(path = ':memory:'): SqliteStore {
  return new SqliteStore(openSqlite(path), handler.layout());
}

/** Whether two JSON values are one value: numbers by value, objects by member whatever their order, arrays in order. */
export function same(expected: unknown, actual: unknown): boolean {
  if (Array.isArray(expected)) {
    return Array.isArray(actual) && expected.length === actual.length && expected.every((item, i) => same(item, actual[i]));
  }
  if (expected !== null && typeof expected === 'object') {
    if (actual === null || typeof actual !== 'object' || Array.isArray(actual)) return false;
    const a = Object.entries(expected as Record<string, unknown>);
    const b = actual as Record<string, unknown>;
    return a.length === Object.keys(b).length && a.every(([name, value]) => name in b && same(value, b[name]));
  }
  return expected === actual;
}
