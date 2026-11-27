// The notes server on development mode (D117): the notes schema served by
// @superschematic/rules-handler, the handler of the behaviors' core, in a
// server written by hand. A note is kept as JSON in the handler's own
// tables over one SQLite connection, a publish takes effect at once, and
// Workflow, Comments and Revisions run on the core
// (runtime/engine/rust/rules-behaviors). One Fetch handler serves the
// routes, the event stream and the MCP endpoint, which
// @hono/node-server listens with on Node.js and on Bun.
//
// How it is built (README.md, "How it works"):
//
// - The type is Note, as the schema file names it, and the routes are
//   under /namespaces/default/schemas/Note.
// - A row is its own fields beside its behaviors' fields, each named after
//   its behavior (workflowStatus, commentsMessages), and its version is
//   _version, which the ETag carries.
// - A caller is the one its bearer token names (authenticate), and the
//   access policy is data (devPolicy), asked before every request.
// - The event stream carries what the behaviors emit about their rows, and
//   with signals=1 the signals beside them, a streamed reply's draft among
//   them.
// - A publish has no compatibility rule: a version may add a required
//   field, and its publish is refused while a stored note lacks it (409
//   conflict) unless it discards those notes.
import { readFileSync } from 'node:fs';
import type { Server } from 'node:http';

import { serve } from '@hono/node-server';
import { McpUnknownToolError, serveMcp, type McpToolSet } from '@superschematic/http-runtime/mcp';
import { Dev, SqliteStore, type Authenticate, type Caller, type DevMcp, type Policy, type SqliteConnection } from '@superschematic/rules-handler';

/** The notes schema, in the JSON data form of a schema file. */
export const notesSchema: Record<string, unknown> = JSON.parse(readFileSync(new URL('../schemas/notes.schema.json', import.meta.url), 'utf8'));

/** The callers this example knows, by bearer token. */
export const callers: ReadonlyMap<string, Caller> = new Map<string, Caller>([
  // An author: reads and writes notes.
  ['alice-token', { subject: 'alice', permissions: ['notes.read', 'notes.write'] }],
  // An editor: everything under notes, publishing and reviewing included.
  ['bob-token', { subject: 'bob', permissions: ['notes'] }],
  // A reader: reads notes, comments on them and proposes changes.
  ['carol-token', { subject: 'carol', permissions: ['notes.read'] }],
  // A schema owner: defines and publishes schemas, reads no notes.
  ['dana-token', { subject: 'dana', permissions: ['schemas'] }],
]);

/**
 * authenticate is the host's Authenticate: it turns a request's bearer
 * token into the caller it names, or undefined (401). This example looks
 * the token up in a fixed table; a deployment verifies a signed token or a
 * session instead.
 */
export const authenticate: Authenticate = (request) => {
  const match = /^Bearer\s+(\S+)$/i.exec((request.headers.get('authorization') ?? '').trim());
  return match === null ? undefined : callers.get(match[1]!);
};

/**
 * The access policy, as development mode's data, asked before every
 * request. Permissions are dotted names, and a permission covers every one
 * nested under it, so bob's notes covers notes.read, notes.write, and the
 * notes.publish and notes.review the schema's behaviors name. Defining and
 * publishing take schemas.<action>; a reader may post, react, stream a
 * reply and propose a change, which need notes.read; the rest takes
 * notes.<action>. Signals and read state are asked as reads.
 */
export const devPolicy: Policy = {
  rules: [
    { action: ['define', 'publish'], require: 'schemas.{action}' },
    {
      action: 'write',
      operation: ['post', 'react', 'unreact', 'propose', 'begin', 'appendDraft', 'finish', 'interrupt'],
      require: 'notes.read',
    },
    { action: ['read', 'write'], require: 'notes.{action}' },
  ],
};

/**
 * The caller the server publishes its schema as: it defines and publishes
 * schemas, and reads notes, so the list of live types shows it Note.
 */
const deployer: Caller = { subject: 'engine-notes', permissions: ['schemas', 'notes.read'] };

/**
 * The MCP endpoint: the HTTP runtime's transport over each caller's tools.
 * A refused call's problem is the document the handler writes, which the
 * runtime's ProblemBody types.
 */
const mcp: DevMcp = {
  serve: (request, tools, { caller, requestId }) =>
    serveMcp(request, tools as McpToolSet, { serverInfo: { name: 'engine-notes', version: '0.0.0' }, caller: caller.subject, requestId }),
  unknownTool: (message) => new McpUnknownToolError(message),
};

/**
 * openNotesDev serves the notes schema on development mode over a SQLite
 * connection (bun:sqlite's Database or node:sqlite's DatabaseSync, over a
 * file or in memory), and publishes the schema when Note is not live yet,
 * so a restart over the same file keeps the notes and the version they
 * are at. The change queue and the deadlines run after each request.
 */
export async function openNotesDev(connection: SqliteConnection): Promise<Dev> {
  const dev = await Dev.open((layout, codec) => new SqliteStore(connection, layout, codec), { policy: devPolicy, authenticate, mcp });
  const live = await dev.request(deployer, { kind: 'types' });
  if (!live.some((type) => type.name === 'Note')) {
    await dev.request(deployer, { kind: 'define', document: notesSchema });
    await dev.request(deployer, { kind: 'publish', type: 'Note' });
  }
  return dev;
}

export interface Listening {
  /** The server's origin, such as http://127.0.0.1:8787. */
  url: string;
  close(): Promise<void>;
}

/**
 * listen serves dev's routes, under /namespaces/default, with
 * @hono/node-server, on Node.js and on Bun. Port 0 picks a free one.
 */
export function listen(dev: Dev, port = 0, hostname = '127.0.0.1'): Promise<Listening> {
  return new Promise((resolve, reject) => {
    const server = serve({ fetch: (request: Request) => dev.http(request), port, hostname }, (info) => {
      server.off('error', reject);
      resolve({
        url: `http://${hostname}:${info.port}`,
        close: () =>
          new Promise<void>((done, fail) => {
            server.close((error) => (error ? fail(error) : done()));
            // An open event stream holds its connection until it ends.
            server.closeAllConnections();
          }),
      });
    }) as Server;
    // A port in use fails the start instead of leaving it waiting.
    server.once('error', reject);
  });
}
