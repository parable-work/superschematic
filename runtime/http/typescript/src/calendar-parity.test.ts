import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { calendarIcs, calendarReference, type CalendarDetail, type CalendarEntry } from './calendar.js';

/*
The calendar's vectors (D152, #616) the Go runtime writes
(`go test ./crosstable -run TestCalendarParityVectors -update` in
runtime/schema/go) and the Go, TypeScript and Rust runtimes run: a busy
entry's reference number, and the iCalendar text of entries in full and
busy, stamped at a time under a calendar's name.
*/

interface Vectors {
  readonly references: readonly { name: string; entry: CalendarEntry; want: string }[];
  readonly feeds: readonly { name: string; entries: CalendarEntry[]; detail: CalendarDetail; now: number; calendarName: string; want: string }[];
}

const vectors = JSON.parse(readFileSync(fileURLToPath(new URL('../../testdata/calendar_parity.json', import.meta.url)), 'utf8')) as Vectors;

describe('calendar parity', () => {
  test('the file holds references and feeds', () => {
    expect(vectors.references.length).toBeGreaterThan(0);
    expect(vectors.feeds.length).toBeGreaterThan(0);
  });
  test.each(vectors.references.map(tc => [tc.name, tc] as const))('reference: %s', (_, tc) => {
    expect(calendarReference(tc.entry)).toBe(tc.want);
  });
  test.each(vectors.feeds.map(tc => [tc.name, tc] as const))('feed: %s', (_, tc) => {
    expect(calendarIcs(tc.entries, tc.detail, new Date(tc.now), tc.calendarName)).toBe(tc.want);
  });
});
