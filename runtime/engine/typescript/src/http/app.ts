/*
The engine's HTTP API (D16): a fixed set of routes that carry the
namespace and the schema name as path parameters and resolve them per
request, so publishing a version mounts nothing. The engine serves its
own routes (D58): routing, decoding, the media types, ETags and If-Match,
the Preconditions header, the success envelope and the problem documents
(D15), Location, the D37 stand-in rule and the event stream's every byte
are the Rust engine's. engineApp is the Hono adapter that mounts them
(src/native/http.ts): every route goes through the HTTP runtime, its
request id, rate limit, timeout, body limit and authentication gate. The
runtime gate only requires a caller; what the caller may do is the
engine's access policy, asked on every call with the principal the
deployment's Authenticator returned.

An instance's sequence is its entity tag. A response that carries an
instance, or a behavior operation's result, sends `ETag: "<seq>"`, and
PATCH, DELETE and an operation honour `If-Match` inside the write
transaction, so a lost update answers 412. They also take the
`Preconditions` header, a JSON object of each behavior's entry by its
name (`{"Lease": {"token": 7}}`). A create's body carries `behaviors`
beside `data`, the parameters it gives the type's behaviors.

Besides the instances and the event log, the routes serve a schema's
describe document, the namespace's tools document, the behaviors the
engine runs, a search across the namespace's schemas and a value of the
value store by its hash; the MCP endpoint is the ./mcp entry point's. The
namespaces are listed, created, archived and unarchived under
/namespaces, as the access policy's manage allows.

With the runtime's `authenticateService`, a calling service is verified
on every route and reaches the engine beside the end user, or standing in
for one (callers.ts, D37).

With `actingFor`, the routes honor the `Acting-For` request header (D77):
its value is the subject the caller acts for, which the engine's principal
carries as `actingFor` and every event it writes records as `for`. The
default, false, ignores the header: a request that carries it is answered
as if it did not. true honors it for any caller the runtime authenticated,
and the access policy decides; a function decides per request, and the
subject it approves is the one the engine acts for. A header is read by
one rule, the Rust engine's and the Go binding's too: ASCII whitespace
around it is trimmed, and two headers, a comma, a control character or
any other whitespace is 400 invalid_argument.

With `identity`, the HTTP runtime's identity service (identity.ts, D50),
the service authenticates every route in place of `authenticate`, which is
refused beside it, its credentialed CORS answers the trusted origins on
the app's own routes alone, and the app serves the user model's session
routes under /auth: login, logout, me, capabilities and changePassword,
register when `identityRoutes.register` asks for it, and the
administration routes under /auth/admin when the store has roles.
*/

import type { RouterRuntimeOptions } from '@superschematic/http-runtime/hono';

import type { Principal } from '../access.js';
import type { IdentityService } from '@superschematic/http-runtime/identity';
import type { Hono } from 'hono';

import { nativeEngineApp, type ServedEngine } from '../native/http.js';
import type { ToolFilter } from '../types/tools.js';
import type { StreamOptions } from './stream.js';

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
  /**
   * The identity service (D50), engineIdentity's: it authenticates every
   * route in place of authenticate, which is refused beside it, and the
   * app serves the session routes under /auth.
   */
  identity?: IdentityService;
  /**
   * Which of the user model's routes the app serves beside login, logout,
   * me, capabilities and changePassword: register (off by default), and
   * the administration routes under /auth/admin (on when the store has
   * roles). sessions false serves none of them: the server that mounts
   * the app serves the user model's routes itself (an API's, D58), and
   * the identity service authenticates the app's routes and answers their
   * CORS alone.
   */
  identityRoutes?: { register?: boolean; administration?: boolean; sessions?: boolean };
  /**
   * Aborted when the host stops serving: every event stream ends then, as
   * when its client goes away, so a server's shutdown waits for none.
   */
  done?: AbortSignal;
  /**
   * Narrows the tools document each caller gets, as the MCP endpoint's
   * `tools` option does (EngineMcpOptions.tools): a tool it leaves out is
   * hidden from that caller, with the reason. It narrows only the tools
   * document; the routes themselves answer as the access policy says. It
   * is asked on the JavaScript thread, so an engine opened with
   * openEngineAsync serves without one.
   */
  tools?: ToolFilter;
  /**
   * Whether the routes honor the Acting-For header (D77), whose value is the
   * subject the caller acts for: false (the default) ignores it, true
   * honors it for any authenticated caller and leaves the rest to the
   * access policy, and a function decides per request, given the engine's
   * principal for the caller, the subject and the request.
   */
  actingFor?: ActingForOption;
}

/**
 * Whether a mount honors the Acting-For header (D77): false ignores it, true
 * honors it, and a function decides per request.
 */
export type ActingForOption = boolean | ((principal: Principal, subject: string, request: Request) => boolean | Promise<boolean>);

/** The request header that names the subject a caller acts for (D77). */
export const ACTING_FOR_HEADER = 'Acting-For';

/** The media type of a JSON body. */
export const JSON_MEDIA_TYPE = 'application/json';
/** The media type of an instance update, RFC 7386. */
export const MERGE_PATCH_MEDIA_TYPE = 'application/merge-patch+json';
/** The request header that carries a write's preconditions, a JSON object by behavior name. */
export const PRECONDITIONS_HEADER = 'Preconditions';

/**
 * engineApp builds the Hono app that serves an engine, openEngine's,
 * openEngineAsync's or one opening in the background
 * (openEngineInBackground, @superschematic/engine/server), which answers
 * 503 until it opens. Mount it where the deployment wants it
 * (`app.route('/api', engineApp(engine, options))`); on its own it answers
 * unknown paths and failures with problem documents.
 */
export function engineApp(engine: ServedEngine, options: EngineHttpOptions = {}): Hono {
  return nativeEngineApp(engine, options);
}
