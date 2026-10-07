// What the client's tests share: an engine with a Task schema served in
// process under /api, and a client whose fetch hands each request to the
// app's fetch, so no request leaves the process.
import assert from 'node:assert/strict';

import type { Authenticator } from '@superschematic/http-runtime';
import { Hono } from 'hono';

import { EngineClient, EngineProblem, type EngineClientOptions } from '../dist/client/index.js';
import { engineApp, type EngineHttpOptions } from '../dist/http/index.js';
import type { AccessPolicy, Engine, EngineOptions } from '../dist/index.js';
import { hold, openMetaSchema, testBehaviors } from './behavior-fixtures.ts';
import { alice, openTestEngine, schemaDocument, type Field } from './helpers.ts';

/** A bearer token is the caller's subject; reader may only read, outsider nothing. */
export const authenticate: Authenticator = async (ctx) => {
  const token = ctx.bearerToken;
  return token === 'alice' || token === 'bob' || token === 'reader' || token === 'outsider' ? { subject: token, permissions: [] } : null;
};

export const policy: AccessPolicy = ({ principal, action }) => {
  if (principal.subject === 'outsider') {
    return false;
  }
  if (principal.subject === 'reader') {
    return action === 'read';
  }
  return true;
};

/**
 * A task moves from todo through doing to done, and Dependencies gates
 * done; test.Counter counts and test.Hold fences writes by a generation
 * they present as its precondition.
 */
export function taskDocument(extraFields: Field[] = []): Record<string, unknown> {
  const document = schemaDocument('Task', [{ name: 'title', typeRef: { name: 'string' }, required: true }, ...extraFields]) as {
    types: { Task: Record<string, unknown> };
  };
  document.types.Task.behaviors = [
    {
      name: 'Workflow',
      config: {
        states: ['todo', 'doing', 'done'],
        transitions: [
          { from: 'todo', to: 'doing' },
          { from: 'doing', to: 'done' },
        ],
      },
    },
    { name: 'Dependencies' },
    { name: 'test.Counter' },
    { name: 'test.Hold' },
  ];
  return document;
}

export interface Sent {
  method: string;
  /** The path and query. */
  url: string;
  headers: Headers;
}

export interface Served {
  engine: Engine;
  app: Hono;
  /** Every request a client of this server sent, in order. */
  requests: Sent[];
  /** The fetch each client sends with: the app's, in process. */
  fetch: (input: string, init: RequestInit) => Promise<Response>;
  /** A client of this server, as alice unless the options say otherwise. */
  client(options?: Partial<EngineClientOptions>): EngineClient;
}

/** serve opens an engine with the Task schema published and mounts its app under /api. */
export function serve(httpOptions: EngineHttpOptions = {}, engineOptions: Partial<EngineOptions> = {}): Served {
  const engine = openTestEngine({ policy, metaSchema: openMetaSchema(), behaviors: [...testBehaviors, hold], ...engineOptions });
  engine.schemas.define(alice, taskDocument());
  engine.schemas.publish(alice, 'Task');
  const app = new Hono();
  app.route('/api', engineApp(engine, { authenticate, ...httpOptions }));
  const requests: Sent[] = [];
  const fetch = async (input: string, init: RequestInit): Promise<Response> => {
    const request = new Request(input, init);
    const url = new URL(request.url);
    requests.push({ method: request.method, url: `${url.pathname}${url.search}`, headers: request.headers });
    return app.fetch(request);
  };
  return {
    engine,
    app,
    requests,
    fetch,
    client: (options = {}) => new EngineClient({ baseUrl: 'http://engine.test/api', fetch, auth: { token: 'alice' }, ...options }),
  };
}

/** refused awaits a call that must be refused with the status and code, and returns its problem. */
export async function refused<T extends EngineProblem = EngineProblem>(call: Promise<unknown>, status: number, code: string | undefined): Promise<T> {
  try {
    await call;
  } catch (error) {
    assert.ok(error instanceof EngineProblem, `expected an EngineProblem, got ${error instanceof Error ? `${error.name}: ${error.message}` : String(error)}`);
    assert.deepEqual([error.status, error.code], [status, code], error.message);
    return error as T;
  }
  return assert.fail(`expected ${status} ${String(code)}`);
}

/** until waits for a condition, checking every few milliseconds, and fails after a while. */
export async function until(condition: () => boolean, what: string, timeoutMs = 5_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!condition()) {
    if (Date.now() > deadline) {
      assert.fail(`timed out waiting for ${what}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}
