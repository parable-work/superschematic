// The notes server: an engine over one SQLite file, the notes schema
// published into it, and the engine's HTTP API, event stream and MCP
// endpoint on one Hono app.
import { readFileSync } from 'node:fs';
import type { Server } from 'node:http';

import { serve } from '@hono/node-server';
import { openEngine, type Engine, type Principal } from '@superschematic/engine';
import { engineApp, type EngineHttpOptions } from '@superschematic/engine/http';
import { engineMcp } from '@superschematic/engine/mcp';
import { Hono } from 'hono';

import { authenticate, policy } from './auth.ts';

/** The notes schema, in the JSON data form of a schema file. */
export const notesSchema: Record<string, unknown> = JSON.parse(
  readFileSync(new URL('../schemas/notes.schema.json', import.meta.url), 'utf8')
);

/** The principal the server publishes its schema as. */
const deployer: Principal = { subject: 'engine-notes', permissions: ['schemas'] };

/**
 * openNotes opens the engine's file, creating it if absent, and publishes
 * the schema. Publishing the version that is already live mints nothing, so
 * a restart with the same file changes nothing; a changed schema becomes the
 * next version if the compatibility rule allows it, and otherwise the
 * engine refuses it and the server does not start.
 */
export function openNotes(path: string, schema: Record<string, unknown> = notesSchema): Engine {
  const engine = openEngine({ path, policy });
  try {
    engine.schemas.define(deployer, schema);
    engine.schemas.publish(deployer, 'notes');
  } catch (error) {
    engine.close();
    throw error;
  }
  return engine;
}

/** notesApp serves the engine under /api: the HTTP API, the event stream and MCP. */
export function notesApp(engine: Engine): Hono {
  const options: EngineHttpOptions = { authenticate, rateLimitPerMinute: 600, timeoutSeconds: 10 };
  const app = new Hono();
  app.route('/api', engineApp(engine, options));
  app.route('/api', engineMcp(engine, { ...options, serverInfo: { name: 'engine-notes', version: '0.0.0' } }));
  return app;
}

export interface Listening {
  /** The server's origin, such as http://127.0.0.1:8787. */
  url: string;
  close(): Promise<void>;
}

/** listen serves the app with @hono/node-server, on Node.js and on Bun. Port 0 picks a free one. */
export function listen(app: Hono, port = 0, hostname = '127.0.0.1'): Promise<Listening> {
  return new Promise((resolve, reject) => {
    const server = serve({ fetch: app.fetch, port, hostname }, (info) => {
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
