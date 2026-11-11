import { describe, expect, test } from 'bun:test';
import {
  HttpProblem,
  SecretValue,
  decodeCursor,
  encodeCursor,
  problemBody,
  refusedCursor,
  tableCreate,
  tableDelete,
  tableFilter,
  tableList,
  tableRead,
  tableUpdate,
  type RequestContext,
  type TableOperation,
  type TableRequest,
} from './index';

/*
The handlers of a generated router's table operations (D106), over a fake
of the repositories: what each kind reads and writes of its repository,
the mapping of a request's filter onto the repository's, and the problem
each error of the transaction is answered with. The generated router runs
them over the real repositories on Postgres in tsrestgen's
table_operations_runtime.test.ts.
*/

/** The columns of the tables the fake repositories hold, as a table's definition lists them. */
const issueColumns = [
  { name: 'id', column: 'id', required: true },
  { name: 'teamId', column: 'team_id', required: true, relation: 'Team' },
  { name: 'title', column: 'title', required: true },
  { name: 'body', column: 'body' },
  { name: 'assigneeId', column: 'assignee_id', relation: 'Member' },
  { name: 'secret', column: 'secret' },
  { name: 'deletedAt', column: 'deleted_at' },
];
const teamColumns = [
  { name: 'id', column: 'id', required: true },
  { name: 'key', column: '"key"', required: true },
  { name: 'name', column: '"name"', required: true },
];

type Call = readonly [method: string, ...args: unknown[]];

/** A fake of the repositories: one repository under member, whose methods answer what answers gives and record their calls. */
function fake(member: string, columns: unknown, answers: Partial<Record<'findOne' | 'findPage' | 'createOne' | 'updateOne' | 'deleteOne', (...args: any[]) => unknown>> = {}) {
  const calls: Call[] = [];
  const state: { actors: unknown[]; callers: unknown[]; transactions: number; failed?: unknown; open: boolean } = { actors: [], callers: [], transactions: 0, open: false };
  const method =
    (name: keyof typeof answers) =>
    async (...args: unknown[]) => {
      if (!state.open) throw new Error(`${name} ran outside the transaction`);
      calls.push([name, ...args]);
      const answer = answers[name];
      if (!answer) throw new Error(`the handler called ${name}, which the test did not expect`);
      return answer(...args);
    };
  const repository = {
    def: { columns },
    findOne: method('findOne'),
    findPage: method('findPage'),
    createOne: method('createOne'),
    updateOne: method('updateOne'),
    deleteOne: method('deleteOne'),
  };
  const orm = {
    as(principal: unknown) {
      state.actors.push(principal);
      return orm;
    },
    withBehaviorCaller(caller: unknown) {
      state.callers.push(caller);
      return orm;
    },
    async transaction<T>(fn: (tx: Record<string, unknown>) => Promise<T>): Promise<T> {
      state.transactions++;
      state.open = true;
      try {
        return await fn({ [member]: repository });
      } catch (error) {
        // The transaction rolls back, and throws what its work threw.
        state.failed = error;
        throw error;
      } finally {
        state.open = false;
      }
    },
  };
  return { orm, calls, state };
}

const ctx = { principal: { subject: 'sam', permissions: ['issues.write', 'issues.read', 'issues.write'] }, serviceCaller: null } as unknown as RequestContext;
const request = (path: Record<string, unknown> = {}, input?: unknown, query: Record<string, unknown> = {}): TableRequest => ({ path, query, input });

const issueView = ['id', 'teamId', 'title', 'body', 'assigneeId'] as const;
const read: TableOperation = { kind: 'read', type: 'Issue', repository: 'issue', key: 'id', view: issueView };
const list: TableOperation = { kind: 'list', type: 'Issue', repository: 'issue', parents: ['teamId'], view: issueView, search: 'q' };
const create: TableOperation = { kind: 'create', type: 'Issue', repository: 'issue', parents: ['teamId'], view: issueView, body: ['title', 'body', 'assigneeId'] };
const update: TableOperation = { kind: 'update', type: 'Issue', repository: 'issue', key: 'id', view: issueView, body: ['title', 'body', 'assigneeId'] };
const remove: TableOperation = { kind: 'delete', type: 'Issue', repository: 'issue', key: 'id', parents: ['teamId'], view: issueView };
const createTeam: TableOperation = { kind: 'create', type: 'Team', repository: 'team', view: ['id', 'key', 'name'], body: ['key', 'name'] };

const row = { id: 'i1', teamId: 't1', title: 'Crash on save', body: null, assigneeId: 'm1', secret: 's3cret', deletedAt: null, createdBy: 'sam' };
const view = { id: 'i1', teamId: 't1', title: 'Crash on save', body: null, assigneeId: 'm1' };

/** The problem a handler's refusal is, as the route writes it. */
async function problemOf(run: Promise<unknown>) {
  try {
    await run;
  } catch (error) {
    expect(error).toBeInstanceOf(HttpProblem);
    const { status, code, detail, details, errors } = problemBody(error as HttpProblem, 'req-1') as Record<string, unknown>;
    return { status, code, detail, ...(details !== undefined ? { details } : {}), ...(errors !== undefined ? { errors } : {}) };
  }
  throw new Error('expected a refusal');
}

/** A Postgres error as pg's DatabaseError carries it. */
function pgError(code: string, fields: Record<string, string> = {}): Error {
  return Object.assign(new Error(`driver text: relation "issue" violates ${fields.constraint ?? 'a constraint'}`), { code, severity: 'ERROR', ...fields });
}

describe('a read', () => {
  test('answers the view of the live row its key names, in one transaction, as the behaviors’ caller', async () => {
    const { orm, calls, state } = fake('issue', issueColumns, { findOne: () => row });
    expect(await tableRead(orm, read, ctx, request({ id: 'i1' }))).toEqual(view);
    expect(calls).toEqual([['findOne', { id: { eq: 'i1' } }]]);
    expect(state.transactions).toBe(1);
    // The repositories act as the route's principal, and their behaviors
    // run as it with its permissions, each once.
    expect(state.actors).toEqual([ctx.principal]);
    expect(state.callers).toEqual([{ subject: 'sam', permissions: ['issues.write', 'issues.read'] }]);
  });

  test('the view holds its members alone, in its order, a @decrypted one opened', async () => {
    const secret: TableOperation = { ...read, view: ['title', 'secret', 'id'], decrypted: ['secret'] };
    const { orm } = fake('issue', issueColumns, { findOne: () => ({ ...row, secret: new SecretValue('s3cret') }) });
    const answered = (await tableRead(orm, secret, ctx, request({ id: 'i1' }))) as Record<string, unknown>;
    expect(answered).toEqual({ title: 'Crash on save', secret: 's3cret', id: 'i1' });
    expect(Object.keys(answered)).toEqual(['title', 'secret', 'id']);
    expect(JSON.stringify(answered)).not.toContain('createdBy');
  });

  test('keeps to the path’s parents, and a key that holds no such row is 404 not_found', async () => {
    const under: TableOperation = { ...read, parents: ['teamId'] };
    const { orm, calls } = fake('issue', issueColumns, { findOne: () => null });
    expect(await problemOf(tableRead(orm, under, ctx, request({ id: 'i1', teamId: 't2' })))).toEqual({
      status: 404,
      code: 'not_found',
      detail: 'Issue not found',
    });
    expect(calls).toEqual([['findOne', { teamId: { eq: 't2' }, id: { eq: 'i1' } }]]);
  });

  test('a public route’s caller is no one', async () => {
    const { orm, state } = fake('issue', issueColumns, { findOne: () => row });
    await tableRead(orm, read, { principal: null, serviceCaller: null } as unknown as RequestContext, request({ id: 'i1' }));
    expect(state.actors).toEqual([null]);
    expect(state.callers).toEqual([{ subject: '' }]);
  });
});

describe('a request’s filter as the repository’s', () => {
  test('each member is the row’s, its operators as they are', () => {
    const made = new Date('2026-01-01T00:00:00Z');
    expect(
      tableFilter({
        status: { eq: 'open', in: ['open', 'done'] },
        priority: { gt: 1, gte: 2, lt: 8, lte: 9 },
        createdAt: { gte: made },
        dueOn: { isNull: true, lt: '2026-03-01' },
        assigneeId: { isNull: false },
      })
    ).toEqual({
      status: { eq: 'open', in: ['open', 'done'] },
      priority: { gt: 1, gte: 2, lt: 8, lte: 9 },
      createdAt: { gte: made },
      dueOn: { isNull: true, lt: '2026-03-01' },
      assigneeId: { isNull: false },
    });
    expect(tableFilter({})).toEqual({});
  });

  test('contains is the repository’s ilike, with what a pattern reads otherwise escaped', () => {
    expect(tableFilter({ title: { contains: 'crash' } })).toEqual({ title: { ilike: 'crash' } });
    expect(tableFilter({ title: { contains: '100%_done\\now', eq: 'x' } })).toEqual({ title: { eq: 'x', ilike: '100\\%\\_done\\\\now' } });
    expect('contains' in tableFilter({ title: { contains: 'a' } }).title!).toBe(false);
  });
});

describe('a list', () => {
  const rows = [row, { ...row, id: 'i2', title: 'Second' }];

  test('pages through findPage: the parents, the filter, the search, the limit, the sort and the cursor', async () => {
    const { orm, calls } = fake('issue', issueColumns, { findPage: () => ({ rows, next: { id: 'i2' } }) });
    const page = await tableList(
      orm,
      list,
      ctx,
      request({ teamId: 't1' }, undefined, { q: 'crash save' }),
      { cursor: encodeCursor({ id: 'i0' }), limit: 2, sort: { field: 'title', desc: true } },
      { title: { contains: '50%' }, assigneeId: { isNull: true } }
    );
    expect(calls).toEqual([
      [
        'findPage',
        { title: { ilike: '50\\%' }, assigneeId: { isNull: true }, searchText: 'crash save', allOf: [{ teamId: { eq: 't1' } }] },
        { limit: 2, sort: { field: 'title', desc: true }, after: { id: 'i0' } },
      ],
    ]);
    expect(page).toEqual({ items: [view, { ...view, id: 'i2', title: 'Second' }], next: encodeCursor({ id: 'i2' }) });
    expect(decodeCursor<unknown>(page.next!)).toEqual({ id: 'i2' });
  });

  test('without a sort, a cursor, a filter, a search or a parent it asks for the first page of every row', async () => {
    const plain: TableOperation = { kind: 'list', type: 'Team', repository: 'team', view: ['id', 'key'] };
    const { orm, calls } = fake('team', teamColumns, { findPage: () => ({ rows: [{ id: 't1', key: 'core', name: 'Core' }], next: null }) });
    expect(await tableList(orm, plain, ctx, request(), { limit: 20 })).toEqual({ items: [{ id: 't1', key: 'core' }], next: null });
    expect(calls).toEqual([['findPage', {}, { limit: 20 }]]);
  });

  test('a search of no text searches nothing', async () => {
    for (const q of [undefined, '']) {
      const { orm, calls } = fake('issue', issueColumns, { findPage: () => ({ rows: [], next: null }) });
      await tableList(orm, list, ctx, request({ teamId: 't1' }, undefined, q === undefined ? {} : { q }), { limit: 2 });
      expect(calls[0]![1]).toEqual({ allOf: [{ teamId: { eq: 't1' } }] });
    }
  });

  test('the parents hold beside a filter of the same member', async () => {
    const { orm, calls } = fake('issue', issueColumns, { findPage: () => ({ rows: [], next: null }) });
    await tableList(orm, list, ctx, request({ teamId: 't1' }), { limit: 2 }, { teamId: { eq: 't2' } });
    expect(calls[0]![1]).toEqual({ teamId: { eq: 't2' }, allOf: [{ teamId: { eq: 't1' } }] });
  });

  test('a cursor that is no page key, or names no row of the order, is the refused-cursor 400', async () => {
    const refused = problemBody(refusedCursor(), 'req-1');
    const want = { status: 400, code: refused.code, detail: refused.detail, details: refused.details };
    for (const key of ['green', 7, ['i1']]) {
      const { orm, calls } = fake('issue', issueColumns, { findPage: () => ({ rows: [], next: null }) });
      expect(await problemOf(tableList(orm, list, ctx, request({ teamId: 't1' }), { cursor: encodeCursor(key), limit: 2 }))).toEqual(want);
      expect(calls).toEqual([]);
    }
    // The repositories' PageKeyError, by its name.
    const gone = Object.assign(new Error('no Issue row has the key’s id'), { name: 'PageKeyError' });
    const { orm } = fake('issue', issueColumns, {
      findPage: () => {
        throw gone;
      },
    });
    expect(await problemOf(tableList(orm, list, ctx, request({ teamId: 't1' }), { cursor: encodeCursor({ id: 'i9' }), limit: 2 }))).toEqual(want);
  });
});

describe('a create', () => {
  test('sets the body’s members, one absent or null left unset, and each parent from the path', async () => {
    const { orm, calls } = fake('issue', issueColumns, { createOne: () => row });
    const body = { title: 'Crash on save', body: null, extra: 'not a member' };
    expect(await tableCreate(orm, create, ctx, request({ teamId: 't1' }, body))).toEqual(view);
    expect(calls).toEqual([['createOne', { title: 'Crash on save', teamId: 't1' }]]);
  });

  test('a unique key another row holds is 409 conflict, naming the key’s members and none of the driver’s words', async () => {
    for (const [detail, want] of [
      ['Key (key)=(core) already exists.', 'Team with that key already exists'],
      ['Key ("key", name)=(core, Core) already exists.', 'Team with that key and name already exists'],
      ['Schlüssel »(key)=(core)« existiert bereits.', 'Team with that key already exists'],
      // A key over an expression, and a violation that lists nothing.
      ['Key (lower(name::text))=(core) already exists.', 'Team conflicts with an existing row'],
      [undefined, 'Team conflicts with an existing row'],
    ] as const) {
      const failure = pgError('23505', { constraint: 'team_key_key', table: 'team', ...(detail === undefined ? {} : { detail }) });
      const { orm, state } = fake('team', teamColumns, {
        createOne: () => {
          throw failure;
        },
      });
      const problem = await problemOf(tableCreate(orm, createTeam, ctx, request({}, { key: 'core', name: 'Core' })));
      expect(problem).toEqual({ status: 409, code: 'conflict', detail: want });
      expect(JSON.stringify(problem)).not.toContain('driver text');
      // The transaction threw the driver's error, and the handler mapped it after.
      expect(state.failed).toBe(failure);
    }
  });

  test('a relation’s key that names no row is 400 at the member, and a parent’s is 404', async () => {
    const failing = (detail: string) =>
      fake('issue', issueColumns, {
        createOne: () => {
          throw pgError('23503', { constraint: 'fk_issue_x', table: 'issue', detail });
        },
      }).orm;
    expect(
      await problemOf(tableCreate(failing('Key (assignee_id)=(6f9619ff-8b86-4d01-b42d-00cf4fc964ff) is not present in table "member".'), create, ctx, request({ teamId: 't1' }, { title: 'x', assigneeId: 'm9' })))
    ).toEqual({
      status: 400,
      code: 'bad_request',
      detail: 'Request body does not match the declared input',
      details: { location: 'body', reason: 'validation failed' },
      errors: { assigneeId: [{ validator: 'reference', message: 'names no Member' }] },
    });
    expect(await problemOf(tableCreate(failing('Key (team_id)=(6f9619ff-8b86-4d01-b42d-00cf4fc964ff) is not present in table "team".'), create, ctx, request({ teamId: 't9' }, { title: 'x' })))).toEqual({
      status: 404,
      code: 'not_found',
      detail: 'Team not found',
    });
  });

  test('a write that stamps its caller and has none is 401 unauthorized', async () => {
    const { orm } = fake('issue', issueColumns, {
      createOne: () => {
        throw Object.assign(new Error('no acting user: the write stamps who made it'), { name: 'NoUserError' });
      },
    });
    expect(await problemOf(tableCreate(orm, create, ctx, request({ teamId: 't1' }, { title: 'x' })))).toEqual({
      status: 401,
      code: 'unauthorized',
      detail: 'Authentication required',
    });
  });
});

describe('an update', () => {
  test('sets the members the body sets: one left out leaves its column, and null clears one that may be null', async () => {
    const written = { ...row, title: 'Renamed', assigneeId: null };
    const { orm, calls } = fake('issue', issueColumns, { findOne: () => row, updateOne: () => written });
    expect(await tableUpdate(orm, update, ctx, request({ id: 'i1' }, { title: 'Renamed', assigneeId: null }))).toEqual({ ...view, title: 'Renamed', assigneeId: null });
    expect(calls).toEqual([
      ['findOne', { id: { eq: 'i1' } }],
      ['updateOne', 'i1', { title: 'Renamed', assigneeId: null }],
    ]);
  });

  test('null on a column that takes none is 400 at the member, and nothing is written', async () => {
    const { orm, calls } = fake('issue', issueColumns, { findOne: () => row });
    expect(await problemOf(tableUpdate(orm, update, ctx, request({ id: 'i1' }, { body: 'kept', title: null })))).toEqual({
      status: 400,
      code: 'bad_request',
      detail: 'Request body does not match the declared input',
      details: { location: 'body', reason: 'validation failed' },
      errors: { title: [{ validator: 'required', message: 'required field' }] },
    });
    expect(calls.map(call => call[0])).toEqual(['findOne']);
    // The database's own refusal of one, should the definition not say, is the same answer.
    const { orm: unsaid } = fake('issue', [{ name: 'title', column: 'title' }], {
      findOne: () => row,
      updateOne: () => {
        throw pgError('23502', { column: 'title', table: 'issue' });
      },
    });
    expect((await problemOf(tableUpdate(unsaid, update, ctx, request({ id: 'i1' }, { title: null })))).errors).toEqual({
      title: [{ validator: 'required', message: 'required field' }],
    });
  });

  test('a body that sets no member writes nothing and answers the row', async () => {
    for (const body of [{}, { extra: 1 }, { title: undefined }]) {
      const { orm, calls } = fake('issue', issueColumns, { findOne: () => row });
      expect(await tableUpdate(orm, update, ctx, request({ id: 'i1' }, body))).toEqual(view);
      expect(calls.map(call => call[0])).toEqual(['findOne']);
    }
  });

  test('a key that holds no live row is 404, before the body is read', async () => {
    const { orm, calls } = fake('issue', issueColumns, { findOne: () => null });
    expect(await problemOf(tableUpdate(orm, update, ctx, request({ id: 'i9' }, { title: null })))).toEqual({ status: 404, code: 'not_found', detail: 'Issue not found' });
    expect(calls.map(call => call[0])).toEqual(['findOne']);
    // A row that went between the read and the write: the repositories' NotFoundError, by its name.
    const { orm: gone } = fake('issue', issueColumns, {
      findOne: () => row,
      updateOne: () => {
        throw Object.assign(new Error('record not found'), { name: 'NotFoundError' });
      },
    });
    expect(await problemOf(tableUpdate(gone, update, ctx, request({ id: 'i1' }, { title: 'x' })))).toEqual({ status: 404, code: 'not_found', detail: 'Issue not found' });
  });

  test('a relation’s key that names no row is 400 at the member, whatever wraps the driver’s error', async () => {
    const { orm } = fake('issue', issueColumns, {
      findOne: () => row,
      updateOne: () => {
        throw new Error('orm: the update failed', { cause: pgError('23503', { detail: 'Key (assignee_id)=(x) is not present in table "member".' }) });
      },
    });
    expect((await problemOf(tableUpdate(orm, update, ctx, request({ id: 'i1' }, { assigneeId: 'm9' })))).errors).toEqual({
      assigneeId: [{ validator: 'reference', message: 'names no Member' }],
    });
  });
});

describe('a delete', () => {
  test('deletes the live row under its key and parents through deleteOne, and answers its view as it was', async () => {
    const { orm, calls } = fake('issue', issueColumns, { findOne: () => row, deleteOne: () => undefined });
    expect(await tableDelete(orm, remove, ctx, request({ id: 'i1', teamId: 't1' }))).toEqual(view);
    expect(calls).toEqual([
      ['findOne', { teamId: { eq: 't1' }, id: { eq: 'i1' } }],
      ['deleteOne', 'i1'],
    ]);
  });

  test('a row another table still references is 409 conflict, with none of the driver’s words', async () => {
    const referenced = pgError('23503', { constraint: 'fk_comment_issue', table: 'comment', detail: 'Key (id)=(x) is still referenced from table "comment".' });
    const { orm, state } = fake('issue', issueColumns, {
      findOne: () => row,
      deleteOne: () => {
        throw referenced;
      },
    });
    const problem = await problemOf(tableDelete(orm, remove, ctx, request({ id: 'i1', teamId: 't1' })));
    expect(problem).toEqual({ status: 409, code: 'conflict', detail: 'Issue is still referenced' });
    expect(JSON.stringify(problem)).not.toContain('comment');
    expect(state.failed).toBe(referenced);
  });

  test('a row under another parent is 404, and nothing is deleted', async () => {
    const { orm, calls } = fake('issue', issueColumns, { findOne: () => null });
    expect((await problemOf(tableDelete(orm, remove, ctx, request({ id: 'i1', teamId: 't2' })))).status).toBe(404);
    expect(calls.map(call => call[0])).toEqual(['findOne']);
  });
});

describe('an error of the transaction', () => {
  /** What an update answers when its write throws error. */
  const failing = (error: unknown) =>
    tableUpdate(
      fake('issue', issueColumns, {
        findOne: () => row,
        updateOne: () => {
          throw error;
        },
      }).orm,
      update,
      ctx,
      request({ id: 'i1' }, { title: 'x' })
    );

  test('a refusal of the behaviors’ core is answered as a behavior operation answers it', async () => {
    const refusal = {
      code: 'vetoed',
      status: 409,
      message: 'behavior StateRules vetoes update of Issue i1: a done issue takes no new title',
      details: { vetoCode: 'frozen' },
    };
    expect(await problemOf(failing(Object.assign(new Error(refusal.message), { name: 'RulesRefusal', refusal })))).toEqual({
      status: 409,
      code: 'vetoed',
      detail: refusal.message,
      details: { vetoCode: 'frozen' },
    });
    // The repositories' NotFoundError over the core's not_found keeps the core's words.
    const missing = { code: 'not_found', status: 404, message: 'Issue i1 does not exist' };
    const over = Object.assign(new Error('record not found', { cause: Object.assign(new Error(missing.message), { refusal: missing }) }), { name: 'NotFoundError' });
    expect(await problemOf(failing(over))).toEqual({ status: 404, code: 'not_found', detail: 'Issue i1 does not exist' });
    // A refusal of the core's own failure is the server's error, with none of its words.
    const internal = failing(Object.assign(new Error('core'), { refusal: { code: 'behavior_error', status: 500, message: 'a behavior threw' } }));
    await expect(internal).rejects.not.toBeInstanceOf(HttpProblem);
    await expect(internal).rejects.toThrow('behavior operation failed: behavior_error');
  });

  test('a problem the handler or the repositories threw is answered as it is', async () => {
    const problem = new HttpProblem(422, 'no', { code: 'unprocessable' });
    await expect(failing(problem)).rejects.toBe(problem);
  });

  test('any other error is thrown as it is, for the route’s own answer', async () => {
    const other = new Error('connection reset');
    await expect(failing(other)).rejects.toBe(other);
    // A Postgres error the handlers have no answer for.
    const deadlock = pgError('40P01');
    await expect(failing(deadlock)).rejects.toBe(deadlock);
    // A violation whose column is none of the body's.
    const unknown = pgError('23503', { detail: 'Key (owner_id)=(x) is not present in table "member".' });
    await expect(failing(unknown)).rejects.toBe(unknown);
  });

  test('a repository the repositories do not hold is the generator’s error', async () => {
    const { orm } = fake('team', teamColumns);
    await expect(tableRead(orm, read, ctx, request({ id: 'i1' }))).rejects.toThrow('the repositories hold no issue, the repository of Issue');
  });
});
