import { describe, expect, test } from 'bun:test';
import { grantAuthenticator, issueGrant } from './access.js';
import { createAgents } from './agents.js';
import type { RequestContext } from './operation.js';
import * as postgres from './postgres.js';

// An agent run's token (D194): the plain grantAuthenticator refuses it, and
// the agent dispatcher's authenticator alone resolves it, as the run, with
// its role's permissions narrowed to whom it acts for. Over an ORM held in
// memory, its repositories answering the filters the two read through; the
// dispatcher against Postgres is TestAgentRunsEndToEnd's
// (internal/generator/tsgen).

type Row = Record<string, unknown>;

const time = (value: unknown): unknown => (value instanceof Date ? value.getTime() : value);

/** Whether row holds filter: eq, isNull, lt, lte, gt, gte and anyOf, as the generated repositories read them. */
function holds(row: Row, filter: Row): boolean {
  return Object.entries(filter).every(([field, condition]) => {
    if (field === 'anyOf') return (condition as Row[]).some(one => holds(row, one));
    const c = condition as Row;
    const value = time(row[field]);
    if ('eq' in c && value !== time(c.eq)) return false;
    if ('isNull' in c && (value === null || value === undefined) !== c.isNull) return false;
    if ('lt' in c && !((value as number) < (time(c.lt) as number))) return false;
    if ('lte' in c && !((value as number) <= (time(c.lte) as number))) return false;
    if ('gt' in c && !((value as number) > (time(c.gt) as number))) return false;
    if ('gte' in c && !((value as number) >= (time(c.gte) as number))) return false;
    return true;
  });
}

/** A repository of rows in memory, its table's definition def. */
function table(def: Row, rows: Row[] = []) {
  let next = rows.length;
  return {
    def: { key: 'id', ...def },
    rows,
    async findOne(filter: Row) {
      return rows.find(row => holds(row, filter)) ?? null;
    },
    async findMany(filter: Row) {
      return { rows: rows.filter(row => holds(row, filter)) };
    },
    async createOne(input: Row) {
      const row = { id: `${String(def.name)}-${++next}`, revokedAt: null, ...input };
      rows.push(row);
      return row;
    },
    async updateOne(id: unknown, update: Row) {
      const row = rows.find(r => r.id === id)!;
      Object.assign(row, update);
      return row;
    },
  };
}

const RUN = 'run-1';
const BOSS = 'user-boss';

/** An ORM with the grants, the roles, a user's role and the agents' tables, and a running run that acts for the user. */
function world() {
  const orm: any = {
    grant: table({ name: 'Grant', access: 'grants' }),
    role: table({ name: 'UserRole', access: 'roles' }, [
      { id: 'role-agent', name: 'worker-agent', permissions: ['tasks.read', 'tasks.write', 'notes.read'], reaches: ['TaskNote:task'] },
      { id: 'role-boss', name: 'boss', permissions: ['tasks.read', 'notes.read'] },
      { id: 'role-party', name: 'guest', permissions: ['tasks.read'], reaches: [] },
    ]),
    userRoleGrant: table({ name: 'UserRoleGrant' }, [{ id: 'urg-1', userId: BOSS, roleId: 'role-boss' }]),
    agent: table({ name: 'Agent', agents: 'definitions' }),
    agentRun: table({ name: 'AgentRun', agents: 'runs' }, [
      { id: RUN, agent: 'reviewer', status: 'running', definitionVersion: 1, actingForKind: 'user', actingFor: BOSS, input: {} },
    ]),
    agentPolicy: table({ name: 'AgentPolicy', agents: 'policies' }),
    agentApproval: table({ name: 'AgentApproval', agents: 'approvals' }),
    agentControl: table({ name: 'AgentControl', agents: 'controls' }),
    asSystem: () => orm,
    withBehaviorCaller: () => orm,
    transaction: (fn: (tx: unknown) => unknown) => fn(orm),
  };
  const agents = createAgents({ orm, requests: {} as never, endpoint: 'http://localhost/api/agents' });
  return { orm, agents };
}

const bearer = (token: string): RequestContext => ({ headers: new Headers({ authorization: `Bearer ${token}` }) }) as unknown as RequestContext;

describe("an agent run's token (D194)", () => {
  test('the plain grantAuthenticator refuses a live run\'s token 401, and agents.authenticator resolves it as the run, its permissions narrowed', async () => {
    const { orm, agents } = world();
    const { grant, token } = await issueGrant(orm, { holder: RUN, holderKind: 'agent', role: 'worker-agent', targetType: 'Task', targetId: 'task-a' });
    const plain = grantAuthenticator({ orm });
    await expect(plain(bearer(token))).rejects.toMatchObject({ status: 401, message: expect.stringContaining('agent dispatcher') });

    const principal = (await agents.authenticator()(bearer(token)))!;
    expect(principal).toMatchObject({ subject: RUN, kind: 'agent', actingFor: { kind: 'user', subject: BOSS }, credential: { kind: 'grant', id: grant.id } });
    // The agent role's permissions narrowed to those the user it acts for holds.
    expect([...principal.permissions].sort()).toEqual(['notes.read', 'tasks.read']);
    expect(principal.grants).toEqual([{ id: String(grant.id), targetType: 'Task', targetId: 'task-a', reaches: { TaskNote: ['task'] } }]);
    expect(principal.claims).toMatchObject({ run: RUN, agent: 'reviewer' });
    expect((await agents.principalOf(new Request('http://x/', { headers: { authorization: `Bearer ${token}` } })))?.subject).toBe(RUN);
  });

  test("a run that ended is refused by agents.authenticator too, while its grant stands, and a party's grant runs as its holder through either", async () => {
    const { orm, agents } = world();
    const { token } = await issueGrant(orm, { holder: RUN, holderKind: 'agent', role: 'worker-agent', targetType: 'Task', targetId: 'task-a' });
    orm.agentRun.rows[0].status = 'done';
    await expect(agents.authenticator()(bearer(token))).rejects.toMatchObject({ status: 401 });
    expect(await agents.principalOf(new Request('http://x/', { headers: { authorization: `Bearer ${token}` } }))).toBeNull();

    const party = await issueGrant(orm, { holder: 'party-1', holderKind: 'party', role: 'guest', targetType: 'Task', targetId: 'task-a' });
    for (const authenticate of [grantAuthenticator({ orm }), agents.authenticator()]) {
      expect(await authenticate(bearer(party.token))).toMatchObject({ subject: 'party-1', kind: 'party', permissions: ['tasks.read'] });
    }
  });

  test("with runTokens inner, a run's token goes to inner: the dispatcher's authenticator resolves it, and another has none", async () => {
    const { orm, agents } = world();
    const { token } = await issueGrant(orm, { holder: RUN, holderKind: 'agent', role: 'worker-agent', targetType: 'Task', targetId: 'task-a' });
    const viaDispatcher = grantAuthenticator({ orm, inner: agents.authenticator(), runTokens: 'inner' });
    expect(await viaDispatcher(bearer(token))).toMatchObject({ subject: RUN, kind: 'agent' });
    const viaApp = grantAuthenticator({ orm, inner: async () => null, runTokens: 'inner' });
    expect(await viaApp(bearer(token))).toBeNull();
    expect(await grantAuthenticator({ orm, runTokens: 'inner' })(bearer(token))).toBeNull();
  });

  test('the authenticator that admits a run is no export of the package', () => {
    expect(Object.keys(postgres)).toContain('grantAuthenticator');
    expect(Object.keys(postgres).filter(name => /admitting/iu.test(name))).toEqual([]);
  });
});
