/*
fixture-access-api's per-record access end to end (D156), run by
TestGeneratedAccessEndToEnd with API_DIR set to the materialized package,
CREATE_SQL to fixture-access-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated
router serves the API over the generated repositories of
fixture-access-db (createOrm), in a schema of the test's own.

TestGeneratedAccessEndToEndOnSQLite runs the same cases with ORM_DIALECT
sqlite (D182), CREATE_SQL fixture-access-db's sqlite/create.sql and
SQLITE_FILE a file of its own: the router serves the API over the
repositories of the ./orm/sqlite entry, on openSqlite's connection to the
file.

A request names its caller in its Authorization header: a grant's token
runs as the grant's holder (grantAuthenticator), but an agent run's, which
it refuses (D194); `<subject>:<permissions>`
is a user holding those permissions; and `agent=<subject>/<for>:<permissions>`
an agent run acting for the user <for>. The router records the access log
through accessLogOf.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { accessLogOf, activeGrants, calendarRoutes, grantAuthenticator, issueGrant, revokeGrant } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@schemas/fixture-access-db-types/orm';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const createSQL = process.env.CREATE_SQL;
const dialect = process.env.ORM_DIALECT ?? 'postgres';
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
const sqliteFile = process.env.SQLITE_FILE;
if (!apiDir || !createSQL || (dialect === 'sqlite' ? !sqliteFile : !dsn)) {
  throw new Error('API_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, or ORM_DIALECT=sqlite and SQLITE_FILE, are required');
}
const { buildRouter } = await import(`${apiDir}/router.ts`);

/** A UUID's wire form, which a row and a view hold. */
const uuid = (n: number) => parseIdentityUUID(`00000000-0000-4000-8000-${n.toString(16).padStart(12, '0')}`)!;

// The users: an owner of folders and documents, another user, a third the
// owner names, a party to a contract and an administrator of requests.
const OWNER_ID = uuid(0xa1);
const OTHER_ID = uuid(0xa2);
const NAMED_ID = uuid(0xa3);
const PARTY_ID = uuid(0xa4);
const ADMIN_ID = uuid(0xa5);
const READS = 'contracts.read,requests.read,requests.write';
const OWNER = `${OWNER_ID}:${READS}`;
const OTHER = `${OTHER_ID}:${READS}`;
const NAMED = `${NAMED_ID}:${READS}`;
const PARTY = `${PARTY_ID}:requests.read`;
const ADMIN = `${ADMIN_ID}:requests.read,requests.admin`;
const REVEALER = `${ADMIN_ID}:contracts.read,contracts.reveal`;
// An agent run, by its own id, acting for the owner, or for the other user.
const RUN_ID = uuid(0xb1);
const AGENT_FOR_OWNER = `agent=${RUN_ID}/${OWNER_ID}:${READS}`;
const AGENT_FOR_OTHER = `agent=${RUN_ID}/${OTHER_ID}:${READS}`;

/** The user or the agent run the credential names; none without one. */
const callers = async (ctx: RequestContext): Promise<Principal | null> => {
  const header = ctx.headers.get('authorization');
  if (!header?.startsWith('Bearer ')) return null;
  const [who, permissions = ''] = header.slice('Bearer '.length).split(':');
  const granted = permissions === '' ? [] : permissions.split(',');
  if (who!.startsWith('agent=')) {
    const [subject, forWhom] = who!.slice('agent='.length).split('/');
    return { subject: subject!, kind: 'agent', actingFor: { kind: 'user', subject: forWhom! }, permissions: granted };
  }
  return { subject: who!, kind: 'user', permissions: granted };
};

const schema = `access_ts_${process.hrtime.bigint()}`;
const FIELD_KEY = Buffer.alloc(32, 7).toString('base64');
let admin: pg.Client | undefined;
let pool: pg.Pool | undefined;
let sqlite: { close(): void } | undefined;
let orm: ReturnType<typeof createOrm>;
let app: Hono;

interface Answer {
  readonly status: number;
  readonly body: any;
}

/** A request to the app as the caller the credential names, or none. */
async function call(method: string, path: string, as?: string, body?: unknown): Promise<Answer> {
  const response = await app.request(path, {
    method,
    headers: {
      ...(as !== undefined ? { authorization: `Bearer ${as}` } : {}),
      ...(body !== undefined ? { 'content-type': 'application/json' } : {}),
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  return { status: response.status, body: await response.json() };
}

/** The data of a call that succeeds. */
async function ok(method: string, path: string, as: string, body?: unknown): Promise<any> {
  const answer = await call(method, path, as, body);
  if (answer.status !== 200) throw new Error(`${method} ${path} answered ${answer.status}: ${JSON.stringify(answer.body)}`);
  return answer.body.data;
}

/** The status a call answers. */
async function status(method: string, path: string, as: string, body?: unknown): Promise<number> {
  return (await call(method, path, as, body)).status;
}

/** Every item of a list a caller sees. */
async function items(path: string, as: string): Promise<any[]> {
  const out: any[] = [];
  let next: string | null = null;
  do {
    const page: any = await ok('GET', next === null ? path : `${path}${path.includes('?') ? '&' : '?'}cursor=${next}`, as);
    out.push(...page.items);
    next = page.next;
  } while (next !== null);
  return out;
}

const titles = (rows: readonly { title: string }[]) => rows.map(row => row.title).sort();

/** The access log's rows, as the database holds them. */
async function logRows(): Promise<any[]> {
  const { rows } = await orm.accessEntry.findMany({}, { count: false, limit: 1000 });
  return rows;
}

let contractA: any;
let contractB: any;
let unitA: any;
let unitB: any;
let requestA: any;
let requestB: any;

beforeAll(async () => {
  if (dialect === 'sqlite') {
    // The file made from the database's SQLite create.sql, then opened as
    // a server under stack dev opens it.
    const { Database } = await import('bun:sqlite');
    const raw = new Database(sqliteFile!, { create: true });
    raw.exec(readFileSync(createSQL, 'utf8'));
    raw.close();
    const { openSqlite, parseFieldKeys } = await import('@superschematic/http-runtime/sqlite');
    const db = openSqlite(sqliteFile!);
    sqlite = db;
    const onSQLite = await import('@schemas/fixture-access-db-types/orm/sqlite');
    orm = onSQLite.createOrm(db, { fieldKeys: parseFieldKeys(FIELD_KEY) });
  } else {
    admin = new pg.Client({ connectionString: dsn });
    await admin.connect();
    await admin.query(`CREATE SCHEMA ${schema}`);
    pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 4 });
    await pool.query(readFileSync(createSQL, 'utf8'));
    orm = createOrm(pool, { fieldKeys: FIELD_KEY });
  }
  app = new Hono();
  app.route('/', buildRouter({}, { authenticate: grantAuthenticator({ orm, inner: callers }), orm, accessLog: accessLogOf(orm) }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());

  // The contracts, written by the system: contract A lists the party.
  const system = orm.asSystem();
  contractA = await system.contract.createOne({ name: 'A', terms: 'Pay on the first' } as never);
  contractB = await system.contract.createOne({ name: 'B', terms: 'Pay on the last' } as never);
  await system.contractParty.createOne({ contractId: contractA.id, party: PARTY_ID } as never);
  unitA = await system.unit.createOne({ name: 'A1', contractId: contractA.id } as never);
  unitB = await system.unit.createOne({ name: 'B1', contractId: contractB.id } as never);
  requestA = await system.request.createOne({ unitId: unitA.id, title: 'Fix the door' } as never);
  requestB = await system.request.createOne({ unitId: unitB.id, title: 'Paint the hall' } as never);
  // A role a grant names: what a tenant may do, and the rows it reaches
  // from the contract a grant targets.
  await system.role.createOne({
    name: 'tenant',
    permissions: ['requests.read', 'requests.write', 'contracts.read'],
    reaches: ['Unit:contract', 'Request:unit.contract'],
  } as never);
});

afterAll(async () => {
  sqlite?.close();
  await pool?.end();
  await admin?.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
  await admin?.end();
});

describe('the access of fixture-access-api', () => {
  let mine: any;
  let secret: any;

  test('a private row never appears to another user in a list, a read or a search', async () => {
    mine = await ok('POST', '/api/folders', OWNER, { name: 'Mine', owner: OWNER_ID, audience: 'private' });
    expect(mine.audience).toBe('private');
    secret = await ok('POST', '/api/documents', OWNER, { folderId: mine.id, title: 'Secret plan' });
    // A document in a folder inherits its audience.
    expect(secret.audience).toBe('inherit');
    await ok('POST', '/api/documents', OWNER, { title: 'Public plan', audience: 'everyone' });

    expect(titles(await items('/api/documents', OWNER))).toEqual(['Public plan', 'Secret plan']);
    expect(titles(await items('/api/documents', OTHER))).toEqual(['Public plan']);
    expect(titles(await items('/api/documents?q=plan', OTHER))).toEqual(['Public plan']);
    expect(titles(await items('/api/documents?q=secret', OTHER))).toEqual([]);
    expect(await status('GET', `/api/documents/${secret.id}`, OTHER)).toBe(404);
    expect(await status('GET', `/api/documents/${secret.id}`, OWNER)).toBe(200);
    expect((await items('/api/folders', OTHER)).map(folder => folder.name)).not.toContain('Mine');
    // A write is held as a read is: the row is not there for another.
    expect(await status('PATCH', `/api/documents/${secret.id}`, OTHER, { title: 'Mine now' })).toBe(404);
    // The repository, outside any operation, reads every row, as a job does.
    expect((await orm.document.getOne(secret.id)).title).toBe('Secret plan');
  });

  test('a named row is visible to its owner and to the principals it names', async () => {
    const shared = await ok('POST', '/api/documents', OWNER, { title: 'Shared notes', audience: 'named', audienceNamed: [NAMED_ID] });
    expect(await status('GET', `/api/documents/${shared.id}`, OWNER)).toBe(200);
    expect(await status('GET', `/api/documents/${shared.id}`, NAMED)).toBe(200);
    expect(await status('GET', `/api/documents/${shared.id}`, OTHER)).toBe(404);
    expect(titles(await items('/api/documents', NAMED))).toContain('Shared notes');
    expect(titles(await items('/api/documents', OTHER))).not.toContain('Shared notes');
  });

  test("a document inherits its folder's audience, and follows it when the folder's changes", async () => {
    const team = await ok('POST', '/api/folders', OWNER, { name: 'Team', owner: OWNER_ID });
    const doc = await ok('POST', '/api/documents', OWNER, { folderId: team.id, title: 'Team doc' });
    expect(await status('GET', `/api/documents/${doc.id}`, OTHER)).toBe(200);
    // Only the folder's owner changes it: a row rule.
    expect(await status('PATCH', `/api/folders/${team.id}`, OTHER, { audience: 'private' })).toBe(404);
    await ok('PATCH', `/api/folders/${team.id}`, OWNER, { audience: 'private' });
    expect(await status('GET', `/api/documents/${doc.id}`, OTHER)).toBe(404);
    expect(await status('GET', `/api/documents/${doc.id}`, OWNER)).toBe(200);
    await ok('PATCH', `/api/folders/${team.id}`, OWNER, { audience: 'named', audienceNamed: [OTHER_ID] });
    expect(await status('GET', `/api/documents/${doc.id}`, OTHER)).toBe(200);
    expect(await status('GET', `/api/documents/${doc.id}`, NAMED)).toBe(404);
    // A document that leaves its folder's audience keeps its own.
    await ok('PATCH', `/api/documents/${doc.id}`, OWNER, { audience: 'everyone' });
    await ok('PATCH', `/api/folders/${team.id}`, OWNER, { audience: 'private' });
    expect(await status('GET', `/api/documents/${doc.id}`, NAMED)).toBe(200);
  });

  test('an agent run sees what whom it acts for sees, and writes no read-only row', async () => {
    const before = (await logRows()).filter(row => row.kind === 'agentRead').length;
    expect(await status('GET', `/api/documents/${secret.id}`, AGENT_FOR_OWNER)).toBe(200);
    expect(await status('GET', `/api/documents/${secret.id}`, AGENT_FOR_OTHER)).toBe(404);
    // Documents are read-only to agent runs unless a row says otherwise.
    expect(await status('PATCH', `/api/documents/${secret.id}`, AGENT_FOR_OWNER, { title: 'Edited' })).toBe(404);
    const open = await ok('POST', '/api/documents', OWNER, { title: 'Agent may edit', audience: 'everyone', agentAccess: 'followRole' });
    expect((await ok('PATCH', `/api/documents/${open.id}`, AGENT_FOR_OWNER, { title: 'Edited by a run' })).title).toBe('Edited by a run');
    const closed = await ok('POST', '/api/documents', OWNER, { title: 'No agents', audience: 'everyone', agentAccess: 'none' });
    expect(await status('GET', `/api/documents/${closed.id}`, AGENT_FOR_OWNER)).toBe(404);
    expect(await status('GET', `/api/documents/${closed.id}`, OWNER)).toBe(200);
    // Each read the run made is in the access log, with whom it acted for.
    const reads = (await logRows()).filter(row => row.kind === 'agentRead');
    expect(reads.length).toBe(before + 3);
    expect(reads.at(-1)).toMatchObject({ principal: RUN_ID, principalKind: 'agent', actingFor: OWNER_ID, operation: 'getDocument' });
  });

  test("a row rule follows a path through the table's relations", async () => {
    expect(titles(await items('/api/my/requests', PARTY))).toEqual(['Fix the door']);
    expect(titles(await items('/api/my/requests', OTHER))).toEqual([]);
    expect(titles(await items('/api/my/requests', ADMIN))).toEqual(['Fix the door', 'Paint the hall']);
  });

  let token: string;
  test("a party with a grant reaches only the rows its role's paths allow", async () => {
    const party = uuid(0xc1);
    ({ token } = await issueGrant(orm.asSystem(), { holder: party, holderKind: 'party', role: 'tenant', targetType: 'Contract', targetId: contractA.id }));
    expect(token.startsWith('grant_')).toBe(true);
    expect(titles(await items('/api/requests', token))).toEqual(['Fix the door']);
    expect(await status('GET', `/api/requests/${requestA.id}`, token)).toBe(200);
    expect(await status('GET', `/api/requests/${requestB.id}`, token)).toBe(404);
    expect((await items('/api/units', token)).map(unit => unit.name)).toEqual(['A1']);
    expect(await status('GET', `/api/contracts/${contractA.id}`, token)).toBe(200);
    expect(await status('GET', `/api/contracts/${contractB.id}`, token)).toBe(404);
    // A table the role's paths do not reach has no rows for it.
    expect(await items('/api/documents', token)).toEqual([]);
    // A row it makes is one its grant reaches, or no row at all.
    const made = await ok('POST', `/api/units/${unitA.id}/requests`, token, { title: 'Leaking tap' });
    expect(made.unitId).toBe(unitA.id);
    expect(await status('POST', `/api/units/${unitB.id}/requests`, token, { title: 'Not mine' })).toBe(403);
    expect((await orm.request.findMany({ title: { eq: 'Not mine' } } as never, { count: false })).rows).toEqual([]);
    // Each call is in the access log, by the grant's id.
    const calls = (await logRows()).filter(row => row.kind === 'grantCall' && row.principal === party);
    expect(calls.length).toBe(9);
    expect(calls[0]).toMatchObject({ principalKind: 'party', operation: 'listRequests' });
  });

  test("a role whose path is * reaches every row of the table, the grant's target among them", async () => {
    await orm.asSystem().role.createOne({ name: 'auditor', permissions: ['contracts.read'], reaches: ['Contract:*'] } as never);
    const issued = await issueGrant(orm.asSystem(), { holder: uuid(0xd1), holderKind: 'party', role: 'auditor', targetType: 'Contract', targetId: contractA.id });
    try {
      expect(await status('GET', `/api/contracts/${contractA.id}`, issued.token)).toBe(200);
      expect(await status('GET', `/api/contracts/${contractB.id}`, issued.token)).toBe(200);
    } finally {
      // Ended here, so the later reads of the active grants hold none of it.
      await revokeGrant(orm, issued.grant.id as string);
    }
  });

  test("an agent run's grant is refused as its holder: the agent dispatcher alone resolves a run's token", async () => {
    // As its grant's bare holder a run would act with its role's
    // permissions, narrowed to no one it acts for and held to no autonomy
    // (D194): agents.authenticator resolves it.
    const run = await issueGrant(orm.asSystem(), { holder: uuid(0xb2), holderKind: 'agent', role: 'tenant', targetType: 'Contract', targetId: contractA.id });
    try {
      const refused = await call('GET', '/api/requests', run.token);
      expect(refused.status).toBe(401);
      expect(JSON.stringify(refused.body)).toContain('agent dispatcher');
      expect(await status('GET', `/api/requests/${requestA.id}`, run.token)).toBe(401);
      expect((await logRows()).filter(row => row.principal === uuid(0xb2))).toEqual([]);
    } finally {
      // Ended here, so the later reads of the active grants hold none of it.
      await revokeGrant(orm, run.grant.id as string);
    }
  });

  test('an expired or revoked grant is refused on the very next call', async () => {
    const system = orm.asSystem();
    // Revoked.
    const revoked = await issueGrant(system, { holder: uuid(0xc2), holderKind: 'party', role: 'tenant', targetType: 'Contract', targetId: contractA.id });
    expect(await status('GET', '/api/requests', revoked.token)).toBe(200);
    await revokeGrant(orm, revoked.grant.id as string);
    expect(await status('GET', '/api/requests', revoked.token)).toBe(401);
    // Ended by its date.
    const dated = await issueGrant(system, {
      holder: uuid(0xc3),
      holderKind: 'party',
      role: 'tenant',
      targetType: 'Contract',
      targetId: contractA.id,
      endsAt: new Date(Date.now() + 400),
    });
    expect(await status('GET', '/api/requests', dated.token)).toBe(200);
    await new Promise(resolve => setTimeout(resolve, 500));
    expect(await status('GET', '/api/requests', dated.token)).toBe(401);
    // Ended by its target reaching a state.
    const closing = await issueGrant(system, {
      holder: uuid(0xc4),
      holderKind: 'party',
      role: 'tenant',
      targetType: 'Contract',
      targetId: contractB.id,
      endsWhen: { field: 'status', state: 'closed' },
    });
    expect(titles(await items('/api/requests', closing.token))).toEqual(['Paint the hall']);
    await system.contract.updateOne(contractB.id, { status: 'closed' } as never);
    expect(await status('GET', '/api/requests', closing.token)).toBe(401);
    // An unknown token is refused as an ended one is.
    expect(await status('GET', '/api/requests', 'grant_unknown')).toBe(401);
    // The read of the active grants holds only the first grant still.
    const active = await activeGrants(orm);
    expect(active.map(grant => grant.holder)).toEqual([uuid(0xc1)]);
  });

  test('a reveal opens the value for a holder of its permission and writes an access-log row', async () => {
    const masked = await ok('GET', `/api/contracts/${contractA.id}`, OWNER);
    expect(masked.terms).toBe('[secret]');
    expect((await logRows()).filter(row => row.kind === 'reveal')).toEqual([]);
    const opened = await ok('GET', `/api/contracts/${contractA.id}`, REVEALER);
    expect(opened.terms).toBe('Pay on the first');
    const reveals = (await logRows()).filter(row => row.kind === 'reveal');
    expect(reveals).toHaveLength(1);
    expect(reveals[0]).toMatchObject({ principal: ADMIN_ID, principalKind: 'user', operation: 'getContract', targetType: 'Contract', targetId: contractA.id, field: 'terms' });
    // A grant's holder whose role holds no reveal sees it masked.
    expect((await ok('GET', `/api/contracts/${contractA.id}`, token)).terms).toBe('[secret]');
  });

  test('the grants and the access log list through the routes the build serves over views of them', async () => {
    const ACCESS_ADMIN = `${ADMIN_ID}:access.admin`;
    expect(await status('GET', '/api/access/grants', OWNER)).toBe(403);
    expect(await status('GET', '/api/access/log', OWNER)).toBe(403);
    // Every grant the table holds, the ended and the revoked among them,
    // without its token's hash, which GrantView leaves out.
    const grants = await items('/api/access/grants', ACCESS_ADMIN);
    const stored = (await orm.asSystem().grant.findMany({}, { count: false, limit: 1000 })).rows;
    expect(grants.map(grant => grant.id).sort()).toEqual(stored.map(grant => grant.id).sort());
    expect(grants.map(grant => grant.holder)).toEqual(expect.arrayContaining([uuid(0xc1), uuid(0xc2), uuid(0xc3), uuid(0xc4)]));
    expect(grants.filter(grant => 'tokenHash' in grant)).toEqual([]);
    expect(grants.find(grant => grant.holder === uuid(0xc2))).toMatchObject({ holderKind: 'party', role: 'tenant', targetType: 'Contract', targetId: contractA.id });
    expect(grants.find(grant => grant.holder === uuid(0xc2)).revokedAt).not.toBeNull();
    expect(grants.find(grant => grant.holder === uuid(0xc1)).revokedAt).toBeNull();
    // The access log lists every row the table holds: the reveal and the
    // party's calls among them.
    const log = await items('/api/access/log', ACCESS_ADMIN);
    expect(log.map(row => row.id).sort()).toEqual((await logRows()).map(row => row.id).sort());
    expect(log.filter(row => row.kind === 'reveal')).toEqual([
      expect.objectContaining({ principal: ADMIN_ID, principalKind: 'user', operation: 'getContract', targetType: 'Contract', targetId: contractA.id, field: 'terms' }),
    ]);
    expect(log.filter(row => row.kind === 'grantCall' && row.principal === uuid(0xc1)).length).toBeGreaterThan(0);
  });

  test("a calendar feed shows only the rows its token's principal reads, busy or in full, and a rotated or revoked one is refused", async () => {
    const calendar = calendarRoutes({ orm, authenticate: request => callers({ headers: request.headers } as RequestContext) });
    await orm.asSystem().role.createOne({ name: 'calendarFeed', permissions: ['documents.calendar'] } as never);
    const day = 86_400_000;
    const window = `from=${new Date(Date.now() - day).toISOString()}&to=${new Date(Date.now() + day).toISOString()}`;
    const send = (path: string, as?: string, init: RequestInit = {}) =>
      calendar(new Request(`http://api.example.com${path}`, { ...init, headers: { ...(as ? { authorization: `Bearer ${as}` } : {}), 'content-type': 'application/json' } })) as Promise<Response>;
    // The read: each caller's documents of the day, as each reads them.
    const read = async (as: string) => ((await (await send(`/api/calendar?${window}`, as)).json()) as any).data.entries.map((entry: any) => entry.title);
    expect(await read(OWNER)).toEqual(expect.arrayContaining(['Secret plan', 'Public plan']));
    expect(await read(OTHER)).toContain('Public plan');
    expect(await read(OTHER)).not.toContain('Secret plan');
    // The other user's feed, in full, read by a client with no header.
    const issue = async (as: string, detail: string) => ((await (await send('/api/calendar/feeds', as, { method: 'POST', body: JSON.stringify({ role: 'calendarFeed', detail }) })).json()) as any).data;
    const other = await issue(OTHER, 'full');
    expect(other.token.startsWith('grant_')).toBe(true);
    const fetched = await send(other.path);
    expect(fetched.status).toBe(200);
    expect(fetched.headers.get('content-type')).toContain('text/calendar');
    const ics = await fetched.text();
    expect(ics).toContain('SUMMARY:Public plan');
    expect(ics).not.toContain('Secret plan');
    // The owner's busy feed holds the owner's private document, as Busy
    // and a reference, and no title.
    const owner = await issue(OWNER, 'busy');
    const busy = await (await send(owner.path)).text();
    expect(busy).not.toContain('plan');
    expect(busy.split('BEGIN:VEVENT').length - 1).toBe((await read(OWNER)).length);
    expect(busy).toMatch(/SUMMARY:Busy \([A-Z2-9]{8}\)/u);
    // A feed's token is no credential of any other route.
    expect(await status('GET', '/api/documents', other.token)).toBe(401);
    // Rotated: the old URL is refused, the new one reads.
    const rotated = ((await (await send(`/api/calendar/feeds/${other.id}/rotate`, OTHER, { method: 'POST' })).json()) as any).data;
    expect((await send(other.path)).status).toBe(401);
    expect((await send(rotated.path)).status).toBe(200);
    // Another user cannot revoke it; its holder can, and it is refused.
    expect((await send(`/api/calendar/feeds/${rotated.id}`, OWNER, { method: 'DELETE' })).status).toBe(404);
    expect((await send(`/api/calendar/feeds/${rotated.id}`, OTHER, { method: 'DELETE' })).status).toBe(204);
    expect((await send(rotated.path)).status).toBe(401);
    expect((await send('/api/calendar/feed/grant_unknown.ics')).status).toBe(401);
  });
});
