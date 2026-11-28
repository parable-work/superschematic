// The calendar's iCalendar document (D152): an entry's dates, a title
// escaped and folded, and a busy feed that shows nothing of the row but a
// reference number; and a route's refusal, the shared problem envelope.
import { expect, test } from 'bun:test';
import { calendarIcs, calendarReference, calendarRoutes, type CalendarEntry } from './calendar.js';

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

test("a route's refusal is the shared problem envelope, with the request id and code", async () => {
  const routes = calendarRoutes({ orm: {}, authenticate: async () => null });
  const refused = (await routes(new Request('http://api.test/api/calendar?from=a&to=b', { headers: { 'x-request-id': 'req-3' } })))!;
  expect(refused.headers.get('content-type')).toBe('application/problem+json');
  expect(refused.headers.get('x-request-id')).toBe('req-3');
  expect(await refused.json()).toEqual({ type: 'about:blank', title: 'Unauthorized', status: 401, detail: 'Authentication required', code: 'unauthorized', requestId: 'req-3' });
});
