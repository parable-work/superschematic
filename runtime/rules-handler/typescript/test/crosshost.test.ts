// A file one host wrote, the other reads (D117): superschematic-dev, the
// Rust host (runtime/engine/rust/rules-dev, its server feature), serves a
// SQLite file this host then opens, and this host writes a file
// superschematic-dev then serves. Each reads the other's rows, versions,
// sub-table rows, deadlines and shared tables' rows, answers as the writer
// answered, and writes on. Both files hold the same tables.
//
// The binary is built here whenever cargo is on PATH (a build of crates
// that have not changed is cargo's to skip); SUPERSCHEMATIC_DEV_BIN names
// one built elsewhere. Without either the tests say so and pass.
import { afterAll, beforeAll, expect, test } from 'bun:test';
import { Database } from 'bun:sqlite';
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { delimiter, join } from 'node:path';
import type { Caller, Dev, SqliteStore } from '../dist/index.js';
import { openDev } from '../dist/sqlite.js';
import { checkout, scratch } from './support.ts';

const workspace = join(checkout, 'runtime/engine/rust');
const onPath = (tool: string): boolean => (process.env.PATH ?? '').split(delimiter).some(dir => dir !== '' && existsSync(join(dir, tool)));

function binary(): string | undefined {
  const named = process.env.SUPERSCHEMATIC_DEV_BIN;
  if (named) return named;
  const built = join(process.env.CARGO_TARGET_DIR || join(workspace, 'target'), 'debug', 'superschematic-dev');
  if (onPath('cargo')) {
    const rustVersion = readFileSync(join(checkout, 'tools.env'), 'utf8').match(/^RUST_VERSION=(\S+)/m)?.[1];
    execFileSync('cargo', ['build', '-p', 'superschematic-rules-dev', '--features', 'server', '--bin', 'superschematic-dev'], {
      cwd: workspace,
      env: { ...process.env, RUSTUP_TOOLCHAIN: process.env.RUSTUP_TOOLCHAIN || rustVersion },
      stdio: 'inherit',
    });
  }
  if (!existsSync(built)) {
    console.warn('crosshost: cargo is not on PATH and SUPERSCHEMATIC_DEV_BIN names no superschematic-dev, so the files are not read across hosts');
    return undefined;
  }
  return built;
}

let bin: string | undefined;
const files = scratch('crosshost');
beforeAll(() => {
  bin = binary();
}, 1_200_000);
afterAll(files.remove);

/** superschematic-dev serving a file, until stopped. */
async function serving(db: string): Promise<{ url: string; stop: () => Promise<void> }> {
  const child = Bun.spawn([bin!, '--db', db, '--listen', '127.0.0.1:0', '--as', 'alice', '--permissions', 'orders'], { stderr: 'pipe', stdout: 'ignore' });
  const reader = child.stderr.getReader();
  const decoder = new TextDecoder();
  let text = '';
  for (;;) {
    const match = text.match(/serving (http:\/\/[^/\s]+)/);
    if (match) {
      reader.releaseLock();
      return {
        url: `${match[1]}/namespaces/default`,
        stop: async () => {
          child.kill('SIGTERM');
          await child.exited;
        },
      };
    }
    const { done, value } = await reader.read();
    if (done) throw new Error(`superschematic-dev exited: ${text}`);
    text += decoder.decode(value);
  }
}

async function over(url: string, method: string, path: string, body?: unknown, type = 'application/json'): Promise<any> {
  // A string body is sent as it is, so its numbers keep their text.
  const text = typeof body === 'string' ? body : JSON.stringify(body);
  const response = await fetch(`${url}${path}`, { method, ...(body === undefined ? {} : { body: text, headers: { 'content-type': type } }) });
  const json = (await response.json()) as { data?: unknown };
  if (!response.ok) throw new Error(`${method} ${path}: ${response.status} ${JSON.stringify(json)}`);
  return json.data;
}

const HOUR = 3_600_000;
const task = {
  name: 'Task',
  behaviors: [
    { name: 'Workflow', field: 'status', config: { states: ['todo', 'doing', 'done'], transitions: [{ from: 'todo', to: 'doing' }, { from: 'doing', to: 'done' }] } },
    { name: 'Timers', config: { deadlines: { nudge: { start: { create: true }, afterMs: 24 * HOUR, then: { transition: 'doing' } } } } },
  ],
  fields: [
    { name: 'id', typeRef: { name: 'string' }, key: true },
    { name: 'title', typeRef: { name: 'string' }, required: true },
    { name: 'points', typeRef: { name: 'Generic.Int64' } },
  ],
};
const invoice = {
  name: 'Invoice',
  behaviors: [{ name: 'Numbering', field: 'number', config: { format: 'INV-{seq:2}' } }],
  fields: [
    { name: 'id', typeRef: { name: 'string' }, key: true },
    { name: 'title', typeRef: { name: 'string' } },
  ],
};
const alice: Caller = { subject: 'alice', permissions: ['orders'] };

/** The tables a file holds, as SQLite keeps their DDL. */
function tables(path: string): unknown[] {
  const db = new Database(path, { readonly: true });
  try {
    return db.prepare("SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY name").all();
  } finally {
    db.close();
  }
}

function close(dev: Dev): void {
  (dev.store as SqliteStore).close();
}

test("a file superschematic-dev wrote is read and written on by this host", async () => {
  if (bin === undefined) return;
  const path = join(files.dir, 'from-rust.db');
  const rust = await serving(path);
  let written: { tasks: unknown; invoices: unknown; t1: unknown };
  try {
    await over(rust.url, 'POST', '/schemas', task);
    await over(rust.url, 'POST', '/schemas/Task/publish');
    await over(rust.url, 'POST', '/schemas', invoice);
    await over(rust.url, 'POST', '/schemas/Invoice/publish');
    await over(rust.url, 'POST', '/schemas/Task/instances', '{"data": {"id": "t1", "title": "Write", "points": 9007199254740993}}');
    await over(rust.url, 'POST', '/schemas/Task/instances', { data: { id: 't2', title: 'Ship', points: 2 } });
    await over(rust.url, 'POST', '/schemas/Task/instances/t1/operations/transition', { to: 'doing' });
    await over(rust.url, 'POST', '/schemas/Invoice/instances', { data: { id: 'i1', title: 'a' } });
    written = {
      tasks: await over(rust.url, 'GET', '/schemas/Task/instances'),
      invoices: await over(rust.url, 'GET', '/schemas/Invoice/instances'),
      t1: await over(rust.url, 'GET', '/schemas/Task/instances/t1'),
    };
  } finally {
    await rust.stop();
  }
  const dev = await openDev(path, { drain: 'whenAsked' });
  try {
    expect(await dev.request(alice, { kind: 'list', type: 'Task' })).toEqual(written.tasks as never);
    expect(await dev.request(alice, { kind: 'list', type: 'Invoice' })).toEqual(written.invoices as never);
    expect(await dev.request(alice, { kind: 'get', type: 'Task', id: 't1' })).toEqual(written.t1 as never);
    expect((await dev.request(alice, { kind: 'describe', type: 'Task' })).version).toBe(1);
    // A value wider than a double reaches the handler as the file holds it.
    const store = dev.store as unknown as { read(read: unknown): { text: string } };
    expect(store.read({ kind: 'row', type: 'Task', id: 't1' }).text).toContain('"points":9007199254740993');
    // Its sub-table rows, deadlines and shared table's count are this host's to move on.
    const moved = await dev.request(alice, { kind: 'operation', type: 'Task', id: 't2', use: 'status', operation: 'transition', params: { to: 'doing' } });
    expect(moved).toEqual({ from: 'todo', to: 'doing' });
    const second = await dev.request(alice, { kind: 'create', type: 'Invoice', data: { id: 'i2', title: 'b' } });
    expect(second.number).toBe('INV-02');
  } finally {
    close(dev);
  }
});

test('a file this host wrote is read and written on by superschematic-dev, with the same tables', async () => {
  if (bin === undefined) return;
  const path = join(files.dir, 'from-typescript.db');
  const dev = await openDev(path);
  let written: { tasks: unknown; invoices: unknown };
  try {
    await dev.request(alice, { kind: 'define', document: task });
    await dev.request(alice, { kind: 'publish', type: 'Task' });
    await dev.request(alice, { kind: 'define', document: invoice });
    await dev.request(alice, { kind: 'publish', type: 'Invoice' });
    await dev.request(alice, { kind: 'create', type: 'Task', data: { id: 't1', title: 'Write', points: 3 } });
    await dev.request(alice, { kind: 'create', type: 'Task', data: { id: 't2', title: 'Ship', points: 2 } });
    await dev.request(alice, { kind: 'operation', type: 'Task', id: 't1', use: '', operation: 'transition', params: { to: 'doing' } });
    await dev.request(alice, { kind: 'create', type: 'Invoice', data: { id: 'i1', title: 'a' } });
    written = {
      tasks: await dev.request(alice, { kind: 'list', type: 'Task' }),
      invoices: await dev.request(alice, { kind: 'list', type: 'Invoice' }),
    };
  } finally {
    close(dev);
  }
  const rust = await serving(path);
  try {
    expect(await over(rust.url, 'GET', '/schemas/Task/instances')).toEqual(written.tasks);
    expect(await over(rust.url, 'GET', '/schemas/Invoice/instances')).toEqual(written.invoices);
    expect((await over(rust.url, 'GET', '/schemas/Task/describe')).version).toBe(1);
    expect(await over(rust.url, 'POST', '/schemas/Task/instances/t1/operations/transition', { to: 'done' })).toEqual({ from: 'doing', to: 'done' });
    expect((await over(rust.url, 'POST', '/schemas/Invoice/instances', { data: { id: 'i2', title: 'b' } })).number).toBe('INV-02');
    expect((await over(rust.url, 'GET', '/schemas/Task/instances/t2')).timersDeadlines.nudge.fired).toBe(false);
  } finally {
    await rust.stop();
  }
  expect(tables(path)).toEqual(tables(join(files.dir, 'from-rust.db')));
});
