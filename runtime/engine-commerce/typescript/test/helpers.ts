// Shared test setup: every test opens a real SQLite file under a fresh
// temporary directory with this package's behaviors named, on a clock the
// test moves, and cleanup closes and removes them. The engine is the Rust
// engine, through its Node-API addon (D58), whose commerce behaviors are
// the Rust ones it is built with, by name. With ENGINE_RECORD set, the
// engines they open record the test's calls through the engine's recorder
// (runtime/engine/typescript/test/recorder.ts), as the engine's own tests
// do, into scenarios/commerce-<file>.json: the corpus a second
// implementation of the engine and these behaviors replays (D58).
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { allowAll, openEngine, type Engine, type EngineOptions, type Principal } from '@superschematic/engine';

import { commerceBehaviors } from '../dist/index.js';
import { endCase, recordOpen, recordPackage, registerDirectory, type TestContextLike } from '../../../engine/typescript/test/recorder.ts';

// This package's behaviors are a replayer's to implement, as the core's
// are: an open names them.
recordPackage('commerce-');

/** The principal that defines and publishes; the test policy allows it everything. */
export const alice: Principal = { subject: 'alice', permissions: [] };

/** A principal with the permissions given. */
export function principal(subject: string, permissions: string[] = []): Principal {
  return { subject, permissions };
}

const directories: string[] = [];
const open: Array<{ close(): void }> = [];

/** A clock a test moves by hand: the engine reads now() once per call. */
export class Clock {
  ms: number;

  constructor(ms = 1_792_573_200_000) {
    this.ms = ms;
  }

  readonly now = (): number => this.ms;

  advance(ms: number): number {
    this.ms += ms;
    return this.ms;
  }
}

/** openTestEngine opens an engine on a fresh file with the commerce behaviors registered, recording with ENGINE_RECORD set. */
export function openTestEngine(options: Partial<EngineOptions> = {}): Engine {
  const directory = mkdtempSync(join(tmpdir(), 'superschematic-commerce-'));
  directories.push(directory);
  registerDirectory(directory);
  const engine = recordOpen({ path: join(directory, 'engine.db'), policy: allowAll, behaviors: commerceBehaviors, ...options }, openEngine);
  open.push(engine);
  return engine;
}

/**
 * cleanup closes what the test opened and removes its files; register it
 * with afterEach, which passes the test's context, under which a recording
 * names the test's scenario.
 */
export function cleanup(context?: TestContextLike): void {
  try {
    endCase(context);
  } finally {
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

/** publish defines and publishes a document as alice. */
export function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

/** A field of a type. */
export function field(name: string, type: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return { name, typeRef: { name: type, ...extra } };
}

/** A General schema whose instance type, named like the schema, has the fields and behaviors given, and the other types. */
export function document(
  name: string,
  fields: Record<string, unknown>[],
  behaviors: Record<string, unknown>[],
  types: Record<string, unknown> = {}
): Record<string, unknown> {
  return { kind: 'General', name, types: { [name]: { name, role: 'EmbeddedStruct', behaviors, fields }, ...types } };
}

/** The Inventory schema: a title, a unit price and a reorder level, composing Stock with the config given. */
export function inventory(config: Record<string, unknown>): Record<string, unknown> {
  return document(
    'Inventory',
    [field('title', 'string'), field('priceCents', 'Generic.Int64'), field('reorderAt', 'Generic.Int64')],
    [{ name: 'Stock', config }]
  );
}

/** A checkout is a cart, then held, then placed, expired, cancelled or returned. */
export const checkoutFlow = {
  states: ['cart', 'held', 'placed', 'expired', 'cancelled', 'returned'],
  initial: 'cart',
  transitions: [
    { from: 'cart', to: 'held' },
    { from: 'cart', to: 'placed' },
    { from: 'held', to: 'placed' },
    { from: 'held', to: 'expired' },
    { from: 'held', to: 'cancelled' },
    { from: 'cart', to: 'cancelled' },
    { from: 'placed', to: 'returned' },
  ],
};

/**
 * The Checkout schema: lines of Inventory, a gift-wrap link to one
 * Inventory with its own quantity, Workflow, Links, Holds with the holds
 * given, and the behaviors after it.
 */
export function checkout(holds: Record<string, unknown>, after: Record<string, unknown>[] = []): Record<string, unknown> {
  return document(
    'Checkout',
    [
      field('title', 'string'),
      field('lines', 'Line', { isArray: true }),
      field('wrapCount', 'Generic.Int64'),
    ],
    [
      { name: 'Workflow', config: checkoutFlow },
      { name: 'Links', config: { links: { wrap: { schema: 'Inventory' } } } },
      { name: 'Holds', config: holds },
      ...after,
    ],
    {
      Line: {
        name: 'Line',
        role: 'EmbeddedStruct',
        fields: [{ ...field('inventoryId', 'string'), required: true }, field('quantity', 'Generic.Int64')],
      },
    }
  );
}

/** Lines of Inventory, each element's quantity, and gift wrap from the wrap link, taken in held. */
export const checkoutHolds = {
  holds: {
    lines: { each: { field: 'lines', target: 'inventoryId' }, schema: 'Inventory', quantity: { field: 'quantity' } },
    wrap: { link: 'wrap', quantity: { field: 'wrapCount' } },
  },
  takeOn: ['held'],
  commitOn: ['placed'],
  releaseOn: ['expired', 'cancelled'],
  returnOn: ['returned'],
};
