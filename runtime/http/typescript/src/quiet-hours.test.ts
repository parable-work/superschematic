import { describe, expect, test } from 'bun:test';
import { instantOfWall, quietSecondsAt } from './quiet-hours';

const HOUR = 3600;
const at = (iso: string) => new Date(iso);
const wall = (iso: string) => Date.parse(`${iso}Z`);

describe('quiet hours across a change of the clocks (#602)', () => {
  test('the night the clocks go forward, 22:00 to 07:00 holds a message 8 hours', () => {
    // 22:00 EST on 2027-03-13; the clocks jump from 02:00 to 03:00 at 07:00Z.
    expect(quietSecondsAt(at('2027-03-14T03:00:00Z'), 'America/New_York', '22:00', '07:00')).toBe(8 * HOUR);
    // An ordinary night holds it 9.
    expect(quietSecondsAt(at('2027-03-21T02:00:00Z'), 'America/New_York', '22:00', '07:00')).toBe(9 * HOUR);
  });

  test('the night the clocks go back, 22:00 to 07:00 holds a message 10 hours', () => {
    // 22:00 EDT on 2027-11-06; the clocks go back from 02:00 to 01:00 at 06:00Z.
    expect(quietSecondsAt(at('2027-11-07T02:00:00Z'), 'America/New_York', '22:00', '07:00')).toBe(10 * HOUR);
  });

  test('a wall time the clocks skip is the instant they jump, and one they show twice the first time', () => {
    // 02:30 on 2027-03-14 never shows: the clock passes it at 03:00 EDT, 07:00Z.
    expect(instantOfWall(wall('2027-03-14T02:30:00'), 'America/New_York')).toBe(Date.parse('2027-03-14T07:00:00Z'));
    // 01:30 on 2027-11-07 shows at 05:30Z (EDT) and at 06:30Z (EST).
    expect(instantOfWall(wall('2027-11-07T01:30:00'), 'America/New_York')).toBe(Date.parse('2027-11-07T05:30:00Z'));
  });

  test('a window that opened before the clocks went back holds the hour that repeats', () => {
    // 01:15 EST, the second time the clock shows it: 01:30 to 03:00 opened at the first 01:30.
    expect(quietSecondsAt(at('2027-11-07T06:15:00Z'), 'America/New_York', '01:30', '03:00')).toBe(1.75 * HOUR);
    // 01:15 EDT, before it opened.
    expect(quietSecondsAt(at('2027-11-07T05:15:00Z'), 'America/New_York', '01:30', '03:00')).toBe(0);
  });

  test('a zone without a change and a window that is not one hold as before', () => {
    expect(quietSecondsAt(at('2027-03-10T23:30:00Z'), 'UTC', '22:00', '07:00')).toBe(7.5 * HOUR);
    expect(quietSecondsAt(at('2027-03-10T12:00:42.500Z'), 'UTC', '12:00', '13:00')).toBe(HOUR - 42);
    expect(quietSecondsAt(at('2027-03-10T12:00:00Z'), 'UTC', '09:00', '09:00')).toBe(0);
    expect(quietSecondsAt(at('2027-03-10T12:00:00Z'), 'UTC', null, '09:00')).toBe(0);
  });
});
