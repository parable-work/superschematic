/*
openEngineAsync (D58): the engine's API with every call returning a
promise, for a server on Postgres, where a call waits on the network and
must not hold the JavaScript thread (the addon refuses a synchronous call
there: sync_on_async_storage). It opens off the thread too (the addon's
openAsync), since the open connects and migrates, and the connection may
be one the thread itself carries: a Cloud SQL socket the server serves
over the Node connector. Each call runs off the thread (the addon's call:
tokio's blocking pool on Postgres, libuv's pool on SQLite) and its promise
settles on it, with the package's error classes (src/errors.ts). Each
member's methods are the synchronous engine's (Engine), each returning a
promise of what that one returns.

A server's calls are here too: deploy, which defines and publishes its
schema documents as the engine's own principal before it reports ready,
and health, what its readiness reads.

Its options are data alone: a policy, a permission matcher, an id source
or a behavior written in TypeScript answers on the JavaScript thread, in a
synchronous call, so openEngineAsync refuses one (TypeError), and a tool
filter is the HTTP adapters' alone. events.watch hears every commit, the
engine's own and, on Postgres, every other process's, through the addon's
watch, a weak threadsafe function that never holds the process open; the
runner runs on a thread of its own (runner.start), on Postgres while its
process holds the runner's lease. engineApp and engineMcp serve it through
the addon's asynchronous handleHttp.
*/

import type { Principal } from '../access.js';
import { EngineError } from '../errors.js';
import type { EventWatcher } from '../types/events.js';
import { DEFAULT_NAMESPACE } from '../types/namespaces.js';
import { loadAddon, type Addon, type AddonEngine, type HttpAnswer, type StreamPull } from './addon.js';
import { carry } from './carry.js';
import type { Engine, EngineRoute } from './engine.js';
import { restoreError } from './errors.js';
import { nativeOptions, type EngineOptions } from './options.js';
import { resolveToolOptions, type ResolvedToolOptions } from './tool-options.js';

type Call = (op: string, principal: Principal | undefined, ...args: unknown[]) => Promise<unknown>;

// A method per call: (principal, ...args) for one that takes a principal.
function principalCall(call: Call, op: string): (principal: Principal, ...args: unknown[]) => Promise<unknown> {
  return (principal, ...args) => call(op, principal, ...args);
}

function plainCall(call: Call, op: string): (...args: unknown[]) => Promise<unknown> {
  return (...args) => call(op, undefined, ...args);
}

/**
 * The methods of a member of the synchronous engine (Engine), each taking
 * what that one takes and returning a promise of what it returns.
 */
export type Promised<T> = {
  [K in keyof T as T[K] extends (...args: never[]) => unknown ? K : never]: T[K] extends (...args: infer A) => infer R
    ? (...args: A) => Promise<Awaited<R>>
    : never;
};

/** What deploy did with one schema document. */
export interface DeployedSchema {
  readonly name: string;
  readonly namespace: string;
  /** The live version after the deploy. */
  readonly version: number;
  /** False when the live version had the document's hash, and nothing was written. */
  readonly defined: boolean;
  /** True when the deploy minted a version. */
  readonly published: boolean;
}

/** What deploy takes beside the documents. */
export interface DeployOptions {
  /** The namespace the documents are defined and published in: default unless set. */
  readonly namespace?: string;
}

/** The engine's state as a server's readiness reads it (health). */
export interface EngineHealth {
  /** False once the engine closed or poisoned, or while its database does not answer a read. */
  readonly ready: boolean;
  readonly closed: boolean;
  /** A panic inside a transaction poisoned the engine, which then refuses every call: restart the process. */
  readonly poisoned: boolean;
  readonly dialect: 'sqlite' | 'postgres';
  /** Whether it listens for other processes' commits (Postgres). */
  readonly listening: boolean;
  /** Why the database does not answer, while it does not. */
  readonly error?: string;
}

/** The engine's API with every call a promise (openEngineAsync). */
export class AsyncEngine {
  readonly dialect: 'sqlite' | 'postgres';
  readonly namespaces: Promised<Pick<Engine['namespaces'], 'list' | 'get' | 'create' | 'archive' | 'unarchive'>> & {
    /** The namespaces' names. */
    names(): Promise<string[]>;
    /** The namespace a call names, `default` when it names none. */
    resolve(namespace: string | undefined): Promise<string>;
  };
  readonly schemas: Promised<
    Pick<Engine['schemas'], 'define' | 'publish' | 'live' | 'draft' | 'version' | 'list' | 'capabilities' | 'validate' | 'behaviors'>
  >;
  readonly instances: Promised<Engine['instances']>;
  readonly events: Promised<Pick<Engine['events'], 'read' | 'head' | 'floor'>> & {
    /** Registers a watcher the engine calls after each commit that appended events, and once when it closes. */
    watch(watcher: EventWatcher): () => void;
    /** How many watchers are registered. */
    readonly watching: number;
  };
  readonly tools: Promised<Pick<Engine['tools'], 'listBehaviors' | 'describeBehavior' | 'describe' | 'manifest' | 'call'>> & {
    readonly options: ResolvedToolOptions;
  };
  readonly runner: Promised<Pick<Engine['runner'], 'start' | 'stop' | 'runDue' | 'prune' | 'status' | 'resume'>> & {
    running(): Promise<boolean>;
    /** When the runner next wakes, in epoch milliseconds, or null. */
    nextWake(): Promise<number | null>;
  };
  readonly values: Promised<Pick<Engine['values'], 'get' | 'sweep'>> & {
    thresholdBytes(): Promise<number>;
    maxBytes(): Promise<number>;
  };
  readonly behaviors: Promised<Pick<Engine['behaviors'], 'has' | 'names' | 'declaration'>>;
  private readonly watchers = new Set<EventWatcher>();
  private head = 0;
  private started: Promise<void> | undefined;
  private watching = false;
  private closed = false;

  private constructor(
    // The addon module that opened it, whose routes it serves.
    private readonly module: Addon,
    private readonly addon: AddonEngine,
    private readonly clock: (() => number) | undefined,
    toolOptions: ResolvedToolOptions
  ) {
    this.dialect = addon.dialect;
    const call: Call = (op, principal, ...args) => this.call(op, principal, args);
    const names = plainCall(call, 'namespaces.names') as () => Promise<string[]>;
    this.namespaces = {
      names,
      /** resolve is the namespace a call names, `default` when it names none. */
      resolve: async (namespace: string | undefined): Promise<string> => {
        if (namespace === undefined) {
          return DEFAULT_NAMESPACE;
        }
        if (!(await names()).includes(namespace)) {
          throw new EngineError('unknown_namespace', `unknown namespace "${String(namespace)}"`);
        }
        return namespace;
      },
      list: principalCall(call, 'namespaces.list'),
      get: principalCall(call, 'namespaces.get'),
      create: principalCall(call, 'namespaces.create'),
      archive: principalCall(call, 'namespaces.archive'),
      unarchive: principalCall(call, 'namespaces.unarchive'),
    } as unknown as AsyncEngine['namespaces'];
    this.schemas = {
      define: principalCall(call, 'schemas.define'),
      publish: principalCall(call, 'schemas.publish'),
      live: principalCall(call, 'schemas.live'),
      draft: principalCall(call, 'schemas.draft'),
      version: principalCall(call, 'schemas.version'),
      list: principalCall(call, 'schemas.list'),
      capabilities: principalCall(call, 'schemas.capabilities'),
      validate: principalCall(call, 'schemas.validate'),
      behaviors: principalCall(call, 'schemas.behaviors'),
    } as unknown as AsyncEngine['schemas'];
    this.instances = {
      create: principalCall(call, 'instances.create'),
      get: principalCall(call, 'instances.get'),
      list: principalCall(call, 'instances.list'),
      lookup: principalCall(call, 'instances.lookup'),
      update: principalCall(call, 'instances.update'),
      delete: principalCall(call, 'instances.delete'),
      invoke: (principal: Principal, schema: string, id: string, operation: string, params: unknown = {}, options?: unknown) =>
        call('instances.invoke', principal, schema, id, operation, params, options),
      operate: (principal: Principal, schema: string, id: string, operation: string, params: unknown = {}, options?: unknown) =>
        call('instances.operate', principal, schema, id, operation, params, options),
      invokeSchema: (principal: Principal, schema: string, operation: string, params: unknown = {}, options?: unknown) =>
        call('instances.invokeSchema', principal, schema, operation, params, options),
    } as unknown as AsyncEngine['instances'];
    this.events = {
      read: principalCall(call, 'events.read'),
      head: plainCall(call, 'events.head') as () => Promise<number>,
      floor: plainCall(call, 'events.floor') as (namespace?: string) => Promise<number>,
      /** watch registers a watcher the engine calls after each commit that appended events, and once when it closes. */
      watch: (watcher: EventWatcher): (() => void) => this.watch(watcher),
      get watching(): number {
        return watchersOf();
      },
    } as unknown as AsyncEngine['events'];
    const watchersOf = () => this.watchers.size;
    this.tools = {
      options: toolOptions,
      listBehaviors: principalCall(call, 'tools.listBehaviors'),
      describeBehavior: principalCall(call, 'tools.describeBehavior'),
      describe: principalCall(call, 'tools.describe'),
      manifest: principalCall(call, 'tools.manifest'),
      call: principalCall(call, 'tools.call'),
    } as unknown as AsyncEngine['tools'];
    this.runner = {
      running: plainCall(call, 'runner.running') as () => Promise<boolean>,
      start: plainCall(call, 'runner.start') as () => Promise<void>,
      stop: plainCall(call, 'runner.stop') as () => Promise<void>,
      runDue: plainCall(call, 'runner.runDue'),
      prune: plainCall(call, 'runner.prune'),
      status: plainCall(call, 'runner.status'),
      nextWake: plainCall(call, 'runner.nextWake') as () => Promise<number | null>,
      resume: plainCall(call, 'runner.resume'),
    } as unknown as AsyncEngine['runner'];
    this.values = {
      get: principalCall(call, 'values.get'),
      sweep: plainCall(call, 'values.sweep'),
      thresholdBytes: plainCall(call, 'values.thresholdBytes') as () => Promise<number>,
      maxBytes: plainCall(call, 'values.maxBytes') as () => Promise<number>,
    } as unknown as AsyncEngine['values'];
    this.behaviors = {
      has: plainCall(call, 'behaviors.has') as (name: string) => Promise<boolean>,
      names: plainCall(call, 'behaviors.names') as () => Promise<string[]>,
      declaration: plainCall(call, 'behaviors.declaration'),
    } as unknown as AsyncEngine['behaviors'];
  }

  /**
   * open opens the engine on options that are data alone, its storage
   * migrated, on this thread: on Postgres it connects and migrates while
   * the thread waits. openEngineAsync opens off it (openAsync).
   */
  static open(options: EngineOptions): AsyncEngine {
    const { json, clock, tools } = asyncOptions(options);
    const loaded = loadAddon(options.addon);
    let addon: AddonEngine;
    try {
      addon = loaded.NativeEngine.open(JSON.stringify(json));
    } catch (error) {
      throw restoreError(error);
    }
    return new AsyncEngine(loaded, addon, clock, tools);
  }

  /**
   * openAsync opens the engine on options that are data alone, its storage
   * created if absent and migrated, off this thread: the promise settles
   * once the engine is open, or with why it could not open (a database
   * that does not answer, a migration refused).
   */
  static async openAsync(options: EngineOptions): Promise<AsyncEngine> {
    const { json, clock, tools } = asyncOptions(options);
    const loaded = loadAddon(options.addon);
    let addon: AddonEngine;
    try {
      addon = await loaded.NativeEngine.openAsync(JSON.stringify(json));
    } catch (error) {
      throw restoreError(error);
    }
    return new AsyncEngine(loaded, addon, clock, tools);
  }

  /**
   * deploy defines and publishes a server's schema documents, in order, as
   * the engine's own principal, which the policy allows everything and no
   * request can be: a document whose live version has its hash is left as
   * it is, so a restart with the same documents writes nothing, and a
   * changed one becomes its next version if the compatibility rule allows
   * it. A document refused, say because its config names a schema not yet
   * live, is tried again after the others, and a pass that deploys none of
   * those left rejects with its first refusal. A server deploys its
   * database's EngineDocument before it reports the engine ready.
   */
  deploy(documents: readonly unknown[], options: DeployOptions = {}): Promise<DeployedSchema[]> {
    return this.call('deploy', undefined, [documents, options]) as Promise<DeployedSchema[]>;
  }

  /** health is the engine's state as a server's readiness reads it; it answers on a closed engine too. */
  health(): Promise<EngineHealth> {
    return this.call('health', undefined, []) as Promise<EngineHealth>;
  }

  /** search searches every schema the namespace reaches that composes Search. */
  search(principal: Principal, params: unknown, target?: { namespace?: string }): Promise<unknown> {
    return this.call('search', principal, [params, target]);
  }

  /** The routes the engine serves, which engineApp and engineMcp mount. */
  routes(): EngineRoute[] {
    return JSON.parse(this.module.engineRoutes()) as EngineRoute[];
  }

  /** handleHttp serves one request off this thread (src/native/http.ts). */
  async handleHttp(head: Record<string, unknown>, body: Buffer | undefined): Promise<HttpAnswer> {
    try {
      // A clock function is read once per request, as for a call.
      return await this.addon.handleHttpAsync(JSON.stringify({ ...head, now: this.clock?.() }), body);
    } catch (error) {
      throw restoreError(error);
    }
  }

  /** streamPull pulls an event stream handleHttp holds open, waiting up to timeoutMs off this thread. */
  async streamPull(id: number, timeoutMs: number): Promise<StreamPull> {
    try {
      return await this.addon.streamPullAsync(id, timeoutMs);
    } catch (error) {
      throw restoreError(error);
    }
  }

  /** streamClose ends an event stream handleHttp holds open. */
  streamClose(id: number): void {
    this.addon.streamClose(id);
  }

  /** shutdown stops the runner and ends every event stream; every later call is refused. */
  shutdown(): void {
    this.closeWatchers();
    this.addon.shutdown();
  }

  /** close shuts the engine down and lets it go; its storage closes once a call in flight returns. */
  close(): void {
    this.closeWatchers();
    this.addon.close();
  }

  private async call(op: string, principal: Principal | undefined, args: readonly unknown[]): Promise<unknown> {
    let end = args.length;
    while (end > 0 && args[end - 1] === undefined) {
      end -= 1;
    }
    const request = JSON.stringify({ op, principal, args: carry(args.slice(0, end)), now: this.clock?.() });
    let text: string;
    try {
      text = await this.addon.call(request);
    } catch (error) {
      throw restoreError(error);
    }
    return text === '' ? undefined : JSON.parse(text);
  }

  private watch(watcher: EventWatcher): () => void {
    if (this.closed) {
      throw new Error('the engine is closed');
    }
    this.watchers.add(watcher);
    if (!this.watching) {
      this.watching = true;
      this.started = this.call('events.head', undefined, []).then(
        (head) => {
          this.head = Math.max(this.head, head as number);
        },
        () => undefined
      );
      this.addon.watch(() => void this.poll());
    }
    return () => {
      this.watchers.delete(watcher);
    };
  }

  // poll tells the watchers of the events since it last looked, once a
  // commit moved the notifier.
  private async poll(): Promise<void> {
    await this.started;
    if (this.closed || this.watchers.size === 0) {
      return;
    }
    let head: number;
    try {
      head = (await this.call('events.head', undefined, [])) as number;
    } catch {
      return;
    }
    for (let cursor = this.head + 1; cursor <= head; cursor += 1) {
      for (const watcher of [...this.watchers]) {
        try {
          watcher.committed(cursor);
        } catch {
          // One watcher's failure is not the writer's, nor another watcher's.
        }
      }
    }
    this.head = Math.max(this.head, head);
  }

  private closeWatchers(): void {
    if (this.closed) {
      return;
    }
    this.closed = true;
    if (this.watching) {
      this.addon.unwatch();
    }
    const watchers = [...this.watchers];
    this.watchers.clear();
    for (const watcher of watchers) {
      try {
        watcher.closed?.();
      } catch {
        // As in poll.
      }
    }
  }
}

// asyncOptions are the options the addon opens with, the clock the facade
// sets before each call, and the tool options with their defaults filled
// in, refusing anything written in TypeScript, which answers on the
// JavaScript thread alone.
function asyncOptions(options: EngineOptions): { json: Record<string, unknown>; clock?: () => number; tools: ResolvedToolOptions } {
  if (options.policy === undefined || options.policy === null) {
    throw new TypeError('an engine needs an access policy (policy); pass allowAll for tests and local use');
  }
  const { json, clock, hosted } = nativeOptions(options);
  if (Object.keys(hosted).length > 0) {
    throw new TypeError(
      `openEngineAsync takes its options as data: ${Object.keys(hosted).join(', ')} written in TypeScript answers on the JavaScript thread, in a synchronous call (openEngine)`
    );
  }
  return { json, clock, tools: resolveToolOptions(options.tools) };
}

/**
 * openEngineAsync opens the engine with its API returning promises, off
 * the JavaScript thread: on Postgres (options.postgres), where a
 * synchronous call is refused, or on SQLite (options.path). Its options
 * are data alone.
 */
export async function openEngineAsync(options: EngineOptions): Promise<AsyncEngine> {
  return AsyncEngine.openAsync(options);
}
