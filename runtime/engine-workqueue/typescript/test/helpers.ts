// Shared test setup, the parts of the engine's own test helpers these
// tests need: every test opens a real SQLite file under a fresh temporary
// directory with this package's behaviors registered, on a clock the test
// moves, and cleanup closes and removes them.
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { allowAll, isBun, openEngine, type DriverName, type Engine, type EngineOptions, type Principal } from '@superschematic/engine';

import { workQueueBehaviors } from '../dist/index.js';

/** The principal that defines and publishes; the test policy allows it everything. */
export const alice: Principal = { subject: 'alice', permissions: [] };

/** The adapters this runtime opens: node:sqlite everywhere, bun:sqlite too on Bun. */
export const drivers: DriverName[] = isBun() ? ['bun', 'node'] : ['node'];

const directories: string[] = [];
const open: Array<{ close(): void }> = [];

/** A clock a test moves by hand: the engine reads now() once per call. */
export class Clock {
  ms: number;

  constructor(ms = 1_000_000) {
    this.ms = ms;
  }

  readonly now = (): number => this.ms;

  advance(ms: number): number {
    this.ms += ms;
    return this.ms;
  }
}

/** openTestEngine opens an engine on a fresh file with the work-queue behaviors registered. */
export function openTestEngine(options: Partial<EngineOptions> = {}): Engine {
  const directory = mkdtempSync(join(tmpdir(), 'superschematic-workqueue-'));
  directories.push(directory);
  const engine = openEngine({ path: join(directory, 'engine.db'), policy: allowAll, behaviors: workQueueBehaviors, ...options });
  open.push(engine);
  return engine;
}

/** cleanup closes what the test opened and removes its files; register it with afterEach. */
export function cleanup(): void {
  for (const item of open.splice(0)) {
    try {
      item.close();
    } catch {
      // Closed by the test.
    }
  }
  for (const directory of directories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
}

/** thrown runs fn, asserts it throws an instance of type, and returns the error. */
export function thrown<T extends Error>(fn: () => unknown, type: new (...args: never[]) => T): T {
  try {
    fn();
  } catch (error) {
    assert.ok(error instanceof type, `expected ${type.name}, got ${error instanceof Error ? `${error.name}: ${error.message}` : String(error)}`);
    return error;
  }
  assert.fail(`expected ${type.name} to be thrown`);
}

export interface BehaviorRef {
  readonly name: string;
  readonly config?: unknown;
}

/**
 * jobsDocument is a General schema named Job whose instance type has a
 * title, a priority and a time limit, and composes the behaviors given.
 */
export function jobsDocument(behaviors: readonly BehaviorRef[], name = 'Job'): Record<string, unknown> {
  return {
    kind: 'General',
    name,
    types: {
      [name]: {
        name,
        role: 'EmbeddedStruct',
        behaviors,
        fields: [
          { name: 'title', typeRef: { name: 'string' }, required: true },
          { name: 'priority', typeRef: { name: 'Generic.Int64' } },
          { name: 'timeLimitMs', typeRef: { name: 'Generic.Int64' } },
        ],
      },
    },
  };
}

/** A job is queued, runs, and ends done or failed; a running job can go back to the queue. */
export const jobFlow = {
  states: ['queued', 'running', 'done', 'failed'],
  transitions: [
    { from: 'queued', to: 'running' },
    { from: 'running', to: 'queued' },
    { from: 'running', to: 'done' },
    { from: 'running', to: 'failed' },
    { from: 'queued', to: 'failed' },
  ],
};

/** publish defines and publishes a document as alice. */
export function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

/**
 * jobsFixture is the General schema-file document the core binary loads
 * with no extension linked (make cli-smoke, fixture-workqueue-json): its
 * Job type composes Workflow, Lease, Assignment and Queue.
 */
export function jobsFixture(): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL('../../../../internal/loader/testdata/services/fixture-workqueue-json/src/job.schema.json', import.meta.url), 'utf8')
  ) as Record<string, unknown>;
}
