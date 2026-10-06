// The jobs server: an engine over one SQLite file with the work-queue
// behaviors registered and a runner for their sweeps and reactions, the
// three schemas published into it, and the engine's HTTP API on one Hono
// app.
import { readFileSync } from 'node:fs';
import type { Server } from 'node:http';

import { serve } from '@hono/node-server';
import { openEngine, type Engine, type Principal } from '@superschematic/engine';
import { engineApp } from '@superschematic/engine/http';
import { workQueueBehaviors } from '@superschematic/engine-workqueue';
import { Hono } from 'hono';

import { authenticate, policy, runner, workers } from './auth.ts';

/**
 * The schemas, in the JSON data form of a schema file, in the order they
 * are defined: workers release leases on jobs, and a batch stamps jobs
 * and rolls them up, so jobs comes first.
 */
export const schemas: Record<string, unknown>[] = ['jobs', 'workers', 'batches'].map((name) =>
  JSON.parse(readFileSync(new URL(`../schemas/${name}.schema.json`, import.meta.url), 'utf8'))
);

/**
 * The principal the server publishes its schemas and registers its
 * workers as. Defining workers and batches reads jobs: their configs are
 * checked against the live version of the schema they name.
 */
const deployer: Principal = { subject: 'engine-jobs', permissions: ['schemas', 'jobs.read', 'workers'] };

export interface JobsOptions {
  /** The time in epoch milliseconds, Date.now by default: a test passes a clock it moves. */
  clock?: () => number;
}

/**
 * openJobs opens the engine's file, creating it if absent, publishes the
 * schemas and registers a worker instance for each worker the server
 * knows. The work-queue behaviors run only because they are registered
 * here; without them the engine refuses a schema that composes one. The
 * runner acts as `runner` once it is started: main.ts starts it, and a
 * test calls runner.runDue() after it moves the clock.
 */
export function openJobs(path: string, options: JobsOptions = {}): Engine {
  const engine = openEngine({ path, policy, behaviors: workQueueBehaviors, runner: { principal: runner }, clock: options.clock });
  try {
    for (const schema of schemas) {
      engine.schemas.define(deployer, schema);
      engine.schemas.publish(deployer, schema.name as string);
    }
    for (const subject of workers) {
      if (engine.instances.get(deployer, 'workers', subject) === undefined) {
        engine.instances.create(deployer, 'workers', { subject }, { id: subject });
      }
    }
  } catch (error) {
    engine.close();
    throw error;
  }
  return engine;
}

/** jobsApp serves the engine's HTTP API under /api. */
export function jobsApp(engine: Engine): Hono {
  const app = new Hono();
  app.route('/api', engineApp(engine, { authenticate, rateLimitPerMinute: 600, timeoutSeconds: 10 }));
  return app;
}

export interface Listening {
  /** The server's origin, such as http://127.0.0.1:8788. */
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
            server.closeAllConnections();
          }),
      });
    }) as Server;
    // A port in use fails the start instead of leaving it waiting.
    server.once('error', reject);
  });
}
