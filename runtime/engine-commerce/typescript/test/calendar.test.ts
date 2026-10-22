// Calendar: time a desk opens in London, which a booking's Holds holds
// through the contract as it enters held, commits as it is booked and
// lets go as it is cancelled; the rules' order and checkHold's answers;
// findAvailability's slots and its window; opening hours and closures
// with the config's permission; listBookings; and a delete that waits
// while committed time is to come. Real SQLite, the engine's Rust
// behaviors, a clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, EngineError } from '@superschematic/engine';

import type { CalendarAvailability, CalendarBooking, CalendarRecord, HoldsRecord } from '../dist/index.js';
import { Clock, alice, cleanup, document, field, openTestEngine, principal, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** The clock's start: Wednesday 21 October 2026, 10:00 in London (09:00 UTC). */
const T0 = 1_792_573_200_000;

/** Thursday 22 October 2026 in London, on summer time. */
function thu(hour: number, minute = 0): number {
  return T0 + DAY + (hour - 10) * HOUR + minute * MINUTE;
}

/** An instant as a date-time field holds it. */
function iso(ms: number): string {
  return new Date(ms).toISOString().replace('.000Z', 'Z');
}

const manager = principal('mo', ['desk.manage']);
const day = [['09:00', '17:30']];

/** A desk: London hours on weekdays and Saturday, one booking at a time, quarter-hour slots of a quarter to two hours. */
const deskConfig = {
  timeZone: 'Europe/London',
  hours: { mon: day, tue: day, wed: day, thu: day, fri: day, sat: day },
  capacity: 1,
  slotMs: 15 * MINUTE,
  minMs: 15 * MINUTE,
  maxMs: 2 * HOUR,
  permission: 'desk.manage',
};

function opened() {
  const clock = new Clock(T0);
  const engine = openTestEngine({ clock: clock.now });
  publish(engine, document('Desk', [field('title', 'string')], [{ name: 'Calendar', config: deskConfig }]));
  publish(
    engine,
    document(
      'Booking',
      [field('title', 'string'), field('slotStart', 'Temporal.DateTime'), field('slotEnd', 'Temporal.DateTime')],
      [
        {
          name: 'Workflow',
          config: {
            states: ['draft', 'held', 'booked', 'expired', 'cancelled'],
            initial: 'draft',
            transitions: [
              { from: 'draft', to: 'held' },
              { from: 'held', to: 'booked' },
              { from: 'held', to: 'expired' },
              { from: 'held', to: 'cancelled' },
              { from: 'draft', to: 'cancelled' },
            ],
          },
        },
        { name: 'Links', config: { links: { desk: { schema: 'Desk' } } } },
        {
          name: 'Holds',
          config: {
            holds: { slot: { link: 'desk', interval: { start: 'slotStart', end: 'slotEnd' } } },
            takeOn: ['held'],
            commitOn: ['booked'],
            releaseOn: ['expired', 'cancelled'],
          },
        },
      ]
    )
  );
  engine.instances.create(alice, 'Desk', { title: 'By the window' }, { id: 'd1' });
  const invoke = (who: typeof alice, schema: string, id: string, operation: string, params: Record<string, unknown> = {}) =>
    engine.instances.invoke(who, schema, id, operation, params);
  const book = (id: string, start: number, end: number) =>
    engine.instances.create(alice, 'Booking', { title: id, slotStart: iso(start), slotEnd: iso(end) }, { id, behaviors: { Links: { desk: 'd1' } } });
  const move = (id: string, to: string) => invoke(alice, 'Booking', id, 'transition', { to });
  const holdsOf = (id: string) => engine.instances.get(alice, 'Booking', id)?.behaviors.Holds as unknown as HoldsRecord;
  const calendarOf = () => engine.instances.get(alice, 'Desk', 'd1')?.behaviors.Calendar as unknown as CalendarRecord;
  const bookings = () =>
    (invoke(alice, 'Desk', 'd1', 'listBookings', { startMs: T0, endMs: T0 + 30 * DAY }) as { items: CalendarBooking[] }).items.map((booking) => [
      booking.holder.id,
      booking.state,
      booking.startMs,
    ]);
  const check = (holder: string, start: number, end: number) =>
    invoke(alice, 'Desk', 'd1', 'checkHold', { holder: { schema: 'Booking', id: holder }, hold: 'slot', item: 0, interval: { startMs: start, endMs: end } }) as {
      fits: boolean;
      code?: string;
      details?: Record<string, unknown>;
    };
  return { engine, clock, invoke, book, move, holdsOf, calendarOf, bookings, check };
}

describe('Calendar', () => {
  test('a booking holds its slot as it enters held, commits as it is booked, and lets go as it is cancelled', () => {
    const { book, move, holdsOf, bookings } = opened();
    const created = book('b1', thu(10), thu(11));
    assert.deepEqual([created.behaviors.Holds?.state, created.behaviors.Holds?.total], ['pending', { amount: 0, currency: null }]);
    move('b1', 'held');
    assert.deepEqual(holdsOf('b1').items, [{ hold: 'slot', item: 0, schema: 'Desk', id: 'd1', startMs: thu(10), endMs: thu(11), state: 'held' }]);
    assert.deepEqual(bookings(), [['b1', 'held', thu(10)]]);

    book('b2', thu(10, 30), thu(11, 30));
    const refused = thrown(() => move('b2', 'held'), BehaviorVetoError);
    assert.deepEqual([refused.behavior, refused.vetoCode], ['Holds', 'unavailable']);
    const entry = (refused.vetoDetails?.items as Array<Record<string, unknown>>)[0];
    assert.deepEqual([entry.code, entry.conflict], ['unavailable', { startMs: thu(10), endMs: thu(11) }]);

    move('b1', 'booked');
    assert.deepEqual(bookings(), [['b1', 'committed', thu(10)]]);
    book('b3', thu(12), thu(13));
    move('b3', 'held');
    move('b3', 'cancelled');
    assert.deepEqual(holdsOf('b3').items.map((item) => item.state), ['released']);
    assert.deepEqual(bookings(), [['b1', 'committed', thu(10)]]);
  });

  test('holdFor meets its rules in their order, and checkHold answers as the record allows', () => {
    const { book, move, check } = opened();
    const code = (id: string, start: number, end: number) => {
      book(id, start, end);
      const refused = thrown(() => move(id, 'held'), BehaviorVetoError);
      return (refused.vetoDetails?.items as Array<Record<string, unknown>>)[0].code;
    };
    assert.equal(code('early', thu(8, 30), thu(9, 30)), 'outside_hours');
    assert.equal(code('grid', thu(10, 5), thu(10, 35)), 'off_grid');
    assert.equal(code('short', thu(10), thu(10, 10)), 'too_short');
    assert.equal(code('long', thu(10), thu(12, 15)), 'too_long');
    assert.equal(code('past', T0 - DAY, T0 - DAY + HOUR), 'too_soon');
    // Seven thousand years are too long, and answered at once.
    assert.equal(code('ages', Date.UTC(1901, 0, 1, 9), Date.UTC(8999, 11, 31)), 'too_long');

    assert.deepEqual(check('free', thu(14), thu(15)), { fits: true });
    book('b1', thu(14), thu(15));
    move('b1', 'held');
    assert.deepEqual(check('b1', thu(14), thu(15)), { fits: true });
    move('b1', 'booked');
    const booked = check('b1', thu(14), thu(15));
    assert.deepEqual([booked.fits, booked.code, booked.details?.supplyState], [false, 'not_requested', 'committed']);
    const outside = thrown(() => check('b9', Date.UTC(1800, 0, 1), Date.UTC(1800, 0, 2)), EngineError);
    assert.equal(outside.code, 'invalid_argument');
  });

  test('findAvailability lists the slots holdFor would take, over at most 31 days', () => {
    const { engine, book, move } = opened();
    book('b1', thu(10), thu(11));
    move('b1', 'held');
    const find = (params: Record<string, unknown>) => engine.instances.invokeSchema(alice, 'Desk', 'findAvailability', params) as CalendarAvailability;
    const found = find({ startMs: thu(9), endMs: thu(12), durationMs: HOUR, limit: 10 });
    assert.deepEqual(
      found.slots.map((slot) => [slot.id, slot.startMs, slot.remaining]),
      [
        ['d1', thu(9), 1],
        ['d1', thu(11), 1],
      ]
    );
    assert.equal(found.truncated, false);
    const limited = find({ startMs: thu(9), endMs: thu(17), durationMs: HOUR, limit: 2 });
    assert.deepEqual([limited.slots.length, limited.truncated], [2, true]);
    const tooLong = thrown(() => find({ startMs: T0, endMs: T0 + 32 * DAY, durationMs: HOUR }), BehaviorVetoError);
    assert.equal(tooLong.vetoCode, 'window_too_long');
  });

  test('hours and closures need the permission, and the zone does not move under time to come', () => {
    const { engine, invoke, book, move, calendarOf } = opened();
    assert.equal(calendarOf().timeZone, 'Europe/London');
    const forbidden = thrown(() => invoke(alice, 'Desk', 'd1', 'addClosure', { startMs: thu(9), endMs: thu(10), reason: 'training' }), EngineError);
    assert.equal(forbidden.code, 'forbidden');
    const closure = invoke(manager, 'Desk', 'd1', 'addClosure', { startMs: thu(9), endMs: thu(10), reason: 'training' });
    assert.deepEqual(closure, { closure: 'closure-1', startMs: thu(9), endMs: thu(10), reason: 'training' });
    assert.deepEqual(calendarOf().closures, [closure]);
    book('b1', thu(9, 30), thu(10, 30));
    const closed = thrown(() => move('b1', 'held'), BehaviorVetoError);
    assert.equal((closed.vetoDetails?.items as Array<Record<string, unknown>>)[0].code, 'closed');

    book('b2', thu(11), thu(12));
    move('b2', 'held');
    const busy = thrown(() => invoke(manager, 'Desk', 'd1', 'addClosure', { startMs: thu(11), endMs: thu(12), reason: 'cleaning' }), BehaviorVetoError);
    assert.equal(busy.vetoCode, 'has_bookings');
    const zone = thrown(() => invoke(manager, 'Desk', 'd1', 'setOpeningHours', { timeZone: 'America/New_York' }), BehaviorVetoError);
    assert.equal(zone.vetoCode, 'has_bookings');
    invoke(manager, 'Desk', 'd1', 'setOpeningHours', { hours: { thu: [['08:00', '18:00']] } });
    assert.deepEqual(calendarOf().hours, { thu: [['08:00', '18:00']] });
    move('b2', 'cancelled');
    assert.deepEqual(invoke(manager, 'Desk', 'd1', 'setOpeningHours', { timeZone: 'America/New_York' }), {
      hours: { thu: [['08:00', '18:00']] },
      timeZone: 'America/New_York',
    });
    assert.equal(engine.instances.get(alice, 'Desk', 'd1')?.behaviors.Calendar?.timeZone, 'America/New_York');
  });

  test('a delete waits while committed time is to come, and a booking made again with the id holds afresh', () => {
    const { engine, clock, book, move, holdsOf, bookings } = opened();
    book('b1', thu(10), thu(11));
    move('b1', 'held');
    move('b1', 'booked');
    const booking = thrown(() => engine.instances.delete(alice, 'Booking', 'b1'), BehaviorVetoError);
    assert.deepEqual([booking.behavior, booking.vetoCode], ['Holds', 'committed']);
    const desk = thrown(() => engine.instances.delete(alice, 'Desk', 'd1'), BehaviorVetoError);
    assert.deepEqual([desk.behavior, desk.vetoCode], ['Calendar', 'held']);

    clock.ms = thu(11);
    assert.equal(engine.instances.delete(alice, 'Booking', 'b1'), true);
    const listed = engine.instances.invoke(alice, 'Desk', 'd1', 'listBookings', { startMs: T0, endMs: T0 + 30 * DAY }) as { items: CalendarBooking[] };
    assert.deepEqual(listed.items.map((item) => [item.holder.id, item.state, item.retired]), [['b1', 'committed', true]]);
    book('b1', thu(14), thu(15));
    move('b1', 'held');
    assert.deepEqual(holdsOf('b1').items.map((item) => item.state), ['held']);
    assert.deepEqual(bookings(), [
      ['b1', 'committed', thu(10)],
      ['b1', 'held', thu(14)],
    ]);
    move('b1', 'cancelled');
    assert.equal(engine.instances.delete(alice, 'Desk', 'd1'), true);
  });
});
