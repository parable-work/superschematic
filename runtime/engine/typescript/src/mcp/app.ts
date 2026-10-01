/*
The engine's MCP endpoint (D16): /namespaces/{namespace}/mcp speaks MCP
over the streamable HTTP transport, one tool per operation of every live
schema the namespace reaches and the schema tools (tools/catalog.ts).

The protocol is the official TypeScript SDK's (@modelcontextprotocol/server,
an optional peer dependency of this entry point): createMcpHandler serves
each request with a fresh low-level Server over a web-standard
Request/Response exchange, statelessly, for the 2025 protocol revisions
(initialize, tools/list, tools/call; GET and DELETE answer 405) and for
2026-07-28 (server/discover). It needs no HTTP server of its own, so the
route mounts on Hono beside the engine's others.

The route goes through the HTTP runtime (D15) like every engine route:
its rate limit, timeout, body limit and authentication gate, with the
deployment's Authenticator. The caller it establishes is the principal
every tools/list and tools/call acts as, so the engine's access policy
answers each call. A call the engine refuses returns a tool error
(isError) whose structured content is the problem document the HTTP API
would answer with; a tool the namespace does not have is a JSON-RPC
invalid-params error, and a malformed message the SDK's JSON-RPC error.
*/

import { readFileSync } from 'node:fs';

import { Server, ProtocolError, ProtocolErrorCode, createMcpHandler, type CallToolResult, type McpRequestContext, type Tool } from '@modelcontextprotocol/server';
import { internal, problemBody, unauthorized, HttpProblem, type OperationSpec, type RequestContext } from '@superschematic/http-runtime';
import { errorHandler, mountManualOperation, notFoundHandler, type RouterRuntimeOptions } from '@superschematic/http-runtime/hono';
import { Hono } from 'hono';

import type { Principal } from '../access.js';
import type { Engine } from '../engine.js';
import { engineProblem } from '../http/problems.js';
import { isPlainObject } from '../instances/patch.js';
import { UnknownToolError } from '../tools/catalog.js';

export interface EngineMcpOptions extends RouterRuntimeOptions {
  /** Requests per minute per client (the runtime's @rateLimit); absent or 0 for none. */
  rateLimitPerMinute?: number;
  /** Seconds a request may take before it answers 504 (the runtime's @timeout); absent or 0 for none. */
  timeoutSeconds?: number;
  /** The name and version initialize reports; @superschematic/engine and this package's version by default. */
  serverInfo?: { name: string; version: string };
  /** Instructions initialize gives the client. */
  instructions?: string;
}

/** The MCP endpoint's path, relative to where the app is mounted. */
export const MCP_PATH = '/namespaces/{namespace}/mcp';

// What the route hands the SDK's factory for one request.
interface Caller {
  principal: Principal;
  namespace: string;
  ctx: RequestContext;
}

/**
 * engineMcp builds the Hono app that serves an engine's MCP endpoint.
 * Mount it where the HTTP API is mounted
 * (`app.route('/api', engineMcp(engine, options))`), with the same
 * options.
 */
export function engineMcp(engine: Engine, options: EngineMcpOptions = {}): Hono {
  const deployed = options.onError;
  const mapError = async (error: unknown, ctx: RequestContext): Promise<HttpProblem | Response | undefined> =>
    engineProblem(error) ?? (deployed ? await deployed(error, ctx) : undefined);
  const runtime: RouterRuntimeOptions = { ...options, onError: mapError };
  const serverInfo = options.serverInfo ?? packageInfo();
  const handler = createMcpHandler((context) => serverFor(engine, context, serverInfo, options.instructions, mapError));
  const app = new Hono();

  for (const method of ['POST', 'GET', 'DELETE'] as const) {
    const spec: OperationSpec = {
      name: `mcp${method[0]}${method.slice(1).toLowerCase()}`,
      namespace: 'engine',
      method,
      path: MCP_PATH,
      pathParams: [{ name: 'namespace', kind: 'string', required: true }],
      queryParams: [],
      bodyParams: [],
      auth: { public: false, required: true, permissions: [] },
      manual: true,
      ...(options.rateLimitPerMinute ? { rateLimitPerMinute: options.rateLimitPerMinute } : {}),
      ...(options.timeoutSeconds ? { timeoutSeconds: options.timeoutSeconds } : {}),
    };
    mountManualOperation(
      app,
      spec,
      (c, ctx) => {
        const principal = principalOf(ctx);
        // An unknown namespace is the HTTP API's 404 problem, before any
        // MCP exchange.
        const namespace = engine.namespaces.resolve(c.req.param('namespace') as string);
        const caller: Caller = { principal, namespace, ctx };
        return handler.fetch(c.req.raw, {
          authInfo: { token: ctx.bearerToken ?? '', clientId: principal.subject, scopes: [...principal.permissions], extra: { caller } },
        });
      },
      runtime
    );
  }

  app.notFound(notFoundHandler());
  app.onError(errorHandler((error) => engineProblem(error)));
  return app;
}

// serverFor is the SDK factory: a Server for one request, whose tools are
// the namespace's as the caller sees them.
function serverFor(
  engine: Engine,
  context: McpRequestContext,
  serverInfo: { name: string; version: string },
  instructions: string | undefined,
  mapError: (error: unknown, ctx: RequestContext) => Promise<HttpProblem | Response | undefined>
): Server {
  const caller = (context.authInfo?.extra as { caller?: Caller } | undefined)?.caller;
  if (!caller) {
    throw new Error('the MCP endpoint serves only requests its route authenticated');
  }
  const { principal, namespace, ctx } = caller;
  const server = new Server(serverInfo, { capabilities: { tools: { listChanged: false } }, ...(instructions ? { instructions } : {}) });
  server.setRequestHandler('tools/list', () => ({ tools: listTools(engine, principal, namespace) }));
  server.setRequestHandler('tools/call', async (request) => {
    try {
      return toolResult(engine.tools.call(principal, request.params.name, request.params.arguments, { namespace }));
    } catch (error) {
      if (error instanceof UnknownToolError) {
        throw new ProtocolError(ProtocolErrorCode.InvalidParams, error.message);
      }
      const mapped = await mapError(error, ctx);
      const problem = mapped instanceof HttpProblem ? mapped : internal(undefined, { cause: error });
      const body = problemBody(problem, ctx.requestId);
      return { isError: true, content: [{ type: 'text', text: JSON.stringify(body) }], structuredContent: { ...body } };
    }
  });
  return server;
}

/**
 * listTools is the namespace's visible tools as MCP tools: the handle as
 * the name, the title and description, the argument schema as the input
 * schema, whether it only reads, and `_meta` with the tool's guidance and
 * its invocation policy under the policy's key.
 */
export function listTools(engine: Engine, principal: Principal, namespace: string): Tool[] {
  const policyKey = engine.tools.options.invocationPolicy.key;
  const tools: Tool[] = [];
  for (const tool of engine.tools.manifest(principal, { namespace }).tools) {
    if (tool.mcp.hidden) {
      continue;
    }
    tools.push({
      name: tool.mcp.handle,
      title: tool.mcp.name,
      description: tool.mcp.description,
      inputSchema: tool.parameters as Tool['inputSchema'],
      annotations: { readOnlyHint: tool.replay?.mode === 'read_only' },
      _meta: { ...(tool.mcp._meta ?? {}), [policyKey]: tool.mcp[policyKey] },
    });
  }
  return tools;
}

// toolResult is a call's result as MCP carries it: its JSON as text, and
// as structured content when it is an object.
function toolResult(result: unknown): CallToolResult {
  const text = JSON.stringify(result === undefined ? null : result);
  return isPlainObject(result) ? { content: [{ type: 'text', text }], structuredContent: result } : { content: [{ type: 'text', text }] };
}

// The caller the runtime gate established, as the HTTP API reads it.
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

function packageInfo(): { name: string; version: string } {
  const manifest = JSON.parse(readFileSync(new URL('../../package.json', import.meta.url), 'utf8')) as { name: string; version: string };
  return { name: manifest.name, version: manifest.version };
}
