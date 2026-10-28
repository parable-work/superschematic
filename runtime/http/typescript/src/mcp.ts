import type { Context, Env, Hono } from 'hono';
import { authorize } from './auth.js';
import { checkRoute, mountManualOperation, type RouterRuntimeOptions } from './hono.js';
import {
  MCP_CONFIRMATION_ARGUMENT,
  MCP_CONFIRMATION_DECLINED,
  MCP_CONFIRMATION_INPUT,
  MCP_CONFIRMATION_REQUIRED,
  McpConfirmations,
  canonicalJson,
  type McpConfirmationBinding,
  type McpConfirmationRefusal,
  type McpConfirmationStore,
} from './mcpconfirm.js';
import type { OperationSpec, RequestContext } from './operation.js';
import { ErrorCode, HttpProblem, problemBody, type ProblemBody } from './problem.js';
import type { Revealable } from './secret.js';

export {
  DEFAULT_MCP_CONFIRMATION_TTL_SECONDS,
  MCP_CONFIRMATION_ARGUMENT,
  MCP_CONFIRMATION_DECLINED,
  MCP_CONFIRMATION_INPUT,
  MCP_CONFIRMATION_REQUIRED,
  MIN_MCP_CONFIRMATION_KEY_BYTES,
  McpConfirmations,
  MemoryMcpConfirmationStore,
  canonicalJson,
} from './mcpconfirm.js';
export type {
  McpConfirmationBinding,
  McpConfirmationMode,
  McpConfirmationOptions,
  McpConfirmationRefusal,
  McpConfirmationStore,
  McpConfirmationToken,
} from './mcpconfirm.js';

/*
The MCP endpoint of a generated API (D81): POST /api/mcp serves the API's
visible @mcp operations as MCP tools over the streamable HTTP transport,
statelessly, as the engine serves its own (runtime/engine/rust/engine/src/
api/mcp.rs, whose conventions this file copies; the runtime does not
depend on the engine):

- The 2025 revisions (a request with no per-request _meta envelope):
  initialize, negotiating one of MCP_INITIALIZE_VERSIONS, ping, tools/list
  and tools/call, alone or in a batch. A request answers 200 with a
  text/event-stream body of one `message` event per request; notifications
  and posted responses alone answer 202.
- 2026-07-28 (a request whose params._meta carries the
  io.modelcontextprotocol/protocolVersion claim): server/discover, ping,
  tools/list and tools/call, each answered as one JSON body, every result
  marked complete and stamped with the server's identity.
- GET and DELETE answer 405: a stateless server keeps no session and opens
  no stream.

serveMcp is the transport over any tool set; mountMcp mounts it on a
generated router with the router's own tools. The endpoint goes through
the runtime like every route: its auth gate, with the router's
authenticator or identity service, establishes the caller, and the list
holds the tools whose routes that caller may call. A tools/call runs in
process through the router, as a request to its operation's route that
carries the caller's credentials, so the route's rate limit, body limit,
service step, permission check, validation and timeout apply unchanged. A
call the route refuses returns a tool error (isError) whose structured
content is the problem document the route answered; a tool the caller has
not got is a JSON-RPC invalid-params error.

A tool whose invocation policy is ask runs only once the person confirms
the call (D93; McpToolSet.confirms, mcpconfirm.ts):

- A 2026-07-28 client whose envelope's capabilities elicit forms is asked
  in band: the call answers input_required with one elicitation/create,
  whose message is the call's summary, and its requestState a confirmation
  the client sends back with the person's answer. accept runs the call;
  decline and cancel are a tool error, confirmation_declined, and run
  nothing.
- Any other client, every client of the 2025 revisions among them, gets a
  two-step confirmation: the call answers a tool error,
  confirmation_required, whose details hold the summary and a
  confirmationToken, and runs nothing; the same call with that token in
  its confirmationToken argument runs. A stateless 2025 server keeps no
  session, so it neither remembers the capabilities a client declared at
  initialize nor routes the answer to a request it sent mid-call back to
  the call that waits for it, which may be on another replica; 2026-07-28
  carries the request and the answer in the call itself.

A token binds the tool, the caller and the exact arguments, expires, is
claimed once, and is an HMAC under the endpoint's confirmation key; one
that does not redeem asks again, saying why.

Only a call that would reach the tool's implementation is confirmed (D98):
before the endpoint mints a confirmation, asks a client to elicit, or
redeems a token or a requestState, it checks the call (McpToolSet.check),
on a generated router through its route's own checks (hono.ts's
checkRoute): the arguments decoded and validated as the route decodes them,
and the route's body limit, service step, authenticator and permission
gate. A call they refuse answers their refusal at once, a tool error
carrying the route's problem, and mints and claims nothing.
*/

/** The 2025 revisions initialize negotiates, the newest first: the one a client asks for when it is here, else the first. */
export const MCP_INITIALIZE_VERSIONS: readonly string[] = ['2025-11-25', '2025-06-18', '2025-03-26', '2024-11-05', '2024-10-07'];

/** The revisions server/discover offers: those a request names in its _meta envelope. */
export const MCP_DISCOVER_VERSIONS: readonly string[] = ['2026-07-28'];

/** The largest body the endpoint reads, the official SDK's limit. */
export const MCP_MAX_BODY_BYTES = 4 * 1024 * 1024;

/** Where a generated router serves its MCP endpoint. */
export const MCP_PATH = '/api/mcp';

// The first revision that carries its version in each request's envelope;
// revisions are dates, so a later one compares greater.
const FIRST_MODERN = '2026-07-28';
const MAX_BATCH = 100;

const VERSION_KEY = 'io.modelcontextprotocol/protocolVersion';
const CLIENT_INFO_KEY = 'io.modelcontextprotocol/clientInfo';
const CLIENT_CAPABILITIES_KEY = 'io.modelcontextprotocol/clientCapabilities';
const SERVER_INFO_KEY = 'io.modelcontextprotocol/serverInfo';

const PARSE_ERROR = -32700;
const INVALID_REQUEST = -32600;
const METHOD_NOT_FOUND = -32601;
const INVALID_PARAMS = -32602;
const INTERNAL_ERROR = -32603;
const SERVER_ERROR = -32000;
const HEADER_MISMATCH = -32020;
const UNSUPPORTED_VERSION = -32022;

/** The name and version initialize and every 2026-07-28 result report. */
export interface McpServerInfo {
  readonly name: string;
  readonly version: string;
}

/** What the transport says about the server. */
export interface McpServerOptions {
  readonly serverInfo: McpServerInfo;
  /** Instructions initialize gives the client; none when absent or empty. */
  readonly instructions?: string;
  /**
   * Mints and redeems the confirmations of the tools the tool set's
   * confirms names (D93). Without it a call of such a tool is refused and
   * runs nothing.
   */
  readonly confirmations?: McpConfirmations;
  /** Who is calling, which a confirmation is bound to: empty for no one. */
  readonly caller?: string;
  /** The request id a confirmation's problem carries; a fresh one unless given. */
  readonly requestId?: string;
}

/** A tool as tools/list answers it. */
export interface McpTool {
  /** The tool's handle, its wire identifier. */
  readonly name: string;
  readonly title?: string;
  readonly description?: string;
  /** The JSON Schema of its arguments. */
  readonly inputSchema: unknown;
  readonly annotations?: Readonly<Record<string, unknown>>;
  readonly _meta?: Readonly<Record<string, unknown>>;
}

/** One item of a tool result's content. */
export type McpContent =
  | { readonly type: 'text'; readonly text: string }
  | { readonly type: 'resource'; readonly resource: { readonly uri: string; readonly mimeType: string; readonly blob: string } };

/**
 * What a tool call came to: a JSON result, sent as text and, for an
 * object, as structured content; content of another kind, sent as it is;
 * or a refusal, sent as a tool error whose structured content is the
 * problem document.
 */
export type McpCallOutcome = { readonly result: unknown } | { readonly content: readonly McpContent[] } | { readonly problem: ProblemBody };

/** Thrown by a tool set for a tool it has not got: the JSON-RPC invalid-params error. */
export class McpUnknownToolError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'McpUnknownToolError';
  }
}

/** The tools one request is served: those its caller is offered, and how a call runs. */
export interface McpToolSet {
  list(): readonly McpTool[] | Promise<readonly McpTool[]>;
  call(name: string, args: Readonly<Record<string, unknown>>): Promise<McpCallOutcome>;
  /**
   * The tool name names when a call of it runs only once the person
   * confirms it, its invocation policy being ask (D93); undefined for a
   * tool that runs as called, or one the set has not got, whose call
   * refuses it. The endpoint confirms the call before it calls call, which
   * never sees the confirmationToken argument.
   */
  confirms?(name: string): McpTool | undefined | Promise<McpTool | undefined>;
  /**
   * The refusal a call of the tool name with args would meet before its
   * implementation runs, its problem document; undefined for a call that
   * would reach the implementation (D98). The endpoint checks a call of a
   * tool confirms names with the arguments the confirmation binds, the
   * confirmationToken's taken out, before it asks the person or redeems a
   * confirmation, and answers a refusal at once, so a call that cannot run
   * asks no one and spends nothing. Throws McpUnknownToolError for a tool
   * the set has not got, as call does. Without it every such call is
   * confirmed, and refused, if at all, once it runs.
   */
  check?(name: string, args: Readonly<Record<string, unknown>>): ProblemBody | undefined | Promise<ProblemBody | undefined>;
}

// A JSON-RPC error: its code, message and data.
interface RpcError {
  readonly code: number;
  readonly message: string;
  readonly data?: unknown;
}

function rpcError(code: number, message: string, data?: unknown): RpcError {
  return data === undefined ? { code, message } : { code, message, data };
}

// mismatch is the SDK's refusal of headers and a body that disagree.
function mismatch(header: string, body: string): RpcError {
  return rpcError(HEADER_MISMATCH, `Bad Request: the request headers and body disagree: ${body}`, { mismatch: { header, body } });
}

function errorValue(error: RpcError): Record<string, unknown> {
  return error.data === undefined ? { code: error.code, message: error.message } : { code: error.code, message: error.message, data: error.data };
}

type Era =
  | { readonly kind: 'refused'; readonly status: number; readonly error: RpcError }
  | { readonly kind: 'legacy' }
  | { readonly kind: 'modern'; readonly revision: string; readonly notification: boolean };

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/**
 * Serves one request to the MCP endpoint with tools, its caller already
 * authenticated: the JSON-RPC exchange as the official SDK's stateless
 * transport answers it.
 */
export async function serveMcp(request: Request, tools: McpToolSet, options: McpServerOptions): Promise<Response> {
  if (request.method.toUpperCase() !== 'POST') {
    return entryError(405, rpcError(SERVER_ERROR, 'Method not allowed.'), null);
  }
  if (!isJson(request.headers.get('content-type'))) {
    return entryError(415, rpcError(SERVER_ERROR, 'Unsupported Media Type: Content-Type must be application/json'), null);
  }
  const bytes = await readCapped(request, MCP_MAX_BODY_BYTES);
  if (bytes === undefined) {
    return entryError(413, rpcError(SERVER_ERROR, `Payload Too Large: Request body must not exceed ${MCP_MAX_BODY_BYTES} bytes`), null);
  }
  const text = new TextDecoder().decode(bytes);
  let body: unknown;
  let parsed = false;
  if (text !== '') {
    try {
      body = JSON.parse(text);
      parsed = true;
    } catch {
      // Not JSON: the 2025 transport refuses it below.
    }
  }
  if (!parsed) {
    return refuseAccept(request) ?? entryError(400, rpcError(PARSE_ERROR, 'Parse error: Invalid JSON'), null);
  }
  const header = (name: string): string | undefined => request.headers.get(name)?.trim();
  const versionHeader = header('mcp-protocol-version');
  const era = classify(body, versionHeader, header('mcp-method'));
  const server = new Server(tools, options);
  switch (era.kind) {
    case 'refused':
      return entryError(era.status, era.error, echoableId(body));
    case 'legacy':
      return server.legacy(request, body, versionHeader);
    case 'modern':
      return server.modern(body, era.revision, era.notification, versionHeader);
  }
}

class Server {
  constructor(
    private readonly tools: McpToolSet,
    private readonly options: McpServerOptions
  ) {}

  // legacy serves the 2025 revisions as the SDK's stateless transport
  // does: every request's response as one event of a stream that ends with
  // the last.
  async legacy(request: Request, body: unknown, versionHeader: string | undefined): Promise<Response> {
    const refused = refuseAccept(request);
    if (refused) return refused;
    const messages: unknown[] = Array.isArray(body) ? body : [body];
    if (messages.length > MAX_BATCH) {
      return entryError(400, rpcError(INVALID_REQUEST, `Invalid Request: Batch must not exceed ${MAX_BATCH} messages`), null);
    }
    if (!messages.every(isMessage)) {
      return entryError(400, rpcError(PARSE_ERROR, 'Parse error: Invalid JSON-RPC message'), null);
    }
    const initializes = messages.some(message => isRequest(message) && message.method === 'initialize');
    if (initializes && messages.length > 1) {
      return entryError(400, rpcError(INVALID_REQUEST, 'Invalid Request: Only one initialization request is allowed'), null);
    }
    if (!initializes && versionHeader !== undefined && !MCP_INITIALIZE_VERSIONS.includes(versionHeader)) {
      return entryError(
        400,
        rpcError(SERVER_ERROR, `Bad Request: Unsupported protocol version: ${versionHeader} (supported versions: ${MCP_INITIALIZE_VERSIONS.join(', ')})`),
        null
      );
    }
    let frames = '';
    for (const message of messages) {
      if (!isRequest(message)) continue;
      const id = message.id;
      let reply: Record<string, unknown>;
      try {
        reply = resultMessage(await this.dispatch(message, false), id);
      } catch (error) {
        reply = errorMessage(rpcErrorOf(error), id);
      }
      frames += `event: message\ndata: ${JSON.stringify(reply)}\n\n`;
    }
    // Notifications and responses alone: nothing to answer.
    if (frames === '') return accepted();
    return new Response(frames, {
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-cache, no-transform', 'x-accel-buffering': 'no' },
    });
  }

  // modern serves one 2026-07-28 message as one JSON body.
  async modern(body: unknown, revision: string, notification: boolean, versionHeader: string | undefined): Promise<Response> {
    if (!MCP_DISCOVER_VERSIONS.includes(revision)) {
      return entryError(
        400,
        rpcError(UNSUPPORTED_VERSION, `Unsupported protocol version: ${revision}`, { supported: MCP_DISCOVER_VERSIONS, requested: revision }),
        echoableId(body)
      );
    }
    if (notification) return accepted();
    if (versionHeader === undefined) {
      return entryError(
        400,
        mismatch('(missing)', `the body envelope names protocol version ${revision} but the required MCP-Protocol-Version header is absent`),
        echoableId(body)
      );
    }
    const message = body as Record<string, unknown>;
    const id = message.id;
    const method = typeof message.method === 'string' ? message.method : '';
    if (!['server/discover', 'ping', 'tools/list', 'tools/call'].includes(method)) {
      // A method the era does not define is refused before any handler
      // runs, with the ladder's status.
      return jsonResponse(404, errorMessage(rpcError(METHOD_NOT_FOUND, 'Method not found'), id));
    }
    let reply: Record<string, unknown>;
    try {
      const result = await this.dispatch(message, true);
      if (isObject(result)) {
        const { _meta, ...rest } = result;
        // A call that asks the person first is input_required (D93).
        const out: Record<string, unknown> = { ...rest, resultType: typeof rest.resultType === 'string' ? rest.resultType : 'complete' };
        if (method === 'server/discover' || method === 'tools/list') {
          // Cache hints: none, and for this caller alone.
          out.ttlMs = 0;
          out.cacheScope = 'private';
        }
        out._meta = { ...(isObject(_meta) ? _meta : {}), [SERVER_INFO_KEY]: this.serverInfo() };
        reply = resultMessage(out, id);
      } else {
        reply = resultMessage(result, id);
      }
    } catch (error) {
      reply = errorMessage(rpcErrorOf(error), id);
    }
    return jsonResponse(200, reply);
  }

  // dispatch runs one request's method; a refusal throws its RpcError.
  async dispatch(message: Record<string, unknown>, modern: boolean): Promise<unknown> {
    const method = typeof message.method === 'string' ? message.method : '';
    const params = message.params;
    if (method === 'initialize' && !modern) return this.initialize(params);
    if (method === 'server/discover' && modern) return this.discover();
    if (method === 'ping') return {};
    if (method === 'tools/list') return this.list();
    if (method === 'tools/call') return this.call(params, modern);
    throw rpcError(METHOD_NOT_FOUND, 'Method not found');
  }

  serverInfo(): McpServerInfo {
    return { name: this.options.serverInfo.name, version: this.options.serverInfo.version };
  }

  initialize(params: unknown): Record<string, unknown> {
    const requested = isObject(params) ? params.protocolVersion : undefined;
    if (typeof requested !== 'string') {
      throw rpcError(INVALID_PARAMS, 'Invalid params for initialize: protocolVersion must be a string');
    }
    const version = MCP_INITIALIZE_VERSIONS.includes(requested) ? requested : MCP_INITIALIZE_VERSIONS[0];
    return this.withInstructions({ protocolVersion: version, capabilities: capabilities(), serverInfo: this.serverInfo() });
  }

  discover(): Record<string, unknown> {
    return this.withInstructions({ supportedVersions: MCP_DISCOVER_VERSIONS, capabilities: capabilities() });
  }

  withInstructions(result: Record<string, unknown>): Record<string, unknown> {
    const instructions = this.options.instructions;
    return instructions ? { ...result, instructions } : result;
  }

  async list(): Promise<Record<string, unknown>> {
    try {
      return { tools: await this.tools.list() };
    } catch (error) {
      throw failure(error);
    }
  }

  // call runs a tool: its result as text and, for an object, structured
  // content; a refusal as a tool error carrying the problem document. A
  // tool that asks the person first runs only once they confirm the call.
  async call(params: unknown, modern: boolean): Promise<Record<string, unknown>> {
    const name = isObject(params) ? params.name : undefined;
    if (typeof name !== 'string') {
      throw rpcError(INVALID_PARAMS, 'Invalid params for tools/call: name must be a string');
    }
    const message = params as Record<string, unknown>;
    const args = message.arguments;
    if (!(args === undefined || args === null || isObject(args))) {
      throw rpcError(INVALID_PARAMS, 'Invalid params for tools/call: arguments must be an object');
    }
    let callArgs: Readonly<Record<string, unknown>> = isObject(args) ? args : {};
    let outcome: McpCallOutcome;
    try {
      const asks = await this.tools.confirms?.(name);
      if (asks) {
        const gate = await this.confirm(asks, callArgs, message, modern);
        if ('result' in gate) return gate.result;
        callArgs = gate.args;
      }
      outcome = await this.tools.call(name, callArgs);
    } catch (error) {
      if (error instanceof McpUnknownToolError) throw rpcError(INVALID_PARAMS, error.message);
      throw failure(error);
    }
    if ('problem' in outcome) return toolError(outcome.problem);
    if ('content' in outcome) return { content: outcome.content };
    const result = outcome.result ?? null;
    return isObject(result)
      ? { content: [{ type: 'text', text: JSON.stringify(result) }], structuredContent: result }
      : { content: [{ type: 'text', text: JSON.stringify(result) }] };
  }

  // confirm is the confirmation of a call of tool, which asks the person
  // first (D93): the arguments it runs with, the token's taken out, once
  // the call carries a confirmation that redeems, else what the call
  // answers instead, the refusal its check met, a confirmation to ask for
  // or the person's refusal.
  async confirm(
    tool: McpTool,
    args: Readonly<Record<string, unknown>>,
    params: Record<string, unknown>,
    modern: boolean
  ): Promise<{ readonly args: Readonly<Record<string, unknown>> } | { readonly result: Record<string, unknown> }> {
    const { [MCP_CONFIRMATION_ARGUMENT]: token, ...rest } = args;
    // A call that would not reach the implementation is refused before
    // anyone is asked, and mints and claims nothing (D98). It is checked
    // with the arguments the binding covers and the call runs with.
    const refusal = await this.tools.check?.(tool.name, rest);
    if (refusal) return { result: toolError(refusal) };
    const confirmations = this.options.confirmations;
    if (!confirmations) {
      const problem = new HttpProblem(500, `${tool.name} runs only once the person confirms it, and this endpoint has no confirmation key`, {
        code: ErrorCode.INTERNAL_ERROR,
      });
      return { result: toolError(problemBody(problem, this.requestId())) };
    }
    const binding: McpConfirmationBinding = { tool: tool.name, caller: this.options.caller ?? '', args: rest };
    const elicits = modern && elicitsForms(clientCapabilities(params));
    let refused: McpConfirmationRefusal | undefined;
    if (token !== undefined) {
      refused = await confirmations.redeem(token, 'token', binding);
      if (!refused) return { args: rest };
    } else if (elicits && params.requestState !== undefined) {
      const answer = isObject(params.inputResponses) ? params.inputResponses[MCP_CONFIRMATION_INPUT] : undefined;
      const action = isObject(answer) ? answer.action : undefined;
      if (action === 'accept' || action === 'decline' || action === 'cancel') {
        refused = await confirmations.redeem(params.requestState, 'elicit', binding);
        if (!refused && action === 'accept') return { args: rest };
        if (!refused) {
          const what = action === 'decline' ? 'declined' : 'dismissed the confirmation of';
          const problem = new HttpProblem(403, `The person ${what} ${tool.name}, which did not run`, {
            code: MCP_CONFIRMATION_DECLINED,
            details: { tool: tool.name, action },
          });
          return { result: toolError(problemBody(problem, this.requestId())) };
        }
      }
    }
    const summary = confirmationSummary(tool, rest);
    const why = refused ? `${REFUSALS[refused]} ` : '';
    if (elicits) {
      const { token: requestState } = await confirmations.mint('elicit', binding);
      const ask = {
        method: 'elicitation/create',
        params: { mode: 'form', message: `${why}Run ${summary}?`, requestedSchema: { type: 'object', properties: {} } },
      };
      return { result: { resultType: 'input_required', inputRequests: { [MCP_CONFIRMATION_INPUT]: ask }, requestState } };
    }
    const minted = await confirmations.mint('token', binding);
    const detail =
      `${why}${tool.name} runs only once the person confirms it: ${summary}. Show them the call, and once they confirm, ` +
      `call ${tool.name} again with the same arguments and ${MCP_CONFIRMATION_ARGUMENT} set to details.${MCP_CONFIRMATION_ARGUMENT}, before ${minted.expiresAt}`;
    const problem = new HttpProblem(428, detail, {
      code: MCP_CONFIRMATION_REQUIRED,
      details: {
        tool: tool.name,
        summary,
        [MCP_CONFIRMATION_ARGUMENT]: minted.token,
        expiresAt: minted.expiresAt,
        ...(refused ? { refused } : {}),
      },
    });
    return { result: toolError(problemBody(problem, this.requestId())) };
  }

  requestId(): string {
    return this.options.requestId ?? crypto.randomUUID();
  }
}

// What the person, or the agent, reads of a confirmation that did not
// redeem, before the call is asked about again.
const REFUSALS: Readonly<Record<McpConfirmationRefusal, string>> = {
  invalid: 'The confirmation sent is not one this endpoint gave.',
  expired: 'The confirmation sent has expired.',
  mismatch: 'The confirmation sent is for another call.',
  used: 'The confirmation sent was used already.',
};

// The longest arguments' text a confirmation's summary shows.
const SUMMARY_ARGUMENTS = 2000;

// confirmationSummary is what the person confirms: the tool's title and
// handle, and its arguments as JSON with sorted keys, the part past
// SUMMARY_ARGUMENTS characters counted rather than shown. The token binds
// every argument, whatever the summary shows.
function confirmationSummary(tool: McpTool, args: Readonly<Record<string, unknown>>): string {
  const name = tool.title && tool.title !== tool.name ? `${tool.title} (${tool.name})` : tool.name;
  if (Object.keys(args).length === 0) return `${name} with no arguments`;
  const text = canonicalJson(args);
  if (text.length <= SUMMARY_ARGUMENTS) return `${name} with ${text}`;
  return `${name} with ${text.slice(0, SUMMARY_ARGUMENTS)}… (${text.length - SUMMARY_ARGUMENTS} more characters)`;
}

// clientCapabilities is the capabilities a 2026-07-28 request's envelope
// declares.
function clientCapabilities(params: Record<string, unknown>): unknown {
  return isObject(params._meta) ? params._meta[CLIENT_CAPABILITIES_KEY] : undefined;
}

// elicitsForms is whether capabilities let the server elicit a form: an
// elicitation capability that names form, or, as the 2025 revisions
// declared it, one that names neither form nor url.
function elicitsForms(capabilities: unknown): boolean {
  if (!isObject(capabilities) || !isObject(capabilities.elicitation)) return false;
  const elicitation = capabilities.elicitation;
  return 'form' in elicitation ? isObject(elicitation.form) : !('url' in elicitation);
}

function toolError(problem: ProblemBody): Record<string, unknown> {
  return { content: [{ type: 'text', text: JSON.stringify(problem) }], structuredContent: problem, isError: true };
}

function capabilities(): Record<string, unknown> {
  return { tools: { listChanged: false } };
}

function isRpcError(value: unknown): value is RpcError {
  return isObject(value) && typeof value.code === 'number' && typeof value.message === 'string' && !(value instanceof Error);
}

function rpcErrorOf(error: unknown): RpcError {
  return isRpcError(error) ? error : failure(error);
}

// failure is a handler's error that is no tool error: the SDK's internal
// error, whose message stays off the wire.
function failure(error: unknown): RpcError {
  return isRpcError(error) ? error : rpcError(INTERNAL_ERROR, 'Internal error');
}

// classify routes a parsed body as the SDK's entry does.
function classify(body: unknown, versionHeader: string | undefined, methodHeader: string | undefined): Era {
  const invalid = (message: string): Era => ({ kind: 'refused', status: 400, error: rpcError(INVALID_REQUEST, message) });
  const refused = (error: RpcError): Era => ({ kind: 'refused', status: 400, error });
  if (Array.isArray(body)) {
    if (body.length === 0) return invalid('Bad Request: empty JSON-RPC batch');
    for (const element of body) {
      if (claimOf(isObject(element) ? element.params : undefined) !== undefined) {
        return invalid('Bad Request: JSON-RPC batches may not contain requests for protocol revision 2026-07-28 or later');
      }
      if (!isMessage(element)) return invalid('Bad Request: JSON-RPC batch contains an invalid message');
    }
    return { kind: 'legacy' };
  }
  if (isResponse(body)) return { kind: 'legacy' };
  const message = isObject(body) ? body : {};
  const params = message.params;
  const method = typeof message.method === 'string' ? message.method : '';
  const headerModern = versionHeader !== undefined && versionHeader >= FIRST_MODERN;
  if (isRequest(body)) {
    if (method === 'initialize' && !validModernClaim(params)) {
      if (headerModern) {
        return refused(mismatch(versionHeader, 'an initialize request (legacy handshake) was sent with a modern MCP-Protocol-Version header'));
      }
      return { kind: 'legacy' };
    }
    const claim = claimOf(params);
    if (claim !== undefined) {
      const [issue] = envelopeIssues(params);
      if (issue) {
        const [key, problem] = issue;
        return refused(
          rpcError(INVALID_PARAMS, `Invalid _meta envelope for protocol revision 2026-07-28: ${key}: ${problem}`, { envelope: { key, problem } })
        );
      }
      const claimed = typeof claim === 'string' ? claim : '';
      if (versionHeader !== undefined && versionHeader !== claimed) {
        return refused(mismatch(versionHeader, `the body envelope names protocol version ${claimed} but the MCP-Protocol-Version header names ${versionHeader}`));
      }
      const methodRefused = methodMismatch(methodHeader, method, 'body');
      if (methodRefused) return methodRefused;
      return { kind: 'modern', revision: claimed, notification: false };
    }
    if (headerModern) {
      let missing: string[];
      if (!isObject(params) || !('_meta' in params)) {
        missing = ['_meta'];
      } else {
        const absent = envelopeIssues(params)
          .filter(([, problem]) => problem === 'missing')
          .map(([key]) => key);
        missing = absent.length > 0 ? absent : [VERSION_KEY];
      }
      return refused(
        rpcError(
          INVALID_PARAMS,
          `Invalid params: the MCP-Protocol-Version header names protocol revision ${versionHeader}, but the request is missing the required per-request envelope key(s): ${missing.join(', ')}`,
          { envelope: { missing } }
        )
      );
    }
    return { kind: 'legacy' };
  }
  if (isNotification(body)) {
    const claim = claimOf(params);
    if (claim !== undefined) {
      if (typeof claim !== 'string') {
        const problem = 'expected a protocol version string';
        return refused(
          rpcError(INVALID_PARAMS, `Invalid _meta envelope for protocol revision 2026-07-28: ${VERSION_KEY}: ${problem}`, {
            envelope: { key: VERSION_KEY, problem },
          })
        );
      }
      if (versionHeader !== undefined && versionHeader !== claim) {
        return refused(mismatch(versionHeader, `the notification envelope names protocol version ${claim} but the MCP-Protocol-Version header names ${versionHeader}`));
      }
      if (claim >= FIRST_MODERN) {
        const methodRefused = methodMismatch(methodHeader, method, 'notification body');
        if (methodRefused) return methodRefused;
      }
      return { kind: 'modern', revision: claim, notification: true };
    }
    if (headerModern) {
      const methodRefused = methodMismatch(methodHeader, method, 'notification body');
      if (methodRefused) return methodRefused;
      return { kind: 'modern', revision: versionHeader, notification: true };
    }
    return { kind: 'legacy' };
  }
  return invalid('Bad Request: the request body is not a valid JSON-RPC message');
}

function methodMismatch(header: string | undefined, method: string, what: string): Era | undefined {
  if (header === undefined || header === method) return undefined;
  return { kind: 'refused', status: 400, error: mismatch(header, `the ${what} names method ${method} but the Mcp-Method header names ${header}`) };
}

// claimOf is the envelope's protocol-version claim, when the params carry
// one.
function claimOf(params: unknown): unknown {
  if (!isObject(params) || !isObject(params._meta)) return undefined;
  return params._meta[VERSION_KEY];
}

// validModernClaim is whether the params carry a well-formed envelope that
// names a 2026 revision, which makes even initialize a 2026 request.
function validModernClaim(params: unknown): boolean {
  const claim = claimOf(params);
  return typeof claim === 'string' && claim >= FIRST_MODERN && envelopeIssues(params).length === 0;
}

// envelopeIssues lists what is wrong with a 2026 request's envelope: each
// required key that is missing, then each that is malformed.
function envelopeIssues(params: unknown): Array<[string, string]> {
  const meta = isObject(params) && isObject(params._meta) ? params._meta : {};
  const issues: Array<[string, string]> = [];
  for (const key of [VERSION_KEY, CLIENT_INFO_KEY, CLIENT_CAPABILITIES_KEY]) {
    if (!(key in meta)) issues.push([key, 'missing']);
  }
  if (VERSION_KEY in meta && typeof meta[VERSION_KEY] !== 'string') issues.push([VERSION_KEY, 'expected string']);
  if (CLIENT_INFO_KEY in meta) {
    const info = meta[CLIENT_INFO_KEY];
    if (!isObject(info) || typeof info.name !== 'string' || typeof info.version !== 'string') {
      issues.push([CLIENT_INFO_KEY, 'expected an object with a name and a version']);
    }
  }
  if (CLIENT_CAPABILITIES_KEY in meta && !isObject(meta[CLIENT_CAPABILITIES_KEY])) issues.push([CLIENT_CAPABILITIES_KEY, 'expected object']);
  return issues;
}

function isRpc(message: unknown): message is Record<string, unknown> {
  return isObject(message) && message.jsonrpc === '2.0';
}

function isId(id: unknown): boolean {
  return typeof id === 'string' || (typeof id === 'number' && Number.isInteger(id));
}

function paramsOk(message: Record<string, unknown>): boolean {
  return !('params' in message) || isObject(message.params);
}

function isRequest(message: unknown): message is Record<string, unknown> {
  return isRpc(message) && typeof message.method === 'string' && 'id' in message && isId(message.id) && paramsOk(message);
}

function isNotification(message: unknown): message is Record<string, unknown> {
  return isRpc(message) && typeof message.method === 'string' && !('id' in message) && paramsOk(message);
}

function isResponse(message: unknown): boolean {
  if (!isRpc(message) || 'method' in message) return false;
  const result = 'id' in message && isId(message.id) && isObject(message.result);
  const error = isObject(message.error) && Number.isInteger(message.error.code) && typeof message.error.message === 'string';
  return result || error;
}

function isMessage(message: unknown): boolean {
  return isRequest(message) || isNotification(message) || isResponse(message);
}

// echoableId is the id an entry-built error answers with: a single
// request's, else null.
function echoableId(body: unknown): unknown {
  return isObject(body) && typeof body.method === 'string' && 'id' in body && isId(body.id) ? body.id : null;
}

// isJson is whether a Content-Type is JSON's, parameters aside.
function isJson(contentType: string | null): boolean {
  return (contentType ?? '').split(';')[0].trim().toLowerCase() === 'application/json';
}

// refuseAccept is the transport's 406 for a client that does not take both
// JSON and an event stream.
function refuseAccept(request: Request): Response | undefined {
  const accept = request.headers.get('accept') ?? '';
  if (accept.includes('application/json') && accept.includes('text/event-stream')) return undefined;
  return entryError(406, rpcError(SERVER_ERROR, 'Not Acceptable: Client must accept both application/json and text/event-stream'), null);
}

// readCapped reads the body, or undefined once it passes limit bytes; it
// stops reading there, so an oversized body is never held.
async function readCapped(request: Request, limit: number): Promise<Uint8Array | undefined> {
  const declared = Number(request.headers.get('content-length') ?? '');
  if (Number.isFinite(declared) && declared > limit) return undefined;
  if (!request.body) return new Uint8Array();
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > limit) {
      await reader.cancel().catch(() => {});
      return undefined;
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return bytes;
}

function resultMessage(result: unknown, id: unknown): Record<string, unknown> {
  return { result, jsonrpc: '2.0', id };
}

function errorMessage(error: RpcError, id: unknown): Record<string, unknown> {
  return { jsonrpc: '2.0', id, error: errorValue(error) };
}

// entryError is an error the entry or the transport answers before any
// method runs, as the SDK writes it.
function entryError(status: number, error: RpcError, id: unknown): Response {
  return jsonResponse(status, { jsonrpc: '2.0', error: errorValue(error), id });
}

function jsonResponse(status: number, value: unknown): Response {
  return new Response(JSON.stringify(value), { status, headers: { 'content-type': 'application/json' } });
}

function accepted(): Response {
  return new Response(null, { status: 202 });
}

// The router's endpoint.

/** Where one tool argument goes in the request to its operation's route. */
export interface McpToolArgument {
  /** The argument's key in the tool's arguments: its property in inputSchema. */
  readonly key: string;
  /** path: a path parameter; query: a query parameter, once per element of a list; body: a member of the JSON body object. */
  readonly in: 'path' | 'query' | 'body';
  /** The parameter's or the member's wire name. */
  readonly name: string;
}

/** One tool of a generated router: what tools/list answers, and the operation whose route a call runs through. */
export interface McpRouteTool extends McpTool {
  /** The operation's key in the router's operation table. */
  readonly operation: string;
  readonly arguments: readonly McpToolArgument[];
  /**
   * Whether a call runs only once the person confirms it: true for a tool
   * whose invocation policy is one the build's policy confirms, ask in the
   * core's (D93).
   */
  readonly confirm?: boolean;
}

/** The router options of an API's MCP endpoint. */
export interface McpOptions {
  /** The name and version initialize reports; the API's name and its served OpenAPI version by default. */
  readonly serverInfo?: McpServerInfo;
  /** Instructions initialize gives the client. */
  readonly instructions?: string;
  /** Requests per minute per client on the endpoint itself; absent or 0 for none. Each call's route keeps its own @rateLimit. */
  readonly rateLimitPerMinute?: number;
  /** Seconds a request to the endpoint may take before it answers 504; absent or 0 for none. Each call's route keeps its own @timeout. */
  readonly timeoutSeconds?: number;
  /**
   * The key the endpoint signs its confirmations with (D93): at least 32
   * bytes, random, the same on every replica. Required when a tool
   * confirms; the generated entrypoint passes the @envVars secret
   * outputs.api.mcpConfirmationKey names.
   */
  readonly confirmationKey?: string | Revealable<string>;
  /** Seconds a confirmation is good for; 600 unless set. */
  readonly confirmationTtlSeconds?: number;
  /**
   * Where a confirmation is claimed so it redeems once; a store in process
   * memory unless given, so a token is single-use per replica. A
   * deployment with several replicas passes one they share.
   */
  readonly confirmationStore?: McpConfirmationStore;
}

/** What the generated router says about its endpoint. */
export interface McpMount {
  /** The API's name: the server's default name, and the name in an unknown tool's error. */
  readonly api: string;
  /** The server's default version. */
  readonly version: string;
  /**
   * Whether the endpoint needs a caller: true when a route of the API
   * does, so the endpoint authenticates as those routes do and answers 401
   * without a credential.
   */
  readonly requiresAuth: boolean;
}

/**
 * Headers of the endpoint's request a call's request does not carry: its
 * own body's, its conditions, the MCP transport's and the connection's.
 * Every other one goes on, so an authenticator that reads its own header
 * (an API key, say) authenticates the call as it did the endpoint.
 */
const UNFORWARDED = new Set([
  'accept',
  'connection',
  'content-encoding',
  'content-length',
  'content-type',
  'expect',
  'host',
  'if-match',
  'if-modified-since',
  'if-none-match',
  'if-range',
  'if-unmodified-since',
  'keep-alive',
  'last-event-id',
  'mcp-method',
  'mcp-name',
  'mcp-protocol-version',
  'mcp-session-id',
  'range',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
]);

/** The operation of each tool, checked when the router mounts the endpoint. */
function toolOperations(operations: Readonly<Record<string, OperationSpec>>, tools: readonly McpRouteTool[]): Map<string, { tool: McpRouteTool; spec: OperationSpec }> {
  const byName = new Map<string, { tool: McpRouteTool; spec: OperationSpec }>();
  for (const tool of tools) {
    const spec = Object.hasOwn(operations, tool.operation) ? operations[tool.operation] : undefined;
    if (!spec) throw new Error(`mountMcp: tool ${tool.name} names operation ${tool.operation}, which the router does not have`);
    if (byName.has(tool.name)) throw new Error(`mountMcp: two tools are named ${tool.name}`);
    if (tool.confirm && tool.arguments.some(argument => argument.key === MCP_CONFIRMATION_ARGUMENT)) {
      throw new Error(`mountMcp: tool ${tool.name} asks the person before it runs, and takes an argument ${MCP_CONFIRMATION_ARGUMENT}, which carries its confirmation`);
    }
    byName.set(tool.name, { tool, spec });
  }
  return byName;
}

/**
 * Mounts the API's MCP endpoint on app, the generated router whose
 * operation table is operations: POST /api/mcp, and GET and DELETE, which
 * answer 405, each behind the runtime's auth gate with runtime, the
 * router's options. Each request lists the tools whose routes its caller
 * may call, by the route's auth requirement and the router's permission
 * matcher, and runs a call through app in process, a call of a tool that
 * confirms once the person confirms it, bound to the caller (D93), and
 * only once its route's check, which the route's mount recorded on app,
 * admits it (checkRoute, D98). Throws when a tool names an operation the
 * router does not have, and when a tool confirms and
 * options.confirmationKey is absent or short.
 */
export function mountMcp<E extends Env>(
  app: Hono<E>,
  operations: Readonly<Record<string, OperationSpec>>,
  tools: readonly McpRouteTool[],
  runtime: RouterRuntimeOptions,
  mount: McpMount,
  options: McpOptions = {}
): void {
  const byName = toolOperations(operations, tools);
  const confirming = tools.find(tool => tool.confirm);
  if (confirming && options.confirmationKey === undefined) {
    throw new Error(
      `mountMcp: tool ${confirming.name} runs only once the person confirms it, so options.mcp.confirmationKey is required: a random secret of at least 32 bytes, the same on every replica (D93)`
    );
  }
  const confirmations = confirming
    ? new McpConfirmations({
        key: options.confirmationKey as string | Revealable<string>,
        scope: mount.api,
        ...(options.confirmationTtlSeconds !== undefined ? { ttlSeconds: options.confirmationTtlSeconds } : {}),
        ...(options.confirmationStore ? { store: options.confirmationStore } : {}),
      })
    : undefined;
  const server: McpServerOptions = {
    serverInfo: options.serverInfo ?? { name: mount.api, version: mount.version },
    ...(options.instructions ? { instructions: options.instructions } : {}),
    ...(confirmations ? { confirmations } : {}),
  };
  // Each request is served as its caller, whom a confirmation is bound to.
  const serve = (c: Context<E>, ctx: RequestContext) =>
    serveMcp(c.req.raw, routerTools(app, byName, runtime, mount, c, ctx), { ...server, caller: callerOf(ctx), requestId: ctx.requestId });
  for (const method of ['POST', 'GET', 'DELETE'] as const) {
    const spec: OperationSpec = {
      name: '$mcp',
      namespace: 'mcp',
      method,
      path: MCP_PATH,
      pathParams: [],
      queryParams: [],
      bodyParams: [],
      auth: { public: !mount.requiresAuth, required: mount.requiresAuth, permissions: [] },
      manual: true,
    };
    mountManualOperation(app, spec, serve, runtime, {
      rateLimitPerMinute: options.rateLimitPerMinute,
      timeoutSeconds: options.timeoutSeconds,
    });
  }
}

/** The tools one request to the endpoint is served, as its caller. */
function routerTools<E extends Env>(
  app: Hono<E>,
  byName: ReadonlyMap<string, { tool: McpRouteTool; spec: OperationSpec }>,
  runtime: RouterRuntimeOptions,
  mount: McpMount,
  c: Context<E>,
  ctx: RequestContext
): McpToolSet {
  // A tool whose route the caller may call: its auth requirement, by the
  // router's permission matcher, admits the caller the gate established.
  const offered = (spec: OperationSpec): boolean => {
    try {
      authorize(ctx.principal, spec.auth, runtime.permissionMatcher);
      return true;
    } catch {
      return false;
    }
  };
  return {
    list: () =>
      [...byName.values()]
        .filter(({ spec }) => offered(spec))
        .map(({ tool }) => ({
          name: tool.name,
          ...(tool.title !== undefined ? { title: tool.title } : {}),
          ...(tool.description !== undefined ? { description: tool.description } : {}),
          inputSchema: tool.confirm ? withConfirmationArgument(tool.inputSchema) : tool.inputSchema,
          ...(tool.annotations !== undefined ? { annotations: tool.annotations } : {}),
          ...(tool._meta !== undefined ? { _meta: tool._meta } : {}),
        })),
    confirms: name => {
      const entry = byName.get(name);
      return entry?.tool.confirm && offered(entry.spec) ? entry.tool : undefined;
    },
    // The call's request through its route's check (D98): the same
    // request call makes, and every step of the route before the
    // implementation but its rate limit.
    check: async (name, args) => {
      const entry = byName.get(name);
      if (!entry || !offered(entry.spec)) throw new McpUnknownToolError(`${mount.api} has no tool ${JSON.stringify(name)}`);
      const request = toolRequest(entry.tool, entry.spec, args, c.req.raw, ctx);
      if (request instanceof HttpProblem) return problemBody(request, ctx.requestId);
      const refused = await checkRoute(app, request, c.env);
      return refused ? problemOf(refused, ctx.requestId) : undefined;
    },
    call: async (name, args) => {
      const entry = byName.get(name);
      if (!entry || !offered(entry.spec)) throw new McpUnknownToolError(`${mount.api} has no tool ${JSON.stringify(name)}`);
      const request = toolRequest(entry.tool, entry.spec, args, c.req.raw, ctx);
      if (request instanceof HttpProblem) return { problem: problemBody(request, ctx.requestId) };
      return outcomeOf(await app.fetch(request, c.env), ctx.requestId, new URL(request.url).pathname);
    },
  };
}

/**
 * Who is calling, which a confirmation is bound to (D93): the principal's
 * subject and the calling service's deployable the gate established, as
 * JSON, null for each it did not.
 */
function callerOf(ctx: RequestContext): string {
  return JSON.stringify([ctx.principal?.subject ?? null, ctx.serviceCaller?.deployable ?? null]);
}

/**
 * The input schema of a tool that confirms (D93), with the optional
 * argument a confirmation token comes back in, so a client that checks
 * arguments against it sends the token, and a model reads where it goes.
 */
function withConfirmationArgument(inputSchema: unknown): unknown {
  if (!isObject(inputSchema)) return inputSchema;
  const properties = isObject(inputSchema.properties) ? inputSchema.properties : {};
  return {
    ...inputSchema,
    properties: {
      ...properties,
      [MCP_CONFIRMATION_ARGUMENT]: {
        type: 'string',
        description: 'The confirmation token a call of this tool answered with confirmation_required, sent once the person confirms the call; leave it out to ask for one.',
      },
    },
  };
}

/** A path or query value as the route reads it. */
function wireText(value: unknown): string {
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') return String(value);
  return JSON.stringify(value);
}

/**
 * The request to the tool's route that a call with args makes, carrying
 * the endpoint request's credentials and request id, or the refusal of
 * args that name an argument the tool does not take or leave out a path
 * parameter.
 */
function toolRequest(tool: McpRouteTool, spec: OperationSpec, args: Readonly<Record<string, unknown>>, raw: Request, ctx: RequestContext): Request | HttpProblem {
  const keys = new Set(tool.arguments.map(argument => argument.key));
  for (const key of Object.keys(args)) {
    if (!keys.has(key)) {
      const takes = keys.size > 0 ? `takes ${[...keys].join(', ')}` : 'takes no arguments';
      return new HttpProblem(400, `${tool.name} ${takes}, not ${JSON.stringify(key)}`, { code: ErrorCode.BAD_REQUEST });
    }
  }
  let path = spec.path;
  const query = new URLSearchParams();
  const body: Record<string, unknown> = {};
  let hasBody = false;
  for (const argument of tool.arguments) {
    const value = args[argument.key];
    switch (argument.in) {
      case 'path':
        if (value === undefined || value === null) {
          return new HttpProblem(400, `${tool.name}: ${argument.key} is required`, { code: ErrorCode.BAD_REQUEST });
        }
        path = path.split(`{${argument.name}}`).join(encodeURIComponent(wireText(value)));
        break;
      case 'query':
        if (value === undefined || value === null) break;
        for (const item of Array.isArray(value) ? value : [value]) query.append(argument.name, wireText(item));
        break;
      case 'body':
        if (value === undefined) break;
        body[argument.name] = value;
        hasBody = true;
        break;
    }
  }
  const url = new URL(raw.url);
  url.pathname = path;
  url.search = query.toString();
  const headers = new Headers();
  raw.headers.forEach((value, name) => {
    if (!UNFORWARDED.has(name.toLowerCase())) headers.append(name, value);
  });
  headers.set('accept', 'application/json');
  headers.set('x-request-id', ctx.requestId);
  if (hasBody) headers.set('content-type', 'application/json');
  return new Request(url, {
    method: spec.method,
    headers,
    signal: ctx.signal,
    ...(hasBody ? { body: JSON.stringify(body) } : {}),
  });
}

function mediaType(response: Response): string {
  return (response.headers.get('content-type') ?? '').split(';')[0].trim().toLowerCase();
}

function isJsonMedia(type: string): boolean {
  return type === 'application/json' || type.endsWith('+json');
}

function isTextMedia(type: string): boolean {
  return type === '' || type.startsWith('text/') || type === 'application/xml' || type.endsWith('+xml');
}

function base64(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

/**
 * A route's refusal (4xx or 5xx) as its problem document, or one made of
 * its status and text when the route answered another body.
 */
async function problemOf(response: Response, requestId: string): Promise<ProblemBody> {
  const text = await response.text();
  if (isJsonMedia(mediaType(response))) {
    try {
      const problem: unknown = JSON.parse(text);
      if (isObject(problem)) return problem as ProblemBody;
    } catch {
      // Not JSON after all: made from its text below.
    }
  }
  return problemBody(new HttpProblem(response.status, text.trim() || 'Request failed'), requestId);
}

/**
 * What the route's response comes to: a refusal as its problem document
 * (problemOf); a success's envelope data, a JSON body that is no envelope
 * as it is, text as text content, and any other body as a resource at the
 * route's path.
 */
async function outcomeOf(response: Response, requestId: string, uri: string): Promise<McpCallOutcome> {
  if (response.status >= 400) return { problem: await problemOf(response, requestId) };
  const type = mediaType(response);
  const bytes = new Uint8Array(await response.arrayBuffer());
  const text = new TextDecoder().decode(bytes);
  if (bytes.length === 0) return { result: null };
  if (isJsonMedia(type)) {
    try {
      const value: unknown = JSON.parse(text);
      return { result: isObject(value) && 'data' in value && isObject(value.meta) ? value.data : value };
    } catch {
      return { content: [{ type: 'text', text }] };
    }
  }
  if (isTextMedia(type)) return { content: [{ type: 'text', text }] };
  return { content: [{ type: 'resource', resource: { uri, mimeType: type, blob: base64(bytes) } }] };
}
