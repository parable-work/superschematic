import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { behaviorCaller, behaviorOperation, behaviorParams, behaviorPreconditions } from './behavior-routes';
import { mountOperation } from './hono';
import { HttpProblem, type OperationSpec, type RequestContext } from './index';
import { NotFoundError } from './orm';
import { RulesRefusal, type CoreRefusal } from './rules';

// The helpers a generated router's behavior operations run through (D103):
// the caller the core checks a behavior's permissions against, built from
// the route's principal or calling service, the input with each optional
// member that is null left out and its preconditions apart (D104), and
// each refusal of the core's
// answered as the problem of its status, code, message and details, a
// refusal of status 500 as the server's own internal error.

const vetoed: CoreRefusal = {
  code: 'vetoed',
  status: 409,
  class: 'BehaviorVetoError',
  message: 'behavior Workflow vetoes transition of Order 7: no transition leads from paid to draft; from paid it can move to shipped',
  details: { behavior: 'Workflow', use: 'status', action: 'transition', vetoCode: 'transition_not_allowed' },
};
const forbidden: CoreRefusal = {
  code: 'forbidden',
  status: 403,
  class: 'EngineError',
  message: "sam may not move Order 7's status from paid to shipped: the transition needs permission orders.ship",
};
const notFound: CoreRefusal = { code: 'not_found', status: 404, class: 'EngineError', message: 'Order 7 does not exist' };
const busy: CoreRefusal = {
  code: 'busy',
  status: 503,
  class: 'EngineError',
  message: 'transition of Order 7: the row kept moving while the command ran; it ran 8 times and nothing was written, so it may be tried again',
};
const internalRefusal: CoreRefusal = { code: 'internal', status: 500, class: 'EngineError', message: 'the layout names no table Order' };
const behaviorError: CoreRefusal = { code: 'behavior_error', status: 500, class: 'BehaviorError', message: 'Workflow panicked: secret state' };

/** What behaviorOperation throws for a run that throws error. */
async function thrown(error: unknown): Promise<unknown> {
  try {
    await behaviorOperation(async () => {
      throw error;
    });
  } catch (caught) {
    return caught;
  }
  throw new Error('behaviorOperation resolved');
}

/** A RequestContext with the principal and the calling service given. */
function context(principal: RequestContext['principal'], serviceCaller: RequestContext['serviceCaller'] = null): RequestContext {
  return { principal, serviceCaller } as RequestContext;
}

describe('behaviorCaller', () => {
  test("the route's principal, with each permission once", () => {
    expect(behaviorCaller(context({ subject: 'kim', permissions: ['orders.write', 'orders.ship', 'orders.write'] }))).toEqual({
      subject: 'kim',
      permissions: ['orders.write', 'orders.ship'],
    });
  });

  test('a calling service stands in for an end user, holding no permission', () => {
    expect(behaviorCaller(context(null, { deployable: 'fulfilment', serves: ['shop-orders'], subject: 'fulfilment@acme' }))).toEqual({
      subject: 'fulfilment',
      standsIn: true,
    });
  });

  test('the principal goes before a calling service that forwards it', () => {
    const caller = behaviorCaller(context({ subject: 'kim', permissions: [] }, { deployable: 'fulfilment', serves: [], subject: 'f' }));
    expect(caller).toEqual({ subject: 'kim', permissions: [] });
  });

  test('no one on a route without a caller', () => {
    expect(behaviorCaller(context(null))).toEqual({ subject: '' });
  });
});

describe('behaviorParams', () => {
  test('an optional member that is null is left out, as its absence', () => {
    expect(behaviorParams({ to: 'paid', from: null }, ['from'])).toEqual({ to: 'paid' });
    expect('from' in (behaviorParams({ to: 'paid', from: null }, ['from']) as object)).toBe(false);
  });

  test('an input with no null optional member is handed on as it is', () => {
    const input = { to: 'paid', from: 'draft' };
    expect(behaviorParams(input, ['from'])).toBe(input);
    expect(behaviorParams({ to: 'paid' }, ['from'])).toEqual({ to: 'paid' });
  });

  test('a null of a member that is not optional, and an input that is no object, stay as they are', () => {
    expect(behaviorParams({ to: null, from: null }, ['from'])).toEqual({ to: null });
    expect(behaviorParams(null, ['from'])).toBeNull();
    expect(behaviorParams([null], ['from'])).toEqual([null]);
  });

  test('a member omit names is left out whatever its value, the preconditions the handler presents apart (D104)', () => {
    expect(behaviorParams({ acknowledge: [1], preconditions: { Lease: { token: 3 } } }, ['acknowledge', 'preconditions'], ['preconditions'])).toEqual({ acknowledge: [1] });
    expect(behaviorParams({ preconditions: null, acknowledge: null }, ['acknowledge'], ['preconditions'])).toEqual({});
    const input = { to: 'running' };
    expect(behaviorParams(input, [], ['preconditions'])).toBe(input);
  });
});

describe('behaviorPreconditions', () => {
  test("the input's preconditions, each use's entry by its use id, with each null entry left out", () => {
    expect(behaviorPreconditions({ to: 'running', preconditions: { Lease: { token: 3 } } })).toEqual({ Lease: { token: 3 } });
    expect(behaviorPreconditions({ preconditions: { Lease: { token: 0 }, Other: null } })).toEqual({ Lease: { token: 0 } });
  });

  test('none for an input without them, with null or only null entries, or that is no object', () => {
    expect(behaviorPreconditions({ to: 'running' })).toBeUndefined();
    expect(behaviorPreconditions({ preconditions: null })).toBeUndefined();
    expect(behaviorPreconditions({ preconditions: { Lease: null } })).toBeUndefined();
    expect(behaviorPreconditions({ preconditions: [{ token: 1 }] })).toBeUndefined();
    expect(behaviorPreconditions(null)).toBeUndefined();
    expect(behaviorPreconditions(undefined)).toBeUndefined();
  });
});

describe('behaviorOperation', () => {
  test("resolves to the operation's result", async () => {
    expect(await behaviorOperation(async () => ({ from: 'draft', to: 'paid' }))).toEqual({ from: 'draft', to: 'paid' });
  });

  for (const refusal of [vetoed, forbidden, notFound, busy]) {
    test(`a ${refusal.code} refusal is the problem of its status, code, message and details`, async () => {
      const problem = await thrown(new RulesRefusal(refusal));
      expect(problem).toBeInstanceOf(HttpProblem);
      expect(problem).toMatchObject({ status: refusal.status, code: refusal.code, message: refusal.message, details: refusal.details });
      // The engine's class rides on the adapter's error, not on the route's problem.
      expect((problem as HttpProblem).extensions).toBeUndefined();
    });
  }

  test("the repository's NotFoundError is the not_found refusal it stands for", async () => {
    const error = Object.assign(new NotFoundError('Order 7 not found'), { cause: new RulesRefusal(notFound) });
    expect(await thrown(error)).toMatchObject({ status: 404, code: 'not_found', message: 'Order 7 does not exist' });
  });

  test("a refusal is recognized by its shape, whichever copy of the runtime threw it", async () => {
    const foreign = Object.assign(new Error('refused'), { refusal: { ...forbidden } });
    expect(await thrown(foreign)).toMatchObject({ status: 403, code: 'forbidden', message: forbidden.message });
    const wrapped = Object.assign(new Error('not found'), { cause: foreign });
    expect(await thrown(wrapped)).toMatchObject({ status: 403, code: 'forbidden' });
  });

  for (const refusal of [internalRefusal, behaviorError]) {
    test(`a ${refusal.code} refusal, of status 500, is rethrown as an error of the server's own`, async () => {
      const error = await thrown(new RulesRefusal(refusal));
      expect(error).not.toBeInstanceOf(HttpProblem);
      expect(error).toBeInstanceOf(Error);
      expect((error as Error).message).not.toContain(refusal.message);
      expect((error as Error).cause).toBeInstanceOf(RulesRefusal);
    });
  }

  test('any other error is rethrown as it is', async () => {
    const error = new Error('connection refused');
    expect(await thrown(error)).toBe(error);
    const problem = new HttpProblem(400, 'bad');
    expect(await thrown(problem)).toBe(problem);
  });
});

describe('a route that runs a behavior operation', () => {
  const spec: OperationSpec = {
    name: 'statusTransition',
    namespace: 'order',
    method: 'POST',
    path: '/api/order/{id}/status/transition',
    pathParams: [{ name: 'id', kind: 'string', required: true }],
    queryParams: [],
    bodyParams: [],
    auth: { public: true, required: false, permissions: [] },
    manual: false,
  };

  /** The answer of a route whose operation throws error. */
  async function answer(error: CoreRefusal): Promise<{ status: number; body: Record<string, unknown> }> {
    const app = new Hono();
    mountOperation(app, spec, () =>
      behaviorOperation(async () => {
        throw new RulesRefusal(error);
      })
    );
    const response = await app.request('/api/order/7/status/transition', { method: 'POST' });
    return { status: response.status, body: (await response.json()) as Record<string, unknown> };
  }

  test("answers a veto as the core's problem", async () => {
    const { status, body } = await answer(vetoed);
    expect(status).toBe(409);
    expect(body).toMatchObject({ status: 409, code: 'vetoed', detail: vetoed.message, details: vetoed.details });
    expect(body.class).toBeUndefined();
  });

  test("answers a refusal of status 500 as the server's internal error, with none of the core's words", async () => {
    const { status, body } = await answer(behaviorError);
    expect(status).toBe(500);
    expect(body).toMatchObject({ status: 500, code: 'internal_error' });
    expect(JSON.stringify(body)).not.toContain('secret state');
  });
});
