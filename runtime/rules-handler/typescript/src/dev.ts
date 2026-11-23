// The development host (D117): the handler over a store in one process, as
// superschematic-rules-dev's Dev is in Rust (runtime/engine/rust/rules-dev/
// src/lib.rs, http.rs and server.rs), with a clock, the caller its runner
// runs the queues as, the access policy, and the routes over the Fetch
// API's Request and Response. Each request runs in one transaction; the
// change queue and the deadlines due run after it, unless the host drains
// when asked. JavaScript runs one request's transaction to its end before
// the next starts, since the store answers at once, so the store has one
// writer, as SQLite has.
//
// The host keeps authentication: authenticate reads the caller from a
// request, and a request it reads none from is 401. devCallers is
// development's own: a subject and permissions from two headers, or a
// default caller, for a server only its developer reaches.

import type {
  Answer,
  Caller,
  EventsQuery,
  HandlerRequest,
  HttpResponseJson,
  Json,
  JsonObject,
  Layout,
  Policy,
  Refusal,
  Routed,
} from './contract.js';
import { runDue, serve, RefusalError, type Outcome, type Ran, type Store } from './host.js';
import { encode, JSON_CODEC, type Codec } from './json.js';
import { loadHandler, type HandlerModule, type WasmSource } from './wasm.js';

/** The header development's callers name a subject by. */
export const SUBJECT_HEADER = 'x-superschematic-subject';
/** The header development's callers name their permissions by, separated by commas. */
export const PERMISSIONS_HEADER = 'x-superschematic-permissions';
/** The largest body a route other than MCP's reads, the HTTP runtimes' default: 1 MiB. */
export const BODY_LIMIT = 1024 * 1024;
/** The largest body the MCP endpoint reads, the official SDK's limit. */
export const MCP_BODY_LIMIT = 4 * 1024 * 1024;

/** Reads the caller a request establishes; undefined for none (401). */
export type Authenticate = (request: Request) => Caller | undefined;

/**
 * Development's callers: the subject SUBJECT_HEADER names, with the
 * permissions PERMISSIONS_HEADER lists, or fallback when a request names
 * none. Anyone who reaches the server is whoever they say, so it is for a
 * server only its developer reaches.
 */
export function devCallers(fallback?: Caller): Authenticate {
  return request => {
    const header = (name: string) => {
      const value = request.headers.get(name)?.trim();
      return value === undefined || value === '' ? undefined : value;
    };
    const subject = header(SUBJECT_HEADER);
    if (subject === undefined) {
      return fallback;
    }
    const permissions = (header(PERMISSIONS_HEADER) ?? '')
      .split(',')
      .map(permission => permission.trim())
      .filter(permission => permission !== '');
    return { subject, permissions };
  };
}

/** A row as the handler answers it: its key, its own and owned fields, and its version. */
export type Row = { [field: string]: Json } & { _version?: number };

/** A tool the caller may call, as MCP lists it, with the call the handler makes of it. */
export interface ToolEntry {
  tool: McpTool;
  call: JsonObject;
}

/** What each request answers, by its kind. */
export interface Responses {
  define: { type: string; defined: boolean };
  publish: { type: string; version: number; rewritten: number; discarded: { type: string; id: string }[] };
  describe: JsonObject;
  get: Row;
  create: Row;
  update: Row;
  delete: { deleted: { type: string; id: string }[] };
  list: { items: Row[]; next: string | null };
  operation: Json;
  tableOperation: Json;
  deadline: Json;
  change: Json;
  types: { namespace: string; name: string; liveVersion: number }[];
  tools: ToolEntry[];
}

/** What a request of a kind answers. */
export type ResponseOf<R extends HandlerRequest> = Responses[R['kind']];

/** A tool as tools/list answers it (@superschematic/http-runtime/mcp's McpTool). */
export interface McpTool {
  readonly name: string;
  readonly title?: string;
  readonly description?: string;
  readonly inputSchema: unknown;
  readonly annotations?: Readonly<Record<string, unknown>>;
  readonly _meta?: Readonly<Record<string, unknown>>;
}

/** What a tool call came to: a JSON result, or a refusal's problem document. */
export type McpCallOutcome = { readonly result: unknown } | { readonly problem: Record<string, unknown> };

/** The tools one request is served (@superschematic/http-runtime/mcp's McpToolSet). */
export interface McpToolSet {
  list(): readonly McpTool[] | Promise<readonly McpTool[]>;
  call(name: string, args: Readonly<Record<string, unknown>>): Promise<McpCallOutcome>;
}

/** The MCP endpoint's transport, which the host brings. */
export interface DevMcp {
  /**
   * Serves one request to the MCP endpoint over the caller's tools, its
   * caller already authenticated: @superschematic/http-runtime/mcp's
   * serveMcp fits, given its server info, the caller's subject and the
   * request id.
   */
  serve(request: Request, tools: McpToolSet, context: { readonly caller: Caller; readonly requestId: string }): Promise<Response>;
  /** The error a call of a tool the caller is not offered throws: that module's McpUnknownToolError. */
  unknownTool?(message: string): Error;
}

/** How the host serves. */
export interface DevOptions {
  /** The handler's module; loaded from wasm, or the bundled file, unless given. */
  readonly handler?: HandlerModule;
  /** Where the module comes from when no handler is given. */
  readonly wasm?: WasmSource;
  /** The access policy the handler asks before each request (D58's form); "allowAll" unless given. */
  readonly policy?: Policy;
  /** The time in epoch milliseconds; Date.now unless given. */
  readonly clock?: () => number;
  /** The caller the runner runs the queues as; development-runner unless given. */
  readonly runner?: Caller;
  /** When the host runs its queues: after each request, or only when asked (runDue). */
  readonly drain?: 'afterEachRequest' | 'whenAsked';
  /** How the module's documents and the store's columns are read and written; JSON's unless given. */
  readonly codec?: Codec;
  /** Reads a request's caller; devCallers() unless given, which names none without SUBJECT_HEADER. */
  readonly authenticate?: Authenticate;
  /** The path the routes are mounted under, such as /dev; none unless given. */
  readonly prefix?: string;
  /** How long a waiting event stream goes between reads of the queue, in milliseconds: 200. */
  readonly poll?: number;
  /** How long a waiting event stream goes between heartbeats, in milliseconds: 5000. */
  readonly heartbeat?: number;
  /** A fresh version 4 UUID; crypto.randomUUID unless given. */
  readonly uuid?: () => string;
  /** The MCP endpoint's transport; without it the endpoint is 404. */
  readonly mcp?: DevMcp;
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** An HTTP response the handler wrote, as a Fetch API Response. */
export function responseOf(written: HttpResponseJson, init?: { body?: BodyInit | null }): Response {
  const headers = new Headers();
  for (const [name, value] of written.headers) {
    headers.append(name, value);
  }
  return new Response(init?.body !== undefined ? init.body : written.body, { status: written.status, headers });
}

// readCapped reads a request's body up to a limit: undefined when it is
// larger.
async function readCapped(request: Request, limit: number): Promise<Uint8Array | undefined> {
  if (request.body === null) {
    return new Uint8Array();
  }
  const declared = Number(request.headers.get('content-length') ?? '');
  if (Number.isFinite(declared) && declared > limit) {
    return undefined;
  }
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > limit) {
      await reader.cancel();
      return undefined;
    }
    chunks.push(value);
  }
  const out = new Uint8Array(size);
  let at = 0;
  for (const chunk of chunks) {
    out.set(chunk, at);
    at += chunk.byteLength;
  }
  return out;
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise(resolve => {
    if (signal?.aborted) {
      resolve();
      return;
    }
    const timer = setTimeout(done, ms);
    function done() {
      clearTimeout(timer);
      signal?.removeEventListener('abort', done);
      resolve();
    }
    signal?.addEventListener('abort', done, { once: true });
  });
}

/** The development host: the handler over a store in one process. */
export class Dev {
  readonly handler: HandlerModule;
  readonly store: Store;
  readonly #policy: Policy;
  readonly #clock: () => number;
  readonly #runner: Caller;
  readonly #drain: 'afterEachRequest' | 'whenAsked';
  readonly #codec: Codec;
  readonly #authenticate: Authenticate;
  readonly #prefix: string;
  readonly #poll: number;
  readonly #heartbeat: number;
  readonly #uuid: () => string;
  readonly #mcp: DevMcp | undefined;

  /**
   * The host over a store. store may be a function that makes one from the
   * handler's layout, which a SqliteStore is made from.
   */
  static async open(store: Store | ((layout: Layout, codec: Codec) => Store), options: DevOptions = {}): Promise<Dev> {
    const handler = options.handler ?? (await loadHandler(options.wasm));
    const made = typeof store === 'function' ? store(handler.layout(), options.codec ?? JSON_CODEC) : store;
    return new Dev(handler, made, options);
  }

  constructor(handler: HandlerModule, store: Store, options: DevOptions = {}) {
    this.handler = handler;
    this.store = store;
    this.#policy = options.policy ?? 'allowAll';
    this.#clock = options.clock ?? Date.now;
    this.#runner = options.runner ?? { subject: 'development-runner', permissions: [] };
    this.#drain = options.drain ?? 'afterEachRequest';
    this.#codec = options.codec ?? JSON_CODEC;
    this.#authenticate = options.authenticate ?? devCallers();
    this.#prefix = (options.prefix ?? '').replace(/\/+$/, '');
    this.#poll = options.poll ?? 200;
    this.#heartbeat = options.heartbeat ?? 5000;
    this.#uuid = options.uuid ?? (() => crypto.randomUUID());
    this.#mcp = options.mcp;
    // A policy the handler cannot read fails here, not at the first request.
    this.#call('events', { mode: 'admit', caller: { subject: 'development-runner' }, query: {}, policy: this.#policy });
  }

  /** The access policy the host's handler asks. */
  get policy(): Policy {
    return this.#policy;
  }

  #call(operation: 'route' | 'respond' | 'events' | 'tool', input: unknown): unknown {
    return this.#codec.parse(this.handler.call(operation, encode(input, this.#codec)));
  }

  /**
   * Serves one request as a caller with the host's id for it, then, unless
   * the host drains when asked, runs the queues: what it came to, never
   * thrown. respond asks for the HTTP response the answer is written as.
   */
  run(caller: Caller, request: HandlerRequest, requestId: string = this.#uuid(), respond?: Answer): Outcome {
    const outcome = serve(
      this.handler,
      this.store,
      request,
      { caller, now: this.#clock(), request: requestId, id: this.#uuid() },
      { policy: this.#policy, codec: this.#codec, ...(respond === undefined ? {} : { respond: { answer: respond, requestId } }) },
    );
    if (this.#drain === 'afterEachRequest') {
      try {
        this.runDue();
      } catch (error) {
        if (error instanceof RefusalError) {
          return { ok: false, refusal: error.refusal };
        }
        throw error;
      }
    }
    return outcome;
  }

  /** Serves one request as a caller: its answer, or a RefusalError carrying its refusal. */
  async request<R extends HandlerRequest>(caller: Caller, request: R): Promise<ResponseOf<R>> {
    const outcome = this.run(caller, request);
    if (!outcome.ok) {
      throw new RefusalError(outcome.refusal);
    }
    return outcome.response as ResponseOf<R>;
  }

  /** Runs the change queue and the deadlines due by the host's clock, as its runner. */
  runDue(): Ran[] {
    return runDue(this.handler, this.store, this.#clock(), this.#runner, () => ({ request: this.#uuid(), id: this.#uuid() }), {
      policy: this.#policy,
      codec: this.#codec,
    });
  }

  #written(input: Record<string, unknown>): HttpResponseJson {
    return this.#call('respond', input) as HttpResponseJson;
  }

  #answer(outcome: Outcome, answer: Answer, requestId: string): HttpResponseJson {
    if (outcome.http !== undefined) {
      return outcome.http;
    }
    return this.#written(outcome.ok ? { answer, response: outcome.response, requestId } : { answer, refused: outcome.refusal, requestId });
  }

  /**
   * Serves one HTTP request: its caller is the one authenticate reads, and
   * its id the X-Request-Id it carries or a fresh one, which its response
   * carries. The routes are the engine's, under the host's prefix.
   */
  async http(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const requestId = request.headers.get('x-request-id') || this.#uuid();
    let path = url.pathname;
    if (this.#prefix !== '') {
      if (path !== this.#prefix && !path.startsWith(`${this.#prefix}/`)) {
        return responseOf(this.#written({ problem: { status: 404, detail: 'Not Found', code: 'not_found' }, requestId }));
      }
      path = path.slice(this.#prefix.length) || '/';
    }
    const mcpPath = path.endsWith('/mcp');
    const bytes = await readCapped(request, mcpPath ? MCP_BODY_LIMIT : BODY_LIMIT);
    if (bytes === undefined) {
      return responseOf(this.#written({ problem: { status: 413, detail: 'Request body exceeds the accepted size', code: 'payload_too_large' }, requestId }));
    }
    const caller = this.#authenticate(request);
    const headers: [string, string][] = [];
    request.headers.forEach((value, name) => headers.push([name, value]));
    const routed = this.#call('route', {
      request: { method: request.method, path, query: url.search.replace(/^\?/, ''), headers, body: bytes.byteLength === 0 ? undefined : decoder.decode(bytes) },
      requestId,
    }) as Routed;
    if ('response' in routed) {
      return responseOf(routed.response);
    }
    if (caller === undefined) {
      return responseOf(this.#written({ unauthorized: true, requestId }));
    }
    if ('handle' in routed) {
      const { request: handled, answer } = routed.handle;
      return responseOf(this.#answer(this.run(caller, handled, requestId, answer), answer, requestId));
    }
    if ('events' in routed) {
      const query = routed.events;
      const admitted = this.#call('events', { mode: 'admit', caller, query, policy: this.#policy }) as { refused?: Refusal };
      if (admitted.refused !== undefined) {
        return responseOf(this.#written({ refused: admitted.refused, requestId }));
      }
      if (query.stream) {
        return this.#stream(caller, query, request.signal);
      }
      return responseOf(this.#answer(this.eventsPage(caller, query), { kind: 'data' }, requestId));
    }
    if (this.#mcp === undefined) {
      return responseOf(
        this.#written({ problem: { status: 404, detail: "This host serves no MCP endpoint: pass DevOptions.mcp, such as @superschematic/http-runtime/mcp's serveMcp", code: 'not_found' }, requestId }),
      );
    }
    const forwarded = new Request(request.url, { method: request.method, headers: request.headers, ...(bytes.byteLength === 0 ? {} : { body: bytes as BodyInit }) });
    return this.#mcp.serve(forwarded, this.mcpTools(caller, requestId), { caller, requestId });
  }

  /** The last cursor the event queue holds; 0 for none. */
  eventsHead(): number {
    const events = this.store.events(0);
    return events.length === 0 ? 0 : events[events.length - 1]!.seq;
  }

  /** A page of the event queue as the caller may read it: { events, next, more }, or its refusal. */
  eventsPage(caller: Caller, query: EventsQuery): Outcome {
    try {
      const head = this.eventsHead();
      const start = query.after === 'head' ? head : query.after;
      const events = this.store.events(start);
      const answer = this.#call('events', { mode: 'page', caller, query, events, head, policy: this.#policy }) as { page?: unknown; refused?: Refusal };
      return answer.refused !== undefined ? { ok: false, refusal: answer.refused } : { ok: true, response: answer.page };
    } catch (error) {
      if (error instanceof RefusalError) return { ok: false, refusal: error.refusal };
      throw error;
    }
  }

  /**
   * What a stream sends after a cursor, as the caller may read it: the
   * frames of the events it keeps, the cursor it reads on from, and the
   * ready event at it; or its refusal. The read is asked of the policy
   * again each time.
   */
  eventsSince(caller: Caller, query: EventsQuery, cursor: number): { frames: string; next: number; ready: string } | { refused: Refusal } {
    const events = this.store.events(cursor);
    return this.#call('events', { mode: 'since', caller, query, events, cursor, policy: this.#policy }) as { frames: string; next: number; ready: string } | { refused: Refusal };
  }

  // A stream reads the queue from its cursor each time it is pulled: what
  // it holds after the cursor, the ready event once it has caught up, then
  // a read each poll and a heartbeat comment each heartbeat while nothing
  // comes. A refusal of a read (the policy changed) ends it, and so does
  // the client going.
  #stream(caller: Caller, query: EventsQuery, signal: AbortSignal | undefined): Response {
    let cursor = query.after === 'head' ? this.eventsHead() : query.after;
    let ready = false;
    let beat = Date.now();
    let cancelled = false;
    const heartbeat = this.handler.layout().heartbeat;
    const body = new ReadableStream<Uint8Array>({
      pull: async controller => {
        for (;;) {
          if (cancelled || signal?.aborted) {
            controller.close();
            return;
          }
          let since: ReturnType<Dev['eventsSince']>;
          try {
            since = this.eventsSince(caller, query, cursor);
          } catch {
            controller.close();
            return;
          }
          if ('refused' in since) {
            controller.close();
            return;
          }
          cursor = since.next;
          if (since.frames !== '') {
            beat = Date.now();
            controller.enqueue(encoder.encode(since.frames));
            return;
          }
          if (!ready) {
            ready = true;
            beat = Date.now();
            controller.enqueue(encoder.encode(since.ready));
            return;
          }
          if (Date.now() - beat >= this.#heartbeat) {
            beat = Date.now();
            controller.enqueue(encoder.encode(heartbeat));
            return;
          }
          await sleep(this.#poll, signal);
        }
      },
      cancel: () => {
        cancelled = true;
      },
    });
    return new Response(body, {
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store', 'x-accel-buffering': 'no' },
    });
  }

  /** The tools the caller may call, each { tool, call }; a RefusalError when the caller may list none. */
  tools(caller: Caller): ToolEntry[] {
    const outcome = this.run(caller, { kind: 'tools' });
    if (!outcome.ok) {
      throw new RefusalError(outcome.refusal);
    }
    return Array.isArray(outcome.response) ? (outcome.response as ToolEntry[]) : [];
  }

  /** Calls a tool by its handle with its arguments, as the caller: undefined for a tool the caller is not offered. */
  callTool(caller: Caller, name: string, args: Readonly<Record<string, unknown>>): Outcome | undefined {
    const entry = this.tools(caller).find(tool => tool.tool.name === name);
    if (entry === undefined) {
      return undefined;
    }
    const made = this.#call('tool', { call: entry.call, arguments: args }) as { request?: HandlerRequest; refused?: Refusal };
    if (made.refused !== undefined || made.request === undefined) {
      return { ok: false, refusal: made.refused ?? { code: 'internal', status: 500, class: 'Error', message: 'a tool made no request' } };
    }
    return this.run(caller, made.request);
  }

  /** The caller's tools as an MCP transport asks for them; a refused call's problem carries requestId. */
  mcpTools(caller: Caller, requestId: string = this.#uuid()): McpToolSet {
    const unknownTool = this.#mcp?.unknownTool ?? ((message: string) => new Error(message));
    return {
      list: () => this.tools(caller).map(entry => entry.tool),
      call: async (name, args) => {
        const called = this.callTool(caller, name, args);
        if (called === undefined) {
          throw unknownTool(`development mode has no tool "${name}"`);
        }
        if (called.ok) {
          return { result: called.response };
        }
        return { problem: JSON.parse(this.#written({ refused: called.refusal, requestId }).body) as Record<string, unknown> };
      },
    };
  }
}
