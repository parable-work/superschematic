// fixture-calendar-db's tables end to end (D70, D103's step 4): desks whose
// time Calendar opens and bookings that hold a slot of one through the
// contract Supply. TestCalendarTablesEndToEnd generates the package,
// type-checks it, applies the generated DDL and runs these cases on each
// dialect: on bun:sqlite, the SQLite create.sql D27 plans and the HTTP
// runtime's adapter over the layout orm.ts embeds (fixtureCalendarDbRules);
// on Postgres, the generated create.sql and the generated repository itself
// (createOrm). So the generator's layout, its DDL, the sub-tables of the
// desk's Calendar and the booking's Holds, and the adapter are held to one
// another: a booking entering held takes its slot under the desk's lock,
// booking it commits it, and two bookings of the desk's one place for one
// slot do not both hold it; the other's move is unavailable until the
// first lets go.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readRow, runCreate, sqliteRulesStore, type CoreCaller, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixtureCalendarDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const DAY = 86_400_000;

/** A slot of an hour at 09:00 UTC on a Monday a week or more ahead: 09:00 or 10:00 in London, inside the desk's hours and on its grid. */
function slot(weeks: number): { startMs: number; endMs: number } {
  const now = new Date(Date.now() + 7 * DAY * weeks);
  const ahead = (8 - now.getUTCDay()) % 7 || 7;
  const startMs = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + ahead, 9);
  return { startMs, endMs: startMs + 3_600_000 };
}

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates a desk row through the core and resolves to its key. */
  desk(title: string): Promise<string>;
  /** Creates a booking of a desk's slot through the core and resolves to its key. */
  booking(title: string, deskId: string, at: { startMs: number; endMs: number }): Promise<string>;
  /** Moves a booking's status as alice. */
  move(key: string, to: string): Promise<any>;
  /** A desk's bookings over the month ahead, as listBookings answers. */
  bookings(key: string): Promise<{ state: string; startMs: number }[]>;
  /** A booking's Holds state as the store holds it. */
  state(key: string): Promise<unknown>;
  close(): Promise<void>;
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'calendar-db-'));
  const file = join(dir, 'calendar.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  return {
    dialect: 'bun:sqlite',
    desk: title => runCreate(host, 'Desk', { title }, caller),
    booking: (title, deskId, at) => runCreate(host, 'Booking', { title, slotStart: at.startMs, slotEnd: at.endMs, deskId }, caller),
    async move(key, to) {
      const action = { kind: 'operation' as const, key, useId: 'status', operation: 'transition', params: { to } };
      return (await execute(host, 'Booking', { caller, now: Date.now(), action })).result;
    },
    async bookings(key) {
      const params = { startMs: Date.now(), endMs: Date.now() + 31 * DAY };
      const action = { kind: 'operation' as const, key, useId: 'Calendar', operation: 'listBookings', params };
      const listed = (await execute(host, 'Desk', { caller, now: Date.now(), action })).result as any;
      return listed.items.map((item: any) => ({ state: item.state, startMs: item.startMs }));
    },
    async state(key) {
      const row = await readRow(host, 'Booking', key);
      return row?.owned.Holds?.state ?? null;
    },
    async close() {
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_calendar');
  const orm = createOrm(db.pool, alice);
  return {
    dialect: 'Postgres',
    async desk(title) {
      return (await orm.desk.createOne({ title } as never)).id;
    },
    async booking(title, deskId, at) {
      const row = { title, slotStart: new Date(at.startMs).toISOString(), slotEnd: new Date(at.endMs).toISOString(), deskId };
      return (await orm.booking.createOne(row as never)).id;
    },
    move: (key, to) => orm.booking.status.transition(key as never, { to } as never),
    async bookings(key) {
      const listed = (await orm.desk.calendar.listBookings(key as never, { startMs: Date.now(), endMs: Date.now() + 31 * DAY } as never)) as any;
      return listed.items.map((item: any) => ({ state: item.state, startMs: item.startMs }));
    },
    async state(key) {
      const row = (await orm.booking.findOne({ id: { eq: key } } as never)) as any;
      return row?.holdsState ?? null;
    },
    close: () => db.drop(),
  };
}

const dialects: { readonly name: string; readonly skip: boolean; readonly open: () => Promise<Tables> }[] = [
  { name: 'bun:sqlite', skip: false, open: sqliteTables },
  { name: 'Postgres', skip: dsn === '', open: postgresTables },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(dialect.name, () => {
    let t: Tables;

    beforeAll(async () => {
      t = await dialect.open();
    });

    afterAll(async () => {
      await t?.close();
    });

    test(`a booking entering held takes its slot, and booking it commits it, on ${dialect.name}`, async () => {
      const desk = await t.desk('By the window');
      const at = slot(1);
      const booking = await t.booking('b1', desk, at);
      expect(await t.state(booking)).toBe('pending');
      expect(await t.move(booking, 'held')).toEqual({ from: 'draft', to: 'held' });
      expect(await t.state(booking)).toBe('held');
      expect(await t.bookings(desk)).toEqual([{ state: 'held', startMs: at.startMs }]);
      expect(await t.move(booking, 'booked')).toEqual({ from: 'held', to: 'booked' });
      expect(await t.bookings(desk)).toEqual([{ state: 'committed', startMs: at.startMs }]);
    });

    test(`two bookings of the one place do not both hold it, on ${dialect.name}`, async () => {
      const desk = await t.desk('By the door');
      const at = slot(2);
      const first = await t.booking('first', desk, at);
      const second = await t.booking('second', desk, at);
      const moved = await Promise.allSettled([t.move(first, 'held'), t.move(second, 'held')]);
      const held = moved.filter(result => result.status === 'fulfilled');
      expect(held.length).toBe(1);
      const refused = moved.find(result => result.status === 'rejected') as PromiseRejectedResult;
      expect(String(refused.reason?.message ?? refused.reason)).toMatch(/Desk \S+ is held for part of /);
      expect(await t.bookings(desk)).toEqual([{ state: 'held', startMs: at.startMs }]);
      const [winner, loser] = moved[0]!.status === 'fulfilled' ? [first, second] : [second, first];
      expect(await t.move(winner, 'cancelled')).toEqual({ from: 'held', to: 'cancelled' });
      expect(await t.bookings(desk)).toEqual([]);
      expect(await t.move(loser, 'held')).toEqual({ from: 'draft', to: 'held' });
      expect(await t.bookings(desk)).toEqual([{ state: 'held', startMs: at.startMs }]);
    });
  });
}
