// The calendar's iCalendar document (D152): an entry's dates, a title
// escaped and folded, and a busy feed that shows nothing of the row but a
// reference number.
import { expect, test } from 'bun:test';
import type { Principal } from './auth.js';
import { calendarIcs, calendarReference, readCalendar, type CalendarEntry } from './calendar.js';

const now = new Date('2026-11-25T12:00:00Z');

const entries: CalendarEntry[] = [
  { source: 'contract', kind: 'renewal', type: 'Contract', key: 'c1', title: 'Renew; the lease, now', start: '2027-01-05', allDay: true },
  {
    source: 'filing',
    kind: 'deadline',
    type: 'Filing',
    key: 'f1',
    title: 'A filing whose title runs long enough that its summary line passes seventy-five octets and folds',
    start: '2027-01-31T17:00:00.000Z',
    occurrence: { key: '2027-01-31T17:00', due: '2027-01-29T17:00', state: 'upcoming' },
  },
];

test('a full feed titles each entry, escaped and folded, a date-only entry all day', () => {
  const ics = calendarIcs(entries, 'full', now);
  expect(ics.startsWith('BEGIN:VCALENDAR\r\nVERSION:2.0\r\n')).toBe(true);
  expect(ics.endsWith('END:VCALENDAR\r\n')).toBe(true);
  expect(ics).toContain('DTSTART;VALUE=DATE:20270105\r\n');
  expect(ics).toContain('SUMMARY:Renew\\; the lease\\, now\r\n');
  expect(ics).toContain('DTSTART:20270131T170000Z\r\n');
  expect(ics).toContain('DTSTAMP:20261125T120000Z\r\n');
  for (const line of ics.split('\r\n')) expect(new TextEncoder().encode(line).length).toBeLessThanOrEqual(75);
  expect(ics).toContain('\r\n ');
});

test('a busy feed shows Busy and a reference number, and no title', () => {
  const ics = calendarIcs(entries, 'busy', now);
  expect(ics).not.toContain('Renew');
  expect(ics).not.toContain('filing whose');
  expect(ics).toContain(`SUMMARY:Busy (${calendarReference(entries[0]!)})`);
  expect(calendarReference(entries[0]!)).toMatch(/^[A-Z2-9]{8}$/u);
  expect(calendarReference(entries[0]!)).not.toBe(calendarReference(entries[1]!));
});

// A source's permission (#601): the schema's, from the definition, unless
// the options name one for the source, which wins.
test("a source needs the schema's permission unless the options name another", async () => {
  let reads = 0;
  const repository = {
    def: { name: 'Contract', key: 'id', calendar: [{ name: 'contract', kind: 'renewal', title: 'title', field: 'endsOn', permission: 'contracts.read' }], columns: [{ name: 'endsOn' }] },
    async findMany() {
      reads += 1;
      return { rows: [{ id: 'c1', title: 'Renew', endsOn: '2027-01-05' }] };
    },
    async findOne() {
      return null;
    },
  };
  const orm: object = { contracts: repository, as: () => orm, asSystem: () => orm, query: async () => ({ rows: [] }) };
  const principal = (permissions: string[]) => ({ subject: 'u1', kind: 'user', permissions }) as unknown as Principal;
  const read = { from: '2027-01-01', to: '2027-02-01' };
  expect((await readCalendar(orm, principal([]), read)).entries).toEqual([]);
  expect((await readCalendar(orm, principal(['contracts.read']), read)).entries.map(e => e.key)).toEqual(['c1']);
  expect((await readCalendar(orm, principal(['contracts.read']), read, { permissions: { contract: 'calendar.all' } })).entries).toEqual([]);
  expect((await readCalendar(orm, principal(['calendar.all']), read, { permissions: { contract: 'calendar.all' } })).entries.map(e => e.key)).toEqual(['c1']);
  expect(reads).toBe(2);
});
