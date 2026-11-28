import { describe, expect, test } from 'bun:test';
import { callEntry, grantTokenHash, reachesOf } from './access.js';
import { accessCallerOf, currentAccess, currentCaller, withCaller } from './caller.js';
import { audienceConditions } from './orm.js';

const OWNER = '00000000-0000-4000-8000-0000000000a1';
const RUN = '00000000-0000-4000-8000-0000000000b1';
const definition = { effective: '"doc"."effective_audience"', agents: '"doc"."effective_agents"' };

describe('the audience predicate (D156)', () => {
  test("keeps a user to the rows everyone sees and the rows that name the user's id", () => {
    const args: unknown[] = ['before'];
    expect(audienceConditions(definition, accessCallerOf({ subject: OWNER }), args, 1)).toEqual([
      '("doc"."effective_audience" IS NULL OR "doc"."effective_audience" && ARRAY[$2]::uuid[])',
    ]);
    expect(args).toEqual(['before', OWNER]);
  });

  test('keeps an anonymous caller, and one whose subject is no id, to the rows everyone sees', () => {
    for (const caller of [accessCallerOf(null), accessCallerOf({ subject: 'service-name' })]) {
      const args: unknown[] = [];
      expect(audienceConditions(definition, caller, args, 0)).toEqual(['"doc"."effective_audience" IS NULL']);
      expect(args).toEqual([]);
    }
  });

  test('admits an agent run as itself or whom it acts for, and holds it to the row\'s agent access', () => {
    const caller = accessCallerOf({ subject: RUN, kind: 'agent', actingFor: { kind: 'user', subject: OWNER } });
    const args: unknown[] = [];
    expect(audienceConditions(definition, caller, args, 0)).toEqual([
      '("doc"."effective_audience" IS NULL OR "doc"."effective_audience" && ARRAY[$1, $2]::uuid[]) AND "doc"."effective_agents" <> \'none\'',
    ]);
    expect(args).toEqual([RUN, OWNER]);
    expect(audienceConditions(definition, caller, [], 0, true)[0]).toEndWith(`"doc"."effective_agents" = 'followRole'`);
  });

  test('admits a caller through the teams it belongs to (#735)', () => {
    const TEAM = '00000000-0000-4000-8000-0000000000c1';
    const OUTER = '00000000-0000-4000-8000-0000000000c2';
    const caller = accessCallerOf({ subject: OWNER, teams: [TEAM, OUTER] });
    const args: unknown[] = [];
    expect(audienceConditions(definition, caller, args, 0)).toEqual([
      '("doc"."effective_audience" IS NULL OR "doc"."effective_audience" && ARRAY[$1, $2, $3]::uuid[])',
    ]);
    expect(args).toEqual([OWNER, TEAM, OUTER]);
  });

  test("holds a projection to its source's rows", () => {
    const probe = { ...definition, probe: '"doc_view"."id" IN (SELECT "doc"."id" FROM "doc"%s)' };
    expect(audienceConditions(probe, accessCallerOf(null), [], 0)).toEqual(['"doc_view"."id" IN (SELECT "doc"."id" FROM "doc" WHERE "doc"."effective_audience" IS NULL)']);
  });
});

describe("the operation's caller (D156)", () => {
  test('is the whole principal inside an operation, anonymous for none, and nothing outside one', () => {
    expect(currentAccess()).toBeUndefined();
    withCaller({ subject: RUN, kind: 'agent', actingFor: { kind: 'user', subject: OWNER } }, () => {
      expect(currentCaller()).toBe(RUN);
      expect(currentAccess()).toEqual({ subject: RUN, kind: 'agent', actingFor: { kind: 'user', subject: OWNER } });
    });
    withCaller(null, () => {
      expect(currentCaller()).toBeUndefined();
      expect(currentAccess()).toEqual({ kind: 'user' });
    });
  });
});

describe('grants and the access log (D156)', () => {
  test("a role's reaches are its paths by type", () => {
    expect(reachesOf(['Unit:contract', 'Request:unit.contract', 'Request:unit.owner', 'bad', ':x', 'y:'])).toEqual({
      Unit: ['contract'],
      Request: ['unit.contract', 'unit.owner'],
    });
    expect(reachesOf(null)).toEqual({});
  });

  test("a token's hash is its SHA-256 in lowercase hex", () => {
    expect(grantTokenHash('grant_abc')).toBe(new Bun.CryptoHasher('sha256').update('grant_abc').digest('hex'));
  });

  test("a call's log entry names an API key's or a grant's call and an agent run's read, and nothing else", () => {
    expect(callEntry(null, 'getDocument', 'GET')).toBeUndefined();
    expect(callEntry({ subject: OWNER, permissions: [] }, 'getDocument', 'GET')).toBeUndefined();
    expect(callEntry({ subject: OWNER, permissions: [], credential: { kind: 'apiKey', id: 'k1' } }, 'getDocument', 'GET')).toEqual({
      kind: 'keyCall',
      principal: OWNER,
      principalKind: 'user',
      operation: 'getDocument',
      credential: 'k1',
    });
    expect(callEntry({ subject: RUN, kind: 'party', permissions: [], credential: { kind: 'grant', id: 'g1' } }, 'listRequests', 'GET')).toMatchObject({
      kind: 'grantCall',
      principalKind: 'party',
      credential: 'g1',
    });
    const agent = { subject: RUN, kind: 'agent' as const, actingFor: { kind: 'user' as const, subject: OWNER }, permissions: [] };
    expect(callEntry(agent, 'getDocument', 'GET')).toEqual({ kind: 'agentRead', principal: RUN, principalKind: 'agent', actingFor: OWNER, operation: 'getDocument' });
    expect(callEntry(agent, 'updateDocument', 'PATCH')).toBeUndefined();
  });
});
