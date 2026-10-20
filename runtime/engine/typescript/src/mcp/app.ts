/*
The engine's MCP endpoint (D16): /namespaces/{namespace}/mcp speaks MCP
over the streamable HTTP transport, one tool per operation of every live
schema the namespace reaches, the schema tools and the search across
schemas. The engine serves it itself (D58): MCP's stateless JSON-RPC, on
the 2025 protocol revisions (initialize, tools/list, tools/call; GET and
DELETE answer 405) and 2026-07-28 (server/discover), so instances share
nothing for it. engineMcp is the Hono adapter that mounts it
(src/native/http.ts).

The route goes through the HTTP runtime (D15) like every engine route:
its rate limit, timeout, body limit, service step and authentication
gate, with the deployment's Authenticator, or the identity service of
the core user model (D50), and, when it passes one, its service
authenticator (D37). The caller it establishes is the principal every
tools/list and tools/call acts as, so the engine's access policy answers
each call, and the list holds the tools the policy lets that caller see.
The mount's `tools` filter narrows it further, per caller. A call the
engine refuses returns a tool error (isError) whose structured content
is the problem document the HTTP API would answer with; a tool the
namespace does not have is a JSON-RPC invalid-params error.
*/

import type { RouterRuntimeOptions } from '@superschematic/http-runtime/hono';
import type { IdentityService } from '@superschematic/http-runtime/identity';
import type { Hono } from 'hono';

import { nativeEngineMcp, type ServedEngine } from '../native/http.js';
import type { ToolFilter } from '../types/tools.js';

export interface EngineMcpOptions extends RouterRuntimeOptions {
  /** Requests per minute per client (the runtime's @rateLimit); absent or 0 for none. */
  rateLimitPerMinute?: number;
  /** Seconds a request may take before it answers 504 (the runtime's @timeout); absent or 0 for none. */
  timeoutSeconds?: number;
  /** The name and version initialize reports; @superschematic/engine and this package's version by default. */
  serverInfo?: { name: string; version: string };
  /** Instructions initialize gives the client. */
  instructions?: string;
  /**
   * The identity service (D50), engineIdentity's: it authenticates the
   * endpoint in place of authenticate, which is refused beside it, and its
   * credentialed CORS answers the trusted origins on the endpoint alone.
   * The session routes are engineApp's.
   */
  identity?: IdentityService;
  /**
   * Narrows the tools each caller is offered, beyond what the access
   * policy hides: asked with the caller's principal and each tool, true
   * keeps it. A tool it leaves out is not in the caller's tools/list, and
   * tools/call of it is the invalid-params error of a tool the namespace
   * does not have, so an agent's session limited to one schema's
   * operations reaches no other tool. Absent, every tool the policy lets
   * the caller see. It is asked on the JavaScript thread, so an engine
   * opened with openEngineAsync serves without one.
   */
  tools?: ToolFilter;
}

/** The MCP endpoint's path, relative to where the app is mounted. */
export const MCP_PATH = '/namespaces/{namespace}/mcp';

/**
 * engineMcp builds the Hono app that serves an engine's MCP endpoint,
 * openEngine's or openEngineAsync's. Mount it where the HTTP API is
 * mounted (`app.route('/api', engineMcp(engine, options))`), with the same
 * options.
 */
export function engineMcp(engine: ServedEngine, options: EngineMcpOptions = {}): Hono {
  return nativeEngineMcp(engine, options);
}
