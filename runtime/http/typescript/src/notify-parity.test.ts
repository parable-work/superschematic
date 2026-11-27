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
      "The notifier's pure parts (D153, D191): addresses is an address normalised by its kind (TypeScript's normaliseAddress, Rust's normalise_address); replies is a reply's words read (readReply, read_reply) and whether its text is shaped as a code (looksLikeCode, looks_like_code); quietHours is how many seconds a wall time waits out quiet hours (quietDelaySeconds at an instant in a zone, whose wall time is wall, and Rust's quiet_delay_seconds of the wall time); trust is a channel's trust over a principal's permissions (replyAllows, reply_allows; narrowedPermissions, narrowed_permissions), a null cap no cap; codeHashes is a code's stored hash (codeHash, code_hash); sameIds is an id in one form (sameId, same_id). runtime/http/typescript's notify-parity.test.ts writes the wants with NOTIFY_PARITY_WRITE=1.",
    addresses: addresses.map(([kind, address]) => ({ kind, address, want: normaliseAddress(kind, address) })),
    replies: replies.map(text => {
      const read = readReply(text);
      return { text, want: { verb: read.verb, reference: read.reference ?? null, args: read.args, ack: read.ack, undo: read.undo, code: looksLikeCode(text) } };
    }),
    quietHours: quiet.map(tc => ({ ...tc, wall: hhmmss(new Date(tc.at), tc.zone), want: quietDelaySeconds(new Date(tc.at), tc.zone, tc.start, tc.end) })),
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
