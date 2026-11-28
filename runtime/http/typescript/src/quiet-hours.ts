/*
Quiet hours in a recipient's zone (D153, #602). A preference's window is two
wall times, [start, end), which a window that crosses midnight (22:00 to
07:00) reads as one. The window starts and ends at instants: each wall time
on its date in the zone is the first instant whose wall time reaches it. On
a night the clocks change inside the window, that is the instant the clock
shows it:

- When the clocks go forward past it (02:30 on a night that jumps from
  02:00 to 03:00), the instant of the jump, 03:00, when the wall time first
  passes it.
- When the clocks go back over it (01:30 on a night that goes from 02:00
  back to 01:00), the first time the clock shows it, not the second.

So a window holds a message from the instant it starts to the instant it
ends: the hours it covers are the hours that pass, one fewer on the night
the clocks go forward and one more on the night they go back. Counting
wall minutes gave the hours of an ordinary night on both.
*/

const DAY_MS = 86_400_000;

const formatters = new Map<string, Intl.DateTimeFormat>();

function formatterOf(zone: string): Intl.DateTimeFormat {
  let made = formatters.get(zone);
  if (!made) {
    made = new Intl.DateTimeFormat('en-US', { timeZone: zone, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' });
    formatters.set(zone, made);
  }
  return made;
}

/** The wall time of instant ms in zone, as the milliseconds since the epoch the same wall time is in UTC, to the second. */
export function wallOf(ms: number, zone: string): number {
  const parts: Record<string, number> = {};
  for (const part of formatterOf(zone).formatToParts(new Date(ms))) {
    if (part.type !== 'literal') parts[part.type] = Number(part.value);
  }
  return Date.UTC(parts.year!, parts.month! - 1, parts.day!, parts.hour! % 24, parts.minute!, parts.second!);
}

/** The zone's offset from UTC at instant ms, in milliseconds. */
function offsetAt(ms: number, zone: string): number {
  return wallOf(ms, zone) - Math.floor(ms / 1000) * 1000;
}

/**
 * The first instant whose wall time in zone reaches wall (the milliseconds
 * since the epoch the wall time is in UTC): the instant the clock shows it,
 * the earlier of two when the clocks go back over it, and the instant they
 * jump when they go forward past it.
 */
export function instantOfWall(wall: number, zone: string): number {
  const before = offsetAt(wall - DAY_MS, zone);
  const after = offsetAt(wall + DAY_MS, zone);
  const shown = [wall - before, wall - after].filter(at => wallOf(at, zone) === wall).sort((a, b) => a - b);
  if (shown.length > 0) return shown[0]!;
  // A wall time the clocks skip: the instant of the jump, the first whose
  // wall time is past it, found by halving the seconds between the two
  // offsets' readings of it.
  let lo = wall - Math.max(before, after);
  let hi = wall - Math.min(before, after);
  while (hi - lo > 1000) {
    const mid = lo + Math.floor((hi - lo) / 2000) * 1000;
    if (wallOf(mid, zone) >= wall) hi = mid;
    else lo = mid;
  }
  return hi;
}

/** The minutes of the day of a time of day: HH:MM, or the HH:MM:SS a Temporal.Time is stored as, its seconds dropped. */
export function minutesOfDay(text: string): number | undefined {
  const m = /^(\d{1,2}):(\d{2})(?::\d{2}(?:\.\d+)?)?$/u.exec(text);
  return m ? Number(m[1]) * 60 + Number(m[2]) : undefined;
}

/**
 * How many seconds from at until the quiet hours [start, end) of zone end,
 * or 0 when at is outside them: the window of the day before at's (which a
 * window that crosses midnight ends in) and of at's own day, each from the
 * instant its start is reached to the instant its end is.
 */
export function quietSecondsAt(at: Date, zone: string, start: string | null | undefined, end: string | null | undefined): number {
  const from = start ? minutesOfDay(start) : undefined;
  const to = end ? minutesOfDay(end) : undefined;
  if (from === undefined || to === undefined || from === to) return 0;
  const ms = at.getTime();
  const wall = wallOf(ms, zone);
  const today = wall - (((wall % DAY_MS) + DAY_MS) % DAY_MS);
  for (const day of [today - DAY_MS, today]) {
    const opens = instantOfWall(day + from * 60_000, zone);
    const closes = instantOfWall(day + (from < to ? 0 : DAY_MS) + to * 60_000, zone);
    if (ms >= opens && ms < closes) return Math.ceil((closes - ms) / 1000);
  }
  return 0;
}
