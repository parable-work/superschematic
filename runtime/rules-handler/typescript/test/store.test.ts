// The development layout on bun:sqlite and the host over it, as
// rules-dev's tests/store.rs holds its SqliteStore and Dev: a unit whose
// fence does not hold applies nothing, a shared table's row is written held
// to its version, a file keeps its versions and rows across opens, the host
// runs its queues after each request and a deadline when it falls due, and
// a request whose rows keep moving is refused as busy after eight runs.
import { afterAll, expect, test } from 'bun:test';
import { join } from 'node:path';
import { Dev, RefusalError, serve, type Caller, type Store, type StoreRead, type StoreWrite, type Unit } from '../dist/index.js';
import { openDev } from '../dist/sqlite.js';
import { handler, scratch, sqlite } from './support.ts';

const HOUR = 3_600_000;
const caller: Caller = { subject: 'alice', permissions: ['orders'] };
const files = scratch('store');
afterAll(files.remove);

const read = (store: ReturnType<typeof sqlite>, request: StoreRead) => JSON.parse((store.read(request) as { text: string }).text) as Record<string, any>;

test('a unit whose fence does not hold applies nothing', () => {
  const store = sqlite();
  store.begin();
  const insert: Unit = { writes: [{ write: 'insert', type: 'Note', id: 'n1', data: { title: 'a' }, owned: {} }] };
  expect(store.apply(insert)).toBe(true);
  let again: unknown;
  try {
    store.apply(insert);
  } catch (error) {
    again = error;
  }
  expect(again).toBeInstanceOf(RefusalError);
  expect((again as RefusalError).refusal.code).toBe('conflict');
  const stale: Unit = {
    writes: [
      { write: 'arm', type: 'Note', id: 'n1', useId: 'Timers', name: 'x', at: 5, payload: null },
      { write: 'update', type: 'Note', id: 'n1', expected: 7, data: { title: 'b' }, owned: {} },
    ],
  };
  expect(store.apply(stale)).toBe(false);
  expect(read(store, { kind: 'row', type: 'Note', id: 'n1' }).row.data.title).toBe('a');
  expect(store.armed('Note', 'n1')).toEqual([]);
  store.commit();
});

test("a shared table's row is held to its version", () => {
  const store = sqlite();
  const write = (change: Extract<StoreWrite, { write: 'sharedRow' }>['change']): StoreWrite => ({
    write: 'sharedRow',
    type: 'Invoice',
    useId: 'number',
    table: 'counts',
    key: '*',
    change,
  });
  const held = () => read(store, { kind: 'sharedRow', type: 'Invoice', useId: 'number', table: 'counts', key: '*' }).sharedRow;
  store.begin();
  expect(held()).toBeNull();
  const insert: Unit = { writes: [write({ kind: 'insert', values: { last: 1, counting: null } })] };
  expect(store.apply(insert)).toBe(true);
  expect(held()).toEqual({ key: '*', values: { last: 1 }, version: 1 });
  expect(store.apply(insert)).toBe(false);
  const update = (expected: number, last: number): Unit => ({ writes: [write({ kind: 'update', expectedVersion: expected, values: { last } })] });
  expect(store.apply(update(1, 2))).toBe(true);
  const stale: Unit = { writes: [...update(1, 9).writes, { write: 'insert', type: 'Invoice', id: 'i9', data: { title: 'late' }, owned: {} }] };
  expect(store.apply(stale)).toBe(false);
  expect(held()).toEqual({ key: '*', values: { last: 2 }, version: 2 });
  expect(read(store, { kind: 'row', type: 'Invoice', id: 'i9' }).row).toBeNull();
  expect(store.apply({ writes: [{ write: 'dropUse', type: 'Invoice', behavior: 'Numbering', useId: 'number' }] })).toBe(true);
  expect(held()).toBeNull();
  store.commit();
});

test('a file keeps its versions and rows across opens', async () => {
  const path = join(files.dir, 'reopen.db');
  const first = await openDev(path, { clock: () => 1_000_000 });
  await first.request(caller, { kind: 'define', document: { name: 'Note', fields: [{ name: 'title', typeRef: { name: 'string' } }] } });
  await first.request(caller, { kind: 'publish', type: 'Note' });
  await first.request(caller, { kind: 'create', type: 'Note', data: { id: '0190f0e2-7b1c-7d4e-8f00-000000000001', title: 'Kept' } });
  const again = await openDev(path, { clock: () => 1_000_000 });
  const page = await again.request(caller, { kind: 'list', type: 'Note' });
  expect(page.items[0]!.title).toBe('Kept');
  // A UUID key reads back in base62, as rules-dev's does.
  expect(page.items[0]!.id).toBe('2xEI9Uvk79iTmuoTQj4vB');
  expect((await again.request(caller, { kind: 'describe', type: 'Note' })).version).toBe(1);
});

const ticket = {
  name: 'Ticket',
  behaviors: [
    { name: 'Workflow', field: 'status', config: { states: ['open', 'closed'], transitions: [{ from: 'open', to: 'closed' }] } },
    { name: 'Timers', config: { deadlines: { respond: { start: { create: true }, afterMs: HOUR, then: { transition: 'closed' } } } } },
  ],
  fields: [{ name: 'title', typeRef: { name: 'string' } }],
};

test('the host runs its queues after each request and a deadline when it falls due', async () => {
  let now = 1_000_000;
  const store = sqlite();
  const dev = new Dev(handler, store, { clock: () => now });
  await dev.request(caller, { kind: 'define', document: ticket });
  await dev.request(caller, { kind: 'publish', type: 'Ticket' });
  const id = String((await dev.request(caller, { kind: 'create', type: 'Ticket', data: { title: 't' } })).id);
  expect(store.queued()).toEqual([]);
  expect(store.armed('Ticket', id)).toHaveLength(1);
  now += HOUR;
  // The next request runs the deadline that fell due, after it answers.
  expect((await dev.request(caller, { kind: 'get', type: 'Ticket', id })).status).toBe('open');
  expect((await dev.request(caller, { kind: 'get', type: 'Ticket', id })).status).toBe('closed');
  expect(store.armed('Ticket', id)).toEqual([]);
  expect(store.subRows('Ticket', id, 'Timers', 'deadlines')[0]!.values.fired).toBe(true);

  const waiting = sqlite();
  const manual = new Dev(handler, waiting, { drain: 'whenAsked' });
  await manual.request(caller, { kind: 'define', document: ticket });
  await manual.request(caller, { kind: 'publish', type: 'Ticket' });
  await manual.request(caller, { kind: 'create', type: 'Ticket', data: { title: 't' } });
  expect(waiting.queued()).toHaveLength(1);
  expect(manual.runDue()).toHaveLength(1);
  expect(waiting.queued()).toEqual([]);
});

test('a request whose rows keep moving is refused as busy after eight runs', async () => {
  const store = sqlite();
  const dev = new Dev(handler, store);
  await dev.request(caller, { kind: 'define', document: { name: 'Note', fields: [{ name: 'title', typeRef: { name: 'string' } }] } });
  await dev.request(caller, { kind: 'publish', type: 'Note' });
  let applied = 0;
  const moving: Store = {
    due: now => store.due(now),
    queued: () => store.queued(),
    events: after => store.events(after),
    begin: () => store.begin(),
    commit: () => store.commit(),
    rollback: () => store.rollback(),
    read: request => store.read(request),
    apply: () => {
      applied++;
      return false;
    },
  };
  const outcome = serve(handler, moving, { kind: 'create', type: 'Note', data: { title: 'x' } }, { caller, now: 1, request: 'r1', id: '00000000-0000-4000-8000-000000000001' });
  expect(applied).toBe(8);
  expect(outcome).toEqual({
    ok: false,
    refusal: {
      code: 'busy',
      status: 503,
      class: 'EngineError',
      message: 'create: the rows kept moving while the request ran; it ran 8 times and nothing was written, so it may be tried again',
    },
  });
  await expect(dev.request({ subject: '' }, { kind: 'types' })).rejects.toBeInstanceOf(RefusalError);
});
