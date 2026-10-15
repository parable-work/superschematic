/*
engineApp and engineMcp on the Rust engine (D58): thin Hono adapters over
the engine's own routes (api::handle). The engine owns routing, decoding,
the media types, ETags and If-Match, the Preconditions header, the
envelope and the problem documents, Location, the D37 stand-in rule, the
event stream's every byte and MCP's JSON-RPC; these adapters keep what the
host keeps: the HTTP runtime's request id, rate limit, timeout, body limit
and authentication gate, the identity service's CORS and its /auth routes,
the mount's tool filter and its onError.

Every route of routes() mounts through the runtime's mountManualOperation
(engineApp the API's, engineMcp the MCP endpoint's), and its handler hands
the request to the addon: its method, the prefix it is mounted under, its
path and query as received, its headers, its body's bytes, and the caller
the runtime established. The answer's body comes back as bytes, or as an
event stream the engine holds open, which a ReadableStream pulls on this
thread: a pull that finds nothing waits for a commit (events.watch, whose
commits from elsewhere arrive through a weak threadsafe function) or the
heartbeat, and pulls again. A stream route's timeout covers its first page
alone, since the handler returns once the stream starts.

A tool filter written in TypeScript (the mount's tools option) is asked on
this thread, during the request. A behavior written in TypeScript that
throws inside a request answers 500, as on the TypeScript engine; the
mount's onError is asked about what it threw first.
*/

import { readFileSync } from 'node:fs';

import { HttpProblem, problemResponse, type OperationSpec, type ParamSpec, type RequestContext } from '@superschematic/http-runtime';
import {
  DEFAULT_BODY_LIMIT_BYTES,
  disconnectSignal,
  errorHandler,
  mountManualOperation,
  notFoundHandler,
  type RouterRuntimeOptions,
} from '@superschematic/http-runtime/hono';
import {
  identityCorsRoutes,
  identityOperationSpec,
  identityRouterOptions,
  identityRoutes,
  mountIdentityOperations,
} from '@superschematic/http-runtime/identity';
import { Hono, type Context } from 'hono';

import type { EngineHttpOptions } from '../http/app.js';
import { callerAuthenticator, principalOf } from '../http/callers.js';
import { engineProblem } from '../http/problems.js';
import { checkStreamOptions } from '../http/stream.js';
import type { EngineMcpOptions } from '../mcp/app.js';
import type { ToolFilter } from '../tools/catalog.js';
import type { EngineRoute, NativeEngine } from './engine.js';

// The routes whose body the runtime's body limit caps, as the TypeScript
// app's routes that take an input.
const BODY_ROUTES = new Set(['createNamespace', 'defineSchema', 'createInstance', 'updateInstance', 'invokeOperation', 'invokeSchemaOperation', 'searchSchemas']);

const INTEGER_PARAMS = new Set(['version']);

interface Mount {
  readonly routes: readonly EngineRoute[];
  readonly runtime: RouterRuntimeOptions;
  readonly rateLimitPerMinute?: number;
  readonly timeoutSeconds?: number;
  readonly bodyLimitBytes?: number;
  readonly tools?: ToolFilter;
  readonly onError?: RouterRuntimeOptions['onError'];
  /** The engine's own options for the request: stream, mcp. */
  readonly options: Record<string, unknown>;
  readonly heartbeatMs: number;
}

/** nativeEngineApp is engineApp for an engine on the Rust engine. */
export function nativeEngineApp(engine: NativeEngine, options: EngineHttpOptions = {}): Hono {
  const stream = checkStreamOptions(options.stream ?? {});
  const { identity } = options;
  const deployed = options.onError;
  const runtime: RouterRuntimeOptions = {
    ...options,
    authenticate: callerAuthenticator(identity ? identityRouterOptions(options, 'engineApp').authenticate : options.authenticate),
    onError: async (error, ctx) => engineProblem(error) ?? (deployed ? await deployed(error, ctx) : undefined),
  };
  const app = new Hono();
  const cors = identity ? identityCorsRoutes(app, identity) : () => {};
  mountRoutes(app, engine, cors, {
    routes: engine.routes().filter((route) => !route.mcp),
    runtime,
    rateLimitPerMinute: options.rateLimitPerMinute,
    timeoutSeconds: options.timeoutSeconds,
    bodyLimitBytes: options.bodyLimitBytes,
    tools: options.tools,
    onError: deployed,
    options: { stream },
    heartbeatMs: stream.heartbeatMs,
  });
  if (identity) {
    const administration = options.identityRoutes?.administration ?? identity.store.hasRoles();
    const sets = {
      sessions: { register: options.identityRoutes?.register === true },
      ...(administration ? { administration: {} } : {}),
      namespace: 'engine',
      rateLimitPerMinute: options.rateLimitPerMinute,
      timeoutSeconds: options.timeoutSeconds,
    };
    for (const entry of identityRoutes(identity, sets)) cors(identityOperationSpec(identity, entry, sets));
    mountIdentityOperations(app, identity, sets, runtime);
  }
  app.notFound(notFoundHandler());
  app.onError(errorHandler((error) => engineProblem(error)));
  return app;
}

/** nativeEngineMcp is engineMcp for an engine on the Rust engine: its MCP endpoint, the engine's own. */
export function nativeEngineMcp(engine: NativeEngine, options: EngineMcpOptions = {}): Hono {
  const deployed = options.onError;
  const authenticate = options.identity ? identityRouterOptions(options, 'engineMcp').authenticate : options.authenticate;
  const runtime: RouterRuntimeOptions = {
    ...options,
    authenticate: callerAuthenticator(authenticate),
    onError: async (error, ctx) => engineProblem(error) ?? (deployed ? await deployed(error, ctx) : undefined),
  };
  const app = new Hono();
  const cors = options.identity ? identityCorsRoutes(app, options.identity) : () => {};
  const serverInfo = options.serverInfo ?? packageInfo();
  mountRoutes(app, engine, cors, {
    routes: engine.routes().filter((route) => route.mcp),
    runtime,
    rateLimitPerMinute: options.rateLimitPerMinute,
    timeoutSeconds: options.timeoutSeconds,
    tools: options.tools,
    onError: deployed,
    options: { mcp: { serverInfo, ...(options.instructions ? { instructions: options.instructions } : {}) } },
    heartbeatMs: 5_000,
  });
  app.notFound(notFoundHandler());
  app.onError(errorHandler((error) => engineProblem(error)));
  return app;
}

function mountRoutes(app: Hono, engine: NativeEngine, cors: (spec: OperationSpec) => void, mount: Mount): void {
  for (const route of mount.routes) {
    const spec: OperationSpec = {
      name: route.name,
      namespace: 'engine',
      method: route.method,
      path: route.path,
      pathParams: [...route.path.matchAll(/\{([^{}]+)\}/gu)].map(
        (match): ParamSpec => ({ name: match[1], kind: INTEGER_PARAMS.has(match[1]) ? 'integer' : 'string', required: true })
      ),
      queryParams: [],
      bodyParams: [],
      auth: { public: false, required: true, permissions: [] },
      manual: true,
      ...(mount.rateLimitPerMinute ? { rateLimitPerMinute: mount.rateLimitPerMinute } : {}),
      ...(mount.timeoutSeconds ? { timeoutSeconds: mount.timeoutSeconds } : {}),
    };
    cors(spec);
    const bodyLimitBytes = BODY_ROUTES.has(route.name) ? (mount.bodyLimitBytes ?? DEFAULT_BODY_LIMIT_BYTES) : undefined;
    mountManualOperation(app, spec, (c, ctx) => serve(engine, route, mount, c, ctx), mount.runtime, bodyLimitBytes === undefined ? {} : { bodyLimitBytes });
  }
}

async function serve(engine: NativeEngine, route: EngineRoute, mount: Mount, c: Context, ctx: RequestContext): Promise<Response> {
  const principal = principalOf(ctx);
  const caller =
    principal.service?.standsIn === true
      ? { service: { deployable: principal.service.deployable, serves: principal.service.serves, subject: principal.service.subject } }
      : {
          principal: { subject: principal.subject, permissions: principal.permissions, ...(principal.claims !== undefined ? { claims: principal.claims } : {}) },
          ...(principal.service !== undefined
            ? { service: { deployable: principal.service.deployable, serves: principal.service.serves, subject: principal.service.subject } }
            : {}),
        };
  const url = new URL(c.req.url);
  const parts = url.pathname.split('/');
  const segments = route.path.split('/').length - 1;
  const prefix = parts.slice(0, Math.max(1, parts.length - segments)).join('/');
  const headers: string[][] = [];
  c.req.raw.headers.forEach((value, name) => headers.push([name, value]));
  const body = c.req.method === 'GET' || c.req.method === 'HEAD' ? undefined : Buffer.from(await c.req.arrayBuffer());
  const head = {
    method: c.req.method,
    prefix,
    path: url.pathname,
    query: url.search.startsWith('?') ? url.search.slice(1) : url.search,
    headers,
    caller,
    requestId: ctx.requestId,
    options: mount.options,
  };
  const { answer, thrown } = engine.handleHttp(head, body !== undefined && body.length > 0 ? body : undefined, mount.tools);
  if (thrown !== undefined && mount.onError !== undefined) {
    // What a behavior written in TypeScript threw: the mount's to map.
    const mapped = await mount.onError(thrown.value, ctx);
    if (mapped instanceof Response) {
      return mapped;
    }
    if (mapped instanceof HttpProblem) {
      return problemResponse(mapped, ctx.requestId);
    }
  }
  const responseHeaders = new Headers();
  for (const [name, value] of answer.headers) {
    responseHeaders.append(name, value);
  }
  if (answer.stream !== undefined && answer.stream !== null) {
    return new Response(eventBody(engine, answer.stream, mount.heartbeatMs, disconnectSignal(c)), { status: answer.status, headers: responseHeaders });
  }
  const bytes = answer.body ?? null;
  return new Response(bytes === null || answer.status === 204 || answer.status === 304 ? null : new Uint8Array(bytes), {
    status: answer.status,
    headers: responseHeaders,
  });
}

// eventBody pulls an event stream the engine holds open, on this thread:
// a pull that finds nothing waits for a commit or the heartbeat.
function eventBody(engine: NativeEngine, id: number, heartbeatMs: number, signal: AbortSignal): ReadableStream<Uint8Array> {
  let done = false;
  let wake: (() => void) | undefined;
  let beatAt: number | undefined;
  let unwatch: (() => void) | undefined;
  const teardown = () => {
    if (done) {
      return;
    }
    done = true;
    unwatch?.();
    signal.removeEventListener('abort', teardown);
    try {
      engine.streamClose(id);
    } catch {
      // Closed with the engine.
    }
    wake?.();
  };
  return new ReadableStream<Uint8Array>({
    start() {
      try {
        unwatch = engine.events.watch({ committed: () => wake?.(), closed: () => wake?.() });
      } catch {
        // The engine closed: the first pull ends the stream.
      }
      signal.addEventListener('abort', teardown);
      if (signal.aborted) {
        teardown();
      }
    },
    async pull(controller) {
      for (;;) {
        if (done) {
          end(controller);
          return;
        }
        let pulled;
        try {
          pulled = engine.streamPull(id);
        } catch {
          teardown();
          end(controller);
          return;
        }
        if (pulled.end) {
          teardown();
          end(controller);
          return;
        }
        if (pulled.data !== undefined && pulled.data !== null) {
          beatAt = undefined;
          controller.enqueue(new Uint8Array(pulled.data));
          return;
        }
        // Nothing yet: wait for a commit, or until the engine's heartbeat
        // is due.
        beatAt ??= Date.now() + heartbeatMs;
        const delay = Math.max(1, beatAt - Date.now());
        await new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, delay);
          wake = () => {
            clearTimeout(timer);
            resolve();
          };
        });
        wake = undefined;
      }
    },
    cancel() {
      teardown();
    },
  });
}

// Closing a stream the client already cancelled throws; either way it is over.
function end(controller: ReadableStreamDefaultController<Uint8Array>): void {
  try {
    controller.close();
  } catch {
    // Already closed.
  }
}

function packageInfo(): { name: string; version: string } {
  const manifest = JSON.parse(readFileSync(new URL('../../package.json', import.meta.url), 'utf8')) as { name: string; version: string };
  return { name: manifest.name, version: manifest.version };
}
