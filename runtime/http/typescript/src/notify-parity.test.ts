import { describe, expect, test } from 'bun:test';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { codeHash, looksLikeCode, narrowedPermissions, normaliseAddress, quietDelaySeconds, readReply, replyAllows, sameId, wallMinutes } from './notify';

/*
The notifier's pure parts (D153), which the Rust runtime's orm::notify runs
too (D191): an address normalised by its kind, a reply's words read, the
quiet hours a wall time waits out, a channel's trust over a principal's
permissions, a code's hash, and an id in one form. This test computes every
want from the inputs below and holds runtime/http/testdata/notify_parity.json
to them; NOTIFY_PARITY_WRITE=1 writes the file instead. The Rust test reads
the file (orm::notify's the_parity_vectors_hold).
*/

const CORPUS = fileURLToPath(new URL('../../testdata/notify_parity.json', import.meta.url));

const addresses: [string, string][] = [
  ['email', ' Ann@Example.TEST '],
  ['email', 'ben@example.test'],
  ['phone', '+1 (555) 010-0001'],
  ['phone', '0044 20 7946 0000'],
  ['phone', '(555) 010-0002'],
  ['phone', '+0044 20'],
  ['handle', ' @ann '],
  ['handle', 'Ann'],
];

const replies = ['ack', 'ACK 1', 'ok', 'Acknowledged #7', 'done 142', 'done 142 at the door', '  done   142  ', 'undo 3', 'UNDO', 'approve 12', 'deny 12 not now', '#5', '123456', 'A1B2', 'abc', 'ack 9x', '', 'done #'];

const quiet: { at: string; zone: string; start: string | null; end: string | null }[] = [
  { at: '2027-03-10T23:30:00Z', zone: 'UTC', start: '22:00', end: '07:00' },
  { at: '2027-03-11T06:59:00Z', zone: 'UTC', start: '22:00', end: '07:00' },
  { at: '2027-03-10T23:30:00Z', zone: 'Europe/Paris', start: '22:00', end: '07:00' },
  { at: '2027-03-10T12:00:00Z', zone: 'UTC', start: '22:00', end: '07:00' },
  { at: '2027-03-10T12:00:00Z', zone: 'UTC', start: '12:00', end: '13:00' },
  { at: '2027-03-10T12:00:42Z', zone: 'UTC', start: '12:00', end: '13:00' },
  { at: '2027-03-10T12:00:00Z', zone: 'UTC', start: null, end: null },
  { at: '2027-03-10T12:00:00Z', zone: 'UTC', start: '09:00', end: '09:00' },
  { at: '2027-03-10T13:00:00Z', zone: 'UTC', start: '12:00', end: '13:00' },
  { at: '2027-03-10T12:00:00Z', zone: 'UTC', start: '9:00', end: 'noon' },
  { at: '2027-07-01T03:15:10Z', zone: 'America/New_York', start: '22:00', end: '07:00' },
  // The HH:MM:SS a Temporal.Time is stored as.
  { at: '2027-03-10T23:30:00Z', zone: 'UTC', start: '22:00:00', end: '07:00:00' },
  { at: '2027-03-10T12:00:42Z', zone: 'UTC', start: '12:00:00', end: '13:00:00.5' },
  { at: '2027-03-10T23:30:00Z', zone: 'UTC', start: '22:00:0', end: '07:00:00' },
];

// Quiet hours on nights the clocks change inside the window (#602): each
// window starts and ends at the instants its wall times are reached in the
// zone, which counting wall minutes did not.
const quietChanges: { at: string; zone: string; start: string; end: string }[] = [
  // New York goes forward at 02:00 on 2027-03-14 (07:00Z): 22:00 to 07:00 is 8 hours.
  { at: '2027-03-14T03:00:00Z', zone: 'America/New_York', start: '22:00', end: '07:00' },
  { at: '2027-03-14T06:59:00Z', zone: 'America/New_York', start: '22:00', end: '07:00' },
  { at: '2027-03-14T07:00:00Z', zone: 'America/New_York', start: '22:00', end: '07:00' },
  // An end the clocks skip, 02:30: the window ends at the jump, 03:00 EDT.
  { at: '2027-03-14T06:15:00Z', zone: 'America/New_York', start: '01:00', end: '02:30' },
  { at: '2027-03-14T07:00:00Z', zone: 'America/New_York', start: '01:00', end: '02:30' },
  // A start the clocks skip, 02:30: the window starts at the jump.
  { at: '2027-03-14T06:59:00Z', zone: 'America/New_York', start: '02:30', end: '04:00' },
  { at: '2027-03-14T07:00:00Z', zone: 'America/New_York', start: '02:30', end: '04:00' },
  // New York goes back at 02:00 on 2027-11-07 (06:00Z): 22:00 to 07:00 is 10 hours.
  { at: '2027-11-07T02:00:00Z', zone: 'America/New_York', start: '22:00', end: '07:00' },
  { at: '2027-11-07T11:59:00Z', zone: 'America/New_York', start: '22:00', end: '07:00' },
  // An end the clocks show twice, 01:30: the first.
  { at: '2027-11-07T05:00:00Z', zone: 'America/New_York', start: '00:30', end: '01:30' },
  { at: '2027-11-07T05:45:00Z', zone: 'America/New_York', start: '00:30', end: '01:30' },
  // A start the clocks show twice, 01:30: from the first, so the hour that repeats is inside.
  { at: '2027-11-07T06:15:00Z', zone: 'America/New_York', start: '01:30', end: '03:00' },
  // Paris goes forward at 02:00 on 2027-03-28 (01:00Z), and back at 03:00 on 2027-10-31 (01:00Z).
  { at: '2027-03-27T22:30:00Z', zone: 'Europe/Paris', start: '23:00', end: '07:00' },
  { at: '2027-10-30T21:30:00Z', zone: 'Europe/Paris', start: '23:00', end: '07:00' },
  // A zone whose clocks change by half an hour.
  { at: '2027-04-03T13:00:00Z', zone: 'Australia/Lord_Howe', start: '22:00', end: '07:00' },
];

const trust: { held: string[]; cap: string[] | null; need: string }[] = [
  { held: ['tickets.work'], cap: null, need: 'tickets.work' },
  { held: ['tickets.work'], cap: [], need: 'tickets.work' },
  { held: ['tickets'], cap: ['tickets.work'], need: 'tickets.work' },
  { held: ['tickets.work'], cap: ['tickets'], need: 'tickets.work' },
  { held: ['tickets.read'], cap: null, need: 'tickets.work' },
  { held: ['agents.approve', 'tickets.work'], cap: ['agents.approve'], need: 'tickets.work' },
  { held: ['agents.approve', 'tickets.work'], cap: ['agents.approve'], need: 'agents.approve' },
  { held: ['reports', 'reports.export'], cap: ['reports.export', 'reports.read'], need: 'reports.read' },
];

const codes = ['123456', ' 123456 ', 'a1b2', 'A1B2', ''];

const ids = ['0f8b6c4e-1b2a-4c3d-9e8f-123456789abc', '0F8B6C4E-1B2A-4C3D-9E8F-123456789ABC', 'SMS', 'not an id'];

function hhmmss(at: Date, zone: string): string {
  const minutes = wallMinutes(at, zone);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(Math.floor(minutes / 60))}:${pad(minutes % 60)}:${pad(at.getUTCSeconds())}`;
}

function vectors() {
  return {
    comment:
      "The notifier's pure parts (D153, D191): addresses is an address normalised by its kind (TypeScript's normaliseAddress, Rust's normalise_address); replies is a reply's words read (readReply, read_reply) and whether its text is shaped as a code (looksLikeCode, looks_like_code); quietHours is how many seconds a wall time waits out quiet hours (quietDelaySeconds at an instant in a zone, whose wall time is wall, and Rust's quiet_delay_seconds of the wall time); quietHoursAcrossChanges is the same on nights the clocks change inside the window, from an instant in a zone (quietDelaySeconds, Go's QuietDelayAt); trust is a channel's trust over a principal's permissions (replyAllows, reply_allows; narrowedPermissions, narrowed_permissions), a null cap no cap; codeHashes is a code's stored hash (codeHash, code_hash); sameIds is an id in one form (sameId, same_id). runtime/http/typescript's notify-parity.test.ts writes the wants with NOTIFY_PARITY_WRITE=1.",
    addresses: addresses.map(([kind, address]) => ({ kind, address, want: normaliseAddress(kind, address) })),
    replies: replies.map(text => {
      const read = readReply(text);
      return { text, want: { verb: read.verb, reference: read.reference ?? null, args: read.args, ack: read.ack, undo: read.undo, code: looksLikeCode(text) } };
    }),
    quietHours: quiet.map(tc => ({ ...tc, wall: hhmmss(new Date(tc.at), tc.zone), want: quietDelaySeconds(new Date(tc.at), tc.zone, tc.start, tc.end) })),
    quietHoursAcrossChanges: quietChanges.map(tc => ({ ...tc, want: quietDelaySeconds(new Date(tc.at), tc.zone, tc.start, tc.end) })),
    trust: trust.map(tc => ({ ...tc, allows: replyAllows(tc.held, tc.cap, tc.need), narrowed: narrowedPermissions(tc.held, tc.cap) })),
    codeHashes: codes.map(code => ({ code, want: codeHash(code) })),
    sameIds: ids.map(id => ({ id, want: sameId(id) })),
  };
}

describe('notify parity', () => {
  test('the vectors are current', () => {
    const want = `${JSON.stringify(vectors(), null, 2)}\n`;
    if (process.env.NOTIFY_PARITY_WRITE === '1') writeFileSync(CORPUS, want);
    expect(readFileSync(CORPUS, 'utf8')).toBe(want);
  });
});
