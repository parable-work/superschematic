/*
openEngineAsync (D58): the engine's API with every call returning a
promise, for a server on Postgres, where a call waits on the network and
must not hold the JavaScript thread (the addon refuses a synchronous call
there: sync_on_async_storage). Each call runs off the thread (the addon's
call: tokio's blocking pool on Postgres, libuv's pool on SQLite) and its
promise settles on it, with the TypeScript engine's error classes.

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
import type { EventWatcher } from '../events/notifier.js';
import { DEFAULT_NAMESPACE } from '../namespaces.js';
import { EngineError } from '../errors.js';
import { resolveToolOptions, type ResolvedToolOptions } from '../tools/options.js';
import { loadAddon, type AddonEngine, type HttpAnswer, type StreamPull } from './addon.js';
import type { EngineRoute } from './engine.js';
import { restoreError } from './errors.js';
import { nativeOptions, type NativeEngineOptions } from './options.js';

type Call = (op: string, principal: Principal | undefined, ...args: unknown[]) => Promise<unknown>;

// A method per call: (principal, ...args) for one that takes a principal.
function principalCall(call: Call, op: string): (principal: Principal, ...args: unknown[]) => Promise<unknown> {
  return (principal, ...args) => call(op, principal, ...args);
}

function plainCall(call: Call, op: string): (...args: unknown[]) => Promise<unknown> {
  return (...args) => call(op, undefined, ...args);
}

/** The engine's API over the Rust engine, every call a promise (openEngineAsync). */
export class AsyncNativeEngine {
  readonly dialect: 'sqlite' | 'postgres';
  readonly namespaces;
  readonly schemas;
  readonly instances;
  readonly events;
  readonly tools;
  readonly runner;
  readonly values;
  readonly behaviors;
  private readonly watchers = new Set<EventWatcher>();
  private head = 0;
  private started: Promise<void> | undefined;
  private watching = false;
  private closed = false;

  private constructor(
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
    };
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
    };
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
    };
    this.events = {
      read: principalCall(call, 'events.read'),
      head: plainCall(call, 'events.head') as () => Promise<number>,
      floor: plainCall(call, 'events.floor') as (namespace?: string) => Promise<number>,
      /** watch registers a watcher the engine calls after each commit that appended events, and once when it closes. */
      watch: (watcher: EventWatcher): (() => void) => this.watch(watcher),
      get watching(): number {
        return watchersOf();
      },
    };
    const watchersOf = () => this.watchers.size;
    this.tools = {
      options: toolOptions,
      listBehaviors: principalCall(call, 'tools.listBehaviors'),
      describeBehavior: principalCall(call, 'tools.describeBehavior'),
      describe: principalCall(call, 'tools.describe'),
      manifest: principalCall(call, 'tools.manifest'),
      call: principalCall(call, 'tools.call'),
    };
    this.runner = {
      running: plainCall(call, 'runner.running') as () => Promise<boolean>,
      start: plainCall(call, 'runner.start') as () => Promise<void>,
      stop: plainCall(call, 'runner.stop') as () => Promise<void>,
      runDue: plainCall(call, 'runner.runDue'),
      prune: plainCall(call, 'runner.prune'),
      status: plainCall(call, 'runner.status'),
      nextWake: plainCall(call, 'runner.nextWake') as () => Promise<number | null>,
      resume: plainCall(call, 'runner.resume'),
    };
    this.values = {
      get: principalCall(call, 'values.get'),
      sweep: plainCall(call, 'values.sweep'),
      thresholdBytes: plainCall(call, 'values.thresholdBytes') as () => Promise<number>,
      maxBytes: plainCall(call, 'values.maxBytes') as () => Promise<number>,
    };
    this.behaviors = {
      has: plainCall(call, 'behaviors.has') as (name: string) => Promise<boolean>,
      names: plainCall(call, 'behaviors.names') as () => Promise<string[]>,
      declaration: plainCall(call, 'behaviors.declaration'),
    };
  }

  /** open opens the Rust engine on options that are data alone, its storage migrated. */
  static open(options: NativeEngineOptions): AsyncNativeEngine {
    if (options.policy === undefined || options.policy === null) {
      throw new TypeError('an engine needs an access policy (policy); pass allowAll for tests and local use');
    }
    const { json, clock, hosted } = nativeOptions(options);
    if (Object.keys(hosted).length > 0) {
      throw new TypeError(
        `openEngineAsync takes its options as data: ${Object.keys(hosted).join(', ')} written in TypeScript answers on the JavaScript thread, in a synchronous call (openEngine)`
      );
    }
    const tools = resolveToolOptions(options.tools);
    let addon: AddonEngine;
    try {
      addon = loadAddon().NativeEngine.open(JSON.stringify(json));
    } catch (error) {
      throw restoreError(error);
    }
    return new AsyncNativeEngine(addon, clock, tools);
  }

  /** search searches every schema the namespace reaches that composes Search. */
  search(principal: Principal, params: unknown, target?: { namespace?: string }): Promise<unknown> {
    return this.call('search', principal, [params, target]);
  }

  /** The routes the engine serves, which engineApp and engineMcp mount. */
  routes(): EngineRoute[] {
    return JSON.parse(loadAddon().engineRoutes()) as EngineRoute[];
  }

  /** handleHttp serves one request off this thread (src/native/http.ts). */
  async handleHttp(head: Record<string, unknown>, body: Buffer | undefined): Promise<HttpAnswer> {
    try {
      return await this.addon.handleHttpAsync(JSON.stringify(head), body);
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
    const request = JSON.stringify({ op, principal, args: args.slice(0, end), now: this.clock?.() });
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

/**
 * openEngineAsync opens the Rust engine with the engine's API returning
 * promises: on Postgres (options.postgres), where a synchronous call is
 * refused, or on SQLite (options.path).
 */
export async function openEngineAsync(options: NativeEngineOptions): Promise<AsyncNativeEngine> {
  return AsyncNativeEngine.open(options);
}
