/*
The engine's HTTP API (D16): a fixed set of routes that carry the
namespace and the schema name as path parameters and resolve them per
request, so publishing a version mounts nothing. Every route goes through
the HTTP runtime (D15): its request id, rate limit, timeout, body limit,
authentication gate, success envelope and problem documents. The runtime
gate only requires a caller; what the caller may do is the engine's
access policy, asked on every call with the principal the deployment's
Authenticator returned.

An instance's sequence is its entity tag. A response that carries an
instance, or a behavior operation's result, sends `ETag: "<seq>"`, and
PATCH, DELETE and an operation honour `If-Match` inside the write
transaction (expectedSeq), so a lost update answers 412.

A schema-level behavior operation, which has no instance, has a route of
its own under the schema; it sends no ETag, since it names no instance.
Besides the instances and the event log, the routes serve a schema's
describe document and the namespace's tools document (tools/catalog.ts);
the MCP endpoint is the ./mcp entry point's.
*/

import { Hono } from 'hono';
import {
  HttpProblem,
  OperationResult,
  badRequest,
  decodeParams,
  envelopeResponse,
  notFound,
  problemResponse,
  unauthorized,
  type OperationSpec,
  type ParamSpec,
  type RequestContext,
} from '@superschematic/http-runtime';
import {
  disconnectSignal,
  errorHandler,
  mountManualOperation,
  mountOperation,
  notFoundHandler,
  type DecodedRequest,
  type RouterRuntimeOptions,
} from '@superschematic/http-runtime/hono';

import type { Principal } from '../access.js';
import type { Engine } from '../engine.js';
import type { InstanceRecord } from '../instances/store.js';
import { isPlainObject } from '../instances/patch.js';
import type { SchemaRecord } from '../registry/catalog.js';
import { engineProblem } from './problems.js';
import { checkStreamOptions, eventStream, type StreamOptions } from './stream.js';

export interface EngineHttpOptions extends RouterRuntimeOptions {
  /** Requests per minute per client on each route (the runtime's @rateLimit); absent or 0 for none. */
  rateLimitPerMinute?: number;
  /**
   * Seconds a request may take before it answers 504 (the runtime's
   * @timeout); absent or 0 for none. On the event stream it covers the
   * first page, not the stream.
   */
  timeoutSeconds?: number;
  /** The event stream's page size and heartbeat. */
  stream?: StreamOptions;
}

/** The media type of a JSON body. */
export const JSON_MEDIA_TYPE = 'application/json';
/** The media type of an instance update, RFC 7386. */
export const MERGE_PATCH_MEDIA_TYPE = 'application/merge-patch+json';

const SCHEMAS = '/namespaces/{namespace}/schemas';
const SCHEMA = `${SCHEMAS}/{name}`;
const INSTANCES = `${SCHEMA}/instances`;
const INSTANCE = `${INSTANCES}/{id}`;
const OPERATION = `${INSTANCE}/operations/{operation}`;
const SCHEMA_OPERATION = `${SCHEMA}/operations/{operation}`;
const EVENTS = '/namespaces/{namespace}/events';
const TOOLS = '/namespaces/{namespace}/tools';

const PATH_PARAMS: Record<string, ParamSpec> = {
  namespace: { name: 'namespace', kind: 'string', required: true },
  name: { name: 'name', kind: 'string', required: true },
  version: { name: 'version', kind: 'integer', required: true },
  id: { name: 'id', kind: 'string', required: true },
  operation: { name: 'operation', kind: 'string', required: true },
};

const EVENT_QUERY: readonly ParamSpec[] = [
  { name: 'after', kind: 'integer', required: false, min: 0 },
  { name: 'limit', kind: 'integer', required: false },
  { name: 'schema', kind: 'string', required: false },
  { name: 'instanceId', kind: 'string', required: false },
];

/**
 * engineApp builds the Hono app that serves an engine. Mount it where the
 * deployment wants it (`app.route('/api', engineApp(engine, options))`);
 * on its own it answers unknown paths and failures with problem
 * documents.
 */
export function engineApp(engine: Engine, options: EngineHttpOptions = {}): Hono {
  const stream = checkStreamOptions(options.stream ?? {});
  const deployed = options.onError;
  // One options object for every mount, so the routes share the runtime's
  // default rate-limit store.
  const runtime: RouterRuntimeOptions = {
    ...options,
    onError: async (error, ctx) => engineProblem(error) ?? (deployed ? await deployed(error, ctx) : undefined),
  };
  const app = new Hono();

  const spec = (name: string, method: OperationSpec['method'], path: string, parts: Partial<OperationSpec> = {}): OperationSpec => ({
    name,
    namespace: 'engine',
    method,
    path,
    pathParams: [...path.matchAll(/\{([^{}]+)\}/gu)].map((match) => PATH_PARAMS[match[1]]),
    queryParams: [],
    bodyParams: [],
    auth: { public: false, required: true, permissions: [] },
    manual: false,
    ...(options.rateLimitPerMinute ? { rateLimitPerMinute: options.rateLimitPerMinute } : {}),
    ...(options.timeoutSeconds ? { timeoutSeconds: options.timeoutSeconds } : {}),
    ...parts,
  });
  const body = { input: { parse: (value: unknown) => value, required: true } };
  const route = (operation: OperationSpec, handler: (ctx: RequestContext, request: DecodedRequest) => unknown) =>
    mountOperation(app, operation, async (ctx, request) => handler(ctx, request), runtime);

  route(spec('listSchemas', 'GET', SCHEMAS), (ctx, { path }) =>
    engine.schemas.list(principalOf(ctx), { namespace: path.namespace as string })
  );

  route(spec('defineSchema', 'POST', SCHEMAS, body), (ctx, { path, input }) => {
    const refused = mediaTypeRefusal(ctx, JSON_MEDIA_TYPE);
    if (refused) return refused;
    if (!isPlainObject(input)) throw badRequest('A schema document is a JSON object');
    return schemaView(engine.schemas.define(principalOf(ctx), input, { namespace: path.namespace as string }));
  });

  route(spec('getSchema', 'GET', SCHEMA), (ctx, { path }) => {
    const { namespace, name } = path as { namespace: string; name: string };
    const record = engine.schemas.live(principalOf(ctx), name, { namespace });
    if (!record) throw notFound(`schema ${name} has no live version in namespace ${namespace}`);
    return schemaView(record);
  });

  route(spec('getSchemaDraft', 'GET', `${SCHEMA}/draft`), (ctx, { path }) => {
    const { namespace, name } = path as { namespace: string; name: string };
    const record = engine.schemas.draft(principalOf(ctx), name, { namespace });
    if (!record) throw notFound(`schema ${name} has no draft in namespace ${namespace}`);
    return schemaView(record);
  });

  route(spec('getSchemaVersion', 'GET', `${SCHEMA}/versions/{version}`), (ctx, { path }) => {
    const { namespace, name, version } = path as { namespace: string; name: string; version: number };
    const record = engine.schemas.version(principalOf(ctx), name, version, { namespace });
    if (!record) throw notFound(`schema ${name} has no version ${version} in namespace ${namespace}`);
    return schemaView(record);
  });

  route(spec('describeSchema', 'GET', `${SCHEMA}/describe`), (ctx, { path }) => {
    const { namespace, name } = path as { namespace: string; name: string };
    return engine.tools.describe(principalOf(ctx), name, { namespace });
  });

  route(spec('publishSchema', 'POST', `${SCHEMA}/publish`), (ctx, { path }) => {
    const { namespace, name } = path as { namespace: string; name: string };
    return engine.schemas.publish(principalOf(ctx), name, { namespace });
  });

  route(
    spec('listInstances', 'GET', INSTANCES, {
      queryParams: [
        { name: 'limit', kind: 'integer', required: false },
        { name: 'cursor', kind: 'string', required: false },
      ],
    }),
    (ctx, { path, query }) => {
      const { namespace, name } = path as { namespace: string; name: string };
      const { limit, cursor } = query as { limit?: number; cursor?: string };
      return engine.instances.list(principalOf(ctx), name, { namespace, limit, cursor });
    }
  );

  route(spec('createInstance', 'POST', INSTANCES, body), (ctx, { path, input }) => {
    const refused = mediaTypeRefusal(ctx, JSON_MEDIA_TYPE);
    if (refused) return refused;
    const { namespace, name } = path as { namespace: string; name: string };
    const { id, data, behaviors } = createInput(input);
    const record = engine.instances.create(principalOf(ctx), name, data, { namespace, id, behaviors });
    return new OperationResult(record, 201, {
      etag: etagOf(record),
      location: `${ctx.path.replace(/\/+$/u, '')}/${encodeURIComponent(record.id)}`,
    });
  });

  route(spec('getInstance', 'GET', INSTANCE), (ctx, { path }) => {
    const { namespace, name, id } = path as { namespace: string; name: string; id: string };
    const record = engine.instances.get(principalOf(ctx), name, id, { namespace });
    if (!record) throw notFound(`${name} ${id} does not exist in namespace ${namespace}`);
    return new OperationResult(record, 200, { etag: etagOf(record) });
  });

  route(spec('updateInstance', 'PATCH', INSTANCE, body), (ctx, { path, input }) => {
    const refused = mediaTypeRefusal(ctx, MERGE_PATCH_MEDIA_TYPE);
    if (refused) return refused;
    const { namespace, name, id } = path as { namespace: string; name: string; id: string };
    const principal = principalOf(ctx);
    const expectedSeq = expectedSeqOf(ctx, () => engine.instances.get(principal, name, id, { namespace }));
    const record = engine.instances.update(principal, name, id, input, { namespace, expectedSeq });
    return new OperationResult(record, 200, { etag: etagOf(record) });
  });

  route(spec('deleteInstance', 'DELETE', INSTANCE), (ctx, { path }) => {
    const { namespace, name, id } = path as { namespace: string; name: string; id: string };
    const principal = principalOf(ctx);
    const expectedSeq = expectedSeqOf(ctx, () => engine.instances.get(principal, name, id, { namespace }));
    if (!engine.instances.delete(principal, name, id, { namespace, expectedSeq })) {
      throw notFound(`${name} ${id} does not exist in namespace ${namespace}`);
    }
    return null;
  });

  // A behavior operation: the body is its parameters, {} when absent.
  route(spec('invokeOperation', 'POST', OPERATION, { input: { parse: (value: unknown) => value, required: false } }), (ctx, { path, input }) => {
    if (input !== undefined) {
      const refused = mediaTypeRefusal(ctx, JSON_MEDIA_TYPE);
      if (refused) return refused;
    }
    const { namespace, name, id, operation } = path as { namespace: string; name: string; id: string; operation: string };
    const principal = principalOf(ctx);
    const expectedSeq = expectedSeqOf(ctx, () => engine.instances.get(principal, name, id, { namespace }));
    const outcome = engine.instances.operate(principal, name, id, operation, input ?? {}, { namespace, expectedSeq });
    return new OperationResult(outcome.result, 200, { etag: `"${outcome.seq}"` });
  });

  // A schema-level behavior operation: the body is its parameters, {} when absent.
  route(
    spec('invokeSchemaOperation', 'POST', SCHEMA_OPERATION, { input: { parse: (value: unknown) => value, required: false } }),
    (ctx, { path, input }) => {
      if (input !== undefined) {
        const refused = mediaTypeRefusal(ctx, JSON_MEDIA_TYPE);
        if (refused) return refused;
      }
      const { namespace, name, operation } = path as { namespace: string; name: string; operation: string };
      return new OperationResult(engine.instances.invokeSchema(principalOf(ctx), name, operation, input ?? {}, { namespace }), 200);
    }
  );

  route(spec('listTools', 'GET', TOOLS), (ctx, { path }) => engine.tools.manifest(principalOf(ctx), { namespace: path.namespace as string }));

  // The event log: a JSON page, or with Accept: text/event-stream the
  // stream. A manual route, so the handler owns the streaming response;
  // the runtime still applies the rate limit, the timeout and the gate.
  mountManualOperation(
    app,
    spec('readEvents', 'GET', EVENTS, { queryParams: EVENT_QUERY, manual: true }),
    (c, ctx) => {
      const principal = principalOf(ctx);
      const namespace = c.req.param('namespace') as string;
      const query = decodeParams('query', EVENT_QUERY, (name) => {
        const values = ctx.query.getAll(name);
        return values.length > 0 ? values : undefined;
      }) as { after?: number; limit?: number; schema?: string; instanceId?: string };
      const after = lastEventIdOf(ctx) ?? query.after;
      const read = { namespace, schema: query.schema, instanceId: query.instanceId, after };
      if (!wantsEventStream(ctx.headers.get('accept'))) {
        return envelopeResponse(engine.events.read(principal, { ...read, limit: query.limit }), ctx.requestId);
      }
      return eventStream({ engine, principal, read, options: stream, signal: disconnectSignal(c), requestId: ctx.requestId });
    },
    runtime
  );

  app.notFound(notFoundHandler());
  app.onError(errorHandler((error) => engineProblem(error)));
  return app;
}

/**
 * The engine's principal for the caller the runtime gate established. The
 * HTTP runtime's Principal has the engine's shape; a copy keeps only its
 * members. A principal without a subject is no caller.
 */
function principalOf(ctx: RequestContext): Principal {
  const caller = ctx.principal;
  if (!caller || typeof caller.subject !== 'string' || caller.subject === '') {
    throw unauthorized();
  }
  return {
    subject: caller.subject,
    permissions: caller.permissions,
    ...(caller.claims !== undefined ? { claims: caller.claims } : {}),
  };
}

/** A schema version as the API returns it: the stored record without its canonical text, which the hash identifies. */
function schemaView(record: SchemaRecord): Omit<SchemaRecord, 'canonical'> {
  const { canonical: _canonical, ...view } = record;
  return view;
}

/**
 * The body of a create: `{ "data": {...} }`, with an optional `"id"` and
 * optional `"behaviors"`, the parameters it gives the type's behaviors by
 * behavior name, which the engine checks (null is none).
 */
function createInput(input: unknown): { id?: string; data: unknown; behaviors?: Record<string, unknown> } {
  if (!isPlainObject(input)) {
    throw badRequest('A create body is a JSON object with the instance as "data"');
  }
  const unknown = Object.keys(input).filter((key) => key !== 'id' && key !== 'data' && key !== 'behaviors');
  if (unknown.length > 0) {
    throw badRequest(`A create body has only "id", "data" and "behaviors", not ${unknown.map((key) => JSON.stringify(key)).join(', ')}`);
  }
  if (!('data' in input)) {
    throw badRequest('A create body needs the instance as "data"');
  }
  if (input.id !== undefined && typeof input.id !== 'string') {
    throw badRequest('"id" is a string');
  }
  const behaviors = input.behaviors ?? undefined;
  return {
    ...(input.id !== undefined ? { id: input.id } : {}),
    data: input.data,
    ...(behaviors !== undefined ? { behaviors: behaviors as Record<string, unknown> } : {}),
  };
}

function etagOf(record: InstanceRecord): string {
  return `"${record.seq}"`;
}

/**
 * The sequence If-Match names, for expectedSeq. `*` and an absent header
 * name none: the instance must only exist. A weak tag never matches (If-Match
 * compares strongly), nor does a tag that is not a sequence, so a list with
 * no sequence in it is 0, which no instance is at. From a list of several,
 * the one the instance is at, if any; the engine checks it again inside the
 * write, so a change in between still answers 412.
 */
function expectedSeqOf(ctx: RequestContext, current: () => InstanceRecord | undefined): number | undefined {
  const header = ctx.headers.get('if-match');
  if (header === null || header.trim() === '*') {
    return undefined;
  }
  const seqs = new Set<number>();
  for (const tag of header.split(',')) {
    const match = /^"([0-9]{1,15})"$/u.exec(tag.trim());
    if (match) {
      seqs.add(Number(match[1]));
    }
  }
  if (seqs.size <= 1) {
    return seqs.size === 1 ? [...seqs][0] : 0;
  }
  const seq = current()?.seq;
  return seq !== undefined && seqs.has(seq) ? seq : 0;
}

/** A 415 problem, with Accept-Patch on PATCH, unless the body is of the media type the route takes. */
function mediaTypeRefusal(ctx: RequestContext, expected: string): Response | undefined {
  const type = ctx.headers.get('content-type')?.split(';')[0]?.trim().toLowerCase();
  if (type === expected) {
    return undefined;
  }
  const response = problemResponse(
    new HttpProblem(415, `The body of ${ctx.method} ${ctx.operation.path} is ${expected}, not ${type || 'untyped'}`, {
      code: 'unsupported_media_type',
    }),
    ctx.requestId
  );
  if (ctx.method === 'PATCH') {
    response.headers.set('accept-patch', expected);
  }
  return response;
}

/** The cursor a reconnecting EventSource sends; it wins over `after`, which stays in the URL it reconnects to. */
function lastEventIdOf(ctx: RequestContext): number | undefined {
  const header = ctx.headers.get('last-event-id');
  if (header === null || header.trim() === '') {
    return undefined;
  }
  if (!/^[0-9]{1,15}$/u.test(header.trim())) {
    throw badRequest('Last-Event-ID is an event cursor, a non-negative integer');
  }
  return Number(header.trim());
}

/** Whether Accept asks for text/event-stream with a quality above zero. */
function wantsEventStream(accept: string | null): boolean {
  if (!accept) {
    return false;
  }
  return accept.split(',').some((range) => {
    const [type, ...parameters] = range.split(';').map((part) => part.trim().toLowerCase());
    if (type !== 'text/event-stream') {
      return false;
    }
    const quality = parameters.find((parameter) => parameter.startsWith('q='));
    return quality === undefined || Number(quality.slice(2)) > 0;
  });
}
