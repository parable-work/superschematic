import { describe, expect, test } from 'bun:test';
import { InboundError, createInbound, emailAuthenticated } from './inbound.js';
import { identityLapsed, partyTablesOf, sameUUID } from './parties.js';

// The inbound path's pure parts (D157). The path itself, its conversations,
// sends, triage and run requests run against Postgres in
// TestPartiesEndToEnd (internal/generator/tsgen).

describe('emailAuthenticated', () => {
  test('DMARC passing, with neither SPF nor DKIM failing, is an email the core trusts', () => {
    expect(emailAuthenticated({ spf: 'pass', dkim: 'pass', dmarc: 'pass' })).toBe(true);
    // Forwarded mail often has SPF neutral, and DKIM carries its DMARC.
    expect(emailAuthenticated({ spf: 'neutral', dkim: 'pass', dmarc: 'PASS' })).toBe(true);
  });
  test('a failing result, or none, is an unknown sender', () => {
    expect(emailAuthenticated({ spf: 'pass', dkim: 'pass', dmarc: 'fail' })).toBe(false);
    expect(emailAuthenticated({ spf: 'fail', dkim: 'pass', dmarc: 'pass' })).toBe(false);
    expect(emailAuthenticated({ spf: 'pass', dkim: 'softfail', dmarc: 'pass' })).toBe(false);
    expect(emailAuthenticated({ spf: 'pass', dkim: 'pass' })).toBe(false);
    expect(emailAuthenticated(undefined)).toBe(false);
  });
});

describe('identityLapsed', () => {
  test("a user's identity never lapses", async () => {
    expect(await identityLapsed({ asSystem: () => ({}) }, { principalKind: 'user', principal: 'x' })).toBe(false);
  });
  test("a party's identity in a database with no grants table does not", async () => {
    expect(await identityLapsed({ asSystem: () => ({}) }, { principalKind: 'party', principal: 'x' })).toBe(false);
  });
});

describe('partyTablesOf and sameUUID', () => {
  test('the @party tables are found by their definitions', () => {
    const repository = (name: string, party?: object) => ({ def: { name, key: 'id', ...(party ? { party } : {}) }, findOne: async () => null });
    expect(partyTablesOf({ contact: repository('Contact', {}), job: repository('Job') })).toEqual({ Contact: 'contact' });
  });
  test('a UUID is the same in either form', () => {
    expect(sameUUID('0f8b6c4e-1b2a-4c3d-9e8f-123456789abc', '0F8B6C4E-1B2A-4C3D-9E8F-123456789ABC')).toBe(true);
    expect(sameUUID('SMS', 'SMS')).toBe(true);
    expect(sameUUID('SMS', 'MAIL')).toBe(false);
  });
});

describe('createInbound', () => {
  test('an ORM without the conversations is refused', () => {
    expect(() => createInbound({ orm: {}, notifier: {} as never, runs: {} as never })).toThrow('@conversations("conversations")');
  });
  test('its errors are InboundErrors', () => {
    expect(new InboundError('x').name).toBe('InboundError');
  });
});
